package media

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Request is the provider-neutral input. The adapter maps it onto the model's
// own field names and snaps values to what the schema offers.
type Request struct {
	Prompt     string   `json:"prompt"`
	Negative   string   `json:"negative_prompt,omitempty"`
	Lyrics     string   `json:"lyrics,omitempty"`
	Images     []string `json:"images,omitempty"` // first/last frame, or reference images
	Video      string   `json:"video,omitempty"`  // enhancement source
	Audio      string   `json:"audio,omitempty"`  // talking-avatar audio
	Duration   int      `json:"duration,omitempty"`
	Ratio      string   `json:"ratio,omitempty"`      // e.g. 9:16
	Resolution string   `json:"resolution,omitempty"` // e.g. 720p, 2k
	// NativeAudio asks a video model to generate its own soundtrack. Off by
	// default: the workflow mixes voice, music and effects itself.
	NativeAudio bool `json:"native_audio,omitempty"`
}

var (
	ratioFields       = []string{"aspectRatio", "ratio"}
	firstFrameFields  = []string{"firstImageUrl", "firstFrameUrl"}
	lastFrameFields   = []string{"lastImageUrl", "lastFrameUrl", "endImageUrl"}
	referenceFields   = []string{"imageUrls", "referenceImages", "keyframes"}
	audioSwitchFields = []string{"generateAudio", "sound", "audio", "generateAudioSwitch", "enableAudio"}
)

// Body builds the JSON body for this model or explains why the request cannot
// be satisfied, before anything is submitted or paid for.
func (m Model) Body(r Request) (map[string]any, error) {
	b := map[string]any{}
	has := func(name string) bool { _, ok := m.field(name); return ok }
	r.Prompt = strings.TrimSpace(r.Prompt)

	// Text.
	switch {
	case has("text_prompt"):
		if r.Prompt == "" {
			return nil, fmt.Errorf("%s 需要提示词", m.Name)
		}
		b["text_prompt"] = r.Prompt
	case m.Family == "suno" && has("description"):
		b["description"] = r.Prompt
		if has("make_instrumental") {
			b["make_instrumental"] = "true"
		}
	case m.Family == "suno" && has("tags"):
		b["tags"] = r.Prompt
		b["prompt"] = firstNonEmpty(r.Lyrics, "[Instrumental]")
	case has("prompt"):
		if r.Prompt != "" {
			b["prompt"] = r.Prompt
		}
	}
	if has("title") {
		b["title"] = clip(firstNonEmpty(r.Prompt, "Untitled"), 60)
	}
	if has("lyrics") {
		if r.Lyrics == "" && m.required("lyrics") {
			return nil, fmt.Errorf("%s 需要歌词，背景音乐请选择纯音乐模型", m.Name)
		}
		if r.Lyrics != "" {
			b["lyrics"] = r.Lyrics
		}
	}
	if r.Negative != "" && has("negativePrompt") {
		b["negativePrompt"] = r.Negative
	}

	// Media inputs.
	switch {
	case has("firstImageUrl") || has("firstFrameUrl"):
		name := "firstImageUrl"
		if has("firstFrameUrl") {
			name = "firstFrameUrl"
		}
		if len(r.Images) > 0 {
			b[name] = r.Images[0]
		}
		if len(r.Images) > 1 {
			for _, last := range lastFrameFields {
				if has(last) {
					b[last] = r.Images[1]
					break
				}
			}
		}
	case has("imageUrl") && m.Kind != KindR2V:
		if len(r.Images) > 0 {
			b["imageUrl"] = r.Images[0]
		}
	case has("image"):
		if len(r.Images) > 0 {
			b["image"] = r.Images[0]
		}
	}
	for _, name := range referenceFields {
		f, ok := m.field(name)
		if !ok || len(r.Images) == 0 || (m.Kind != KindR2V && !m.required(name) && b["firstImageUrl"] != nil) {
			continue
		}
		images := r.Images
		if f.MaxItems > 0 && len(images) > f.MaxItems {
			images = images[:f.MaxItems]
		}
		b[name] = images
		break
	}
	for _, name := range []string{"videoUrl"} {
		if has(name) && r.Video != "" {
			b[name] = r.Video
		}
	}
	if has("audioUrl") && r.Audio != "" && m.Kind == KindAvatar {
		b["audioUrl"] = r.Audio
	}

	// Shape and timing.
	if err := m.setDuration(b, r.Duration); err != nil {
		return nil, err
	}
	m.setRatio(b, r.Ratio)
	m.setResolution(b, r.Resolution)
	if has("width") && has("height") {
		m.setSize(b, r.Ratio)
	}
	for _, name := range audioSwitchFields {
		if f, ok := m.field(name); ok && m.Kind != KindMusic && m.Kind != KindSFX {
			if f.Type == "string" || len(f.Enum) > 0 {
				b[name] = strconv.FormatBool(r.NativeAudio)
			} else {
				b[name] = r.NativeAudio
			}
		}
	}
	if has("bgm") {
		b["bgm"] = false
	}

	// Remaining required fields: take documented defaults, never guess.
	for _, name := range m.Required {
		if _, ok := b[name]; ok {
			continue
		}
		f, ok := m.field(name)
		if ok && f.Default != nil {
			b[name] = normalizeDefault(f)
			continue
		}
		if ok && len(f.Enum) > 0 {
			b[name] = typed(f, f.Enum[0])
			continue
		}
		return nil, fmt.Errorf("%s 缺少必填输入 %s", m.Name, requiredLabel(name))
	}
	// Multi-output generators default to a single result per order.
	for _, name := range []string{"n", "imageNum", "numImages", "maxImages"} {
		if f, ok := m.field(name); ok {
			b[name] = typed(f, "1")
		}
	}
	if has("stream") {
		b["stream"] = false
	}
	return b, nil
}

