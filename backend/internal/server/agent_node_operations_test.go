package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

func completedNodeFixture(t *testing.T) (*App, avatar.WorkflowState) {
	t.Helper()
	a, _, _, s := workflowFixture(t)
	s.Status = "succeeded"
	s.Stage = "completed"
	s.ScriptConfirmed = true
	s.AudioConfirmed = true
	s.Script = s.Input.Script
	s.Segments = []avatar.Segment{{ID: "p1", Text: s.Script, Audio: "p1/audio.wav", NativeVideo: "p1/native.mp4", Fingerprint: "old-audio", Duration: 5}}
	// Native speech recomputes the segment fingerprint from its inputs; store the
	// value that inherit() will compare against, or the fixture looks stale.
	s.Segments[0].Fingerprint = a.avatarWorkflow.nativeSpeechFingerprint(&s, s.Segments[0])
	s.Duration = 5
	s.Needs = []avatar.MaterialNeed{{ID: "n1", SegmentID: "p1", Kind: "animation", Content: s.Script, Template: "highlight", Duration: 5}}
	s.Materials = []avatar.Material{{NeedID: "n1", SegmentID: "p1", Kind: "animation", Path: "materials/n1.mp4", Duration: 5, Fingerprint: "old-material"}}
	s.Shots = []avatar.WorkflowShot{{ID: "S1", SegmentID: "p1", Start: 0, End: 5, Primary: "n1", Layout: "full", Card: "none", Recipe: "visual-hero"}}
	for _, file := range []string{"audio.wav", "timestamps.json", "alignment-cache.json", "native.mp4"} {
		path := filepath.Join(a.avatarWorkflow.dir(s.ID), "p1", file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("isolated fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	return a, s
}

func TestNodeSkipRequiresAlternativeAndProducesReviewablePlan(t *testing.T) {
	a, s := completedNodeFixture(t)
	h := avatarPackageHost{a}
	ctx := context.Background()
	owner := a.avatarWorkflow.ownerID(&s)
	args := map[string]any{"node": "F07", "changes": "跳过失败动效，保留替代画面", "skip_need_ids": []any{"n1"}}
	if _, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.edit-node", args, "skip-alone"); err == nil {
		t.Fatal("removed the only visual")
	}
	s.Needs = append(s.Needs, avatar.MaterialNeed{ID: "n2", SegmentID: "p1", Kind: "animation", Content: s.Script, Template: "highlight", Duration: 5})
	s.Materials = append(s.Materials, avatar.Material{NeedID: "n2", SegmentID: "p1", Kind: "animation", Duration: 5, Path: "materials/n2.mp4"})
	file := filepath.Join(a.avatarWorkflow.dir(s.ID), "materials/n2.mp4")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, []byte("replacement footage"), 0600)
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.edit-node", args, "skip-with-alternative"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := h.project(ctx, owner, s.ProjectID)
	proposal := proposalOf(p)
	if proposal.Applied || len(proposal.Edits.Needs) != 1 || proposal.Edits.Needs[0].ID != "n2" {
		t.Fatalf("wrong skip proposal: %+v", proposal)
	}
	unchanged, _ := a.latestAgentRun(ctx, uuid.MustParse(owner), p.ID)
	if unchanged.ID != s.ID || len(unchanged.Needs) != 2 {
		t.Fatal("skip started before review")
	}
	next := avatar.WorkflowState{ID: uuid.NewString(), Input: s.Input, ConfigSnapshot: s.ConfigSnapshot}
	if err := a.avatarWorkflow.inherit(&next, s, proposal.Edits); err != nil {
		t.Fatal(err)
	}
	if len(next.Needs) != 1 || next.Needs[0].ID != "n2" || len(next.Shots) != 0 {
		t.Fatal("skip retained stale shots or material need")
	}
	s.Needs[1].Duration = 2
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.edit-node", args, "skip-short-alternative"); err == nil {
		t.Fatal("partial replacement allowed a gap")
	}
	args["skip_need_ids"] = []any{"missing"}
	if _, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.edit-node", args, "skip-missing"); err == nil {
		t.Fatal("unknown material skipped")
	}
}

