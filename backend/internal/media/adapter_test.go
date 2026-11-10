package media

import (
	"strings"
	"testing"
)

func build(t *testing.T, id string, r Request) map[string]any {
	t.Helper()
	m, ok := Find(id)
	if !ok {
		t.Fatalf("model %s missing", id)
	}
	body, err := m.Body(r)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return body
}

func TestVideoBodiesSnapDurationRatioAndResolution(t *testing.T) {
	// kling v2.6 only offers 5 or 10 seconds and its image field is imageUrl.
	b := build(t, "kling-v2.6-pro/image-to-video", Request{Prompt: "海边日落", Images: []string{"https://x/a.png"}, Duration: 7, Ratio: "9:16"})
	if b["imageUrl"] != "https://x/a.png" || b["duration"] != "5" || b["sound"] != "false" || b["prompt"] != "海边日落" {
		t.Fatalf("kling v2.6 body: %#v", b)
	}
	// seedance uses firstFrameUrl, ratio and generateAudio; 9:16 is offered.
	b = build(t, "bytedance/seedance-2.0-global/image-to-video", Request{Prompt: "p", Images: []string{"https://x/a.png", "https://x/b.png"}, Duration: 6, Ratio: "9:16", Resolution: "720p"})
	if b["firstFrameUrl"] != "https://x/a.png" || b["lastFrameUrl"] != "https://x/b.png" || b["ratio"] != "9:16" || b["duration"] != "6" || b["generateAudio"] != false || b["resolution"] != "720p" {
		t.Fatalf("seedance body: %#v", b)
	}
	// veo offers 16:9 / 9:16 only; 1:1 snaps to the nearest offered shape.
	b = build(t, "rhart-video-v3.1-fast/text-to-video", Request{Prompt: "p", Duration: 5, Ratio: "1:1"})
	if b["aspectRatio"] != "9:16" && b["aspectRatio"] != "16:9" {
		t.Fatalf("veo ratio not snapped: %#v", b)
	}
	if b["duration"] != "8" { // the only length veo fast offers
		t.Fatalf("veo duration not snapped: %#v", b)
	}
	// integer duration with min/max is clamped, not enumerated.
	b = build(t, "kling-video-o3-pro/image-to-video", Request{Prompt: "p", Images: []string{"https://x/a.png"}, Duration: 40})
	if b["duration"] != 15 {
		t.Fatalf("integer duration not clamped: %#v", b)
	}
	// reference-to-video passes every image.
	b = build(t, "vidu/reference-to-video-q3", Request{Prompt: "p", Images: []string{"https://x/a.png", "https://x/b.png"}, Duration: 5})
	if imgs, _ := b["imageUrls"].([]string); len(imgs) != 2 {
		t.Fatalf("reference images missing: %#v", b)
	}
}

func TestImageAndAudioBodies(t *testing.T) {
	b := build(t, "rhart-image-n-pro-official/text-to-image", Request{Prompt: "封面", Ratio: "9:16"})
	if b["aspectRatio"] != "9:16" || b["resolution"] == nil {
		t.Fatalf("nano body: %#v", b)
	}
	b = build(t, "seedream-v4.7/text-to-image", Request{Prompt: "封面", Ratio: "16:9"})
	w, _ := b["width"].(int)
	h, _ := b["height"].(int)
	if w <= h || w == 0 {
		t.Fatalf("seedream size not derived from ratio: %#v", b)
	}
	b = build(t, "alibaba/qwen-image-2.0/text-to-image", Request{Prompt: "p", Ratio: "9:16"})
	if s, _ := b["size"].(string); !strings.Contains(s, "*") {
		t.Fatalf("qwen size: %#v", b)
	}
	b = build(t, "mureka-ai/mureka-v9/generate-bgm", Request{Prompt: "轻快科技感"})
	if b["prompt"] != "轻快科技感" || b["n"] != 1 || b["stream"] != false {
		t.Fatalf("mureka body: %#v", b)
	}
	b = build(t, "rhart-audio/suno-v5.5/single", Request{Prompt: "轻快科技感"})
	if b["description"] != "轻快科技感" || b["make_instrumental"] != "true" && b["make_instrumental"] != true {
		t.Fatalf("suno single body: %#v", b)
	}
	b = build(t, "bytedance/doubao-seed-audio-1.0", Request{Prompt: "雨声"})
	if b["text_prompt"] != "雨声" {
		t.Fatalf("seed audio body: %#v", b)
	}
	b = build(t, "rhart-video/video-upscaler", Request{Video: "https://x/v.mp4", Resolution: "1080p"})
	if b["videoUrl"] != "https://x/v.mp4" || b["targetResolution"] != "1080p" {
		t.Fatalf("upscaler body: %#v", b)
	}
}

func TestBodyRejectsUnsatisfiableRequestsLocally(t *testing.T) {
	cases := []struct {
		id string
		r  Request
	}{
		{"kling-v3.0-pro/image-to-video", Request{Prompt: "缺首帧"}},
		{"vidu/reference-to-video-q3", Request{Prompt: "缺参考图"}},
		{"rhart-video/video-upscaler", Request{}},
		{"rhart-image-n-pro-official/text-to-image", Request{}},
		{"rhart-audio/text-to-audio/music-2.5", Request{Prompt: "需要歌词的模型"}},
	}
	for _, tc := range cases {
		m, _ := Find(tc.id)
		if _, err := m.Body(tc.r); err == nil {
			t.Fatalf("%s accepted an unsatisfiable request", tc.id)
		}
	}
}

func TestEveryRegisteredModelBuildsForItsKindsCanonicalRequest(t *testing.T) {
	img := []string{"https://example.com/a.png", "https://example.com/b.png"}
	for _, m := range Models() {
		r := Request{Prompt: "测试提示词", Duration: 5, Ratio: "9:16", Lyrics: "[Verse] 测试歌词", Images: img, Video: "https://example.com/v.mp4", Audio: "https://example.com/a.mp3"}
		body, err := m.Body(r)
		if err != nil {
			t.Fatalf("%s (%s): %v", m.ID, m.Kind, err)
		}
		for _, req := range m.Required {
			if _, ok := body[req]; !ok {
				t.Fatalf("%s: required field %q missing from %#v", m.ID, req, body)
			}
		}
	}
}
