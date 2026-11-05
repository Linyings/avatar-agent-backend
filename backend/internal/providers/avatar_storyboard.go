package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/narratoai/backend-go/internal/avatar"
	"strings"
)

// AvatarStoryboard reuses the Flash profile and HTTP client without invoking billing handlers.
func (o OpenAIClient) AvatarStoryboard(ctx context.Context, sentences []string) ([]avatar.Shot, ChatUsage, error) {
	profile := openAIModelProfile{name: openAIProfileFlash, apiKey: o.cfg.OpenAIFlashAPIKey, apiBaseURL: o.cfg.OpenAIFlashAPIBaseURL, model: o.cfg.OpenAIFlashModel, thinkingType: o.cfg.OpenAIFlashThinkingType, reasoningEffort: o.cfg.OpenAIFlashReasoningEffort}
	if !profile.configured() {
		return nil, ChatUsage{}, fmt.Errorf("Flash LLM 未配置")
	}
	skeleton := avatar.FixedStoryboard(sentences)
	template, _ := json.Marshal(skeleton)
	script, _ := json.Marshal(sentences)
	system := `你是口播分镜规划器。只返回 JSON 对象 {"shots":[...]}，严格沿用模板的三镜 id、angle、framing、card。按语义将全部原稿句子按原序分配到三镜，sentence_ids 为从0开始的索引，每句恰好一次，每镜至少一句。text 为分配句子的原文连接，禁止改写。keyword 必须为本镜原文中的连续短语，最多18字。layout 仅可为 full 或 split。prompt 只写简短的自然动作要求，不添加新台词，不改变人物外观、衣服和环境。不输出代码。`
	result, err := o.completeNonStream(ctx, profile, map[string]any{"model": profile.model, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": "原稿句子：" + string(script) + "\n模板：" + string(template)}}, "temperature": 0.2, "max_tokens": 2000})
	avatar.RecordModelUsage(ctx, result.usage)
	if err != nil {
		return nil, ChatUsage{}, err
	}
	content := strings.TrimSpace(result.content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	var out struct {
		Shots []avatar.Shot `json:"shots"`
	}
	if json.Unmarshal([]byte(content), &out) != nil {
		return nil, result.usage, fmt.Errorf("分镜模型未返回有效 JSON")
	}
	if err = avatar.ValidateStoryboard(out.Shots, sentences); err != nil {
		return nil, result.usage, err
	}
	return out.Shots, result.usage, nil
}
