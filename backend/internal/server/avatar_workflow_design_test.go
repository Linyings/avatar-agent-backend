package server

import (
	"strings"
	"testing"
)

func TestWorkflowLocalDesignUpgradeDoesNotInvalidateProviderOrders(t *testing.T) {
	old := map[string]string{"video": "same-order-provider", "tts_model": "same-voice", "renderer": "talkcraft-workflow/2", "style_version": "mg-native/1"}
	next := map[string]string{"video": "same-order-provider", "tts_model": "same-voice", "renderer": "talkcraft-workflow/3", "style_version": "mg-semantic/2", "presentation_version": "film-design/1", "skills": "talking-head-storyboard@0123456789ab"}
	if !workflowProviderConfigCompatible(old, next) {
		t.Fatal("local renderer upgrade must allow original order recovery")
	}
	snapshot := newTestApp(t).avatarWorkflow.configSnapshot()
	if !strings.HasPrefix(snapshot["skills"], "talking-head-storyboard@") {
		t.Fatalf("run must record the skill version it was produced with: %q", snapshot["skills"])
	}
	edited := map[string]string{}
	for k, v := range snapshot {
		edited[k] = v
	}
	edited["skills"] = "talking-head-storyboard@ffffffffffff"
	if workflowConfigID(edited) != workflowConfigID(snapshot) {
		t.Fatal("editing skill guidance must not invalidate cached planning or restart runs")
	}
	delete(edited, "skills")
	if workflowConfigID(edited) != workflowConfigID(snapshot) {
		t.Fatal("runs recorded before skill digests must keep their config identity")
	}
	next["video"] = "different-provider"
	if workflowProviderConfigCompatible(old, next) {
		t.Fatal("must not query old order on a different provider")
	}
}
