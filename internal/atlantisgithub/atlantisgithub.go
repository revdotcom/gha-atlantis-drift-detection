package atlantisgithub

import (
	"context"
	"fmt"
	"github.com/cresta/gogit"
	"github.com/cresta/gogithub"
	"go.uber.org/zap"
)

func CheckOutTerraformRepo(ctx context.Context, gitHubClient gogithub.GitHub, cloner *gogit.Cloner, repo string, zap *zap.Logger) (*gogit.Repository, error) {
	token, err := gitHubClient.GetAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	// https://docs.github.com/en/developers/apps/building-github-apps/authenticating-with-github-apps#http-based-git-access-by-an-installation
	zap.Info("Preparing to clone repo.")
	githubRepoURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", token, repo)
	repository, err := cloner.Clone(ctx, githubRepoURL)
	zap.Info("Clone repo cmd complete. Evaluating results.")
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo: %w", err)
	}
	return repository, nil
}