func requiredLabel(name string) string {
	switch name {
	case "imageUrl", "imageUrls", "firstImageUrl", "firstFrameUrl", "keyframes", "referenceImages", "image":
		return "图片"
	case "videoUrl":
		return "视频"
	case "audioUrl":
		return "音频"
	case "prompt", "text_prompt", "description":
		return "提示词"
	}
	return name
}

func (m Model) setDuration(b map[string]any, seconds int) error {
	f, ok := m.field("duration")
	if !ok {
		return nil
	}
	if seconds <= 0 {
		if f.Default != nil {
			b["duration"] = normalizeDefault(f)
		}
		return nil
	}
	if len(f.Enum) > 0 {
		best, bestDiff := "", math.MaxInt
		for _, v := range f.Enum {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				continue
			}
			// Nearest offered length (ties go longer); the renderer trims or
			// holds the last frame, so cost stays close to what was asked.
			diff := 2 * (n - seconds)
			if diff < 0 {
				diff = -diff + 1
			}
			if diff < bestDiff {
				best, bestDiff = v, diff
			}
		}
		if best == "" {
			return fmt.Errorf("%s 没有可用的时长选项", m.Name)
		}
		b["duration"] = typed(f, best)
		return nil
	}
	v := float64(seconds)
	if f.Min != nil && v < *f.Min {
		v = *f.Min
	}
	if f.Max != nil && v > *f.Max {
		v = *f.Max
	}
	b["duration"] = typed(f, strconv.Itoa(int(v)))
	return nil
}

func ratioValue(r string) float64 {
	a, bb, ok := strings.Cut(r, ":")
	if !ok {
		a, bb, ok = strings.Cut(r, "*")
	}
	if !ok {
		return 0
	}
	x, _ := strconv.ParseFloat(a, 64)
	y, _ := strconv.ParseFloat(bb, 64)
	if x <= 0 || y <= 0 {
		return 0
	}
	return x / y
}

func (m Model) setRatio(b map[string]any, ratio string) {
	target := ratioValue(ratio)
	for _, name := range append(append([]string{}, ratioFields...), "size") {
		f, ok := m.field(name)
		if !ok {
			continue
		}
		if target == 0 || len(f.Enum) == 0 {
			if m.required(name) && f.Default != nil {
				b[name] = normalizeDefault(f)
			}
			return
		}
		best, bestDiff := "", math.MaxFloat64
		for _, v := range f.Enum {
			rv := ratioValue(v)
			if rv == 0 {
				continue
			}
			if d := math.Abs(math.Log(rv / target)); d < bestDiff {
				best, bestDiff = v, d
			}
		}
		if best != "" {
			b[name] = best
		}
		return
	}
}

var resolutionRank = map[string]int{"240p": 1, "360p": 2, "480p": 3, "540p": 4, "720p": 5, "768p": 5, "1k": 6, "hd": 5, "1080p": 7, "fhd": 7, "native1080p": 7, "1.5k": 7, "2k": 8, "3k": 9, "4k": 10, "native4k": 10, "8k": 11}

func (m Model) setResolution(b map[string]any, want string) {
	for _, name := range []string{"resolution", "targetResolution"} {
		f, ok := m.field(name)
		if !ok {
			continue
		}
		if want == "" || len(f.Enum) == 0 {
			if m.required(name) {
				b[name] = normalizeDefault(f)
				if b[name] == nil && len(f.Enum) > 0 {
					b[name] = f.Enum[0]
				}
			}
			return
		}
		target := resolutionRank[strings.ToLower(want)]
		best, bestDiff := "", math.MaxInt
		for _, v := range f.Enum {
			rank, ok := resolutionRank[strings.ToLower(v)]
			if !ok {
				continue
			}
			d := rank - target
			if d < 0 {
				d = -d
			}
			if d < bestDiff {
				best, bestDiff = v, d
			}
		}
		if best != "" {
			b[name] = best
		} else if m.required(name) {
			b[name] = f.Enum[0]
		}
		return
	}
}

func (m Model) setSize(b map[string]any, ratio string) {
	wf, _ := m.field("width")
	hf, _ := m.field("height")
	long := 2048.0
	if wf.Default != nil {
		if v, err := strconv.ParseFloat(fmt.Sprint(wf.Default), 64); err == nil && v > 0 {
			long = v
		}
	}
	r := ratioValue(ratio)
	if r == 0 {
		r = 1
	}
	w, h := long, long
	if r >= 1 {
		h = long / r
	} else {
		w = long * r
	}
	clamp := func(v float64, f Field) int {
		if f.Min != nil && v < *f.Min {
			v = *f.Min
		}
		if f.Max != nil && v > *f.Max {
			v = *f.Max
		}
		return int(math.Round(v/16) * 16)
	}
	b["width"], b["height"] = clamp(w, wf), clamp(h, hf)
}

func typed(f Field, v string) any {
	if f.Type == "integer" || f.Type == "number" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	if f.Type == "boolean" {
		return v == "true" || v == "True"
	}
	return v
}

func normalizeDefault(f Field) any {
	if f.Default == nil {
		return nil
	}
	switch v := f.Default.(type) {
	case float64:
		if f.Type == "integer" {
			return int(v)
		}
		return v
	case bool:
		if f.Type == "string" {
			return strconv.FormatBool(v)
		}
		return v
	case string:
		return typed(f, v)
	}
	return f.Default
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Sorted family names, for stable catalog output.
func Families(k Kind) []string {
	seen := map[string]bool{}
	for _, m := range ByKind(k) {
		seen[m.Family] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
