package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/narratoai/backend-go/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func generalTestRequest(purpose string) map[string]any {
	return map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-narration-v3", "purpose": purpose}}
}
func TestGeneralResponseValidation(t *testing.T) {
	for _, content := range []string{`{}`, `{"units":[`, `{"units":[]}`, `{"units":[{}]}`, `{"originals":[],"placeholder":true}`} {
		if !errors.Is(validateGeneralResponse(content, generalTestRequest("retrieve")), ErrGeneralResponseInvalid) {
			t.Fatalf("accepted malformed response: %.60s", content)
		}
	}
	for _, content := range []string{`{"issues":["chapter 3 has no cut"]}`, `{"units":[{"chapterId":1,"candidates":[{"subtitleIds":[1],"matchType":"context","reason":"same event"},{}]}]}`} {
		if err := validateGeneralResponse(content, generalTestRequest("retrieve")); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateGeneralResponse(`broken`, map[string]any{"model_operation": "movie_director_script"}); err != nil {
		t.Fatal("unrelated workflow changed")
	}
}
func TestGeneralWhitespaceGuard(t *testing.T) {
	g := generalResponseGuard{}
	if err := g.append(`{"units":[`); err != nil {
		t.Fatal(err)
	}
	if err := g.append(strings.Repeat(" ", 3000)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(g.append(strings.Repeat("\n", 1100)), ErrGeneralResponseInvalid) {
		t.Fatal("did not stop blank stream across chunks")
	}
	g = generalResponseGuard{}
	if !errors.Is(g.append(strings.Repeat("x", 512*1024+1)), ErrGeneralResponseInvalid) {
		t.Fatal("unbounded output")
	}
}
func TestGeneralInvalidStreamNeverCompletes(t *testing.T) {
	for _, content := range []string{`{"units":[`, strings.Repeat("\n", 5000)} {
		t.Run(fmt.Sprint(len(content)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				data, _ := json.Marshal(map[string]any{"id": "test-general", "choices": []any{map[string]any{"delta": map[string]any{"content": content}}}, "usage": map[string]int{"total_tokens": 10}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
			}))
			defer server.Close()
			client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "test"}, nil)
			completed := false
			err := client.Stream(context.Background(), "script_draft", generalTestRequest("retrieve"), func(event string, data map[string]any) error {
				if event == "completed" {
					completed = true
				}
				return nil
			})
			if !errors.Is(err, ErrGeneralResponseInvalid) || completed {
				t.Fatalf("err=%v completed=%v", err, completed)
			}
		})
	}
}

func TestGeneralIssueObjectNormalization(t *testing.T) {
	request := generalTestRequest("chapters")
	input := `{"chapters":[{"text":"原稿一字不改"}],"accepted":true,"issues":[{"chapterId":3,"message":"证据不足","reason":"没有对应动作","suggestion":"改用前因说明","severity":"high"},"另一条问题"]}`
	normalized, err := normalizeGeneralResponse(input, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGeneralResponse(normalized, request); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal([]byte(normalized), &out)
	text := out["issues"].([]any)[0].(string)
	for _, value := range []string{"chapterId=3", "证据不足", "没有对应动作", "改用前因说明", "severity=high"} {
		if !strings.Contains(text, value) {
			t.Fatalf("lost issue detail %s: %s", value, text)
		}
	}
	if out["chapters"].([]any)[0].(map[string]any)["text"] != "原稿一字不改" || len(out["issues"].([]any)) != 2 {
		t.Fatal("changed script or dropped issues")
	}
	for _, input := range []string{`{"issues":[""]}`, `{"issues":[null]}`, `{"issues":[{}]}`, `{"issues":[{"message":"问题","unknown":"不可丢弃"}]}`, `{"issues":[{"message":42}]}`, `{"issues":"问题"}`, `{"issues":[{"chapterId":[1],"message":"问题"}]}`} {
		if _, err := normalizeGeneralResponse(input, request); !errors.Is(err, ErrGeneralResponseInvalid) {
			t.Fatalf("accepted invalid issue: %s %v", input, err)
		}
	}
	original := `{"issues":[{"movie_only":true}]}`
	if got, err := normalizeGeneralResponse(original, map[string]any{"movie_prompt_version": "1"}); err != nil || got != original {
		t.Fatal("changed movie response")
	}
}

func TestGeneralStreamNormalizesIssuesBeforeCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := `{"issues":[{"chapterId":2,"message":"无法拆分","suggestion":"补充证据"}]}`
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": content}}}, "usage": map[string]int{"total_tokens": 10}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "test"}, nil)
	completed := false
	err := client.Stream(context.Background(), "script_draft", generalTestRequest("chapters"), func(event string, data map[string]any) error {
		if event == "completed" {
			completed = true
			result := data["metadata"].(map[string]any)["structured_result"].(map[string]any)
			if _, ok := result["issues"].([]any)[0].(string); !ok {
				t.Fatal("object leaked to client")
			}
		}
		return nil
	})
	if err != nil || !completed {
		t.Fatalf("err=%v completed=%v", err, completed)
	}
}

func TestGeneralNormalizationCannotHideWhitespaceOrFalseAcceptance(t *testing.T) {
	req := generalTestRequest("chapters")
	if _, err := normalizeGeneralResponse(`{"issues":[{"message":"problem"}]}`+strings.Repeat(" ", 4097), req); !errors.Is(err, ErrGeneralResponseInvalid) {
		t.Fatal("normalization bypassed guard")
	}
	normalized, err := normalizeGeneralResponse(`{"accepted":true,"issues":[{"message":"missing evidence"}]}`, req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal([]byte(normalized), &out)
	if len(out["issues"].([]any)) != 1 {
		t.Fatal("accepted=true erased rejection")
	}
	if err := validateGeneralResponse(`{"chapters":[],"issues":[]}`, req); !errors.Is(err, ErrGeneralResponseInvalid) {
		t.Fatal("empty issues bypassed chapter validation")
	}
}
