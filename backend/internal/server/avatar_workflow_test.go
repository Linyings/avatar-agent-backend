package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workflowFixture(t *testing.T) (*App, uuid.UUID, models.Project, avatar.WorkflowState) {
	a := newTestApp(t)
	owner := uuid.New()
	a.avatarMVP.options.DataRoot = t.TempDir()
	// Native speech generates picture and sound through the video service, so
	// every workflow fixture needs a working provider stub.
	a.avatarMVP.options.Protocol = "runninghub"
	a.avatarMVP.options.BaseURL = "https://test.invalid"
	a.avatarMVP.options.APIKey = "unit-test"
	a.avatarMVP.options.Model = avatar.NativeSpeechModel
	a.avatarMVP.options.GenerationPath = avatar.SeedancePath
	a.avatarMVP.provider = &workflowProviderSpy{ready: true}
	a.avatarMVP.providerErr = nil
	in := avatar.WorkflowInput{Revision: 1, SpeechMode: avatar.NativeSpeechMode, Script: "第一句。第二句。", Style: "flat-vector", Ratio: "9:16"}
	p := models.Project{Name: "workflow", OwnerID: owner, ProjectMetadata: jsonMap(map[string]any{"kind": "avatar_mvp", "avatar_v2": in})}
	if err := a.db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	s, err := a.avatarWorkflow.create(context.Background(), owner, p.ID, "request-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture exercises the persisted v2 contract; v3 tests opt in explicitly.
	s.Version = "avatar-workflow/2.2"
	a.avatarWorkflow.save(&s)
	return a, owner, p, s
}
func TestAvatarWorkflowSourceChangeKeepsOtherFilesAnalysis(t *testing.T) {
	a, _, _, previous := workflowFixture(t)
	previous.Input.Assets = []avatar.SourceAsset{{ID: "v1", Kind: "video", SHA256: "same"}, {ID: "v2", Kind: "video", SHA256: "old"}}
	previous.Descriptions = []avatar.SourceDescription{{ID: "d1", AssetID: "v1", Text: "unchanged"}, {ID: "d2", AssetID: "v2", Text: "stale"}}
	previous.Steps = []avatar.WorkflowStep{{ID: "F01.4/v1", Status: "succeeded"}, {ID: "F01.4/v1-b0", Status: "succeeded"}, {ID: "F01.4/v2", Status: "succeeded"}}
	next := avatar.WorkflowState{ID: uuid.NewString(), Input: previous.Input, ConfigSnapshot: previous.ConfigSnapshot}
	next.Input.Assets = append([]avatar.SourceAsset(nil), previous.Input.Assets...)
	next.Input.Assets[1].SHA256 = "replacement"
	if err := a.avatarWorkflow.inherit(&next, previous, avatar.WorkflowEdits{}); err != nil {
		t.Fatal(err)
	}
	if len(next.Descriptions) != 1 || next.Descriptions[0].AssetID != "v1" || len(next.Steps) != 2 {
		t.Fatalf("lost independent cache or retained stale descriptions: %#v %#v", next.Descriptions, next.Steps)
	}
	for _, step := range next.Steps {
		if step.CostStatus != "reused" || step.Attempt != 0 {
			t.Fatal("reused analysis billed again")
		}
	}
}
func TestAvatarWorkflowSourceBatchFailureContinuesIndependentAnalysis(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	os.MkdirAll(a.avatarWorkflow.dir(s.ID), 0700)
	s.Input.Assets = []avatar.SourceAsset{{ID: "v1", Kind: "video", Path: "assets/v1", Name: "source", Duration: 40, SHA256: "hash"}}
	dir := a.avatarWorkflow.projectDir(s.ProjectID)
	os.MkdirAll(filepath.Join(dir, "assets"), 0700)
	os.WriteFile(filepath.Join(dir, "assets/v1"), []byte("fixture"), 0600)
	a.avatarWorkflow.save(&s)
	a.avatarWorkflow.media = func(context.Context, string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"batches":[{"start":0,"end":20,"times":[0],"frames":[]},{"start":20,"end":40,"times":[20],"frames":[]}]}`), nil
	}
	calls := 0
	a.avatarWorkflow.content = func(_ context.Context, _ string, in any, _ []string) (json.RawMessage, error) {
		calls++
		if in.(map[string]any)["start"].(float64) == 0 {
			return nil, errors.New("network interrupted")
		}
		return json.RawMessage(`{"clips":[{"start":20,"end":40,"text":"可见场景变化"}]}`), nil
	}
	err := a.avatarWorkflow.produceWorkflow(context.Background(), &s, owner)
	if err == nil || calls != 2 || len(s.Descriptions) != 1 || s.Descriptions[0].Start != 20 {
		t.Fatalf("independent analysis lost: calls=%d clips=%#v err=%v", calls, s.Descriptions, err)
	}
	for _, step := range s.Steps {
		if step.ID == "F02" {
			t.Fatal("partial analysis presented as complete")
		}
	}
}

func TestAvatarWorkflowConfirmationCannotInjectArtifacts(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Status, s.Confirmation, s.ConfirmationVersion = "awaiting_confirmation", "script", "v1"
	a.avatarWorkflow.save(&s)
	err := a.avatarWorkflow.confirm(context.Background(), owner, s.ID, "script", "v1", []avatar.Segment{{ID: "p1", Text: "第一句。第二句。", Audio: "../../secret", Duration: 88, Fingerprint: "forged"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if got.Segments[0].Audio != "" || got.Segments[0].Duration != 0 || got.Segments[0].Fingerprint != "" {
		t.Fatal("trusted client media metadata")
	}
}

func TestAvatarWorkflowArtifactRangeAndOwner(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	os.MkdirAll(a.avatarWorkflow.dir(s.ID), 0700)
	os.WriteFile(filepath.Join(a.avatarWorkflow.dir(s.ID), "output.mp4"), []byte("0123456789"), 0600)
	s.Artifacts = []avatar.WorkflowArtifact{{ID: "video", Path: "output.mp4", Name: "video.mp4", MediaType: "video/mp4"}}
	a.avatarWorkflow.save(&s)
	for _, test := range []struct {
		owner uuid.UUID
		code  int
	}{{owner, 206}, {uuid.New(), 404}} {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("user", models.User{Base: models.Base{ID: test.owner}}) })
		r.GET("/:id/:artifactId", a.workflowArtifact)
		req := httptest.NewRequest("GET", "/"+s.ID+"/video", nil)
		req.Header.Set("Range", "bytes=2-5")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != test.code {
			t.Fatalf("got %d: %s", rec.Code, rec.Body)
		}
		if rec.Code == 206 && rec.Body.String() != "2345" {
			t.Fatal("range ignored")
		}
	}
}

func TestAvatarWorkflowQualityWarningsDoNotBlockDelivery(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	w.content = func(_ context.Context, prompt string, input any, _ []string) (json.RawMessage, error) {
		if strings.Contains(prompt, "只按内容") {
			return json.RawMessage(`{"segments":[{"id":"p1","text":"第一句。"},{"id":"p2","text":"第二句。"}]}`), nil
		}
		if strings.Contains(prompt, "列出素材需求") {
			return json.RawMessage(`{"needs":[{"id":"n1","segment_id":"p1","kind":"animation","duration":2,"content":"第一句","template":"steps"},{"id":"n2","segment_id":"p2","kind":"animation","duration":2,"content":"第二句","template":"steps"}]}`), nil
		}
		batch := input.(avatar.ShotPlanningBatch)
		return json.RawMessage(fmt.Sprintf(`{"shots":[{"start":%g,"end":%g,"primary":%q,"card":"chapter-title-card","layout":"full"}]}`, batch.Window.Start, batch.Window.End, batch.Materials[0].NeedID)), nil
	}
	var stages []string
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		stages = append(stages, stage)
		if stage == "voice_reference" {
			// Later paragraphs borrow the sound of the first one; the stub only
			// needs to hand back an existing file.
			os.WriteFile(filepath.Join(dir, "voice-reference.wav"), []byte("unit-test-audio"), 0600)
			return json.RawMessage(`{"path":"voice-reference.wav"}`), nil
		}
		if stage == "native_speech" {
			// Seedance returns picture and sound together; this fixture only needs
			// the resulting files and their measured duration.
			var in struct {
				Segment avatar.Segment `json:"segment"`
			}
			b, _ := os.ReadFile(filepath.Join(dir, "media-input.json"))
			json.Unmarshal(b, &in)
			os.MkdirAll(filepath.Join(dir, in.Segment.ID), 0700)
			for _, name := range []string{"native.mp4", "audio.wav", "timestamps.json", "alignment-cache.json"} {
				os.WriteFile(filepath.Join(dir, in.Segment.ID, name), []byte("unit-test-media"), 0600)
			}
			b, _ = json.Marshal(map[string]any{"duration": 2, "video": in.Segment.ID + "/native.mp4", "audio": in.Segment.ID + "/audio.wav", "warnings": []string{"对齐检查脚本未完成"}})
			return b, nil
		}
		if stage == "material" {
			var in struct {
				Need avatar.MaterialNeed `json:"need"`
			}
			b, _ := os.ReadFile(filepath.Join(dir, "media-input.json"))
			json.Unmarshal(b, &in)
			os.MkdirAll(filepath.Join(dir, "materials"), 0700)
			os.WriteFile(filepath.Join(dir, "materials", in.Need.ID+".mp4"), []byte("unit-test-media"), 0600)
			b, _ = json.Marshal(map[string]any{"need_id": in.Need.ID, "segment_id": in.Need.SegmentID, "kind": "animation", "path": "materials/" + in.Need.ID + ".mp4", "duration": 2, "warnings": []string{"首镜检查不通过"}})
			return b, nil
		}
		return json.RawMessage(`{"artifacts":[],"warnings":["质检异常仅提醒"]}`), nil
	}
	w.execute(s, owner)
	s, _ = w.load(context.Background(), owner, s.ID)
	if s.Confirmation != "script" || len(stages) > 0 {
		t.Fatalf("unexpected order: %#v", s)
	}
	if err := w.confirm(context.Background(), owner, s.ID, "script", s.ConfirmationVersion, nil); err != nil {
		t.Fatal(err)
	}
	// Cached fixture audio avoids all provider calls; measured duration is supplied by the media stub.
	for _, seg := range s.Segments {
		fp := w.audioFingerprint(&s, seg)
		p := filepath.Join(w.projectDir(s.ProjectID), "cache", "audio", fp)
		os.MkdirAll(p, 0700)
		os.WriteFile(filepath.Join(p, "audio.wav"), []byte("unit-test-audio"), 0600)
	}
	// Native speech confirms its own sound, so there is no separate audio gate:
	// the run goes straight from F04 to planning and must still deliver.
	s, _ = w.load(context.Background(), owner, s.ID)
	w.execute(s, owner)
	s, _ = w.load(context.Background(), owner, s.ID)
	if s.Status != "succeeded" || len(s.Events) < 2 || !strings.Contains(strings.Join(stages, ","), "preview,render") {
		t.Fatalf("quality blocked delivery: %s %s %#v", s.Status, s.Error, stages)
	}
	quality := 0
	for _, event := range s.Events {
		if event.Type == "quality" {
			quality++
		}
	}
	if quality == 0 {
		t.Fatal("quality warnings were expected to be recorded while still delivering")
	}
	for _, event := range s.Events {
		if event.Read {
			t.Fatal("test unexpectedly acknowledged a popup")
		}
	}
}

type workflowProviderSpy struct {
	submit int
	query  int
	ready  bool
	ids    []string
}

func (p *workflowProviderSpy) Upload(context.Context, string) (string, error) {
	return "https://test.invalid/reference", nil
}
func (p *workflowProviderSpy) Submit(context.Context, avatar.GenerationRequest) (avatar.ProviderResult, error) {
	p.submit++
	return avatar.ProviderResult{TaskID: "original-order", Status: "RUNNING"}, nil
}
func (p *workflowProviderSpy) Query(_ context.Context, id string) (avatar.ProviderResult, error) {
	p.query++
	p.ids = append(p.ids, id)
	if !p.ready {
		return avatar.ProviderResult{}, fmt.Errorf("network unavailable")
	}
	var result avatar.ProviderResult
	json.Unmarshal([]byte(`{"taskId":"original-order","status":"SUCCESS","results":[{"url":"https://test.invalid/result.mp4","outputType":"mp4"}]}`), &result)
	return result, nil
}
func (p *workflowProviderSpy) Download(_ context.Context, _ string, path string) error {
	return os.WriteFile(path, []byte("unit-test-video"), 0600)
}
// F06 must hand back the footage F04 already paid for. It copies the file and
// records any quality warning, but never contacts the provider again.
func TestAvatarWorkflowF06ReusesNativeFootageWithoutSubmitting(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	spy := &workflowProviderSpy{}
	a.avatarMVP.provider = spy
	a.avatarMVP.providerErr = nil
	a.avatarMVP.options.PollInterval = time.Millisecond
	photo := avatar.SourceAsset{ID: "photo", Kind: "photo", Path: "assets/photo"}
	s.Input.Photo = photo.ID
	s.Input.Assets = []avatar.SourceAsset{photo}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "一。", Audio: "p1/audio.wav", NativeVideo: "p1/native.mp4", Duration: 2}}
	s.Segments[0].Fingerprint = w.nativeSpeechFingerprint(&s, s.Segments[0])
	w.save(&s)
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		if stage == "chromakey" {
			os.MkdirAll(filepath.Join(dir, "materials"), 0700)
			var inp struct {
				NeedID string `json:"need_id"`
			}
			if b, e := os.ReadFile(filepath.Join(dir, "media-input.json")); e == nil {
				json.Unmarshal(b, &inp)
			}
			name := inp.NeedID + "-alpha.webm"
			os.WriteFile(filepath.Join(dir, "materials", name), []byte("alpha"), 0600)
			return json.RawMessage(`{"need_id":"` + inp.NeedID + `","path":"materials/` + name + `","kind":"real","duration":2,"has_alpha":true,"alpha_path":"materials/` + name + `"}`), nil
		}
		t.Fatalf("F06 must not render a second video: stage=%s", stage)
		return nil, nil
	}
	for _, dir := range []string{w.dir(s.ID), filepath.Join(w.projectDir(s.ProjectID), "assets")} {
		os.MkdirAll(filepath.Join(dir, "p1"), 0700)
		os.MkdirAll(dir, 0700)
	}
	os.WriteFile(filepath.Join(w.dir(s.ID), "p1", "native.mp4"), []byte("paid-footage"), 0600)
	os.WriteFile(filepath.Join(w.projectDir(s.ProjectID), "assets", "photo"), []byte("unit-test-photo"), 0600)
	need := avatar.MaterialNeed{ID: "n1", SegmentID: "p1", Kind: "avatar", Duration: 2, Angle: "正面"}
	if err := w.produceMaterial(context.Background(), &s, need, "F06"); err != nil {
		t.Fatal(err)
	}
	if spy.submit != 0 {
		t.Fatalf("F06 contacted the provider %d times", spy.submit)
	}
	if len(s.Materials) != 1 || s.Materials[0].Kind != "avatar" {
		t.Fatalf("missing reused material: %#v", s.Materials)
	}
	// F06 runs chromakey after copying; material path now points to the alpha webm.
	if !strings.HasSuffix(s.Materials[0].Path, "-alpha.webm") {
		t.Fatalf("expected alpha webm path, got: %s", s.Materials[0].Path)
	}
	if _, err := os.Stat(filepath.Join(w.dir(s.ID), s.Materials[0].Path)); err != nil {
		t.Fatalf("alpha webm not written: %v", err)
	}
	if s.Status != "running" {
		t.Fatal("material reuse changed state")
	}
}

func TestAvatarWorkflowUnknownCannotBeRetried(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Status = "submission_unknown"
	s.Steps = []avatar.WorkflowStep{{ID: "F06/n1-c1", Status: "failed", Submission: "unknown"}}
	a.avatarWorkflow.save(&s)
	if a.avatarWorkflow.retry(context.Background(), owner, s.ID) == nil {
		t.Fatal("unknown order was retried")
	}
}

func TestAvatarWorkflowRevisionReusesUnchangedDependencies(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	s.Status = "succeeded"
	s.ScriptConfirmed = true
	s.Script = "第一句。第二句。"
	s.Segments = []avatar.Segment{{ID: "p1", Text: s.Script, Audio: "p1/audio.wav", NativeVideo: "p1/native.mp4", Duration: 4}}
	// Native speech derives the segment identity from its inputs, so the fixture
	// must record the value inherit() will recompute rather than a literal.
	s.Segments[0].Fingerprint = a.avatarWorkflow.nativeSpeechFingerprint(&s, s.Segments[0])
	approved := s.Segments[0].Fingerprint
	s.Needs = []avatar.MaterialNeed{{ID: "n1", SegmentID: "p1", Kind: "animation", Duration: 4, Template: "steps"}}
	// Confirmed narration is only reusable when the files it names still exist.
	for _, name := range []string{"audio.wav", "timestamps.json", "alignment-cache.json", "native.mp4"} {
		path := filepath.Join(a.avatarWorkflow.dir(s.ID), "p1", name)
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte("fixture"), 0600)
	}
	a.avatarWorkflow.save(&s)
	var input = s.Input
	input.Style = "line-art"
	input.SubtitleColor = "#FFFFFF"
	a.db.Model(&p).Update("project_metadata", jsonMap(map[string]any{"avatar_v2": input}))
	next, err := a.avatarWorkflow.create(context.Background(), owner, p.ID, "revision-request", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !next.ScriptConfirmed || len(next.Needs) != 1 || a.avatarWorkflow.audioFingerprint(&next, next.Segments[0]) != approved {
		t.Fatal("style change invalidated approved content or voice")
	}
	a.avatarWorkflow.stop(context.Background(), owner, next.ID)
	next.Status = "stopped"
	a.avatarWorkflow.save(&next)
	regen, err := a.avatarWorkflow.create(context.Background(), owner, p.ID, "regenerate-request", 1, avatar.WorkflowEdits{BaseRunID: s.ID, RegenerateSegments: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.avatarWorkflow.audioFingerprint(&regen, regen.Segments[0]) == approved {
		t.Fatal("explicit regeneration incorrectly reused audio")
	}
}
func TestAvatarWorkflowIdempotencyGlobalBusyAndOwnership(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	again, err := a.avatarWorkflow.create(context.Background(), owner, p.ID, "request-1", 1)
	if err != nil || again.ID != s.ID {
		t.Fatalf("not idempotent: %v", err)
	}
	if _, err = a.avatarWorkflow.create(context.Background(), owner, p.ID, "request-2", 1); err == nil {
		t.Fatal("allowed concurrent job")
	}
	if _, err = a.avatarWorkflow.load(context.Background(), uuid.New(), s.ID); err == nil {
		t.Fatal("cross-user access")
	}
}
func TestAvatarWorkflowWarningAndStopPersistAcrossWorkerSave(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.AddWarning("F06/x", "quality", "面部瑕疵")
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	stale := s
	if err := a.avatarWorkflow.stop(context.Background(), owner, s.ID); err != nil {
		t.Fatal(err)
	}
	stale.Stage = "F07"
	if err := a.avatarWorkflow.save(&stale); err != nil {
		t.Fatal(err)
	}
	got, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if !got.StopRequested || got.Status != "stopping" || len(got.Events) != 1 {
		t.Fatalf("lost stop or quality event: %#v", got)
	}
	if err := a.avatarWorkflow.beforeSubmit(&stale, "F06/x"); err == nil {
		t.Fatal("submitted after stop")
	}
}
func TestAvatarWorkflowStaleConfirmationAndRange(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Status = "awaiting_confirmation"
	s.Confirmation = "script"
	s.ConfirmationVersion = "v1"
	a.avatarWorkflow.save(&s)
	if err := a.avatarWorkflow.confirm(context.Background(), owner, s.ID, "script", "old", nil); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", models.User{Base: models.Base{ID: owner}}) })
	r.POST("/:id/confirm", a.workflowConfirm)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/"+s.ID+"/confirm", strings.NewReader(`{"kind":"audio","version":"v1"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatalf("wrong confirmation accepted: %d %s", w.Code, w.Body)
	}
	var raw map[string]any
	json.Unmarshal(w.Body.Bytes(), &raw)
}

func TestAvatarWorkflowRenderOnlyRevisionReusesLockedAudioWithoutNewGate(t *testing.T) {
	a, _, _, previous := workflowFixture(t)
	w := a.avatarWorkflow
	previous.ScriptConfirmed = true
	previous.AudioConfirmed = true
	previous.Script = "第一句。第二句。"
	previous.Segments = []avatar.Segment{{ID: "p1", Text: previous.Script, Audio: "p1/audio.wav", NativeVideo: "p1/native.mp4", Duration: 4}}
	previous.Segments[0].Fingerprint = w.nativeSpeechFingerprint(&previous, previous.Segments[0])
	for _, name := range []string{"audio.wav", "timestamps.json", "alignment-cache.json", "native.mp4"} {
		p := filepath.Join(w.dir(previous.ID), "p1", name)
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte("fixture"), 0600)
	}
	next := avatar.WorkflowState{ID: uuid.NewString(), ProjectID: previous.ProjectID, Input: previous.Input, ConfigSnapshot: previous.ConfigSnapshot}
	next.Input.SubtitleColor = "#FFFFFF"
	if err := w.inherit(&next, previous, avatar.WorkflowEdits{}); err != nil {
		t.Fatal(err)
	}
	if !next.AudioConfirmed || len(next.Steps) != 1 || next.Steps[0].CostStatus != "reused" || next.Segments[0].Duration != 4 {
		t.Fatalf("render-only edit lost locked audio: %#v", next.Steps)
	}
}

