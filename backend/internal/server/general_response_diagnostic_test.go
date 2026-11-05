package server

import (
	"fmt"
	"github.com/narratoai/backend-go/internal/providers"
	"testing"
)

func TestGeneralInvalidResponseDiagnostic(t *testing.T) {
	status, code, message := safeProviderFailureDiagnostics(fmt.Errorf("private content: %w", providers.ErrGeneralResponseInvalid))
	if status != workflowStepStatusFailed || code != "provider_invalid_output" || message != "通用模式模型响应格式无效，请重试该步骤" {
		t.Fatalf("%s %s %s", status, code, message)
	}
}
func TestGeneralPreflightRetrievalIdentity(t *testing.T) {
	for _, step := range []string{"generate-narration-script.general-v3.retrieve", "generate-voiceover.general-v3.retrieve"} {
		if !generalNarrationStep.MatchString(step) {
			t.Fatalf("preflight identity rejected: %s", step)
		}
	}
	if generalNarrationStep.MatchString("compose-final-video.general-v3.retrieve") {
		t.Fatal("retrieval allowed on wrong stage")
	}
}
