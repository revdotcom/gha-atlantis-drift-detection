package drifter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cresta/gogit"
	"github.com/cresta/gogithub"
	"github.com/revdotcom/gha-atlantis-drift-detection/internal/atlantis"
	"github.com/revdotcom/gha-atlantis-drift-detection/internal/atlantisgithub"
	"github.com/revdotcom/gha-atlantis-drift-detection/internal/notification"
	"github.com/revdotcom/gha-atlantis-drift-detection/internal/processedcache"
	"github.com/revdotcom/gha-atlantis-drift-detection/internal/terraform"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v2"
)

type Drifter struct {
	Logger                *zap.Logger
	Repo                  string
	Cloner                *gogit.Cloner
	GithubClient          gogithub.GitHub
	Terraform             *terraform.Client
	AtlantisRepoYmlPath   string
	Notification          notification.Notification
	AtlantisClient        *atlantis.Client
	ResultCache           processedcache.ProcessedCache
	CacheValidDuration    time.Duration
	DirectoryAllowlist    []string
	SkipWorkspaceCheck    bool
	ParallelRuns          int
	AutoGenerateConfig    bool
	DriftedWorkspaceCount int32
}

func (d *Drifter) Drift(ctx context.Context) error {
	d.Logger.Info("Checking out Terraform repository.")
	repo, err := atlantisgithub.CheckOutTerraformRepo(ctx, d.GithubClient, d.Cloner, d.Repo, d.Logger)
	if err != nil {
		return fmt.Errorf("failed to checkout repo %s: %w", d.Repo, err)
	}
	d.Terraform.Directory = repo.Location()
	d.Logger.Info("Repo location:", zap.String("location", repo.Location()))

	defer func() {
		if err := os.RemoveAll(repo.Location()); err != nil {
			d.Logger.Warn("failed to cleanup repo", zap.Error(err))
		}
	}()
	d.Logger.Info("Parsing repo config from directory.")
	if d.AutoGenerateConfig {
		d.Logger.Info("Auto generation of config option enabled.")
		err := d.generateAtlantisProjectsFile()
		if err != nil {
			return err
		}
	}

	cfg, err := atlantis.ParseRepoConfigFromDir(d.AtlantisRepoYmlPath, repo.Location())
	if err != nil {
		return fmt.Errorf("failed to parse repo config: %w", err)
	}
	d.Logger.Info("Finished parsing repo config from directory.")
	if len(cfg.Projects) == 0 {
		d.Logger.Warn("No projects found in repo config.")
	}

	d.Logger.Info("Parsing workspaces.")
	workspaces := atlantis.ConfigToWorkspaces(cfg)
	d.Logger.Info("Finished parsing workspaces. Checking for drift.")
	if err := d.FindDriftedWorkspaces(ctx, workspaces); err != nil {
		return fmt.Errorf("failed to find drifted workspaces: %w", err)
	}
	d.Logger.Info("Total number of workspaces drifted", zap.Int32("drifted workspaces", d.DriftedWorkspaceCount))
	d.Logger.Info("Finished checking for drifted workspaces. Checking for extra workspaces.")
	if err := d.FindExtraWorkspaces(ctx, workspaces); err != nil {
		return fmt.Errorf("failed to find extra workspaces: %w", err)
	}
	d.Notification.WorkspaceDriftSummary(ctx, d.DriftedWorkspaceCount)
	d.Logger.Info("Finished checking for workspaces with extra drift.")
	return nil
}

func (d *Drifter) shouldSkipDirectory(dir string) bool {
	if len(d.DirectoryAllowlist) == 0 {
		return false
	}
	for _, allowedDirectoryPattern := range d.DirectoryAllowlist {
		if strings.Contains(dir, allowedDirectoryPattern) {
			return false
		}
	}
	return true
}

type errFunc func(ctx context.Context) error

