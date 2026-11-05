package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

const scribeSilenceThreshold = 0.5

type ElevenLabsClient struct {
	cfg        config.Config
	httpClient *http.Client
}

type ScribePhrase struct {
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	Text      string  `json:"text"`
	SpeakerID string  `json:"speaker_id,omitempty"`
}

type ScribeResult struct {
	RawResult        map[string]any   `json:"raw_result"`
	PackedTranscript string           `json:"packed_transcript"`
	Phrases          []ScribePhrase   `json:"phrases"`
	Words            []map[string]any `json:"words"`
	AudioEvents      []map[string]any `json:"audio_events"`
	Text             string           `json:"text_content"`
	LanguageCode     string           `json:"language_code,omitempty"`
	Duration         float64          `json:"duration"`
	Model            string           `json:"model"`
	Tokens           int64            `json:"tokens_consumed"`
}

type ScribeSubmission struct {
	RawResult       map[string]any `json:"raw_result"`
	TranscriptionID string         `json:"transcription_id"`
	RequestID       string         `json:"request_id,omitempty"`
	Model           string         `json:"model"`
}

type ElevenLabsHTTPError struct {
	StatusCode int
	Body       string
}

func (e ElevenLabsHTTPError) Error() string {
	return fmt.Sprintf("ElevenLabs Scribe 请求失败: HTTP %d %s", e.StatusCode, strings.TrimSpace(e.Body))
}

func NewElevenLabsClient(cfg config.Config) ElevenLabsClient {
	return ElevenLabsClient{cfg: cfg, httpClient: &http.Client{Timeout: 10 * time.Minute}}
}

func (e ElevenLabsClient) Configured() bool {
	return e.cfg.ElevenLabsConfigured()
}

