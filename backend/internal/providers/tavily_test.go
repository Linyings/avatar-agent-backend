package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

func TestTavilySearchUsesSafeCompactRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tavily-key" {
			t.Fatalf("authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["query"] != "庆余年 第二季 第1-3集 剧情 人物关系 主角 结局 电视剧" {
			t.Fatalf("query = %#v", payload["query"])
		}
		if payload["search_depth"] != "basic" || payload["max_results"] != float64(5) {
			t.Fatalf("search options = %#v", payload)
		}
		if payload["include_raw_content"] != false || payload["include_images"] != false || payload["include_answer"] != true {
			t.Fatalf("unsafe Tavily payload = %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query":  payload["query"],
			"answer": "范闲回京后卷入新的朝堂冲突。",
			"results": []map[string]any{{
				"title": "剧情介绍", "url": "https://example.com/story", "content": "范闲与庆帝、林婉儿等人的关系继续发展。",
			}},
		})
	}))
	defer server.Close()

	client := NewTavilyClient(config.Config{
		TavilyAPIKey:      "tavily-key",
		TavilyAPIBaseURL:  server.URL,
		TavilySearchDepth: "basic",
		TavilyMaxResults:  5,
		TavilyTimeout:     time.Second,
	})
	result, err := client.Search(context.Background(), "庆余年 第二季 第1-3集 剧情 人物关系 主角 结局 电视剧")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.Answer != "范闲回京后卷入新的朝堂冲突。" || len(result.Results) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestTavilySearchRejectsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewTavilyClient(config.Config{
		TavilyAPIKey:     "tavily-key",
		TavilyAPIBaseURL: server.URL,
		TavilyTimeout:    time.Second,
	})
	if _, err := client.Search(context.Background(), "query"); err == nil {
		t.Fatal("provider error should be returned")
	}
}

func TestTavilySearchTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"answer": "too late"})
	}))
	defer server.Close()

	client := NewTavilyClient(config.Config{
		TavilyAPIKey:     "tavily-key",
		TavilyAPIBaseURL: server.URL,
		TavilyTimeout:    10 * time.Millisecond,
	})
	if _, err := client.Search(context.Background(), "query"); err == nil {
		t.Fatal("timeout should be returned")
	}
}
