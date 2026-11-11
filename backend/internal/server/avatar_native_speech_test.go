package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

func TestNativeSpeechGeneratesVideoWithoutTTSAndReusesItsSound(t *testing.T) {
	w, s, _, _ := talkingWorkflow(t)
	s.Input.SpeechMode = avatar.NativeSpeechMode
	s.Input.UseAvatar = true
	s.Input.Direction = "暖色演播室"
	s.Segments = []avatar.Segment{{ID: "p1", Text: "这是原声口播。"}}
	spy := &recordingSpy{workflowProviderSpy: workflowProviderSpy{ready: true}}
	w.app.avatarMVP.provider = spy
	w.app.avatarMVP.options.Model = avatar.NativeSpeechModel
	w.app.avatarMVP.options.Protocol = "runninghub"
	s.ConfigSnapshot["video"] = w.app.avatarMVP.options.VideoConfigID()
	var stages []string
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		stages = append(stages, stage)
		switch stage {
		case "audio_review":
			return json.RawMessage(`{}`), nil
		case "native_speech":
			os.MkdirAll(filepath.Join(dir, "p1"), 0700)
			for _, name := range []string{"native.mp4", "audio.wav", "timestamps.json", "alignment-cache.json"} {
				os.WriteFile(filepath.Join(dir, "p1", name), []byte("test"), 0600)
			}
			return json.RawMessage(`{"duration":5.24,"video":"p1/native.mp4","audio":"p1/audio.wav"}`), nil
		}
		t.Fatalf("unexpected media stage %s", stage)
		return nil, nil
	}
	if err := w.save(s); err != nil {
		t.Fatal(err)
	}
	if err := w.step(context.Background(), s, "F04/p1", w.audioFingerprint(s, s.Segments[0]), func(ctx context.Context, step *avatar.WorkflowStep) error {
		return w.produceNativeSpeech(ctx, s, &s.Segments[0], step)
	}); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) != 1 {
		t.Fatalf("seedance submissions=%d", len(spy.requests))
	}
	req := spy.requests[0]
	if !req.GenerateAudio || len(req.AudioURLs) != 0 || !strings.Contains(req.Prompt, s.Segments[0].Text) {
		t.Fatalf("%#v", req)
	}
	if s.Segments[0].Duration != 5.24 || s.Segments[0].NativeVideo != "p1/native.mp4" {
		t.Fatalf("%#v", s.Segments)
	}
	need := avatar.MaterialNeed{ID: "n1", SegmentID: "p1", Kind: "avatar", Duration: 5.24}
	if err := w.produceMaterial(context.Background(), s, need, "F06"); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) != 1 || len(s.Materials) != 1 {
		t.Fatal("F06 generated a second talking video")
	}
	s.TargetNode, s.ScriptConfirmed = "F06", true
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		return json.RawMessage(`{"needs":[]}`), nil
	}
	w.save(s)
	err := w.produceWorkflow(context.Background(), s, uuid.MustParse(w.ownerID(s)))
	if !errors.Is(err, errWorkflowPaused) || !s.AudioConfirmed || s.Confirmation == "audio" || len(spy.requests) != 1 {
		t.Fatalf("native workflow stopped for TTS or regenerated person: %v, audio=%v confirmation=%s", err, s.AudioConfirmed, s.Confirmation)
	}
}