func (e ElevenLabsClient) TranscribeScribe(ctx context.Context, audio []byte, filename, languageCode string) (ScribeResult, error) {
	if !e.Configured() {
		return ScribeResult{}, fmt.Errorf("ElevenLabs API Key 未配置")
	}
	if filename == "" {
		filename = "audio.mp3"
	}
	model := e.cfg.ElevenLabsScribeModel
	if strings.TrimSpace(model) == "" {
		model = "scribe_v2"
	}

	body, contentType, err := buildScribeMultipart(audio, filename, model, languageCode, nil)
	if err != nil {
		return ScribeResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.cfg.ElevenLabsBaseURL, "/")+"/v1/speech-to-text", &body)
	if err != nil {
		return ScribeResult{}, err
	}
	req.Header.Set("xi-api-key", e.cfg.ElevenLabsAPIKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return ScribeResult{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return ScribeResult{}, fmt.Errorf("ElevenLabs Scribe 请求失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(respBody))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return ScribeResult{}, err
	}
	return packScribeResult(raw, model, transcriptName(filename)), nil
}

func (e ElevenLabsClient) SubmitScribeTranscription(ctx context.Context, audio []byte, filename, languageCode, webhookMetadata string) (ScribeSubmission, error) {
	if !e.Configured() {
		return ScribeSubmission{}, fmt.Errorf("ElevenLabs API Key 未配置")
	}
	if filename == "" {
		filename = "audio.mp3"
	}
	model := e.cfg.ElevenLabsScribeModel
	if strings.TrimSpace(model) == "" {
		model = "scribe_v2"
	}

	fields := map[string]string{"webhook": "true"}
	if strings.TrimSpace(webhookMetadata) != "" {
		fields["webhook_metadata"] = strings.TrimSpace(webhookMetadata)
	}
	body, contentType, err := buildScribeMultipart(audio, filename, model, languageCode, fields)
	if err != nil {
		return ScribeSubmission{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.cfg.ElevenLabsBaseURL, "/")+"/v1/speech-to-text", &body)
	if err != nil {
		return ScribeSubmission{}, err
	}
	req.Header.Set("xi-api-key", e.cfg.ElevenLabsAPIKey)
	req.Header.Set("Content-Type", contentType)

	raw, err := e.doJSON(req)
	if err != nil {
		return ScribeSubmission{}, err
	}
	requestID := firstString(raw, "request_id", "requestId")
	transcriptionID := firstString(raw, "transcription_id", "transcriptionId", "id")
	if transcriptionID == "" {
		transcriptionID = requestID
	}
	return ScribeSubmission{
		RawResult:       raw,
		TranscriptionID: transcriptionID,
		RequestID:       requestID,
		Model:           model,
	}, nil
}

func (e ElevenLabsClient) GetScribeTranscript(ctx context.Context, transcriptionID, name string) (ScribeResult, error) {
	if !e.Configured() {
		return ScribeResult{}, fmt.Errorf("ElevenLabs API Key 未配置")
	}
	transcriptionID = strings.TrimSpace(transcriptionID)
	if transcriptionID == "" {
		return ScribeResult{}, fmt.Errorf("缺少转录任务 ID")
	}
	model := e.cfg.ElevenLabsScribeModel
	if strings.TrimSpace(model) == "" {
		model = "scribe_v2"
	}
	endpoint := strings.TrimRight(e.cfg.ElevenLabsBaseURL, "/") + "/v1/speech-to-text/transcripts/" + url.PathEscape(transcriptionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ScribeResult{}, err
	}
	req.Header.Set("xi-api-key", e.cfg.ElevenLabsAPIKey)
	raw, err := e.doJSON(req)
	if err != nil {
		return ScribeResult{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = transcriptionID
	}
	return packScribeResult(raw, model, transcriptName(name)), nil
}

func buildScribeMultipart(audio []byte, filename, model, languageCode string, fields map[string]string) (bytes.Buffer, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return body, "", err
	}
	if _, err := part.Write(audio); err != nil {
		return body, "", err
	}
	_ = writer.WriteField("model_id", model)
	_ = writer.WriteField("diarize", "true")
	_ = writer.WriteField("tag_audio_events", "true")
	_ = writer.WriteField("timestamps_granularity", "word")
	if strings.TrimSpace(languageCode) != "" {
		_ = writer.WriteField("language_code", strings.TrimSpace(languageCode))
	}
	for key, value := range fields {
		if strings.TrimSpace(value) != "" {
			_ = writer.WriteField(key, strings.TrimSpace(value))
		}
	}
	if err := writer.Close(); err != nil {
		return body, "", err
	}
	return body, writer.FormDataContentType(), nil
}

func (e ElevenLabsClient) doJSON(req *http.Request) (map[string]any, error) {
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, ElevenLabsHTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(respBody))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func packScribeResult(raw map[string]any, model string, name string) ScribeResult {
	words := collectScribeWords(raw)
	phrases := groupScribePhrases(words, scribeSilenceThreshold)
	text, _ := raw["text"].(string)
	languageCode, _ := raw["language_code"].(string)
	var duration float64
	if len(words) > 0 {
		for _, word := range words {
			if end := numberToFloat(word["end"]); end > duration {
				duration = end
			}
		}
	}
	if duration == 0 && len(phrases) > 0 {
		duration = phrases[len(phrases)-1].End
	}
	if text == "" {
		parts := make([]string, 0, len(phrases))
		for _, phrase := range phrases {
			parts = append(parts, phrase.Text)
		}
		text = strings.Join(parts, "\n")
	}
	events := make([]map[string]any, 0)
	for _, word := range words {
		if typeString(word["type"]) == "audio_event" {
			events = append(events, word)
		}
	}
	return ScribeResult{
		RawResult:        raw,
		PackedTranscript: renderScribeMarkdown(name, phrases, scribeSilenceThreshold),
		Phrases:          phrases,
		Words:            words,
		AudioEvents:      events,
		Text:             text,
		LanguageCode:     languageCode,
		Duration:         duration,
		Model:            model,
		Tokens:           int64(duration * 15),
	}
}

func transcriptName(filename string) string {
	base := strings.TrimSpace(filepath.Base(filename))
	if base == "" || base == "." {
		return "elevenlabs_scribe"
	}
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "" {
		return "elevenlabs_scribe"
	}
	return base
}

func collectScribeWords(raw map[string]any) []map[string]any {
	var words []map[string]any
	appendWords := func(value any) {
		for _, item := range anySlice(value) {
			if word, ok := item.(map[string]any); ok {
				words = append(words, word)
			}
		}
	}
	appendWords(raw["words"])
	for _, rawTranscript := range anySlice(raw["transcripts"]) {
		if transcript, ok := rawTranscript.(map[string]any); ok {
			appendWords(transcript["words"])
		}
	}
	sort.SliceStable(words, func(i, j int) bool {
		return numberToFloat(words[i]["start"]) < numberToFloat(words[j]["start"])
	})
	return words
}

func groupScribePhrases(words []map[string]any, silenceThreshold float64) []ScribePhrase {
	var phrases []ScribePhrase
	var current []map[string]any
	var currentStart float64
	var currentSpeaker string
	var hasStart bool
	var prevEnd *float64

	flush := func() {
		if len(current) == 0 {
			hasStart = false
			currentSpeaker = ""
			return
		}
		parts := make([]string, 0, len(current))
		for _, word := range current {
			raw := strings.TrimSpace(fmt.Sprint(word["text"]))
			if raw == "" {
				continue
			}
			if typeString(word["type"]) == "audio_event" && !strings.HasPrefix(raw, "(") {
				raw = "(" + raw + ")"
			}
			parts = append(parts, raw)
		}
		if len(parts) > 0 {
			end := numberToFloat(current[len(current)-1]["end"])
			if end == 0 {
				end = numberToFloat(current[len(current)-1]["start"])
			}
			text := normalizePackedText(strings.Join(parts, " "))
			phrases = append(phrases, ScribePhrase{Start: currentStart, End: end, Text: text, SpeakerID: currentSpeaker})
		}
		current = nil
		hasStart = false
		currentSpeaker = ""
	}

	for _, word := range words {
		wordType := typeString(word["type"])
		if wordType == "spacing" {
			start := numberToFloat(word["start"])
			end := numberToFloat(word["end"])
			if start > 0 || end > 0 {
				if end-start >= silenceThreshold {
					flush()
				}
			}
			continue
		}

		start := numberToFloat(word["start"])
		speaker := strings.TrimSpace(fmt.Sprint(word["speaker_id"]))
		if speaker == "<nil>" {
			speaker = ""
		}
		if currentSpeaker != "" && speaker != "" && speaker != currentSpeaker {
			flush()
		}
		if prevEnd != nil && start-*prevEnd >= silenceThreshold {
			flush()
		}
		if !hasStart {
			currentStart = start
			currentSpeaker = speaker
			hasStart = true
		}
		current = append(current, word)
		end := numberToFloat(word["end"])
		if end == 0 {
			end = start
		}
		prevEnd = &end
	}
	flush()
	return phrases
}

func renderScribeMarkdown(name string, phrases []ScribePhrase, silenceThreshold float64) string {
	var lines []string
	lines = append(lines, "# Packed transcripts", "")
	lines = append(lines, fmt.Sprintf("Phrase-level, grouped on silences >= %.1fs or speaker change.", silenceThreshold))
	lines = append(lines, "Use `[start-end]` ranges to address cuts while retaining word-boundary precision.", "")
	lines = append(lines, fmt.Sprintf("## %s  (duration: %s, %d phrases)", name, formatScribeDuration(phraseDuration(phrases)), len(phrases)))
	if len(phrases) == 0 {
		lines = append(lines, "  _no speech detected_")
		return strings.Join(lines, "\n")
	}
	for _, phrase := range phrases {
		speaker := ""
		if phrase.SpeakerID != "" {
			speaker = " S" + strings.TrimPrefix(phrase.SpeakerID, "speaker_")
		}
		lines = append(lines, fmt.Sprintf("  [%s-%s]%s %s", formatScribeTime(phrase.Start), formatScribeTime(phrase.End), speaker, phrase.Text))
	}
	return strings.Join(lines, "\n")
}

func anySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	default:
		return nil
	}
}

func typeString(value any) string {
	if value == nil {
		return "word"
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func normalizePackedText(text string) string {
	replacer := strings.NewReplacer(" ,", ",", " .", ".", " ?", "?", " !", "!", " ;", ";", " :", ":")
	return replacer.Replace(text)
}

func phraseDuration(phrases []ScribePhrase) float64 {
	if len(phrases) == 0 {
		return 0
	}
	return phrases[len(phrases)-1].End - phrases[0].Start
}

func formatScribeTime(seconds float64) string {
	return fmt.Sprintf("%06.2f", seconds)
}

func formatScribeDuration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	minutes := int(seconds / 60)
	remaining := seconds - float64(minutes*60)
	return fmt.Sprintf("%dm %04.1fs", minutes, remaining)
}
