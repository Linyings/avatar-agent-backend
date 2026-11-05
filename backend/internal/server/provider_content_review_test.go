package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/providers"
)

func TestProviderContentReviewIsActionableAndNotRetryable(t *testing.T) {
	err := &providers.DashScopeHTTPError{StatusCode: 400, ProviderCode: "content_blocked"}
	status, code, message := safeProviderFailureDiagnostics(err)
	if status != workflowStepStatusFailed || code != "provider_content_blocked" || message != providers.ProviderContentReviewMessage {
		t.Fatalf("got %s %s %s", status, code, message)
	}
	if _, _, err := sanitizeWorkflowDiagnostics(&code, &message); err != nil {
		t.Fatal(err)
	}
	stream := newWorkflowProviderStreamError(err)
	if stream.Retryable || stream.Code != code || stream.Message != message {
		t.Fatalf("stream = %#v", stream)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	respondJSONWorkflowProviderError(c, err)
	if !strings.Contains(w.Body.String(), message) || !strings.Contains(w.Body.String(), `"retryable":false`) {
		t.Fatalf("response = %s", w.Body.String())
	}
}
