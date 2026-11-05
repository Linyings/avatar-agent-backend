package server

import "testing"

func TestWorkflowLocalDesignUpgradeDoesNotInvalidateProviderOrders(t *testing.T) {
	old := map[string]string{"video": "same-order-provider", "tts_model": "same-voice", "renderer": "talkcraft-workflow/2", "style_version": "mg-native/1"}
	next := map[string]string{"video": "same-order-provider", "tts_model": "same-voice", "renderer": "talkcraft-workflow/3", "style_version": "mg-semantic/2", "presentation_version": "film-design/1"}
	if !workflowProviderConfigCompatible(old, next) {
		t.Fatal("local renderer upgrade must allow original order recovery")
	}
	next["video"] = "different-provider"
	if workflowProviderConfigCompatible(old, next) {
		t.Fatal("must not query old order on a different provider")
	}
}
