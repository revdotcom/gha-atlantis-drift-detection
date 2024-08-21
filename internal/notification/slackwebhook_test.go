package notification

import (
	"net/http"
	"testing"

	"github.com/revdotcom/gha-atlantis-drift-detection/internal/testhelper"
)

func TestSlackWebhook_ExtraWorkspaceInRemote(t *testing.T) {
	testhelper.ReadEnvFile(t, "../../")
	wh := NewSlackWebhook(testhelper.EnvOrSkip(t, "SLACK_WEBHOOK_URL"), http.DefaultClient)
	genericNotificationTest(t, wh)
}
