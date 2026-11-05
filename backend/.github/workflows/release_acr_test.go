package workflows

import (
	"os"
	"strings"
	"testing"
)

func TestBackendReleaseDeploysMainAfterVerification(t *testing.T) {
	content, err := os.ReadFile("release-acr.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(content)
	for _, required := range []string{
		"branches: [main]",
		"go test ./...",
		"needs: build-and-push",
		"deploy-service.sh api",
		"group: production-backend",
		"short_sha=\"$(printf '%s' \"$GITHUB_SHA\" | cut -c1-7)\"",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("workflow missing %q", required)
		}
	}
	if strings.Contains(workflow, "needs.build-and-push.outputs.image") {
		t.Error("secret-derived image references must not cross job outputs")
	}
}
