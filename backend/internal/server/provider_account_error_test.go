package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/providers"
)

func TestProviderAccountFailureIsNotUserCreditFailure(t *testing.T) {
	for _, code := range []string{"1113", "insufficient_quota", "insufficient_balance", "Arrearage"} {
		t.Run(code, func(t *testing.T) {
			err := fmt.Errorf("private context: %w", &providers.DashScopeHTTPError{StatusCode: 429, ProviderCode: code})
			status, gotCode, message := safeProviderFailureDiagnostics(err)
			if status != workflowStepStatusFailed || gotCode != "provider_account_unavailable" || message != "模型调用失败，请联系客服" {
				t.Fatalf("got %s %s %s", status, gotCode, message)
			}
			if _, _, err := sanitizeWorkflowDiagnostics(&gotCode, &message); err != nil {
				t.Fatal(err)
			}
			stream := newWorkflowProviderStreamError(err)
			if stream.Retryable || stream.Code != gotCode {
				t.Fatalf("stream = %#v", stream)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			respondJSONWorkflowProviderError(c, err)
			if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), message) || strings.Contains(w.Body.String(), "insufficient_credits") || strings.Contains(w.Body.String(), "private") || !strings.Contains(w.Body.String(), `"retryable":false`) {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
	_, code, message := safeProviderFailureDiagnostics(&providers.DashScopeHTTPError{StatusCode: 402})
	if code != "provider_account_unavailable" || message != "模型调用失败，请联系客服" {
		t.Fatalf("upstream 402 = %s %s", code, message)
	}
	_, code, _ = safeProviderFailureDiagnostics(&providers.DashScopeHTTPError{StatusCode: 429, ProviderCode: "rate_limit"})
	if code != "provider_unavailable" {
		t.Fatalf("rate limit = %s", code)
	}
	// User balance failures still use their own explicit billing path.
	credit := newWorkflowRequestError(402, "insufficient_credits", false, "insufficient credits", ErrInsufficientCredits)
	if credit.Code != "insufficient_credits" {
		t.Fatal("user credit code changed")
	}
}
