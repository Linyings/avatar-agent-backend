package providers

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Base64 video is limited by DashScope to under 10 MB after encoding.
const maxMovieVisionVideoDataURL = 10*1024*1024 - 1024

// MovieVisionMessages accepts a bounded inline JPEG contact sheet or an inline MP4
// clip, never a server-fetched URL. A clip lets an omni model watch the motion and
// hear the segment's own audio instead of reading nine stills.
func MovieVisionMessages(request map[string]any) ([]map[string]any, error) {
	prompt, _ := request["prompt"].(string)
	if strings.TrimSpace(prompt) == "" || len(prompt) > 100000 {
		return nil, fmt.Errorf("画面补证提示词为空或过长")
	}
	if video, ok := request["video_data_url"].(string); ok {
		if _, image := request["image_data_url"]; image {
			return nil, fmt.Errorf("画面分析只能提交图片或视频其中一种")
		}
		if len(video) > maxMovieVisionVideoDataURL || !strings.HasPrefix(video, "data:video/mp4;base64,") {
			return nil, fmt.Errorf("画面分析视频需要不超过 10 MB 的 MP4 片段")
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(video, "data:video/mp4;base64,"))
		// An MP4 starts with a box whose type is "ftyp" at byte 4.
		if err != nil || len(decoded) < 12 || string(decoded[4:8]) != "ftyp" {
			return nil, fmt.Errorf("画面分析视频格式无效")
		}
		return []map[string]any{
			{"role": "system", "content": "依据视频画面和其中的声音补充影视剧情证据。只返回请求指定的 JSON，不把待核对问题当成事实。"},
			{"role": "user", "content": []map[string]any{{"type": "video_url", "video_url": map[string]any{"url": video}}, {"type": "text", "text": prompt}}},
		}, nil
	}
	image, _ := request["image_data_url"].(string)
	if len(image) > 12*1024*1024 || !strings.HasPrefix(image, "data:image/jpeg;base64,") {
		return nil, fmt.Errorf("画面补证需要不超过 12 MB 的 JPEG 图片输入")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(image, "data:image/jpeg;base64,"))
	if err != nil || len(decoded) < 3 || decoded[0] != 0xff || decoded[1] != 0xd8 || decoded[2] != 0xff {
		return nil, fmt.Errorf("画面补证图片格式无效")
	}
	return []map[string]any{
		{"role": "system", "content": "依据图片和时间编号补充影视剧情证据。只返回请求指定的 JSON，不把待核对问题当成事实。"},
		{"role": "user", "content": []map[string]any{{"type": "text", "text": prompt}, {"type": "image_url", "image_url": map[string]any{"url": image}}}},
	}, nil
}
