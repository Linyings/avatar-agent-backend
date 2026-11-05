package avatar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPVideoGatewayWithAnotherVendor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer another-key" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/media":
			w.Write([]byte(`{"url":"https://cdn.example.com/input.png"}`))
		case "/videos":
			var in GenerationRequest
			json.NewDecoder(r.Body).Decode(&in)
			if in.Model != "other-model" || len(in.AudioURLs) != 1 || len(in.ImageURLs) != 1 {
				t.Error("lost custom model or references")
			}
			w.Write([]byte(`{"id":"other-job"}`))
		case "/videos/other-job":
			if r.Method != "GET" {
				t.Error("query must use GET")
			}
			w.Write([]byte(`{"id":"other-job","status":"completed","url":"https://cdn.example.com/out.mp4","usage":{"seconds":5}}`))
		default:
			t.Error("unexpected path")
		}
	}))
	defer server.Close()
	p, err := NewVideoProvider(Options{Provider: "another-vendor", Protocol: "http", BaseURL: server.URL, APIKey: "another-key", Model: "other-model", SupportsReferenceAudio: true})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(file, []byte("test"), 0600)
	ref, err := p.Upload(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Submit(context.Background(), GenerationRequest{Prompt: "test", Duration: "5", ImageURLs: []string{ref}, AudioURLs: []string{"https://cdn.example.com/a.wav"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err = p.Query(context.Background(), result.TaskID)
	if err != nil || result.Status != "SUCCESS" || result.VideoURL() == "" {
		t.Fatalf("query %+v %v", result, err)
	}
}
