package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/providers"
)

type stubStorySearchClient struct {
	configured bool
	calls      atomic.Int64
	queriesMu  sync.Mutex
	queries    []string
	result     providers.TavilySearchResponse
	err        error
	searchFn   func(string) (providers.TavilySearchResponse, error)
}

func (c *stubStorySearchClient) Configured() bool { return c.configured }

func (c *stubStorySearchClient) Search(_ context.Context, query string) (providers.TavilySearchResponse, error) {
	c.calls.Add(1)
	c.queriesMu.Lock()
	c.queries = append(c.queries, query)
	c.queriesMu.Unlock()
	if c.searchFn != nil {
		return c.searchFn(query)
	}
	return c.result, c.err
}

func TestBuildStorySearchQueryIncludesOnlyProvidedMetadata(t *testing.T) {
	query := buildStorySearchQuery(storyContextSearchRequest{
		Title:          "庆余年",
		Season:         "第二季",
		EpisodeRange:   "第1-3集",
		ReleaseVersion: "2024",
		Protagonist:    "范闲",
	})
	want := "庆余年 第二季 第1-3集 2024 主角 范闲 剧情 人物关系 主角 结局 电视剧"
	if query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}
}

func TestFormatStoryVersionCandidatesDeduplicatesAndSanitizes(t *testing.T) {
	candidates := formatStoryVersionCandidates([]providers.TavilySearchResult{
		{Title: "福贵（2005年电视剧）", URL: "https://example.com/2005", Content: "陈创主演。"},
		{Title: "福贵（2005年电视剧）", URL: "https://example.com/2005", Content: "重复来源。"},
		{Title: "活着（1994年电影）", URL: "javascript:alert(1)", Content: strings.Repeat("电影版本", 300)},
	})
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[1].SourceURL != "" {
		t.Fatalf("unsafe URL retained: %#v", candidates[1])
	}
	if len([]rune(candidates[1].Description)) > 500 {
		t.Fatal("description was not trimmed")
	}
	if candidates[0].ID == "" || candidates[0].ID == candidates[1].ID {
		t.Fatal("candidate IDs are invalid")
	}
}

func TestFormatStoryVersionCandidatesRejectsEpisodeOnlyResults(t *testing.T) {
	tests := []struct {
		name    string
		results []providers.TavilySearchResult
		want    []string
	}{
		{
			name: "rejects episode markers in titles and URLs",
			results: []providers.TavilySearchResult{
				{Title: "福贵 第01集", URL: "https://example.com/watch/1"},
				{Title: "福贵 EP01", URL: "https://example.com/ep-01"},
				{Title: "福贵 Episode 1", URL: "https://example.com/episode/1"},
				{Title: "福贵第1集", URL: "https://example.com/watch"},
				{Title: "福贵03 在线观看", URL: "https://example.com/e3"},
				{Title: "福贵（2005年电视剧）", URL: "https://example.com/series"},
			},
			want: []string{"福贵（2005年电视剧）"},
		},
		{
			name: "keeps year-bearing series labels before viewer terms",
			results: []providers.TavilySearchResult{
				{Title: "福贵 2005 在线观看", URL: "https://example.com/series-2005"},
				{Title: "福贵（2005年电视剧）", URL: "https://example.com/series"},
			},
			want: []string{"福贵 2005 在线观看", "福贵（2005年电视剧）"},
		},
		{
			name: "rejects episode path and query forms",
			results: []providers.TavilySearchResult{
				{Title: "福贵在线播放", URL: "https://example.com/series/episode-12"},
				{Title: "福贵在线播放", URL: "https://example.com/watch?episode=12"},
				{Title: "福贵在线播放", URL: "https://example.com/watch?ep=12"},
				{Title: "福贵在线播放", URL: "https://example.com/episode?id=12"},
				{Title: "福贵在线播放", URL: "https://example.com/ep?id=12"},
				{Title: "福贵在线播放", URL: "https://example.com/watch?episodeId=12"},
				{Title: "福贵在线播放", URL: "https://example.com/watch?epId=12"},
				{Title: "福贵（2005年电视剧）", URL: "https://example.com/series"},
			},
			want: []string{"福贵（2005年电视剧）"},
		},
		{
			name: "keeps overview when only its snippet mentions an episode",
			results: []providers.TavilySearchResult{
				{Title: "福贵（2005年电视剧）", URL: "https://example.com/series", Content: "第1集讲述福贵的少年经历。"},
				{Title: "活着（1994年电影）", URL: "https://example.com/movie"},
			},
			want: []string{"福贵（2005年电视剧）", "活着（1994年电影）"},
		},
		{
			name: "preserves eligible result order after filtering",
			results: []providers.TavilySearchResult{
				{Title: "福贵 第2集", URL: "https://example.com/episode/2"},
				{Title: "福贵（2005年电视剧）", URL: "https://example.com/series"},
				{Title: "活着（1994年电影）", URL: "https://example.com/movie"},
			},
			want: []string{"福贵（2005年电视剧）", "活着（1994年电影）"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates := formatStoryVersionCandidates(test.results)
			if len(candidates) != len(test.want) {
				t.Fatalf("candidates = %#v, want labels %q", candidates, test.want)
			}
			for index, want := range test.want {
				if candidates[index].Label != want {
					t.Fatalf("candidate %d = %q, want %q", index, candidates[index].Label, want)
				}
			}
		})
	}
}

