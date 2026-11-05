package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/providers"
)

const (
	storySearchCacheTTL       = 24 * time.Hour
	storySearchRateWindow     = time.Minute
	storySearchRateLimit      = 10
	storySearchMaxContextRune = 6000
)

var (
	errStorySearchRateLimited   = errors.New("story search rate limit exceeded")
	errStorySearchUnconfigured  = errors.New("story search is not configured")
	errStoryResearchUnavailable = errors.New("story research returned no usable results")

	storyEpisodeChineseTitlePattern = regexp.MustCompile(`第\s*0*\d+\s*集`)
	storyEpisodeEnglishTitlePattern = regexp.MustCompile(`(?i)\b(?:ep|episode)\s*[-_.]?\s*0*\d+\b`)
	storyEpisodeViewerTitlePattern  = regexp.MustCompile(`(?:^|[^0-9])\d{1,3}\s*(?:集)?\s*(?:在线观看|在线播放|在线播(?:放)?|播放|观看)`)
	storyEpisodePathPattern         = regexp.MustCompile(`(?i)(?:^|/)(?:episode|episodes|ep|e)(?:[-_./]?\d+)(?:/|$)`)
	storyEpisodePathIDPattern       = regexp.MustCompile(`(?i)(?:^|/)(?:episode|episodes|ep)(?:/|$)`)
	storyEpisodeNumberPattern       = regexp.MustCompile(`^\d+$`)
	storyEpisodeQueryKeys           = map[string]struct{}{"episode": {}, "episode_id": {}, "episodeid": {}, "ep": {}, "ep_id": {}, "epid": {}}
)

type storyContextSearchRequest struct {
	Title           string                 `json:"title"`
	Season          string                 `json:"season"`
	EpisodeRange    string                 `json:"episode_range"`
	ReleaseVersion  string                 `json:"release_version"`
	Protagonist     string                 `json:"protagonist"`
	Notes           string                 `json:"notes"`
	Language        string                 `json:"language"`
	SelectedVersion *storyVersionCandidate `json:"selected_version"`
	ForceRefresh    bool                   `json:"force_refresh,omitempty"`
}

type storyContextSearchSource struct {
	Section string `json:"section"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type storyContextKnowledgeBase struct {
	WorkIdentity      string `json:"work_identity"`
	StoryArchitecture string `json:"story_architecture"`
	EpisodeScope      string `json:"episode_scope"`
	Characters        string `json:"characters"`
	Background        string `json:"background"`
	Uncertainties     string `json:"uncertainties"`
}

type storyContextSearchWarning struct {
	Section string `json:"section"`
	Message string `json:"message"`
}

type storyVersionCandidate struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`
}

type storyContextSearchResponse struct {
	Query           string                      `json:"query"`
	KnowledgeBase   storyContextKnowledgeBase   `json:"knowledge_base"`
	Warnings        []storyContextSearchWarning `json:"warnings"`
	EditableContext string                      `json:"editable_context"`
	Sources         []storyContextSearchSource  `json:"sources"`
	SearchedAt      time.Time                   `json:"searched_at"`
	Cached          bool                        `json:"cached"`
}

type storyVersionSearchResponse struct {
	Query      string                  `json:"query"`
	Candidates []storyVersionCandidate `json:"candidates"`
	SearchedAt time.Time               `json:"searched_at"`
	Cached     bool                    `json:"cached"`
}

type storyResearchSection struct {
	Key     string
	Heading string
	Terms   string
}

type storyResearchResult struct {
	Index    int
	Section  storyResearchSection
	Response providers.TavilySearchResponse
	Err      error
}

