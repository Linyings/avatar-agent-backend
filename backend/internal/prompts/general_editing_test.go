package prompts

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestGeneralOriginalBoundaryCatalogueExcludesCutsInsideDialogueWithoutMutatingRequest(t *testing.T) {
	for _, purpose := range []string{"ground", "repair_grounding"} {
		t.Run(purpose, func(t *testing.T) {
			ctx := map[string]any{"contract": "general-editing-v2", "purpose": purpose, "unit": map[string]any{"id": 17, "role": "original"},
				"boundaries": []int64{858000, 858180, 858220, 858221, 858590, 860899, 860900, 860940, 861990},
				"evidence": []map[string]any{
					{"kind": "dialogue", "startMs": 858180, "endMs": 860940, "text": "瘸爷爷，你以为我还会上当吗？"},
					{"kind": "event", "startMs": 0, "endMs": 1000000},
				},
			}
			before, _ := json.Marshal(ctx)
			_, user, _ := generalEditingOperationPrompt("script_draft", map[string]any{"model_operation": "narration_outline", "video_context": ctx})
			var input struct {
				Boundaries []int64 `json:"boundaries"`
			}
			if err := json.Unmarshal([]byte(strings.Split(user, "\nINPUT:\n")[1]), &input); err != nil {
				t.Fatal(err)
			}
			want := []int64{858000, 858180, 858220, 860900, 860940, 861990}
			if !reflect.DeepEqual(input.Boundaries, want) {
				t.Fatalf("allowed=%v want=%v", input.Boundaries, want)
			}
			after, _ := json.Marshal(ctx)
			if string(before) != string(after) {
				t.Fatal("prompt preparation mutated the identity/billing request")
			}
		})
	}
}

func TestGeneralOriginalBoundaryCatalogueNeverInventsEndpointOrFiltersNarration(t *testing.T) {
	ctx := map[string]any{"unit": map[string]any{"role": "original"}, "boundaries": []int64{858590},
		"evidence": []map[string]any{{"kind": "dialogue", "startMs": 858180, "endMs": 860940}}}
	prepared := generalOriginalSafeBoundaries(ctx)
	if cuts := prepared["boundaries"].([]int64); len(cuts) != 0 {
		t.Fatalf("invented or retained unsafe cut: %v", cuts)
	}
	ctx["unit"].(map[string]any)["role"] = "narration"
	prepared = generalOriginalSafeBoundaries(ctx)
	if !reflect.DeepEqual(prepared["boundaries"], []int64{858590}) {
		t.Fatal("narration unnecessarily constrained by source audio")
	}
}

func TestGeneralEditingVerificationEchoesRequestedUnitInsteadOfExampleOrContext(t *testing.T) {
	for _, purpose := range []string{"ground", "review_text"} {
		for _, id := range []int{1, 4, 23} {
			t.Run(fmt.Sprintf("%s-%d", purpose, id), func(t *testing.T) {
				field := "unit"
				if purpose == "review_text" {
					field = "originalUnit"
				}
				req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{
					"contract": "general-editing-v2", "purpose": purpose,
					field:                map[string]any{"id": id},
					"invalidResult":      map[string]any{"id": 1, "verified": true, "issues": []any{}},
					"establishedContext": []any{map[string]any{"id": 1}},
				}}
				_, user, ok := generalEditingOperationPrompt("script_draft", req)
				rules := strings.Split(user, "\nINPUT:\n")[0]
				if !ok || !strings.Contains(rules, fmt.Sprintf(`Return {"id":%d,`, id)) || strings.Contains(rules, "__UNIT_ID__") {
					t.Fatalf("wrong response schema: %s", rules)
				}
				if !strings.Contains(rules, "INPUT."+field+".id exactly") || !strings.Contains(rules, "BOTH successful and failed") {
					t.Fatal("missing exact identity requirement for failure/repair paths")
				}
				if id != 1 && strings.Contains(rules, `"id":1,`) {
					t.Fatal("fixed example ID still competes with actual unit")
				}
			})
		}
	}
}

func TestGeneralEditingPromptFingerprintTracksEffectiveInstructions(t *testing.T) {
	req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-editing-v2", "purpose": "ground", "unit": map[string]any{"id": 4}}}
	first := GeneralEditingPromptFingerprint("script_draft", req)
	if len(first) != 64 || first != GeneralEditingPromptFingerprint("script_draft", req) {
		t.Fatal("prompt identity is missing or unstable")
	}
	req["video_context"].(map[string]any)["purpose"] = "review_text"
	if first == GeneralEditingPromptFingerprint("script_draft", req) {
		t.Fatal("different effective instructions share an identity")
	}
	if GeneralEditingPromptFingerprint("script_match", req) != "" {
		t.Fatal("unrelated mode has a general prompt identity")
	}
}