func TestBuildStoryVersionSearchQueryIncludesDisambiguationTerms(t *testing.T) {
	query := buildStoryVersionSearchQuery(storyContextSearchRequest{
		Title:        "福贵",
		Season:       "第一季",
		EpisodeRange: "第1-3集",
		Protagonist:  "福贵",
		Notes:        "中国大陆",
		Language:     "zh-CN",
	})
	for _, term := range []string{"福贵", "第一季", "第1-3集", "中国大陆", "同名影视作品", "版本", "年份", "导演", "主演", "电影", "电视剧", "动画"} {
		if !strings.Contains(query, term) {
			t.Fatalf("query %q does not contain %q", query, term)
		}
	}
}

func TestStoryVersionSearchServiceCachesIdenticalRequests(t *testing.T) {
	client := &stubStorySearchClient{configured: true, result: providers.TavilySearchResponse{
		Query: "福贵 版本", Results: []providers.TavilySearchResult{{Title: "福贵（2005年电视剧）", URL: "https://example.com", Content: "陈创主演。"}},
	}}
	service := newStoryContextSearchService(client)
	userID := uuid.New()
	req := storyContextSearchRequest{Title: "福贵", Language: "zh-CN"}

	first, err := service.discoverVersions(context.Background(), userID, req)
	if err != nil || first.Cached {
		t.Fatalf("first discovery = %#v, %v", first, err)
	}
	second, err := service.discoverVersions(context.Background(), userID, req)
	if err != nil || !second.Cached {
		t.Fatalf("cached discovery = %#v, %v", second, err)
	}
	if client.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", client.calls.Load())
	}
}

func TestStoryVersionSearchSharesUncachedRateLimit(t *testing.T) {
	client := &stubStorySearchClient{configured: true, result: providers.TavilySearchResponse{Answer: "ok"}}
	service := newStoryContextSearchService(client)
	userID := uuid.New()
	for index := 0; index < storySearchRateLimit-1; index++ {
		_, err := service.search(context.Background(), userID, storyContextSearchRequest{Title: fmt.Sprintf("剧情%d", index)})
		if err != nil {
			t.Fatalf("story request %d: %v", index, err)
		}
	}
	if _, err := service.discoverVersions(context.Background(), userID, storyContextSearchRequest{Title: "最后一次"}); err != nil {
		t.Fatalf("tenth request: %v", err)
	}
	if _, err := service.discoverVersions(context.Background(), userID, storyContextSearchRequest{Title: "超限"}); !errors.Is(err, errStorySearchRateLimited) {
		t.Fatalf("eleventh request error = %v", err)
	}
}

