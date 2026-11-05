package providers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// isMovieStoryLineOutlineRequest identifies the desktop movie-mode whole-film
// story-line outline request. The PC client sends movie_outline=true; older or
// alternate callers may use purpose=movie_story_line_outline.
func isMovieStoryLineOutlineRequest(req map[string]any) bool {
	if value, ok := req["movie_outline"]; ok {
		switch typed := value.(type) {
		case bool:
			if typed {
				return true
			}
		case string:
			switch strings.ToLower(strings.TrimSpace(typed)) {
			case "true", "yes", "1", "是":
				return true
			}
		case float64:
			if typed == 1 {
				return true
			}
		}
	}
	purpose := strings.ToLower(strings.TrimSpace(fmt.Sprint(req["purpose"])))
	return purpose == "movie_story_line_outline"
}

// movieStoryLineOutlineTaskSuffix is the cache-aware (dynamic) half of the
// movie story-line outline contract. It is used when the request carries a
// reusable cache_context; otherwise the same rules come from
// movieStoryLineOutlinePrompt.
func movieStoryLineOutlineTaskSuffix(req map[string]any) string {
	lines := []string{
		"你正在执行 movie_story_line_outline：把整段素材切分成若干条完整故事线，供剪辑关键剧情音轨图使用。",
		"必须先通读全部 ASR 对白，再结合镜头切点与公开背景资料（仅作背景参考）确定每条故事线的边界。",
		"每条故事线是一个完整剧情单元，可以由多段对白、多个镜头和若干镜头切点共同组成；绝对不要把单句对白，也不要把两段对白之间的空隙单独当成一条故事线。",
		"必须输出 events 数组（每条故事线一项）；缺少 events 数组视为无效输出。旧格式 beats 仅为兼容字段，本次不要输出重复的 beats 数组。",
		"每条故事线必须包含数字 start/end（秒）、非空 title 与 description、dialogueIds、priority（must/should/optional）和 score（0 到 1）。",
		"故事线数量按故事线计，通常 3~40 条；不要拆成 20~60 个细小节点。",
		"同时判断每条故事线用对白是否已经讲清楚，并输出 needsVisual（布尔值）与 visualReason（字符串）：只靠对白仍无法确定说话人是谁（例如只知道是角色1、角色2），或这条故事线跨度很长但对白很少、与前后剧情衔接不上时，设 needsVisual=true 并写清缺什么；对白已讲清楚则设 needsVisual=false、visualReason 为空字符串。不要把所有故事线都标成 true。",
	}
	if language := strings.TrimSpace(fmt.Sprint(req["language"])); language != "" && language != "<nil>" {
		lines = append(lines, "输出语言："+language)
	}
	if prompt := strings.TrimSpace(fmt.Sprint(req["prompt"])); prompt != "" && prompt != "<nil>" {
		lines = append(lines, "用户补充要求："+prompt)
	}
	lines = append(lines, "只输出 JSON，不要输出 Markdown、解释文字或额外字段。")
	return strings.Join(lines, "\n")
}

// movieStoryLineOutlinePrompt is the whole-film story-line segmentation
// contract. It deliberately replaces the generic narrative-understanding
// prompt, which asked for 20-60 small events and caused the model to treat the
// gaps between two dialogue cues as standalone plot units.
func movieStoryLineOutlinePrompt(req map[string]any) (string, string) {
	system := `当前任务是 movie_story_line_outline。你是 NarratoAI 的影视故事线切分助手。你的唯一任务是通读整段素材的全部 ASR 对白，结合镜头切点与公开背景资料（仅作背景参考），把整段素材切分成若干条完整的故事线。

硬性规则：
- 每条故事线是一个完整剧情单元，可以由多段对白、多个镜头和若干镜头切点共同组成，必须能独立讲清“发生了什么、涉及谁、产生了什么变化或结果”。
- 绝对不要把单句对白当成一条故事线，也绝对不要把两段对白之间的空隙当成一条故事线；故事线边界由剧情单元决定，而不是由对白间隙决定。
- 故事线按时间顺序排列，相邻故事线可以紧邻但不要相互重叠；start/end 使用秒，必须是数字，且 start < end。
- dialogueIds 只能引用输入中真实存在的对白编号，并且必须列出这条故事线覆盖到的全部对白编号。
- priority 只能是 must、should 或 optional；score 必须是 0 到 1 的数字。
- 只能使用输入中提供的事实与对白，不得凭空补写未出现的剧情、人物或时间。
- 同时判断每条故事线用对白是否已经讲清楚，并输出 needsVisual（布尔值）与 visualReason（字符串）：只靠对白仍无法确定说话人是谁（例如只知道是角色1、角色2），或这条故事线跨度很长但对白很少、与前后剧情衔接不上时，设 needsVisual=true 并写清缺什么；对白已讲清楚则设 needsVisual=false、visualReason 为空字符串。不要把所有故事线都标成 true。
- 只输出 JSON，不要输出 Markdown、解释文字或额外字段。

输出纪律（最高优先级）：
- 必须最先完整输出 events 数组，每条故事线一项；events 数量按故事线计，通常 3~40 条即可，不要拆成 20~60 个细小节点。
- events 是唯一故事线数组；旧格式 beats 仅为兼容字段，本次不要输出 beats，也不要用 summary 重复每条 description。
- 长素材输出容易截断：title 不超过 20 字，description 不超过 80 字，不要复述逐句对白。

JSON 结构：
{
  "outline": "一段完整、连贯的主线剧情大纲",
  "events": [
    {"start": 0, "end": 45.6, "title": "故事线短标题", "description": "这条故事线发生了什么", "dialogueIds": [1, 2, 3], "priority": "must", "score": 0.9, "needsVisual": false, "visualReason": ""}
  ]
}`
	raw, _ := json.MarshalIndent(req, "", "  ")
	user := "请根据以下全片对白、镜头切点和公开资料切分完整故事线。所有时间单位都是秒。\n\n" + string(raw)
	return system, user
}
