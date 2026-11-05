package prompts

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Service struct {
	baseDir                     string
	templates                   map[string]TemplateMeta
	configuredTemplateListRanks map[string]map[string]int
}

type TemplateMeta struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	TitleI18n       map[string]string `json:"title_i18n,omitempty"`
	Version         string            `json:"version,omitempty"`
	Type            string            `json:"type"`
	Description     string            `json:"description,omitempty"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"`
	Category        string            `json:"category,omitempty"`
	JSONPath        string            `json:"json_path,omitempty"`
	Model           string            `json:"model,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Stream          *bool             `json:"stream,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	Messages        []TemplateMessage `json:"messages,omitempty"`
}

type TemplateMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type RenderedTemplate struct {
	SystemPrompt string
	UserPrompt   string
	Meta         TemplateMeta
}

var defaultTemplateContext = map[string]string{
	"narration_perspective": "第三人称",
	"output_language":       "中文",
	"target_word_count":     "1000-3500",
	"original_ratio":        "60",
	"narration_ratio":       "40",
	"audio_speed":           "5",
}

var templateEnglishTitles = map[string]string{
	"动物世界":      "Animal World",
	"参考文案写作":    "Reference Copywriting",
	"口语化叙述":     "Conversational Narration",
	"幽默搞笑":      "Humorous Comedy",
	"引导式开头":     "Guided Opening",
	"悬疑电影":      "Suspense Film",
	"折中分镜":      "Balanced Storyboarding",
	"深入反思":      "Deep Reflection",
	"混剪":        "Montage",
	"温情治愈":      "Warm Healing",
	"激进分镜":      "Aggressive Storyboarding",
	"激进分镜-全文解说": "Aggressive Full Narration",
	"灵魂深处":      "Deep Soul",
	"理性分析-直播带货": "Rational Livestream Sales Analysis",
	"电影台前幕后":    "Behind the Film",
	"电影电视剧":     "Film and TV Drama",
	"白话高能":      "Plainspoken High Energy",
	"直播带货":      "Livestream Sales",
	"短剧漫剧":      "Short Drama and Comics",
	"胡说八道-纪录片":  "Absurd Documentary",
	"自选开头":      "Custom Opening",
}

const templateOrderConfigFile = "template_order.json"

var variablePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\$\{([a-zA-Z0-9_]+)\}`),
	regexp.MustCompile(`\{\{([a-zA-Z0-9_]+)\}\}`),
}

func NewService(baseDir string) (*Service, error) {
	if baseDir == "" {
		baseDir = "prompt"
	}
	info, err := os.Stat(baseDir)
	if err != nil {
		return nil, fmt.Errorf("提示词模板目录不存在: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("提示词模板路径不是目录: %s", baseDir)
	}
	service := &Service{
		baseDir:                     baseDir,
		templates:                   map[string]TemplateMeta{},
		configuredTemplateListRanks: map[string]map[string]int{},
	}
	if err := service.loadAll(); err != nil {
		return nil, err
	}
	if err := service.loadTemplateSortConfig(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) List(types ...string) []TemplateMeta {
	filters := map[string]bool{}
	for _, item := range types {
		if item != "" {
			filters[strings.ToLower(item)] = true
		}
	}
	out := make([]TemplateMeta, 0, len(s.templates))
	for _, meta := range s.templates {
		if len(filters) == 0 || matchesType(meta, filters) {
			if sortRanks, configured := s.configuredTemplateListRanks[meta.Category]; configured {
				if _, visible := sortRanks[meta.ID]; !visible {
					continue
				}
			}
			copyMeta := meta
			copyMeta.Messages = nil
			out = append(out, copyMeta)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category == out[j].Category {
			sortRanks := s.configuredTemplateListRanks[out[i].Category]
			leftRank, leftKnown := sortRanks[out[i].ID]
			rightRank, rightKnown := sortRanks[out[j].ID]
			if leftKnown != rightKnown {
				return leftKnown
			}
			if leftKnown && leftRank != rightRank {
				return leftRank < rightRank
			}
			return out[i].ID < out[j].ID
		}
		return out[i].Category < out[j].Category
	})
	return out
}

func (s *Service) Get(id string) (TemplateMeta, bool) {
	meta, ok := s.templates[id]
	return meta, ok
}

func (s *Service) Render(id string, context map[string]string) (RenderedTemplate, error) {
	meta, ok := s.Get(id)
	if !ok {
		return RenderedTemplate{}, fmt.Errorf("模板 %s 不存在", id)
	}
	ctx := map[string]string{}
	for key, value := range defaultTemplateContext {
		ctx[key] = value
	}
	for key, value := range context {
		ctx[key] = value
	}
	var systemParts []string
	var userParts []string
	for _, message := range meta.Messages {
		content, err := renderContent(message.Content, ctx)
		if err != nil {
			return RenderedTemplate{}, err
		}
		if content == "" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(message.Role)) == "system" {
			systemParts = append(systemParts, content)
		} else {
			userParts = append(userParts, content)
		}
	}
	return RenderedTemplate{SystemPrompt: strings.Join(systemParts, "\n\n"), UserPrompt: strings.Join(userParts, "\n\n"), Meta: meta}, nil
}

func (s *Service) ResolveTemplate(styleMode string) TemplateMeta {
	if styleMode != "" {
		if meta, ok := s.Get(styleMode); ok {
			return meta
		}
	}
	templates := s.List("fast")
	if len(templates) > 0 {
		meta, _ := s.Get(templates[0].ID)
		return meta
	}
	for _, meta := range s.templates {
		return meta
	}
	return TemplateMeta{}
}

func (s *Service) ResolveTwoPhaseTemplate(styleMode string, step string) (TemplateMeta, bool) {
	styleMode = strings.TrimSpace(styleMode)
	step = strings.Trim(strings.TrimSpace(filepath.ToSlash(step)), "/")
	if styleMode == "" || step == "" {
		return TemplateMeta{}, false
	}
	prefix := "TwoPhases/" + step + "/"
	for _, meta := range s.templates {
		jsonPath := filepath.ToSlash(meta.JSONPath)
		if !strings.HasPrefix(jsonPath, prefix) {
			continue
		}
		fileID := strings.TrimSuffix(filepath.Base(jsonPath), filepath.Ext(jsonPath))
		if meta.ID == styleMode || meta.Title == styleMode || fileID == styleMode {
			return meta, true
		}
	}
	return TemplateMeta{}, false
}

func (s *Service) loadAll() error {
	err := filepath.WalkDir(s.baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".json") || filepath.Base(path) == templateOrderConfigFile {
			return nil
		}
		meta, err := s.loadMeta(path)
		if err != nil {
			return err
		}
		if _, exists := s.templates[meta.ID]; exists {
			return fmt.Errorf("检测到重复的模板ID: %s", meta.ID)
		}
		s.templates[meta.ID] = meta
		return nil
	})
	if err != nil {
		return err
	}
	if len(s.templates) == 0 {
		return fmt.Errorf("模板目录中未找到可用 JSON 模板: %s", s.baseDir)
	}
	return nil
}

func (s *Service) loadTemplateSortConfig() error {
	path := filepath.Join(s.baseDir, templateOrderConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取模板排序配置失败 %s: %w", path, err)
	}
	var orderByCategory map[string][]string
	if err := json.Unmarshal(data, &orderByCategory); err != nil {
		return fmt.Errorf("解析模板排序配置失败 %s: %w", path, err)
	}
	for category, ids := range orderByCategory {
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		ranks := make(map[string]int, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, exists := ranks[id]; !exists {
				ranks[id] = len(ranks)
			}
		}
		s.configuredTemplateListRanks[category] = ranks
	}
	return nil
}

func (s *Service) loadMeta(path string) (TemplateMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TemplateMeta{}, err
	}
	var raw struct {
		ID              string            `json:"id"`
		Title           string            `json:"title"`
		TitleI18n       map[string]string `json:"title_i18n"`
		Version         string            `json:"version"`
		Type            string            `json:"type"`
		Description     string            `json:"description"`
		DescriptionI18n map[string]string `json:"description_i18n"`
		Model           string            `json:"model"`
		ReasoningEffort string            `json:"reasoning_effort"`
		Stream          *bool             `json:"stream"`
		Temperature     *float64          `json:"temperature"`
		TopP            *float64          `json:"top_p"`
		Messages        []TemplateMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return TemplateMeta{}, fmt.Errorf("读取模板元数据失败 %s: %w", path, err)
	}
	if len(raw.Messages) == 0 {
		return TemplateMeta{}, fmt.Errorf("模板文件缺少 messages 数组: %s", path)
	}
	rel, _ := filepath.Rel(s.baseDir, path)
	category := ""
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 1 {
		category = parts[0]
	}
	id := raw.ID
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	title := raw.Title
	if title == "" {
		title = id
	}
	templateType := raw.Type
	if templateType == "" {
		templateType = category
	}
	if templateType == "" {
		templateType = "fast"
	}
	description := raw.Description
	if description == "" {
		description = title + " 风格单轮脚本生成模板"
	}
	titleI18n := normalizeTemplateI18n(raw.TitleI18n)
	if titleI18n == nil {
		titleI18n = map[string]string{}
	}
	titleI18n["zh-CN"] = title
	titleI18n["en"] = firstNonEmpty(titleI18n["en"], templateEnglishTitles[title], title)

	descriptionI18n := normalizeTemplateI18n(raw.DescriptionI18n)
	if descriptionI18n == nil {
		descriptionI18n = map[string]string{}
	}
	descriptionI18n["zh-CN"] = description
	descriptionI18n["en"] = firstNonEmpty(
		descriptionI18n["en"],
		fmt.Sprintf("%s style single-pass script generation template", titleI18n["en"]),
		description,
	)
	version := raw.Version
	if version == "" {
		version = "1.0.0"
	}
	return TemplateMeta{
		ID: id, Title: title, TitleI18n: titleI18n, Version: version, Type: templateType, Description: description, DescriptionI18n: descriptionI18n,
		Category: category, JSONPath: filepath.ToSlash(rel), Model: raw.Model,
		ReasoningEffort: raw.ReasoningEffort, Stream: raw.Stream, Temperature: raw.Temperature, TopP: raw.TopP,
		Messages: raw.Messages,
	}, nil
}

func normalizeTemplateI18n(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, item := range value {
		normalizedKey := strings.TrimSpace(key)
		normalizedValue := strings.TrimSpace(item)
		if normalizedKey != "" && normalizedValue != "" {
			out[normalizedKey] = normalizedValue
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func matchesType(meta TemplateMeta, filters map[string]bool) bool {
	candidate := strings.ToLower(firstNonEmpty(meta.Type, meta.Category))
	if filters[candidate] {
		return true
	}
	if candidate == "fast" {
		return filters["fast_comment"] || filters["fast_video"] || filters["fast_mixed"]
	}
	return false
}

func renderContent(content any, context map[string]string) (string, error) {
	switch value := content.(type) {
	case string:
		return substitute(value, context)
	case []any:
		var parts []string
		for _, item := range value {
			row, ok := item.(map[string]any)
			if !ok || row["type"] != "text" {
				continue
			}
			text, ok := row["text"].(string)
			if !ok {
				continue
			}
			rendered, err := substitute(text, context)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(rendered) != "" {
				parts = append(parts, strings.TrimSpace(rendered))
			}
		}
		return strings.Join(parts, "\n"), nil
	default:
		return "", nil
	}
}

func substitute(template string, context map[string]string) (string, error) {
	result := template
	for _, pattern := range variablePatterns {
		var missing string
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			key := pattern.FindStringSubmatch(match)[1]
			value, ok := context[key]
			if !ok {
				missing = key
				return match
			}
			return value
		})
		if missing != "" {
			return "", fmt.Errorf("模板变量 %q 未找到", missing)
		}
	}
	return strings.TrimSpace(result), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