func TestFormatStorySearchContextTrimsSourcesAndTotalLength(t *testing.T) {
	result := providers.TavilySearchResponse{
		Query:  "庆余年",
		Answer: strings.Repeat("剧情", 2000),
		Results: []providers.TavilySearchResult{{
			Title: strings.Repeat("标题", 100), URL: "https://example.com/story", Content: strings.Repeat("人物关系", 500),
		}},
	}
	contextText, sources := formatStorySearchContext(result, 6000)
	if len([]rune(contextText)) > 6000 {
		t.Fatalf("context length = %d", len([]rune(contextText)))
	}
	if len(sources) != 1 || len([]rune(sources[0].Title)) > 120 || len([]rune(sources[0].Snippet)) > 700 {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestFormatStorySearchContextDropsUnsafeSourceURLs(t *testing.T) {
	_, sources := formatStorySearchContext(providers.TavilySearchResponse{
		Results: []providers.TavilySearchResult{{Title: "异常来源", URL: "javascript:alert(1)", Content: "摘要"}},
	}, 6000)
	if len(sources) != 1 || sources[0].URL != "" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestStorySearchServiceCachesIdenticalRequestsBeforeRateLimit(t *testing.T) {
	client := &stubStorySearchClient{configured: true, result: providers.TavilySearchResponse{
		Query: "庆余年", Answer: "范闲回京后卷入新的朝堂冲突。",
	}}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	service := newStoryContextSearchService(client)
	service.now = func() time.Time { return now }
	userID := uuid.New()
	req := storyContextSearchRequest{Title: "庆余年", Season: "第二季"}

	first, err := service.search(context.Background(), userID, req)
	if err != nil || first.Cached {
		t.Fatalf("first search = %#v, %v", first, err)
	}
	second, err := service.search(context.Background(), userID, req)
	if err != nil || !second.Cached {
		t.Fatalf("cached search = %#v, %v", second, err)
	}
	if client.calls.Load() != int64(len(storyResearchSections)) {
		t.Fatalf("provider calls = %d, want %d", client.calls.Load(), len(storyResearchSections))
	}
}

func TestStorySearchServiceCanForceRefreshAndReplaceTheCachedResponse(t *testing.T) {
	client := &stubStorySearchClient{configured: true}
	client.searchFn = func(query string) (providers.TavilySearchResponse, error) {
		return providers.TavilySearchResponse{Answer: fmt.Sprintf("fresh result %d", client.calls.Load())}, nil
	}
	service := newStoryContextSearchService(client)
	userID := uuid.New()
	req := storyContextSearchRequest{Title: "神厨小福贵", Language: "zh-CN"}

	first, err := service.search(context.Background(), userID, req)
	if err != nil || first.Cached {
		t.Fatalf("first search = %#v, %v", first, err)
	}
	refreshedRequest := req
	refreshedRequest.ForceRefresh = true
	refreshed, err := service.search(context.Background(), userID, refreshedRequest)
	if err != nil || refreshed.Cached {
		t.Fatalf("forced search = %#v, %v", refreshed, err)
	}
	if refreshed.KnowledgeBase.WorkIdentity == first.KnowledgeBase.WorkIdentity {
		t.Fatalf("forced search reused the old result: first=%q refreshed=%q", first.KnowledgeBase.WorkIdentity, refreshed.KnowledgeBase.WorkIdentity)
	}
	second, err := service.search(context.Background(), userID, req)
	if err != nil || !second.Cached || second.KnowledgeBase.WorkIdentity != refreshed.KnowledgeBase.WorkIdentity {
		t.Fatalf("follow-up search = %#v, %v; want refreshed cache", second, err)
	}
	if got, want := client.calls.Load(), int64(len(storyResearchSections)*2); got != want {
		t.Fatalf("provider calls = %d, want %d", got, want)
	}
}

func TestFormatDeepStoryKnowledgeBaseDistributesSourcesAcrossSearchSections(t *testing.T) {
	results := make([]storyResearchResult, len(storyResearchSections))
	for index, section := range storyResearchSections {
		results[index] = storyResearchResult{Index: index, Section: section, Response: providers.TavilySearchResponse{
			Answer: section.Heading,
			Results: []providers.TavilySearchResult{
				{Title: section.Heading + " 资料 A", URL: fmt.Sprintf("https://example.com/%d/a", index), Content: section.Heading},
				{Title: section.Heading + " 资料 B", URL: fmt.Sprintf("https://example.com/%d/b", index), Content: section.Heading},
			},
		}}
	}
	_, sources, _, _, _ := formatDeepStoryKnowledgeBase(results)
	if len(sources) != 5 {
		t.Fatalf("source count = %d, want 5", len(sources))
	}
	for index, want := range storyResearchSections[:5] {
		if sources[index].Section != want.Key {
			t.Fatalf("source %d section = %q, want %q", index, sources[index].Section, want.Key)
		}
	}
}

func TestStorySearchServiceLimitsUncachedRequestsPerUser(t *testing.T) {
	client := &stubStorySearchClient{configured: true, result: providers.TavilySearchResponse{Answer: "ok"}}
	service := newStoryContextSearchService(client)
	userID := uuid.New()
	for index := 0; index < 10; index++ {
		_, err := service.search(context.Background(), userID, storyContextSearchRequest{Title: "作品" + string(rune('A'+index))})
		if err != nil {
			t.Fatalf("request %d: %v", index+1, err)
		}
	}
	if _, err := service.search(context.Background(), userID, storyContextSearchRequest{Title: "第十一个作品"}); err != errStorySearchRateLimited {
		t.Fatalf("eleventh request error = %v", err)
	}
}

func TestNormalizeStoryContextSearchRequestValidatesSelectedVersion(t *testing.T) {
	valid, err := normalizeStoryContextSearchRequest(storyContextSearchRequest{
		Title: " 福贵 ",
		SelectedVersion: &storyVersionCandidate{
			ID: " candidate-1 ", Label: " 福贵（2005年电视剧） ", Description: " 陈创主演。 ", SourceURL: " https://example.com/2005 ",
		},
	})
	if err != nil {
		t.Fatalf("normalize valid request: %v", err)
	}
	if valid.SelectedVersion == nil || valid.SelectedVersion.Label != "福贵（2005年电视剧）" || valid.SelectedVersion.SourceURL != "https://example.com/2005" {
		t.Fatalf("selected version = %#v", valid.SelectedVersion)
	}
	_, err = normalizeStoryContextSearchRequest(storyContextSearchRequest{
		Title: "福贵", SelectedVersion: &storyVersionCandidate{Label: "异常版本", SourceURL: "javascript:alert(1)"},
	})
	if err == nil {
		t.Fatal("unsafe selected version URL should be rejected")
	}
	_, err = normalizeStoryContextSearchRequest(storyContextSearchRequest{
		Title: "福贵", SelectedVersion: &storyVersionCandidate{Label: strings.Repeat("版", 201)},
	})
	if err == nil {
		t.Fatal("oversized selected version label should be rejected")
	}
}

func TestDeepStorySearchReturnsFixedSectionsForSelectedVersion(t *testing.T) {
	client := &stubStorySearchClient{configured: true}
	client.searchFn = func(query string) (providers.TavilySearchResponse, error) {
		return providers.TavilySearchResponse{
			Query:   query,
			Answer:  "该部分的可靠资料。",
			Results: []providers.TavilySearchResult{{Title: "共同来源", URL: "https://example.com/story", Content: "来源摘要"}},
		}, nil
	}
	service := newStoryContextSearchService(client)
	result, err := service.search(context.Background(), uuid.New(), storyContextSearchRequest{
		Title: "福贵",
		SelectedVersion: &storyVersionCandidate{
			ID: "candidate-1", Label: "福贵（2005年电视剧）", Description: "陈创、刘敏涛主演。", SourceURL: "https://example.com/2005",
		},
	})
	if err != nil {
		t.Fatalf("deep search: %v", err)
	}
	for _, section := range storyResearchSections {
		heading := "## " + section.Heading
		if !strings.Contains(result.EditableContext, heading) {
			t.Fatalf("context missing %q: %s", heading, result.EditableContext)
		}
	}
	if client.calls.Load() != int64(len(storyResearchSections)) {
		t.Fatalf("provider calls = %d, want %d", client.calls.Load(), len(storyResearchSections))
	}
	client.queriesMu.Lock()
	defer client.queriesMu.Unlock()
	for _, query := range client.queries {
		if !strings.Contains(query, "福贵（2005年电视剧）") || !strings.Contains(query, "陈创、刘敏涛主演") {
			t.Fatalf("query missing selected version evidence: %q", query)
		}
	}
	if len(result.Sources) != 1 {
		t.Fatalf("deduplicated sources = %#v", result.Sources)
	}
}

func TestDeepStorySearchReturnsPartialResultsWhenOneQueryFails(t *testing.T) {
	client := &stubStorySearchClient{configured: true, searchFn: func(query string) (providers.TavilySearchResponse, error) {
		if strings.Contains(query, "世界观") {
			return providers.TavilySearchResponse{}, errors.New("background unavailable")
		}
		return providers.TavilySearchResponse{Query: query, Answer: "有效资料"}, nil
	}}
	result, err := newStoryContextSearchService(client).search(context.Background(), uuid.New(), storyContextSearchRequest{Title: "福贵"})
	if err != nil {
		t.Fatalf("partial deep search: %v", err)
	}
	if !strings.Contains(result.EditableContext, "## 必要背景\n未检索到可靠资料。") {
		t.Fatalf("missing partial failure marker: %s", result.EditableContext)
	}
}

func TestDeepStorySearchFailsWhenAllQueriesFail(t *testing.T) {
	client := &stubStorySearchClient{configured: true, searchFn: func(string) (providers.TavilySearchResponse, error) {
		return providers.TavilySearchResponse{}, errors.New("provider unavailable")
	}}
	_, err := newStoryContextSearchService(client).search(context.Background(), uuid.New(), storyContextSearchRequest{Title: "福贵"})
	if err == nil {
		t.Fatal("all-provider failure should be returned")
	}
}

func TestStoryContextSearchRouteRequiresAuthentication(t *testing.T) {
	app := newTestApp(t)
	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/search", map[string]any{"title": "庆余年"}, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestStoryVersionSearchRouteRequiresAuthentication(t *testing.T) {
	app := newTestApp(t)
	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/versions", map[string]any{"title": "福贵"}, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestStoryVersionSearchRouteReturnsCandidatesWithoutBilling(t *testing.T) {
	app := newTestApp(t)
	app.storySearch = newStoryContextSearchService(&stubStorySearchClient{configured: true, result: providers.TavilySearchResponse{
		Query: "福贵 版本", Results: []providers.TavilySearchResult{{Title: "福贵（2005年电视剧）", URL: "https://example.com", Content: "陈创主演。"}},
	}})
	token := registerAndLoginWithCredits(t, app, "story-version-search@example.com", 100)

	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/versions", map[string]any{"title": "福贵", "language": "zh-CN"}, token)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "candidates") || !strings.Contains(resp.Body.String(), "2005年电视剧") {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var count int64
	if err := app.db.Table("token_usage_records").Count(&count).Error; err != nil {
		t.Fatalf("count usage records: %v", err)
	}
	if count != 0 {
		t.Fatalf("usage records = %d, want 0", count)
	}
}

func TestStoryVersionSearchRouteReturnsServiceUnavailableWithoutKey(t *testing.T) {
	app := newTestApp(t)
	app.storySearch = newStoryContextSearchService(&stubStorySearchClient{configured: false})
	token := registerAndLogin(t, app, "story-version-unconfigured@example.com")
	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/versions", map[string]any{"title": "福贵"}, token)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestStoryVersionSearchRouteMapsProviderFailureToBadGateway(t *testing.T) {
	app := newTestApp(t)
	app.storySearch = newStoryContextSearchService(&stubStorySearchClient{configured: true, err: errors.New("provider timeout")})
	token := registerAndLogin(t, app, "story-version-provider-error@example.com")
	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/versions", map[string]any{"title": "福贵"}, token)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestStoryContextSearchRouteReturnsEditableContextWithoutBilling(t *testing.T) {
	app := newTestApp(t)
	client := &stubStorySearchClient{configured: true, result: providers.TavilySearchResponse{
		Query: "庆余年 第二季", Answer: "范闲回京后卷入新的朝堂冲突。",
		Results: []providers.TavilySearchResult{{Title: "剧情介绍", URL: "https://example.com", Content: "范闲与庆帝的关系更加紧张。"}},
	}}
	app.storySearch = newStoryContextSearchService(client)
	token := registerAndLoginWithCredits(t, app, "story-search@example.com", 100)

	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/search", map[string]any{
		"title": "庆余年", "season": "第二季", "language": "zh-CN",
	}, token)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "editable_context") || !strings.Contains(resp.Body.String(), "范闲") {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	for _, section := range storyResearchSections {
		if !strings.Contains(resp.Body.String(), section.Heading) {
			t.Fatalf("legacy request response missing %q: %s", section.Heading, resp.Body.String())
		}
	}
	var count int64
	if err := app.db.Table("token_usage_records").Count(&count).Error; err != nil {
		t.Fatalf("count usage records: %v", err)
	}
	if count != 0 {
		t.Fatalf("usage records = %d, want 0", count)
	}
}

func TestStoryContextSearchRouteValidatesTitle(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "story-search-validation@example.com")
	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/search", map[string]any{"title": "   "}, token)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestStoryContextSearchRouteReturnsServiceUnavailableWithoutKey(t *testing.T) {
	app := newTestApp(t)
	app.storySearch = newStoryContextSearchService(&stubStorySearchClient{configured: false})
	token := registerAndLogin(t, app, "story-search-unconfigured@example.com")
	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/search", map[string]any{"title": "庆余年"}, token)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestStoryContextSearchRouteMapsProviderFailureToBadGateway(t *testing.T) {
	app := newTestApp(t)
	app.storySearch = newStoryContextSearchService(&stubStorySearchClient{
		configured: true,
		err:        errors.New("provider timeout"),
	})
	token := registerAndLogin(t, app, "story-search-provider-error@example.com")
	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/search", map[string]any{"title": "庆余年"}, token)
	if resp.Code != http.StatusBadGateway || !strings.Contains(resp.Body.String(), "请手动填写") {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestDeepStorySearchReturnsStructuredKnowledgeAndWarnings(t *testing.T) {
	client := &stubStorySearchClient{configured: true, searchFn: func(query string) (providers.TavilySearchResponse, error) {
		if strings.Contains(query, "不确定 冲突") {
			return providers.TavilySearchResponse{}, errors.New("unavailable")
		}
		return providers.TavilySearchResponse{
			Query:   query,
			Answer:  "有效资料",
			Results: []providers.TavilySearchResult{{Title: query, URL: "https://example.com/source", Content: "来源证据"}},
		}, nil
	}}
	got, err := newStoryContextSearchService(client).search(
		context.Background(), uuid.New(),
		storyContextSearchRequest{Title: "福贵", EpisodeRange: "第1-2集"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.KnowledgeBase.Characters == "" || got.KnowledgeBase.EpisodeScope == "" {
		t.Fatalf("knowledge = %#v", got.KnowledgeBase)
	}
	if got.KnowledgeBase.Uncertainties != "" {
		t.Fatalf("uncertainties = %q, want empty after failed section", got.KnowledgeBase.Uncertainties)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Section != "uncertainties" {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
	if got.EditableContext == "" || !strings.Contains(got.EditableContext, "有效资料") {
		t.Fatalf("editable_context did not preserve successful evidence: %s", got.EditableContext)
	}
	for _, source := range got.Sources {
		if source.Section == "" {
			t.Fatalf("source missing section: %#v", source)
		}
	}
}

func TestStoryContextSearchRouteReturnsStructuredAndCompatibleResponse(t *testing.T) {
	app := newTestApp(t)
	app.storySearch = newStoryContextSearchService(&stubStorySearchClient{
		configured: true,
		result: providers.TavilySearchResponse{
			Answer: "范闲回京后卷入新的朝堂冲突。",
			Results: []providers.TavilySearchResult{{
				Title: "剧情介绍", URL: "https://example.com/story", Content: "范闲与庆帝的关系更加紧张。",
			}},
		},
	})
	token := registerAndLoginWithCredits(t, app, "structured-story-search@example.com", 100)

	resp := request(app, http.MethodPost, "/api/v1/tools/story-context/search", map[string]any{
		"title": "庆余年", "season": "第二季", "episode_range": "第1-2集", "language": "zh-CN",
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var got storyContextSearchResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.KnowledgeBase.WorkIdentity == "" || got.KnowledgeBase.Characters == "" {
		t.Fatalf("knowledge_base = %#v", got.KnowledgeBase)
	}
	if got.Warnings == nil || len(got.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want present empty array", got.Warnings)
	}
	if len(got.Sources) != 1 || got.Sources[0].Section == "" {
		t.Fatalf("sources = %#v", got.Sources)
	}
	if got.EditableContext == "" || !strings.Contains(got.EditableContext, "范闲") {
		t.Fatalf("compatible editable_context = %q", got.EditableContext)
	}
}

func TestStoryContextSearchOpenAPIDocumentsStructuredAndCompatibleResponse(t *testing.T) {
	app := &App{}
	operation := app.operationFor(http.MethodPost, "/api/v1/tools/story-context/search")
	encoded, err := json.Marshal(operation["responses"])
	if err != nil {
		t.Fatalf("marshal OpenAPI response: %v", err)
	}
	document := string(encoded)
	for _, field := range []string{
		"knowledge_base",
		"work_identity",
		"story_architecture",
		"episode_scope",
		"characters",
		"background",
		"uncertainties",
		"warnings",
		"section",
		"editable_context",
	} {
		if !strings.Contains(document, `"`+field+`"`) {
			t.Fatalf("OpenAPI response missing %q: %s", field, document)
		}
	}
}
