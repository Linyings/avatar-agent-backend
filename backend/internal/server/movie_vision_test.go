package server

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/providers"
	"net/http/httptest"
	"testing"
)

func TestMovieVisionCapabilityFollowsConfiguredMainModel(t *testing.T) {
	for _, supported := range []bool{true, false} {
		cfg := config.Config{OpenAIProSupportsVision: supported, OpenAIProAPIKey: "test-key", OpenAIProAPIBaseURL: "https://example.test/v1", OpenAIProModel: "private-model"}
		a := &App{cfg: cfg, openai: providers.NewOpenAIClient(cfg, nil)}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		a.movieVisionCapability(c)
		var result map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["supports_vision"] != supported || result["available"] != true || result["input"] != "image" {
			t.Fatalf("wrong capability: %#v", result)
		}
		if len(result) != 4 || result["model"] != cfg.OpenAIProModel {
			t.Fatalf("expected model name and capability only, got %#v", result)
		}
	}
}

func TestMovieVisionCapabilityReportsVideoInputForDedicatedModel(t *testing.T) {
	cases := []struct {
		model string
		input string
		want  string
	}{{"qwen3.8-omni-flash", "", "video"}, {"qwen3-vl-flash", "", "image"}, {"qwen3-vl-flash", "VIDEO", "video"}, {"qwen3.8-omni-flash", "image", "image"}}
	for _, tc := range cases {
		cfg := config.Config{OpenAIProSupportsVision: false, MovieVisionAPIKey: "vision-key", MovieVisionAPIBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			MovieVisionModel: tc.model, MovieVisionInput: tc.input}
		a := &App{cfg: cfg, openai: providers.NewOpenAIClient(cfg, nil)}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		a.movieVisionCapability(c)
		var result map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["supports_vision"] != true || result["available"] != true || result["input"] != tc.want {
			t.Fatalf("%s input %q: wrong capability %#v", tc.model, tc.input, result)
		}
		if len(result) != 4 || result["model"] != tc.model {
			t.Fatalf("expected selected vision model name without endpoint or credentials: %#v", result)
		}
	}
}