func (d *Drifter) drainAndExecute(ctx context.Context, toRun []errFunc) error {
	if d.ParallelRuns <= 1 {
		for _, r := range toRun {
			if err := r(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	from := make(chan errFunc)
	eg, egctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		for _, r := range toRun {
			select {
			case from <- r:
			case <-egctx.Done():
				return egctx.Err()
			}
		}
		close(from)
		return nil
	})
	for i := 0; i < d.ParallelRuns; i++ {
		eg.Go(func() error {
			for {
				select {
				case <-egctx.Done():
					return egctx.Err()
				case r, ok := <-from:
					if !ok {
						return nil
					}
					if err := r(egctx); err != nil {
						return err
					}
				}
			}
		})
	}
	return eg.Wait()
}

func (d *Drifter) FindDriftedWorkspaces(ctx context.Context, ws atlantis.DirectoriesWithWorkspaces) error {
	runningFunc := func(dir string) errFunc {
		return func(ctx context.Context) error {
			if d.shouldSkipDirectory(dir) {
				d.Logger.Info("Skipping directory", zap.String("dir", dir))
				return nil
			}
			workspaces := ws[dir]
			d.Logger.Info("Checking for drifted workspaces", zap.String("dir", dir))
			for _, workspace := range workspaces {
				cacheKey := &processedcache.ConsiderDriftChecked{
					Dir:       dir,
					Workspace: workspace,
				}
				cacheVal, err := d.ResultCache.GetDriftCheckResult(ctx, cacheKey)
				if err != nil {
					return fmt.Errorf("failed to get cache value for %s/%s: %w", dir, workspace, err)
				}
				if cacheVal != nil {
					if time.Since(cacheVal.When) < d.CacheValidDuration {
						d.Logger.Info("Skipping workspace, already checked", zap.String("dir", dir), zap.String("workspace", workspace))
						continue
					}
					d.Logger.Info("Cache expired, checking again", zap.String("dir", dir), zap.String("workspace", workspace), zap.Duration("cache-age", time.Since(cacheVal.When)), zap.Duration("cache-valid-duration", d.CacheValidDuration))
					if err := d.ResultCache.DeleteDriftCheckResult(ctx, cacheKey); err != nil {
						return fmt.Errorf("failed to delete cache value for %s/%s: %w", dir, workspace, err)
					}
				}

				pr, err := d.AtlantisClient.PlanSummary(ctx, &atlantis.PlanSummaryRequest{
					Repo:      d.Repo,
					Ref:       "master",
					Type:      "Github",
					Dir:       dir,
					Workspace: workspace,
				})
				if err != nil {
					var tmp atlantis.TemporaryError
					if errors.As(err, &tmp) && tmp.Temporary() {
						d.Logger.Warn("Temporary error.  Will try again later.", zap.Error(err))
						continue
					}
					return fmt.Errorf("failed to get plan summary for (%s#%s): %w", dir, workspace, err)
				}
				if err := d.ResultCache.StoreDriftCheckResult(ctx, cacheKey, &processedcache.DriftCheckValue{
					When:  time.Now(),
					Error: "",
					Drift: pr.HasChanges(),
				}); err != nil {
					return fmt.Errorf("failed to store cache value for %s/%s: %w", dir, workspace, err)
				}
				if pr.IsLocked() {
					d.Logger.Info("Plan is locked, skipping drift check", zap.String("dir", dir))
					continue
				}
				if pr.HasChanges() {
					atomic.AddInt32(&d.DriftedWorkspaceCount, 1)
					cliffnote := pr.GetPlanResultSummary()
					if err := d.Notification.PlanDrift(ctx, dir, workspace, cliffnote); err != nil {
						return fmt.Errorf("failed to notify of plan drift in %s: %w", dir, err)
					}
				}
			}
			return nil
		}
	}
	runs := make([]errFunc, 0)
	for _, dir := range ws.SortedKeys() {
		runs = append(runs, runningFunc(dir))
	}
	return d.drainAndExecute(ctx, runs)
}

func (d *Drifter) FindExtraWorkspaces(ctx context.Context, ws atlantis.DirectoriesWithWorkspaces) error {
	if d.SkipWorkspaceCheck {
		return nil
	}
	runFunc := func(dir string) errFunc {
		return func(ctx context.Context) error {
			if d.shouldSkipDirectory(dir) {
				d.Logger.Info("Skipping directory", zap.String("dir", dir))
				return nil
			}
			cacheKey := &processedcache.ConsiderWorkspacesChecked{
				Dir: dir,
			}
			cacheVal, err := d.ResultCache.GetRemoteWorkspaces(ctx, cacheKey)
			if err != nil {
				return fmt.Errorf("failed to get cache value for %s: %w", dir, err)
			}
			if cacheVal != nil {
				if time.Since(cacheVal.When) < d.CacheValidDuration {
					d.Logger.Info("Skipping directory, in cache", zap.String("dir", dir))
					return nil
				}
				d.Logger.Info("Cache expired, checking again", zap.String("dir", dir), zap.Duration("cache-age", time.Since(cacheVal.When)), zap.Duration("cache-valid-duration", d.CacheValidDuration))
				if err := d.ResultCache.DeleteRemoteWorkspaces(ctx, cacheKey); err != nil {
					return fmt.Errorf("failed to delete cache value for %s: %w", dir, err)
				}
			}
			workspaces := ws[dir]
			d.Logger.Info("Checking for extra workspaces", zap.String("dir", dir))
			if err := d.Terraform.Init(ctx, dir); err != nil {
				return fmt.Errorf("failed to init workspace %s: %w", dir, err)
			}
			var expectedWorkspaces []string
			expectedWorkspaces = append(expectedWorkspaces, workspaces...)
			expectedWorkspaces = append(expectedWorkspaces, "default")
			remoteWorkspaces, err := d.Terraform.ListWorkspaces(ctx, dir)
			if err != nil {
				return fmt.Errorf("failed to list workspaces in %s: %w", dir, err)
			}
			for _, w := range remoteWorkspaces {
				if !contains(expectedWorkspaces, w) {
					if err := d.Notification.ExtraWorkspaceInRemote(ctx, dir, w); err != nil {
						return fmt.Errorf("failed to notify of extra workspace %s in %s: %w", w, dir, err)
					}
				}
			}
			if err := d.ResultCache.StoreRemoteWorkspaces(ctx, cacheKey, &processedcache.WorkspacesCheckedValue{
				Workspaces: remoteWorkspaces,
				When:       time.Now(),
			}); err != nil {
				return fmt.Errorf("failed to store cache value for %s: %w", dir, err)
			}
			return nil
		}
	}
	runs := make([]errFunc, 0)
	for _, dir := range ws.SortedKeys() {
		runs = append(runs, runFunc(dir))
	}
	return d.drainAndExecute(ctx, runs)
}

func contains(workspaces []string, w string) bool {
	for _, workspace := range workspaces {
		if workspace == w {
			return true
		}
	}
	return false
}

func (d *Drifter) generateAtlantisProjectsFile() error {
	files, err := findTFFiles(d.Terraform.Directory)
	if err != nil {
		return fmt.Errorf("error finding tf files: %v", err)
	}

	// Look for s3/gcs/azurerm storage backends
	backendPattern := regexp.MustCompile(`backend[\s]+"(s3)|(gcs)|(azurerm)"`)
	directories, err := d.findTerraformRootModules(files, backendPattern)
	if err != nil {
		return fmt.Errorf("error processing files: %v", err)
	}

	yamlOutputBytes, err := d.generateAtlantisRepoYaml(directories)
	if err != nil {
		return fmt.Errorf("error generating YAML: %v", err)
	}
	d.Logger.Info("atlantis YAML generated successfully.")
	d.Logger.Debug("yaml content: ", zap.String("atlantis.yml", string(yamlOutputBytes)))

	writeErr := os.WriteFile(fmt.Sprintf("%s/%s", d.Terraform.Directory, d.AtlantisRepoYmlPath), yamlOutputBytes, 0644)
	if writeErr != nil {
		return fmt.Errorf("error writing Atlantis yaml config file: %v", writeErr)
	}
	return nil
}

func findTFFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".tf") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func (d *Drifter) findTerraformRootModules(files []string, pattern *regexp.Regexp) (map[string]struct{}, error) {
	directories := map[string]struct{}{}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("error reading tf file %s: %w", file, err)
		}

		if pattern.Match(content) {
			reversed := reverseString(file)
			cutPath := strings.SplitN(reversed, "/", 2)[1]
			directory := reverseString(cutPath)
			directories[directory] = struct{}{}
		}
	}
	return directories, nil
}

func (d *Drifter) generateAtlantisRepoYaml(directories map[string]struct{}) ([]byte, error) {
	dirList := make([]string, 0, len(directories))
	for dir := range directories {
		dirList = append(dirList, dir)
	}
	sort.Strings(dirList)

	var projects []map[string]interface{}
	for _, dir := range dirList {
		relativeDir := strings.Replace(dir, fmt.Sprintf("%s/", d.Terraform.Directory), "", 1)
		project := map[string]interface{}{
			"name":     relativeDir,
			"dir":      relativeDir,
			"autoplan": map[string]interface{}{"when_modified": []string{"**/*.tf.*"}},
		}
		projects = append(projects, project)
	}

	result := map[string]interface{}{
		"version":       3,
		"parallel_plan": true,
		"projects":      projects,
	}

	yamlDataBytes, err := yaml.Marshal(result)
	if err != nil {
		return []byte{}, fmt.Errorf("error marshalling to YAML: %w", err)
	}
	return yamlDataBytes, nil
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
