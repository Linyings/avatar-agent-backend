package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

const (
	defaultTavilyBaseURL    = "https://api.tavily.com"
	defaultTavilyDepth      = "basic"
	defaultTavilyMaxResults = 5
	defaultTavilyTimeout    = 20 * time.Second
)

type TavilySearchResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score,omitempty"`
}

type TavilySearchResponse struct {
	Query   string               `json:"query"`
	Answer  string               `json:"answer"`
	Results []TavilySearchResult `json:"results"`
}

type TavilyError struct {
	StatusCode int
	Message    string
}

func (e *TavilyError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("Tavily request failed with HTTP %d: %s", e.StatusCode, e.Message)
	}
	return "Tavily request failed: " + e.Message
}

type TavilyClient struct {
	apiKey      string
	baseURL     string
	searchDepth string
	maxResults  int
	httpClient  *http.Client
}

func NewTavilyClient(cfg config.Config) TavilyClient {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.TavilyAPIBaseURL), "/")
	if baseURL == "" {
		baseURL = defaultTavilyBaseURL
	}
	depth := strings.TrimSpace(cfg.TavilySearchDepth)
	if depth == "" {
		depth = defaultTavilyDepth
	}
	maxResults := cfg.TavilyMaxResults
	if maxResults <= 0 {
		maxResults = defaultTavilyMaxResults
	}
	if maxResults > 10 {
		maxResults = 10
	}
	timeout := cfg.TavilyTimeout
	if timeout <= 0 {
		timeout = defaultTavilyTimeout
	}
	return TavilyClient{
		apiKey:      strings.TrimSpace(cfg.TavilyAPIKey),
		baseURL:     baseURL,
		searchDepth: depth,
		maxResults:  maxResults,
		httpClient:  &http.Client{Timeout: timeout},
	}
}

func (c TavilyClient) Configured() bool {
	return c.apiKey != "" && c.baseURL != ""
}

func (c TavilyClient) Search(ctx context.Context, query string) (TavilySearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return TavilySearchResponse{}, &TavilyError{Message: "query is empty"}
	}
	if !c.Configured() {
		return TavilySearchResponse{}, &TavilyError{Message: "API key is not configured"}
	}
	payload, err := json.Marshal(map[string]any{
		"query":               query,
		"search_depth":        c.searchDepth,
		"topic":               "general",
		"max_results":         c.maxResults,
		"include_answer":      true,
		"include_raw_content": false,
		"include_images":      false,
	})
	if err != nil {
		return TavilySearchResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/search", bytes.NewReader(payload))
	if err != nil {
		return TavilySearchResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TavilySearchResponse{}, &TavilyError{Message: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return TavilySearchResponse{}, &TavilyError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}
	var result TavilySearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return TavilySearchResponse{}, &TavilyError{Message: "invalid JSON response"}
	}
	return result, nil
}