func TestAvatarWorkflowNewProjectUsesV2FromMainProjectEntry(t *testing.T) {
	a := newTestApp(t)
	token := registerAndLogin(t, a, "new-workflow-project@example.com")
	response := request(a, "POST", "/api/v1/projects/", map[string]any{"name": "新数字人", "metadata": map[string]any{"inference_mode": "avatar"}}, token)
	if response.Code != 201 {
		t.Fatal(response.Code)
	}
	var p models.Project
	if err := a.db.Where("name=?", "新数字人").First(&p).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := workflowInput(p); err != nil {
		t.Fatal("new avatar entry fell back to legacy workflow")
	}
}

func TestAvatarWorkflowGenericMetadataCannotReplaceManagedAssetPaths(t *testing.T) {
	old := jsonMap(map[string]any{"avatar_v2": map[string]any{"photo": "original"}, "other": "before"})
	next := jsonMap(map[string]any{"avatar_v2": map[string]any{"photo": "forged", "assets": []any{map[string]any{"path": "../../secret"}}}, "other": "after"})
	var data map[string]any
	json.Unmarshal(preserveAvatarWorkflowMetadata(old, next), &data)
	if data["avatar_v2"].(map[string]any)["photo"] != "original" || data["other"] != "after" {
		t.Fatal("managed data was overwritten")
	}
}

func TestAvatarWorkflowConfirmationKeepsServerOwnedAudioForUnchangedParagraph(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Status = "awaiting_confirmation"
	s.Confirmation = "script"
	s.ConfirmationVersion = "current"
	s.Segments = []avatar.Segment{{ID: "p1", Text: "原稿。", Audio: "p1/audio.wav", Duration: 3, Fingerprint: "server-fingerprint"}}
	a.avatarWorkflow.save(&s)
	if err := a.avatarWorkflow.confirm(context.Background(), owner, s.ID, "script", "current", []avatar.Segment{{ID: "p1", Text: "原稿。", Audio: "forged", Duration: 999}}); err != nil {
		t.Fatal(err)
	}
	got, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if got.Segments[0].Audio != "p1/audio.wav" || got.Segments[0].Duration != 3 {
		t.Fatal("unchanged paragraph lost its valid cached audio")
	}
}
