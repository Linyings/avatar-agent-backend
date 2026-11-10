package avatar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitMediaPostsBodyToRegisteredPathAndClassifiesResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v2/mureka-ai/mureka-v9/generate-bgm":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["prompt"] != "轻快" || r.Header.Get("Authorization") != "Bearer key" {
				t.Errorf("unexpected submit %#v", body)
			}
			_, _ = w.Write([]byte(`{"taskId":"m-1","status":"RUNNING"}`))
		case "/openapi/v2/rejects":
			_, _ = w.Write([]byte(`{"taskId":"","status":"","errorCode":"1007","errorMessage":"field 'n' is required, can not be empty"}`))
		case "/openapi/v2/query":
			_, _ = w.Write([]byte(`{"taskId":"m-1","status":"SUCCESS","results":[{"url":"https://cdn.example.com/a.mp3","outputType":"mp3"},{"url":"https://cdn.example.com/b.png","outputType":"png"}],"usage":{"thirdPartyConsumeMoney":"0.05"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := NewRunningHub(server.URL, "key")
	res, err := c.SubmitMedia(context.Background(), "/openapi/v2/mureka-ai/mureka-v9/generate-bgm", map[string]any{"prompt": "轻快"})
	if err != nil || res.TaskID != "m-1" {
		t.Fatalf("%#v %v", res, err)
	}
	done, err := c.QueryMedia(context.Background(), "m-1")
	if err != nil || done.MediaURL("audio") != "https://cdn.example.com/a.mp3" || done.MediaURL("image") != "https://cdn.example.com/b.png" || done.MediaURL("video") != "" {
		t.Fatalf("media classification wrong: %#v %v", done, err)
	}
	if _, err := c.SubmitMedia(context.Background(), "/openapi/v2/rejects", map[string]any{}); err == nil || !strings.Contains(err.Error(), "1007") || !strings.Contains(err.Error(), "field 'n' is required") || SubmissionUncertain(err) {
		t.Fatalf("a rejected submission must surface the provider reason and be certain: %v", err)
	}
	if _, err := c.SubmitMedia(context.Background(), "https://evil.example.com/x", map[string]any{}); err == nil {
		t.Fatal("non-RunningHub path accepted")
	}
}