var storyResearchSections = []storyResearchSection{
	{Key: "work_identity", Heading: "作品身份", Terms: "正式名称 年份 季 主创 导演 主演 改编版本"},
	{Key: "story_architecture", Heading: "剧情架构", Terms: "主线冲突 阶段目标 关键转折 剧情架构"},
	{Key: "episode_scope", Heading: "当前集范围", Terms: "当前集 剧情范围 集数 边界 避免未来剧情"},
	{Key: "characters", Heading: "人物与关系", Terms: "主要人物 姓名 别名 身份 人物关系 容易混淆人物"},
	{Key: "background", Heading: "必要背景", Terms: "时代 地点 世界观 前置剧情 必要背景"},
	{Key: "uncertainties", Heading: "不确定与冲突", Terms: "不确定 冲突 来源不一致 待核实"},
}

type storyContextSearchClient interface {
	Configured() bool
	Search(context.Context, string) (providers.TavilySearchResponse, error)
}

type storyContextCacheEntry struct {
	response  storyContextSearchResponse
	expiresAt time.Time
}

type storyVersionCacheEntry struct {
	response  storyVersionSearchResponse
	expiresAt time.Time
}

type storyContextSearchService struct {
	mu       sync.Mutex
	client   storyContextSearchClient
	cache    map[string]storyContextCacheEntry
	versions map[string]storyVersionCacheEntry
	attempts map[uuid.UUID][]time.Time
	now      func() time.Time
}

func newStoryContextSearchService(client storyContextSearchClient) *storyContextSearchService {
	return &storyContextSearchService{
		client:   client,
		cache:    make(map[string]storyContextCacheEntry),
		versions: make(map[string]storyVersionCacheEntry),
		attempts: make(map[uuid.UUID][]time.Time),
		now:      time.Now,
	}
}

func (s *storyContextSearchService) search(ctx context.Context, userID uuid.UUID, req storyContextSearchRequest) (storyContextSearchResponse, error) {
	if s == nil || s.client == nil || !s.client.Configured() {
		return storyContextSearchResponse{}, errStorySearchUnconfigured
	}
	query := buildStorySearchQuery(req)
	cacheKey := storySearchCacheKey(req)
	now := s.now().UTC()

	if !req.ForceRefresh {
		s.mu.Lock()
		if entry, ok := s.cache[cacheKey]; ok && now.Before(entry.expiresAt) {
			response := entry.response
			response.Cached = true
			s.mu.Unlock()
			return response, nil
		}
		s.mu.Unlock()
	}
	if err := s.reserveSearchAttempt(userID, now); err != nil {
		return storyContextSearchResponse{}, err
	}

	researchResults := searchDeepStorySections(ctx, s.client, req)
	knowledgeBase, sources, warnings, successCount, firstErr := formatDeepStoryKnowledgeBase(researchResults)
	if successCount == 0 {
		if firstErr != nil {
			return storyContextSearchResponse{}, fmt.Errorf("%w: %v", errStoryResearchUnavailable, firstErr)
		}
		return storyContextSearchResponse{}, errStoryResearchUnavailable
	}
	response := storyContextSearchResponse{
		Query:           query,
		KnowledgeBase:   knowledgeBase,
		Warnings:        warnings,
		EditableContext: compileStoryKnowledgeBase(knowledgeBase, sources, storySearchMaxContextRune),
		Sources:         sources,
		SearchedAt:      now,
	}
	s.mu.Lock()
	s.cache[cacheKey] = storyContextCacheEntry{response: response, expiresAt: now.Add(storySearchCacheTTL)}
	s.mu.Unlock()
	return response, nil
}