func TestGeneralEditingDistinguishesSpeechEvidenceFromPhysicalOutcomes(t *testing.T) {
	for _, purpose := range []string{"ground", "repair_grounding", "review_text"} {
		req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{
			"contract": "general-editing-v2", "purpose": purpose, "unit": map[string]any{"id": 10, "role": "original"},
		}}
		_, user, _ := generalEditingOperationPrompt("script_draft", req)
		for _, rule := range []string{"Sampled still frames cannot disprove", "speaker attribution must also be supported", "does NOT prove the truth", "Physical actions and results still require their own observed evidence", "NOT a new audio transcript", "a subtitle-reading discrepancy alone is not proof"} {
			if !strings.Contains(user, rule) {
				t.Fatalf("%s lacks evidence distinction: %s", purpose, rule)
			}
		}
		if purpose == "ground" && !strings.Contains(user, `{"id":10,"verified":false,"issues":["specific reason"],"candidates":[]}`) {
			t.Fatal("failure schema omits verification or uses the wrong unit")
		}
	}
}

func TestGeneralEditingRepairKeepsOriginalAndNarrationAudioFieldsExclusive(t *testing.T) {
	for _, role := range []string{"original", "narration"} {
		req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{
			"contract": "general-editing-v2", "purpose": "repair_grounding", "unit": map[string]any{"id": 10, "role": role},
		}}
		_, user, _ := generalEditingOperationPrompt("script_draft", req)
		expected := `role="original", text="" (an empty string), and a nonempty org`
		if role == "narration" {
			expected = `role="narration", a nonempty text, and org="" (an empty string)`
		}
		if !strings.Contains(user, expected) {
			t.Fatalf("missing locked role fields: %s", role)
		}
	}
}

func TestGeneralEditingFitSeparatesMachineVerificationFromUserApproval(t *testing.T) {
	req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{
		"contract": "general-editing-v2", "purpose": "fit_text", "unit": map[string]any{"id": 1, "role": "narration", "verified": true},
	}}
	_, prompt, ok := generalEditingOperationPrompt("script_draft", req)
	for _, rule := range []string{"Verification is NOT user approval", "Do not refuse because verified=true", `userApproved=true`, `scriptPolicy="as-saved"`, "retain the facts"} {
		if !ok || !strings.Contains(prompt, rule) {
			t.Fatalf("fit prompt lacks approval distinction: %s", rule)
		}
	}
}

func TestGeneralEditingVersionGate(t *testing.T) {
	for _, purpose := range []string{"plan", "ground", "repair_grounding", "fit_text", "review_text"} {
		req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-editing-v2", "purpose": purpose}}
		system, user, ok := generalEditingOperationPrompt("script_draft", req)
		if !ok || !strings.Contains(system, "registered evidence") || !strings.Contains(user, "INPUT:") {
			t.Fatalf("missing contract: %s", purpose)
		}
		if strings.Contains(user, "20–40") || strings.Contains(user, "narrationBlockEditingPrompt") {
			t.Fatal("legacy block policy leaked")
		}
		if _, _, ok := generalEditingOperationPrompt("script_match", req); ok {
			t.Fatal("wrong operation was intercepted")
		}
	}
	for _, ctx := range []map[string]any{{}, {"purpose": "movie_agent_chat"}, {"contract": "general-editing-v1", "purpose": "plan"}, {"contract": "general-editing-v2", "purpose": "unknown"}} {
		if _, _, ok := generalEditingOperationPrompt("script_draft", map[string]any{"model_operation": "narration_outline", "video_context": ctx}); ok {
			t.Fatalf("intercepted legacy/unknown contract: %v", ctx)
		}
	}
}

func TestGeneralEditingRequiresRealEvidenceAndCompleteAudio(t *testing.T) {
	_, planning, _ := generalEditingOperationPrompt("script_draft", map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-editing-v2", "purpose": "plan"}})
	for _, rule := range []string{"playback TIME", "ONE semantic", "complete registered dialogue", "key actions AND their results", "no fixed block length"} {
		if !strings.Contains(planning, rule) {
			t.Fatalf("missing planning requirement %s", rule)
		}
	}
	_, grounding, _ := generalEditingOperationPrompt("script_draft", map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-editing-v2", "purpose": "ground"}})
	for _, rule := range []string{"standing cow cannot prove it fell", "verified=false", "EXACTLY unchanged", "Use only supplied boundaries"} {
		if !strings.Contains(grounding, rule) {
			t.Fatalf("missing grounding requirement %s", rule)
		}
	}
}
