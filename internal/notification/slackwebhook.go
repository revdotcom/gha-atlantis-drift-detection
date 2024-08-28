package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type SlackWebhook struct {
	WebhookURL string
	HTTPClient *http.Client
}

func (s *SlackWebhook) TemporaryError(ctx context.Context, dir string, workspace string, err error) error {
	return s.sendSlackMessage(ctx, fmt.Sprintf("Unknown error in remote\nDirectory: %s\nWorkspace: %s\nError: %s", dir, workspace, err.Error()))
}

func NewSlackWebhook(webhookURL string, HTTPClient *http.Client) *SlackWebhook {
	if webhookURL == "" {
		return nil
	}
	return &SlackWebhook{
		WebhookURL: webhookURL,
		HTTPClient: HTTPClient,
	}
}

type SlackWebhookMessage struct {
	Text string `json:"text"`
}

func (s *SlackWebhook) sendSlackMessage(ctx context.Context, msg string) error {
	body := SlackWebhookMessage{
		Text: msg,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal slack webhook message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("failed to create slack webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send slack webhook request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send slack webhook request: %w", err)
	}
	return nil
}

func (s *SlackWebhook) ExtraWorkspaceInRemote(ctx context.Context, dir string, workspace string) error {
	msg := ""
	if len(workspace) == 0 {
		msg = fmt.Sprintf("Extra workspace in remote\nDirectory: `%s`", dir)
	} else {
		msg = fmt.Sprintf("Extra workspace in remote\nDirectory: `%s`\nWorkspace: `%s`", dir, workspace)
	}
	return s.sendSlackMessage(ctx, msg)
}

func (s *SlackWebhook) MissingWorkspaceInRemote(ctx context.Context, dir string, workspace string) error {
	msg := ""
	if len(workspace) == 0 {
		msg = fmt.Sprintf("Missing workspace in remote\nRoot module: `%s`", dir)
	} else {
		msg = fmt.Sprintf("Missing workspace in remote\nRoot module: `%s`\nWorkspace: `%s`", dir, workspace)
	}
	return s.sendSlackMessage(ctx, msg)
}

func (s *SlackWebhook) PlanDrift(ctx context.Context, dir string, workspace string, cliffnote string) error {
	msg := ""
	if len(workspace) == 0 {
		if len(cliffnote) > 50 {
			msg = fmt.Sprintf(":exclamation: *Drift detected*\n:terraform: *Root module:* `%s`\n:pencil: *Result:* \n```\n%s\n```", dir, cliffnote)
		} else {
			msg = fmt.Sprintf(":exclamation: *Drift detected*\n:terraform: *Root module:* `%s`\n:pencil: *Result:* `%s`", dir, cliffnote)
		}
	} else {
		if len(cliffnote) > 50 {
			msg = fmt.Sprintf(":exclamation: *Drift detected*\n:terraform: *Root module:* `%s`\nWorkspace: `%s`\n:pencil: *Result:* \n```\n%s\n```", dir, workspace, cliffnote)
		} else {
			msg = fmt.Sprintf(":exclamation: *Drift detected*\n:terraform: *Root module:* `%s`\nWorkspace: `%s`\n:pencil: *Result:* `%s`", dir, workspace, cliffnote)
		}
	}
	return s.sendSlackMessage(ctx, msg)
}

func (s *SlackWebhook) WorkspaceDriftSummary(ctx context.Context, workspacesDrifted int32, workspacesUndrifted int32, totalWorkspaces int32) error {
	var msgBuilder strings.Builder
	if workspacesDrifted == 0 {
		msgBuilder.WriteString(fmt.Sprintf(":checked_animated: *Total Workspaces Drifted:* 0 / %d", totalWorkspaces))
	} else {
		pct := (float32(workspacesDrifted) / float32(totalWorkspaces) * 100)
		msgBuilder.WriteString(fmt.Sprintf(":checkered_flag: *Total Workspaces Drifted:* %d / %d (%.1f%%)", workspacesDrifted, totalWorkspaces, pct))
	}
	undriftPct := (float32(workspacesUndrifted) / float32(totalWorkspaces) * 100)
	msgBuilder.WriteString(fmt.Sprintf("\n:checked_animated: *Total Workspaces Undrifted:* %d / %d (%.1f%%)", workspacesUndrifted, totalWorkspaces, undriftPct))
	return s.sendSlackMessage(ctx, msgBuilder.String())
}

var _ Notification = &SlackWebhook{}