func (s *storyContextSearchService) discoverVersions(ctx context.Context, userID uuid.UUID, req storyContextSearchRequest) (storyVersionSearchResponse, error) {
	if s == nil || s.client == nil || !s.client.Configured() {
		return storyVersionSearchResponse{}, errStorySearchUnconfigured
	}
	query := buildStoryVersionSearchQuery(req)
	cacheKey := storySearchCacheKey(req)
	now := s.now().UTC()

	s.mu.Lock()
	if entry, ok := s.versions[cacheKey]; ok && now.Before(entry.expiresAt) {
		response := entry.response
		response.Cached = true
		s.mu.Unlock()
		return response, nil
	}
	s.mu.Unlock()
	if err := s.reserveSearchAttempt(userID, now); err != nil {
		return storyVersionSearchResponse{}, err
	}

	result, err := s.client.Search(ctx, query)
	if err != nil {
		return storyVersionSearchResponse{}, err
	}
	response := storyVersionSearchResponse{
		Query:      query,
		Candidates: formatStoryVersionCandidates(result.Results),
		SearchedAt: now,
	}
	s.mu.Lock()
	s.versions[cacheKey] = storyVersionCacheEntry{response: response, expiresAt: now.Add(storySearchCacheTTL)}
	s.mu.Unlock()
	return response, nil
}

func (s *storyContextSearchService) reserveSearchAttempt(userID uuid.UUID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-storySearchRateWindow)
	recent := s.attempts[userID][:0]
	for _, attemptedAt := range s.attempts[userID] {
		if attemptedAt.After(cutoff) {
			recent = append(recent, attemptedAt)
		}
	}
	if len(recent) >= storySearchRateLimit {
		s.attempts[userID] = recent
		return errStorySearchRateLimited
	}
	s.attempts[userID] = append(recent, now)
	return nil
}