func TestPackageRerunVoiceConfirmationSubmitsOnce(t *testing.T) {
	a, s := completedNodeFixture(t)
	pool, _ := a.db.DB()
	pool.SetMaxOpenConns(1)
	ctx := context.Background()
	h := avatarPackageHost{a}
	owner := a.avatarWorkflow.ownerID(&s)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Uploads must succeed so F04 reaches the paid submit step; only the
		// generation call is rejected, which is what this test asserts on.
		if strings.Contains(r.URL.Path, "/media/upload/") {
			_, _ = w.Write([]byte(`{"code":0,"data":{"download_url":"https://example.com/asset.mp4"}}`))
			return
		}
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"isolated supplier rejection"}`))
	}))
	defer provider.Close()
	t.Setenv("RUNNINGHUB_BASE_URL", provider.URL)
	t.Setenv("RUNNINGHUB_API_KEY", "isolated-test")
	// Native speech needs the video service, not a TTS model: F04 submits straight
	// to Seedance 2.5. The fixture is the same completed project either way.
	a.avatarMVP.options.Protocol = "runninghub"
	a.avatarMVP.options.BaseURL = provider.URL
	a.avatarMVP.options.APIKey = "isolated-test"
	a.avatarMVP.options.Model = avatar.NativeSpeechModel
	a.avatarMVP.options.GenerationPath = avatar.SeedancePath
	// The provider is built once at startup; rebuild it for the fake endpoint.
	a.avatarMVP.provider, a.avatarMVP.providerErr = avatar.NewVideoProvider(a.avatarMVP.options)
	if err := a.db.Model(&models.Project{}).Where("id=?", s.ProjectID).Update("project_metadata", jsonMap(map[string]any{"kind": "avatar_mvp", "avatar_v2": s.Input})).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	reviewTextConfig(a)
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		return nil, fmt.Errorf("upstream model must not run")
	}
	_, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.rerun", map[string]any{"from": "F04", "target": "F12", "changes": "重做后续"}, "voice-rerun")
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := h.project(ctx, owner, s.ProjectID)
	proposal := proposalOf(p)
	if calls.Load() != 0 {
		t.Fatal("provider invoked before confirmation")
	}
	if _, err = h.Confirm(ctx, owner, s.ProjectID, proposal.ID, proposal.Revision); err != nil {
		t.Fatal(err)
	}
	var next avatar.WorkflowState
	for deadline := time.Now().Add(5 * time.Second); ; {
		next, err = a.latestAgentRun(ctx, uuid.MustParse(owner), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, active := workflowLaunchLocks.Load(next.ID)
		if next.ID != s.ID && !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The rerun must reach F04 for the same target and submit exactly one
	// generation order. Native speech uploads the portrait first, so the raw
	// HTTP call count is not the invariant — a single failed submission is.
	if next.Stage != "F04/p1-c1" || next.TargetNode != "F12" || !next.ScriptConfirmed {
		t.Fatalf("wrong rerun scope: stage=%s target=%s error=%s", next.Stage, next.TargetNode, next.Error)
	}
	if calls.Load() == 0 {
		t.Fatal("rerun never contacted the provider")
	}
	submitted := calls.Load()
	if _, err = h.Confirm(ctx, owner, s.ProjectID, proposal.ID, proposal.Revision); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.db.Model(&models.WorkflowRun{}).Where("project_id=?", p.ID).Count(&count)
	if count != 2 || calls.Load() != submitted {
		t.Fatalf("duplicate confirmation resubmitted work: runs=%d calls=%d want=%d", count, calls.Load(), submitted)
	}
}

func TestPackageNodeFullScriptAppliesAndWaitsForReview(t *testing.T) {
	a, s := completedNodeFixture(t)
	pool, _ := a.db.DB()
	pool.SetMaxOpenConns(1)
	reviewTextConfig(a)
	ctx := context.Background()
	h := avatarPackageHost{a}
	owner := a.avatarWorkflow.ownerID(&s)
	newScript := "完全改写后的口播。"
	a.avatarWorkflow.content = func(_ context.Context, _ string, in any, _ []string) (json.RawMessage, error) {
		if in.(map[string]any)["script"] != newScript {
			return nil, fmt.Errorf("old input overwrote new script")
		}
		return json.Marshal(map[string]any{"segments": []avatar.Segment{{ID: "p1", Text: newScript}}})
	}
	if _, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.edit-node", map[string]any{"node": "F02", "script": newScript, "changes": "全文重写"}, "edit-script"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := h.project(ctx, owner, s.ProjectID)
	proposal := proposalOf(p)
	if _, err := h.Confirm(ctx, owner, s.ProjectID, proposal.ID, proposal.Revision); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		next, err := a.latestAgentRun(ctx, uuid.MustParse(owner), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, active := workflowLaunchLocks.Load(next.ID)
		if next.ID != s.ID && !active {
			if next.Status != "awaiting_confirmation" || next.Confirmation != "script" || next.Script != newScript || next.TargetNode != "F03" {
				t.Fatalf("script edit did not reach review: %s %s %s", next.Status, next.Script, next.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPackageNodeRejectsUnsafeAndCrossNodePatches(t *testing.T) {
	a, s := completedNodeFixture(t)
	h := avatarPackageHost{a}
	owner := a.avatarWorkflow.ownerID(&s)
	for _, args := range []map[string]any{
		{"node": "F04", "changes": "bad", "script": "wrong node"},
		{"node": "F02", "changes": "bad", "needs": []any{map[string]any{"id": "n1"}}},
		{"node": "F07", "changes": "bad", "needs": []any{map[string]any{"id": "n1", "kind": "avatar"}}},
		{"node": "F07", "changes": "bad", "needs": []any{map[string]any{"id": "n1", "path": "/etc/passwd"}}},
		{"node": "F11", "changes": "bad", "presentation": map[string]any{"subtitle_scale": float64(20)}},
		{"node": "F03", "changes": "bad", "segments": []any{map[string]any{"id": "p1", "text": "删掉未授权的正文"}}},
		{"node": "F09", "changes": "bad", "subtitles": []any{map[string]any{"id": "missing", "text": "无效"}}},
	} {
		if _, err := h.Invoke(context.Background(), owner, s.ProjectID, "avatar.edit-node", args, "invalid"); err == nil {
			t.Fatalf("accepted invalid patch: %#v", args)
		}
	}
	if _, err := h.Invoke(context.Background(), uuid.NewString(), s.ProjectID, "avatar.rerun", map[string]any{"from": "F04", "changes": "cross owner"}, "invalid"); err == nil {
		t.Fatal("cross-owner rerun allowed")
	}
}

func TestPackageRerunMaterialCacheAndLaterReuse(t *testing.T) {
	a, old := completedNodeFixture(t)
	w := a.avatarWorkflow
	ctx := context.Background()
	owner := uuid.MustParse(w.ownerID(&old))
	// This test is about the F07 material cache, not speech. Native-speech runs
	// re-plan needs on rerun, so pin the fixture's needs back on each new run.
	pinNeeds := func(s *avatar.WorkflowState) {
		if len(s.Needs) == 0 {
			s.Needs = []avatar.MaterialNeed{{ID: "n1", SegmentID: "p1", Kind: "animation", Content: s.Script, Template: "highlight", Duration: 5}}
		}
	}
	calls := 0
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		if stage != "material" {
			return nil, fmt.Errorf("unexpected stage %s", stage)
		}
		calls++
		path := filepath.Join(dir, "materials", "n1.mp4")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("render-%d", calls)), 0600); err != nil {
			return nil, err
		}
		return json.Marshal(avatar.Material{NeedID: "n1", SegmentID: "p1", Kind: "animation", Path: "materials/n1.mp4", Duration: 5})
	}
	if err := w.produceMaterial(ctx, &old, old.Needs[0], "F07"); err != nil {
		t.Fatal(err)
	}
	firstFingerprint := old.Materials[0].Fingerprint
	if err := w.save(&old); err != nil {
		t.Fatal(err)
	}
	next, err := w.createTo(ctx, owner, uuid.MustParse(old.ProjectID), "rerun-material", old.Input.Revision, "F07", avatar.WorkflowEdits{BaseRunID: old.ID, RerunFrom: "F07"})
	if err != nil {
		t.Fatal(err)
	}
	pinNeeds(&next)
	if err = w.produceMaterial(ctx, &next, next.Needs[0], "F07"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || next.Materials[0].Fingerprint == firstFingerprint {
		t.Fatal("explicit rerun reused old material cache")
	}
	if err = w.produceMaterial(ctx, &next, next.Needs[0], "F07"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("same run retry rebuilt a finished material")
	}
	next.Status = "succeeded"
	if err = w.save(&next); err != nil {
		t.Fatal(err)
	}
	later, err := w.createTo(ctx, owner, uuid.MustParse(old.ProjectID), "reuse-rerun-material", next.Input.Revision, "F12", avatar.WorkflowEdits{BaseRunID: next.ID})
	if err != nil {
		t.Fatal(err)
	}
	pinNeeds(&later)
	if err = w.produceMaterial(ctx, &later, later.Needs[0], "F07"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || later.Materials[0].Fingerprint != next.Materials[0].Fingerprint {
		t.Fatal("unrelated edit lost regenerated material cache")
	}
}

func TestPackageNodeResegmentationDropsOnlyStaleSubtitles(t *testing.T) {
	a, s := completedNodeFixture(t)
	ctx := context.Background()
	owner := uuid.MustParse(a.avatarWorkflow.ownerID(&s))
	pid := uuid.MustParse(s.ProjectID)
	s.Input.SubtitleEdits = map[string]string{"p1": "第壹句。第二句。"}
	if err := a.db.Model(&models.Project{}).Where("id=?", pid).Update("project_metadata", jsonMap(map[string]any{"avatar_v2": s.Input})).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	next, err := a.avatarWorkflow.createTo(ctx, owner, pid, "resegment-with-subtitles", s.Input.Revision, "F03", avatar.WorkflowEdits{BaseRunID: s.ID, Segments: []avatar.Segment{{ID: "new1", Text: s.Script}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Input.SubtitleEdits) != 0 || s.Input.SubtitleEdits["p1"] == "" {
		t.Fatal("stale subtitles retained or original revision mutated")
	}
	_, input, err := (avatarPackageHost{a}).project(ctx, owner.String(), s.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.SubtitleEdits) != 0 || input.Revision != next.Input.Revision {
		t.Fatal("draft retained stale subtitle binding")
	}
}

func TestPackageCompletedRerunIsPersistentProposal(t *testing.T) {
	a, s := completedNodeFixture(t)
	ctx := context.Background()
	h := avatarPackageHost{a}
	owner := a.avatarWorkflow.ownerID(&s)
	_, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.rerun", map[string]any{"from": "F04", "target": "F12", "changes": "从配音开始全部重做"}, "rerun-test")
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := h.project(ctx, owner, s.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalOf(p)
	if proposal == nil || proposal.Applied {
		t.Fatal("missing pending proposal")
	}
	raw, _ := json.Marshal(proposal.Edits)
	var edits map[string]any
	_ = json.Unmarshal(raw, &edits)
	if edits["rerun_from"] != "F04" || proposal.Edits.BaseRunID != s.ID {
		t.Fatalf("rerun origin lost: %s", raw)
	}
	var count int64
	a.db.Model(&models.WorkflowRun{}).Where("project_id=?", s.ProjectID).Count(&count)
	if count != 1 {
		t.Fatal("draft must not submit providers or create runs")
	}
}

func TestPackageRerunInvalidatesRequestedRange(t *testing.T) {
	for _, from := range []string{"F01.4", "F02", "F03", "F04", "F05", "F06", "F07", "F08", "F09", "F10", "F11", "F12"} {
		t.Run(from, func(t *testing.T) {
			a, prior := completedNodeFixture(t)
			next := avatar.WorkflowState{ID: "next-" + from, Input: prior.Input, ConfigSnapshot: prior.ConfigSnapshot}
			var edits avatar.WorkflowEdits
			_ = json.Unmarshal([]byte(`{"base_run_id":"`+prior.ID+`","rerun_from":"`+from+`"}`), &edits)
			if err := a.avatarWorkflow.inherit(&next, prior, edits); err != nil {
				t.Fatal(err)
			}
			if from <= "F03" && next.ScriptConfirmed {
				t.Fatal("rerun must clear script approval")
			}
			if from == "F03" && (next.Script != prior.Script || len(next.Segments) != 0) {
				t.Fatal("F03 must retain script and regenerate segmentation")
			}
			if from >= "F04" && next.Script != prior.Script {
				t.Fatal("upstream script lost")
			}
			if from == "F04" && (len(next.Edits.RegenerateSegments) != 1 || next.Segments[0].Audio != "" || next.AudioConfirmed) {
				t.Fatalf("F04 reused paid result: %#v", next)
			}
			// Native speech produces picture and sound in one generation, so a
			// rerun from F06 must regenerate the sound too; only later nodes keep it.
			if from > "F06" && (next.Segments[0].Audio == "" || !next.AudioConfirmed) {
				t.Fatal("unaffected audio was discarded")
			}
			if from <= "F05" && len(next.Needs) > 0 {
				t.Fatal("old needs were reused")
			}
			if from <= "F10" && len(next.Shots) > 0 {
				t.Fatal("old shots were reused")
			}
			if from > "F10" && len(next.Shots) == 0 {
				t.Fatal("unaffected shots were discarded")
			}
		})
	}
}

func TestPackageNodeEditsOnCompletedRun(t *testing.T) {
	cases := []struct {
		node  string
		patch map[string]any
	}{
		{"F01.4", map[string]any{"descriptions": []any{map[string]any{"id": "d1", "text": "修订已有区间解读"}}}},
		{"F02", map[string]any{"script": "新的完整口播稿。"}},
		{"F03", map[string]any{"segments": []any{map[string]any{"id": "new1", "text": "这是一个测试口播。", "chapter": "开头"}}}},
		{"F05", map[string]any{"needs": []any{map[string]any{"id": "n1", "content": "修订动效文案"}}}},
		{"F06", map[string]any{"needs": []any{map[string]any{"id": "n1", "prompt": "人物保持正面"}}}},
		{"F07", map[string]any{"needs": []any{map[string]any{"id": "n1", "content": "修订动效文案"}}}},
		{"F08", map[string]any{"needs": []any{map[string]any{"id": "n1", "source_start": float64(1)}}}},
		{"F09", map[string]any{"subtitles": []any{map[string]any{"id": "p1", "text": "修订字幕"}}}},
		{"F10", map[string]any{"shots": []any{map[string]any{"id": "S1", "keyword": "新标题"}}}},
		{"F11", map[string]any{"presentation": map[string]any{"subtitle_color": "#ffaa00"}}},
		{"F12", map[string]any{"presentation": map[string]any{"subtitle_scale": float64(1.2)}}},
	}
	for _, c := range cases {
		t.Run(c.node, func(t *testing.T) {
			a, s := completedNodeFixture(t)
			if c.node == "F01.4" || c.node == "F08" {
				s.Input.Assets = []avatar.SourceAsset{{ID: "v1", Kind: "video", Duration: 10, SHA256: "test"}}
				s.Descriptions = []avatar.SourceDescription{{ID: "d1", AssetID: "v1", Start: 0, End: 10, Text: "原解读"}}
			}
			if c.node == "F06" {
				s.Input.UseAvatar = true
				s.Needs[0].Kind = "avatar"
			}
			if c.node == "F08" {
				s.Needs[0].Kind = "real"
				s.Needs[0].SourceID = "v1"
				s.Needs[0].DescriptionID = "d1"
			}
			if err := a.db.Model(&models.Project{}).Where("id=?", s.ProjectID).Update("project_metadata", jsonMap(map[string]any{"avatar_v2": s.Input, "kind": "avatar_mvp"})).Error; err != nil {
				t.Fatal(err)
			}
			if err := a.avatarWorkflow.save(&s); err != nil {
				t.Fatal(err)
			}
			h := avatarPackageHost{a}
			ctx := context.Background()
			owner := a.avatarWorkflow.ownerID(&s)
			// Keep F03 replacement semantically valid for this fixture's complete narration.
			if c.node == "F03" {
				c.patch["segments"].([]any)[0].(map[string]any)["text"] = s.Script
			}
			if c.node == "F09" {
				c.patch["subtitles"].([]any)[0].(map[string]any)["text"] = "第壹句。第二句。"
			}
			c.patch["node"] = c.node
			c.patch["changes"] = "用户指定编辑"
			if _, err := h.Invoke(ctx, owner, s.ProjectID, "avatar.edit-node", c.patch, "edit-node"); err != nil {
				t.Fatal(err)
			}
			p, _, _ := h.project(ctx, owner, s.ProjectID)
			proposal := proposalOf(p)
			if proposal == nil || proposal.Applied {
				t.Fatal("missing pending node edit")
			}
		})
	}
}
