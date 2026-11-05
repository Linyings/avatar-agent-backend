package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/narratoai/backend-go/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMovieVisionUsesMainProModelWithImageContent(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "custom-main-deepseek" || r.Header.Get("Authorization") != "Bearer main-key" {
			t.Fatalf("wrong model or key")
		}
		messages := body["messages"].([]any)
		content := messages[1].(map[string]any)["content"].([]any)
		if content[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/jpeg;base64,/9j/" {
			t.Fatalf("missing image: %#v", content)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"visible evidence\\\"}\"}}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10,\"total_tokens\":30}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "main-key", OpenAIProAPIBaseURL: upstream.URL, OpenAIProModel: "custom-main-deepseek", OpenAIFlashModel: "wrong-model"}, nil)
	var completed map[string]any
	err := client.Stream(context.Background(), "movie_vision", map[string]any{"prompt": "Read the image", "image_data_url": "data:image/jpeg;base64,/9j/"}, func(event string, value map[string]any) error {
		if event == "completed" {
			completed = value
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || completed["content"] != `{"summary":"visible evidence"}` {
		t.Fatalf("unexpected completion: %#v, calls=%d", completed, calls)
	}
}

func TestMovieVisionRejectsInvalidOrRemoteImages(t *testing.T) {
	for _, image := range []string{"", "https://example.com/private", "data:image/jpeg;base64,invalid", "data:image/jpeg;base64,aGVsbG8="} {
		if _, err := MovieVisionMessages(map[string]any{"prompt": "x", "image_data_url": image}); err == nil {
			t.Fatalf("accepted invalid image %q", image)
		}
	}
}

// mp4Clip is the smallest byte string the validator accepts as an MP4: an ftyp box header.
var mp4Clip = "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0})

func TestMovieVisionUsesDedicatedVisionModelWithVideoClip(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("Authorization") != "Bearer vision-key" {
			t.Fatalf("wrong key %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{}\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "main-key", OpenAIProAPIBaseURL: "https://pro.example/v1", OpenAIProModel: "pro-model",
		MovieVisionAPIKey: "vision-key", MovieVisionAPIBaseURL: upstream.URL, MovieVisionModel: "qwen3.8-omni-flash", MovieVisionInput: "video",
		MovieVisionThinkingType: "disabled", MovieVisionReasoningEffort: "medium"}, nil)
	if err := client.Stream(context.Background(), "movie_vision", map[string]any{"prompt": "看视频", "video_data_url": mp4Clip}, func(string, map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "qwen3.8-omni-flash" {
		t.Fatalf("vision model not used: %#v", body["model"])
	}
	content := body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["video_url"].(map[string]any)["url"] != mp4Clip {
		t.Fatalf("missing clip: %#v", content)
	}
	if modalities, _ := body["modalities"].([]any); len(modalities) != 1 || modalities[0] != "text" {
		t.Fatalf("omni must be asked for text only: %#v", body["modalities"])
	}
}

func TestDashScopeVisionProfileTurnsThinkingOffWithoutReasoningEffort(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	profile := openAIModelProfile{name: openAIProfileVision, apiBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", thinkingType: "disabled", reasoningEffort: "medium"}
	payload := map[string]any{}
	client.applyReasoningOptions(payload, profile, "movie_vision", map[string]any{})
	dropReasoningEffortWithoutThinking(payload)
	if payload["enable_thinking"] != false {
		t.Fatalf("thinking not switched off: %#v", payload)
	}
	if _, set := payload["reasoning_effort"]; set {
		t.Fatalf("DashScope rejects reasoning_effort when thinking is off: %#v", payload)
	}
	enabled := map[string]any{"enable_thinking": true, "reasoning_effort": "medium"}
	dropReasoningEffortWithoutThinking(enabled)
	if enabled["reasoning_effort"] != "medium" {
		t.Fatalf("reasoning_effort must stay when thinking is on")
	}
}

func TestMovieVisionKeepsProModelWhenNoVisionModelIsConfigured(t *testing.T) {
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "main-key", OpenAIProAPIBaseURL: "https://pro.example/v1", OpenAIProModel: "pro-model",
		OpenAIFlashAPIKey: "flash-key", OpenAIFlashAPIBaseURL: "https://flash.example/v1", OpenAIFlashModel: "flash-model"}, nil)
	if model := client.ModelFor("movie_vision", nil); model != "pro-model" {
		t.Fatalf("fallback must stay on Pro, got %q", model)
	}
	vision := NewOpenAIClient(config.Config{OpenAIProAPIKey: "main-key", OpenAIProAPIBaseURL: "https://pro.example/v1", OpenAIProModel: "pro-model",
		MovieVisionAPIKey: "k", MovieVisionAPIBaseURL: "https://vision.example/v1", MovieVisionModel: "vision-model"}, nil)
	if model := vision.ModelFor("movie_vision", nil); model != "vision-model" {
		t.Fatalf("dedicated vision model not chosen, got %q", model)
	}
	if model := vision.ModelFor("script", nil); model != "pro-model" {
		t.Fatalf("the vision model must not take over other steps, got %q", model)
	}
}

func TestMovieVisionRejectsInvalidVideoClips(t *testing.T) {
	for _, video := range []string{"data:video/mp4;base64,aGVsbG8=", "https://example.com/clip.mp4", "data:video/webm;base64,AAAA",
		"data:video/mp4;base64," + strings.Repeat("A", maxMovieVisionVideoDataURL)} {
		if _, err := MovieVisionMessages(map[string]any{"prompt": "x", "video_data_url": video}); err == nil {
			t.Fatalf("accepted invalid clip %.60q", video)
		}
	}
	if _, err := MovieVisionMessages(map[string]any{"prompt": "x", "video_data_url": mp4Clip, "image_data_url": "data:image/jpeg;base64,/9j/"}); err == nil {
		t.Fatalf("accepted image and video together")
	}
	if messages, err := MovieVisionMessages(map[string]any{"prompt": "x", "video_data_url": mp4Clip}); err != nil || len(messages) != 2 {
		t.Fatalf("valid clip rejected: %v", err)
	}
}

func TestMovieVisionCapsOutputAndKeepsThinkingOffWithoutBudget(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{}\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "main-key", OpenAIProAPIBaseURL: "https://pro.example/v1", OpenAIProModel: "pro-model",
		MovieVisionAPIKey: "vision-key", MovieVisionAPIBaseURL: upstream.URL, MovieVisionModel: "qwen3.8-omni-flash", MovieVisionInput: "video",
		MovieVisionThinkingType: "disabled"}, nil)
	if err := client.Stream(context.Background(), "movie_vision", map[string]any{"prompt": "看视频", "video_data_url": mp4Clip}, func(string, map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(movieVisionMaxOutputTokens) {
		t.Fatalf("vision output not capped: %#v", body["max_tokens"])
	}
	if _, set := body["thinking_budget"]; set {
		t.Fatalf("thinking budget sent without configuration: %#v", body)
	}
}

func TestMovieVisionThinkingBudgetTurnsDashScopeThinkingOn(t *testing.T) {
	vision := openAIModelProfile{name: openAIProfileVision, apiBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"}
	payload := map[string]any{"enable_thinking": false, "reasoning_effort": "none"}
	applyMovieVisionOutputOptions(payload, vision, 4060)
	if payload["enable_thinking"] != true || payload["thinking_budget"] != 4060 || payload["max_tokens"] != movieVisionMaxOutputTokens {
		t.Fatalf("thinking not enabled with budget: %#v", payload)
	}
	if _, set := payload["reasoning_effort"]; set {
		t.Fatalf("reasoning_effort must not accompany thinking_budget: %#v", payload)
	}
	for name, profile := range map[string]openAIModelProfile{
		"non-dashscope": {name: openAIProfileVision, apiBaseURL: "https://pro.example/v1"},
		"pro fallback":  {name: openAIProfilePro, apiBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	} {
		other := map[string]any{"enable_thinking": false}
		applyMovieVisionOutputOptions(other, profile, 4060)
		if other["enable_thinking"] != false || other["max_tokens"] != movieVisionMaxOutputTokens {
			t.Fatalf("%s: budget must only apply to the DashScope vision profile: %#v", name, other)
		}
		if _, set := other["thinking_budget"]; set {
			t.Fatalf("%s: unexpected thinking_budget: %#v", name, other)
		}
	}
}