func storySearchCacheKey(req storyContextSearchRequest) string {
	req.ForceRefresh = false
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func buildStorySearchQuery(req storyContextSearchRequest) string {
	parts := compactStrings(req.Title, req.Season, req.EpisodeRange, req.ReleaseVersion)
	if req.Protagonist != "" {
		parts = append(parts, "主角", req.Protagonist)
	}
	if req.Notes != "" {
		parts = append(parts, req.Notes)
	}
	if req.Language != "" {
		parts = append(parts, "语言", req.Language)
	}
	parts = append(parts, "剧情", "人物关系", "主角", "结局", "电视剧")
	return strings.Join(parts, " ")
}

func buildStoryVersionSearchQuery(req storyContextSearchRequest) string {
	parts := compactStrings(req.Title, req.Season, req.EpisodeRange)
	if req.Protagonist != "" {
		parts = append(parts, "主角", req.Protagonist)
	}
	parts = append(parts, compactStrings(req.Notes, req.Language)...)
	parts = append(parts, "同名影视作品", "版本", "年份", "类型", "国家地区", "导演", "主演", "电影", "电视剧", "动画")
	return strings.Join(parts, " ")
}

func buildDeepStorySearchQuery(req storyContextSearchRequest, section storyResearchSection) string {
	parts := compactStrings(req.Title, req.Season, req.EpisodeRange, req.ReleaseVersion)
	if req.SelectedVersion != nil {
		parts = append(parts, "已确认版本", req.SelectedVersion.Label)
		if req.SelectedVersion.Description != "" {
			parts = append(parts, "版本线索", req.SelectedVersion.Description)
		}
	}
	if req.Protagonist != "" {
		parts = append(parts, "主角", req.Protagonist)
	}
	parts = append(parts, compactStrings(req.Notes, req.Language)...)
	parts = append(parts, section.Terms)
	return strings.Join(parts, " ")
}

func searchDeepStorySections(ctx context.Context, client storyContextSearchClient, req storyContextSearchRequest) []storyResearchResult {
	results := make([]storyResearchResult, len(storyResearchSections))
	resultCh := make(chan storyResearchResult, len(storyResearchSections))
	for index, section := range storyResearchSections {
		go func(index int, section storyResearchSection) {
			response, err := client.Search(ctx, buildDeepStorySearchQuery(req, section))
			resultCh <- storyResearchResult{Index: index, Section: section, Response: response, Err: err}
		}(index, section)
	}
	for range storyResearchSections {
		result := <-resultCh
		results[result.Index] = result
	}
	return results
}

func formatDeepStoryKnowledgeBase(results []storyResearchResult) (storyContextKnowledgeBase, []storyContextSearchSource, []storyContextSearchWarning, int, error) {
	var knowledgeBase storyContextKnowledgeBase
	sourceGroups := make([][]storyContextSearchSource, len(results))
	warnings := make([]storyContextSearchWarning, 0, len(results))
	seenSources := make(map[string]struct{})
	successCount := 0
	var firstErr error

	for _, result := range results {
		sectionText := ""
		if result.Err == nil {
			sectionText = storyResearchSectionText(result.Response)
		}
		if sectionText == "" {
			message := "该分区未检索到可靠资料，请核对或手动补充。"
			if result.Err != nil {
				message = "该分区检索失败，请核对或手动补充。"
				if firstErr == nil {
					firstErr = result.Err
				}
			}
			warnings = append(warnings, storyContextSearchWarning{Section: result.Section.Key, Message: message})
			continue
		}
		successCount++
		setStoryKnowledgeSection(&knowledgeBase, result.Section.Key, sectionText)

		for _, item := range result.Response.Results {
			source := storyContextSearchSource{
				Section: result.Section.Key,
				Title:   trimRunes(item.Title, 120),
				URL:     safeStorySourceURL(item.URL),
				Snippet: trimRunes(item.Content, 700),
			}
			if source.Title == "" && source.URL == "" && source.Snippet == "" {
				continue
			}
			key := source.URL
			if key == "" {
				key = strings.ToLower(source.Title) + "\n" + source.Snippet
			}
			if _, exists := seenSources[key]; exists {
				continue
			}
			seenSources[key] = struct{}{}
			sourceGroups[result.Index] = append(sourceGroups[result.Index], source)
		}
	}

	// Preserve coverage across identity, plot, episode, character, and background
	// searches instead of letting the first query consume every source slot.
	sources := make([]storyContextSearchSource, 0, 5)
	for rank := 0; len(sources) < 5; rank++ {
		added := false
		for _, group := range sourceGroups {
			if rank >= len(group) {
				continue
			}
			sources = append(sources, group[rank])
			added = true
			if len(sources) == 5 {
				break
			}
		}
		if !added {
			break
		}
	}

	return knowledgeBase, sources, warnings, successCount, firstErr
}

func storyResearchSectionText(response providers.TavilySearchResponse) string {
	if answer := trimRunes(response.Answer, 1200); answer != "" {
		return answer
	}
	snippets := make([]string, 0, len(response.Results))
	for _, item := range response.Results {
		if snippet := trimRunes(item.Content, 500); snippet != "" {
			snippets = append(snippets, snippet)
		}
	}
	return trimRunes(strings.Join(snippets, "\n"), 1200)
}

func setStoryKnowledgeSection(knowledgeBase *storyContextKnowledgeBase, key string, value string) {
	switch key {
	case "work_identity":
		knowledgeBase.WorkIdentity = value
	case "story_architecture":
		knowledgeBase.StoryArchitecture = value
	case "episode_scope":
		knowledgeBase.EpisodeScope = value
	case "characters":
		knowledgeBase.Characters = value
	case "background":
		knowledgeBase.Background = value
	case "uncertainties":
		knowledgeBase.Uncertainties = value
	}
}

func storyKnowledgeSectionText(knowledgeBase storyContextKnowledgeBase, key string) string {
	switch key {
	case "work_identity":
		return knowledgeBase.WorkIdentity
	case "story_architecture":
		return knowledgeBase.StoryArchitecture
	case "episode_scope":
		return knowledgeBase.EpisodeScope
	case "characters":
		return knowledgeBase.Characters
	case "background":
		return knowledgeBase.Background
	case "uncertainties":
		return knowledgeBase.Uncertainties
	default:
		return ""
	}
}

func compileStoryKnowledgeBase(knowledgeBase storyContextKnowledgeBase, sources []storyContextSearchSource, maxRunes int) string {
	lines := []string{"# 联网检索参考", "以下内容来自外部检索，仅用于辅助理解，素材字幕、画面与时间戳优先。"}
	for _, section := range storyResearchSections {
		sectionText := storyKnowledgeSectionText(knowledgeBase, section.Key)
		if sectionText == "" {
			sectionText = "未检索到可靠资料。"
		}
		lines = append(lines, "", "## "+section.Heading, sectionText)
	}
	if len(sources) > 0 {
		lines = append(lines, "", "## 检索来源摘要")
		for index, source := range sources {
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, source.Title), "   来源："+source.URL, "   摘要："+source.Snippet)
		}
	}
	return trimRunes(strings.TrimSpace(strings.Join(lines, "\n")), maxRunes)
}

func compactStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func formatStorySearchContext(result providers.TavilySearchResponse, maxRunes int) (string, []storyContextSearchSource) {
	lines := []string{"# 联网检索参考", "检索词：" + trimRunes(result.Query, 300)}
	if answer := trimRunes(result.Answer, 1800); answer != "" {
		lines = append(lines, "", "## 剧情与人物关系", answer)
	}
	sources := make([]storyContextSearchSource, 0, min(len(result.Results), 5))
	for _, item := range result.Results {
		if len(sources) >= 5 {
			break
		}
		source := storyContextSearchSource{
			Title:   trimRunes(item.Title, 120),
			URL:     safeStorySourceURL(item.URL),
			Snippet: trimRunes(item.Content, 700),
		}
		if source.Title == "" && source.URL == "" && source.Snippet == "" {
			continue
		}
		sources = append(sources, source)
	}
	if len(sources) > 0 {
		lines = append(lines, "", "## 来源摘要")
		for index, source := range sources {
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, source.Title), "   来源："+source.URL, "   摘要："+source.Snippet)
		}
	}
	return trimRunes(strings.TrimSpace(strings.Join(lines, "\n")), maxRunes), sources
}

func formatStoryVersionCandidates(results []providers.TavilySearchResult) []storyVersionCandidate {
	candidates := make([]storyVersionCandidate, 0, min(len(results), 5))
	seen := make(map[string]struct{}, min(len(results), 5))
	for _, item := range results {
		if len(candidates) >= 5 {
			break
		}
		label := trimRunes(item.Title, 160)
		if label == "" {
			continue
		}
		if isEpisodeOnlyStoryResult(label, item.URL) {
			continue
		}
		sourceURL := safeStorySourceURL(item.URL)
		dedupeKey := strings.ToLower(label) + "\n" + sourceURL
		if _, exists := seen[dedupeKey]; exists {
			continue
		}
		seen[dedupeKey] = struct{}{}
		sum := sha256.Sum256([]byte(dedupeKey))
		candidates = append(candidates, storyVersionCandidate{
			ID:          hex.EncodeToString(sum[:]),
			Label:       label,
			Description: trimRunes(item.Content, 500),
			SourceURL:   sourceURL,
		})
	}
	return candidates
}