func TestNativeSpeechReferenceAndKnownOrderResume(t *testing.T) {
	w, s, _, _ := talkingWorkflow(t)
	s.Input.SpeechMode = avatar.NativeSpeechMode
	s.Input.VoiceReference = "voice"
	s.Input.Assets = append(s.Input.Assets, avatar.SourceAsset{ID: "voice", Kind: "audio", Path: "assets/voice.wav", Duration: 5})
	if err := os.WriteFile(filepath.Join(w.projectDir(s.ProjectID), "assets", "voice.wav"), []byte("reference"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "这是新台词。"}}
	w.app.avatarMVP.options.Model = avatar.NativeSpeechModel
	w.app.avatarMVP.options.Protocol = "runninghub"
	s.ConfigSnapshot["video"] = w.app.avatarMVP.options.VideoConfigID()
	spy := &recordingSpy{}
	w.app.avatarMVP.provider = spy
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		if stage == "voice_reference" {
			os.WriteFile(filepath.Join(dir, "voice-reference.wav"), []byte("voice"), 0600)
			return json.RawMessage(`{"path":"voice-reference.wav"}`), nil
		}
		if stage != "native_speech" {
			t.Fatalf("unexpected media stage %s", stage)
		}
		os.MkdirAll(filepath.Join(dir, "p1"), 0700)
		for _, name := range []string{"native.mp4", "audio.wav", "timestamps.json", "alignment-cache.json"} {
			os.WriteFile(filepath.Join(dir, "p1", name), []byte("fixture"), 0600)
		}
		return json.RawMessage(`{"duration":5,"video":"p1/native.mp4","audio":"p1/audio.wav"}`), nil
	}
	w.save(s)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if w.produceAudio(ctx, s, &s.Segments[0], &avatar.WorkflowStep{}) == nil {
		t.Fatal("expected interrupted query")
	}
	if len(spy.requests) != 1 || len(spy.requests[0].AudioURLs) != 1 || !strings.Contains(spy.requests[0].Prompt, "不复述参考音频") {
		t.Fatal("reference voice not sent")
	}
	loaded, err := w.load(context.Background(), uuid.MustParse(w.ownerID(s)), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	spy.ready = true
	if err = w.produceAudio(context.Background(), &loaded, &loaded.Segments[0], &avatar.WorkflowStep{}); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) != 1 {
		t.Fatal("resume resubmitted a paid order")
	}
}

func TestNativeSpeechUnknownOrderBlocksDependentSegments(t *testing.T) {
	w, s, _, _ := talkingWorkflow(t)
	s.Input.SpeechMode = avatar.NativeSpeechMode
	w.app.avatarMVP.options.Model = avatar.NativeSpeechModel
	w.app.avatarMVP.options.Protocol = "runninghub"
	s.ConfigSnapshot["video"] = w.app.avatarMVP.options.VideoConfigID()
	s.ScriptConfirmed = true
	s.Segments = []avatar.Segment{{ID: "p1", Text: "首段。"}, {ID: "p2", Text: "后段。"}}
	s.Steps = []avatar.WorkflowStep{{ID: "F04/p1-c1", Submission: "unknown", Status: "failed", Model: avatar.NativeSpeechModel}}
	spy := &recordingSpy{}
	w.app.avatarMVP.provider = spy
	w.save(s)
	if err := w.produceWorkflow(context.Background(), s, uuid.MustParse(w.ownerID(s))); err == nil {
		t.Fatal("unknown order ignored")
	}
	if len(spy.requests) != 0 {
		t.Fatal("resubmitted unknown order or started a dependent voice")
	}
}

func TestNativeSpeechIgnoresLegacyTTSConfiguration(t *testing.T) {
	a := newTestApp(t)
	in := avatar.WorkflowInput{SpeechMode: avatar.NativeSpeechMode, Style: "flat-vector", Ratio: "9:16"}
	if err := a.avatarWorkflow.validateInput(&in, false); err != nil {
		t.Fatal(err)
	}
}

// The removed TTS pipeline must not be reachable from a draft any more.
func TestTTSSpeechModeIsRejected(t *testing.T) {
	a := newTestApp(t)
	for _, mode := range []string{"", "tts"} {
		in := avatar.WorkflowInput{SpeechMode: mode, Style: "flat-vector", Ratio: "9:16", Voice: "Cherry"}
		if err := a.avatarWorkflow.validateInput(&in, false); err == nil {
			t.Fatalf("speech_mode %q must be rejected after the TTS pipeline was removed", mode)
		}
	}
}

func TestNativeSpeechRejectsTTSOnlyVoiceControls(t *testing.T) {
	a := newTestApp(t)
	cases := []struct {
		name string
		in   avatar.WorkflowInput
	}{
		{"voice model", avatar.WorkflowInput{SpeechMode: avatar.NativeSpeechMode, VoiceModel: "minimax/speech-2.8-hd"}},
		{"voice speed", avatar.WorkflowInput{SpeechMode: avatar.NativeSpeechMode, VoiceSpeed: 1.2}},
		{"segment speed", avatar.WorkflowInput{SpeechMode: avatar.NativeSpeechMode, SegmentVoiceSpeeds: map[string]float64{"p1": 1.1}}},
	}
	for _, c := range cases {
		c.in.Style, c.in.Ratio = "flat-vector", "9:16"
		if err := a.avatarWorkflow.validateInput(&c.in, false); err == nil {
			t.Fatalf("%s must be rejected for native speech", c.name)
		}
	}
}