func isEpisodeOnlyStoryResult(title string, rawURL string) bool {
	if storyEpisodeChineseTitlePattern.MatchString(title) ||
		storyEpisodeEnglishTitlePattern.MatchString(title) ||
		storyEpisodeViewerTitlePattern.MatchString(title) {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	if storyEpisodePathPattern.MatchString(path) {
		return true
	}
	pathHasEpisodeID := storyEpisodePathIDPattern.MatchString(path)
	for key, values := range parsed.Query() {
		normalizedKey := strings.ToLower(key)
		if _, isEpisodeKey := storyEpisodeQueryKeys[normalizedKey]; !isEpisodeKey && !(pathHasEpisodeID && normalizedKey == "id") {
			continue
		}
		for _, value := range values {
			if storyEpisodeNumberPattern.MatchString(strings.TrimSpace(value)) {
				return true
			}
		}
	}
	return false
}

func safeStorySourceURL(value string) string {
	value = trimRunes(value, 240)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return value
}

func trimRunes(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return strings.TrimSpace(string(runes[:maxRunes-3])) + "..."
}

func normalizeStoryContextSearchRequest(req storyContextSearchRequest) (storyContextSearchRequest, error) {
	fields := []struct {
		name  string
		value *string
		max   int
	}{
		{"片名", &req.Title, 200}, {"季", &req.Season, 100}, {"集数范围", &req.EpisodeRange, 100},
		{"年份或版本", &req.ReleaseVersion, 100}, {"主角", &req.Protagonist, 100}, {"补充说明", &req.Notes, 500}, {"语言", &req.Language, 20},
	}
	for _, field := range fields {
		*field.value = strings.TrimSpace(*field.value)
		if len([]rune(*field.value)) > field.max {
			return storyContextSearchRequest{}, fmt.Errorf("%s不能超过 %d 个字符", field.name, field.max)
		}
	}
	if req.SelectedVersion != nil {
		selected := *req.SelectedVersion
		selectedFields := []struct {
			name  string
			value *string
			max   int
		}{
			{"候选版本标识", &selected.ID, 128},
			{"候选版本名称", &selected.Label, 200},
			{"候选版本说明", &selected.Description, 1000},
			{"候选版本来源", &selected.SourceURL, 240},
		}
		for _, field := range selectedFields {
			*field.value = strings.TrimSpace(*field.value)
			if len([]rune(*field.value)) > field.max {
				return storyContextSearchRequest{}, fmt.Errorf("%s不能超过 %d 个字符", field.name, field.max)
			}
		}
		if selected.Label == "" {
			return storyContextSearchRequest{}, errors.New("候选版本名称不能为空")
		}
		if selected.SourceURL != "" && safeStorySourceURL(selected.SourceURL) == "" {
			return storyContextSearchRequest{}, errors.New("候选版本来源必须是 HTTP 或 HTTPS 地址")
		}
		req.SelectedVersion = &selected
	}
	if req.Title == "" {
		return storyContextSearchRequest{}, errors.New("片名不能为空")
	}
	if req.Language == "" {
		req.Language = "zh-CN"
	}
	return req, nil
}

func (a *App) searchStoryContext(c *gin.Context) {
	var req storyContextSearchRequest
	if !bindJSON(c, &req) {
		return
	}
	req, err := normalizeStoryContextSearchRequest(req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response, err := a.storySearch.search(c.Request.Context(), getUser(c).ID, req)
	if err != nil {
		switch {
		case errors.Is(err, errStorySearchUnconfigured):
			fail(c, http.StatusServiceUnavailable, "剧情检索服务尚未配置")
		case errors.Is(err, errStorySearchRateLimited):
			fail(c, http.StatusTooManyRequests, "剧情检索请求过于频繁，请稍后再试")
		case errors.Is(err, errStoryResearchUnavailable):
			fail(c, http.StatusBadGateway, "未检索到可用剧情资料，请手动填写")
		default:
			fail(c, http.StatusBadGateway, "剧情检索服务暂时不可用")
		}
		return
	}
	ok(c, response)
}

func (a *App) searchStoryVersions(c *gin.Context) {
	var req storyContextSearchRequest
	if !bindJSON(c, &req) {
		return
	}
	req, err := normalizeStoryContextSearchRequest(req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	response, err := a.storySearch.discoverVersions(c.Request.Context(), getUser(c).ID, req)
	if err != nil {
		switch {
		case errors.Is(err, errStorySearchUnconfigured):
			fail(c, http.StatusServiceUnavailable, "剧情检索服务尚未配置")
		case errors.Is(err, errStorySearchRateLimited):
			fail(c, http.StatusTooManyRequests, "剧情检索请求过于频繁，请稍后再试")
		default:
			fail(c, http.StatusBadGateway, "剧情检索服务暂时不可用")
		}
		return
	}
	ok(c, response)
}