func TestNativeSpeechRevisionsReuseSoundUntilSpeechOrPersonChanges(t *testing.T) {
	a, _, _, previous := workflowFixture(t)
	w := a.avatarWorkflow
	previous.Input.SpeechMode = avatar.NativeSpeechMode
	previous.ScriptConfirmed, previous.AudioConfirmed = true, true
	previous.Segments = []avatar.Segment{{ID: "p1", Text: "完整台词。", Audio: "p1/audio.wav", NativeVideo: "p1/native.mp4", Duration: 5}}
	previous.Segments[0].Fingerprint = w.nativeSpeechFingerprint(&previous, previous.Segments[0])
	for _, name := range []string{"audio.wav", "native.mp4", "timestamps.json", "alignment-cache.json"} {
		path := filepath.Join(w.dir(previous.ID), "p1", name)
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte("valid fixture"), 0600)
	}
	for _, from := range []string{"F05", "F07", "F10"} {
		next := avatar.WorkflowState{ID: uuid.NewString(), ProjectID: previous.ProjectID, Input: previous.Input, ConfigSnapshot: previous.ConfigSnapshot}
		if err := w.inherit(&next, previous, avatar.WorkflowEdits{RerunFrom: from}); err != nil {
			t.Fatal(err)
		}
		if !next.AudioConfirmed || next.Segments[0].NativeVideo == "" {
			t.Fatalf("%s regenerated paid sound", from)
		}
	}
	next := avatar.WorkflowState{ID: uuid.NewString(), ProjectID: previous.ProjectID, Input: previous.Input, ConfigSnapshot: previous.ConfigSnapshot}
	if err := w.inherit(&next, previous, avatar.WorkflowEdits{NativePrompts: map[string]string{"p1": "自然微笑"}, RegenerateSegments: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}
	if next.AudioConfirmed || next.Segments[0].NativeVideo != "" || next.Segments[0].NativePrompt != "自然微笑" {
		t.Fatal("person edit reused old audio/video")
	}
	firstHash := w.nativeSpeechFingerprint(&next, next.Segments[0])
	// A subsequent local edit must retain the paid regeneration identity.
	next.Edits = avatar.WorkflowEdits{}
	if firstHash != w.nativeSpeechFingerprint(&next, next.Segments[0]) {
		t.Fatal("regenerated fingerprint drifts across runs")
	}
}

func TestNativeSpeechPersonEditRestartsAudioAndIsIdempotent(t *testing.T) {
	a, s := completedNodeFixture(t)
	w := a.avatarWorkflow
	s.Input.SpeechMode = avatar.NativeSpeechMode
	s.Needs[0].Kind = "avatar"
	s.Segments[0].NativeVideo = "p1/native.mp4"
	s.Segments[0].Fingerprint = w.nativeSpeechFingerprint(&s, s.Segments[0])
	os.WriteFile(filepath.Join(w.dir(s.ID), "p1", "native.mp4"), []byte("video"), 0600)
	if err := a.db.Model(&models.Project{}).Where("id=?", s.ProjectID).Update("project_metadata", jsonMap(map[string]any{"kind": "avatar_mvp", "avatar_v2": s.Input})).Error; err != nil {
		t.Fatal(err)
	}
	w.save(&s)
	h := avatarPackageHost{a}
	owner := w.ownerID(&s)
	req := materialNodeRequest{Action: "generate", BaseRunID: s.ID, Prompt: "暖色背景，自然手势", RequestID: "native-person-edit"}
	out, err := h.materialNodeAction(context.Background(), owner, s.ProjectID, "n1", req)
	if err != nil {
		t.Fatal(err)
	}
	next, err := w.load(context.Background(), uuid.MustParse(owner), out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if next.TargetNode != "F04" || next.Edits.MaterialOnly != "" || next.AudioConfirmed || next.Segments[0].NativeVideo != "" || next.Segments[0].NativePrompt != req.Prompt {
		t.Fatalf("person edit retained stale sound or timing: target=%s materialOnly=%q audioConfirmed=%v nativeVideo=%q nativePrompt=%q needs=%d",
			next.TargetNode, next.Edits.MaterialOnly, next.AudioConfirmed, next.Segments[0].NativeVideo, next.Segments[0].NativePrompt, len(next.Needs))
	}
	again, err := h.materialNodeAction(context.Background(), owner, s.ProjectID, "n1", req)
	if err != nil || again.RunID != out.RunID {
		t.Fatalf("non-idempotent person edit: %v", err)
	}
}
