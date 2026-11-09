package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

func TestAdversarialNodeResegmentKeepsExtractedDirections(t *testing.T) {
	a, prior := completedNodeFixture(t)
	prior.ProductionNotes = []string{"制作时只用左前方20度机位。"}
	prior.ResolvedAngles = []string{"左前方20度"}
	if err := a.avatarWorkflow.save(&prior); err != nil {
		t.Fatal(err)
	}
	owner := uuid.MustParse(a.avatarWorkflow.ownerID(&prior))
	next, err := a.avatarWorkflow.createTo(context.Background(), owner, uuid.MustParse(prior.ProjectID), "resegment-directions", prior.Input.Revision, "F03", avatar.WorkflowEdits{BaseRunID: prior.ID, RerunFrom: "F03"})
	if err != nil {
		t.Fatal(err)
	}
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		// F03 receives the already cleaned narration. It cannot re-extract notes.
		return json.Marshal(map[string]any{"segments": []avatar.Segment{{ID: "new1", Text: prior.Script}}})
	}
	a.avatarWorkflow.execute(next, owner)
	next, err = a.avatarWorkflow.load(context.Background(), owner, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "awaiting_confirmation" || next.Confirmation != "script" {
		t.Fatalf("unexpected execution: %s %s", next.Status, next.Error)
	}
	if avatar.Digest(next.ProductionNotes) != avatar.Digest(prior.ProductionNotes) || avatar.Digest(next.ResolvedAngles) != avatar.Digest(prior.ResolvedAngles) {
		t.Fatalf("resegment lost upstream production instructions: notes=%v angles=%v", next.ProductionNotes, next.ResolvedAngles)
	}
}

func TestAdversarialNodeDescriptionEditAcrossVisionConfigChange(t *testing.T) {
	a, prior := completedNodeFixture(t)
	prior.Input.Assets = []avatar.SourceAsset{{ID: "v1", Kind: "video", SHA256: "same", Duration: 5}}
	prior.Descriptions = []avatar.SourceDescription{{ID: "d1", AssetID: "v1", Start: 0, End: 5, Text: "旧的解读"}}
	prior.Steps = []avatar.WorkflowStep{{ID: "F01.4/v1", Status: "succeeded"}}
	next := avatar.WorkflowState{ID: uuid.NewString(), Input: prior.Input, ConfigSnapshot: map[string]string{"vision_model": "replacement-model"}}
	err := a.avatarWorkflow.inherit(&next, prior, avatar.WorkflowEdits{BaseRunID: prior.ID, Descriptions: []avatar.SourceDescription{{ID: "d1", AssetID: "v1", Start: 0, End: 5, Text: "用户明确修正的解读"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Descriptions) != 1 || next.Descriptions[0].Text != "用户明确修正的解读" {
		t.Fatalf("accepted description edit silently disappeared: %#v", next.Descriptions)
	}
	if len(next.Steps) == 0 || next.Steps[0].ID != "F01.4/v1" || next.Steps[0].Status != "succeeded" {
		t.Fatal("manual description would be analyzed again")
	}
	if next.Steps[0].InputHash != avatar.Digest([]any{"same", "replacement-model"}) {
		t.Fatal("analysis cache key would overwrite the manual correction")
	}
}

func TestAdversarialNodeMissingCacheKeepsHistoricalMaterial(t *testing.T) {
	for _, salted := range []bool{false, true} {
		t.Run(fmt.Sprintf("rerun=%v", salted), func(t *testing.T) {
			a, prior := completedNodeFixture(t)
			w := a.avatarWorkflow
			ctx := context.Background()
			owner := uuid.MustParse(w.ownerID(&prior))
			if salted {
				prior.Edits.RerunFrom = "F07"
			}
			calls := 0
			w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
				calls++
				if stage != "material" {
					return nil, fmt.Errorf("unexpected stage %s", stage)
				}
				path := filepath.Join(dir, "materials", "n1.mp4")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					return nil, err
				}
				if err := os.WriteFile(path, []byte(fmt.Sprintf("version-%d", calls)), 0600); err != nil {
					return nil, err
				}
				return json.Marshal(avatar.Material{NeedID: "n1", SegmentID: "p1", Kind: "animation", Path: "materials/n1.mp4", Duration: 5})
			}
			if err := w.produceMaterial(ctx, &prior, prior.Needs[0], "F07"); err != nil {
				t.Fatal(err)
			}
			if !salted {
				prior.Materials[0].BaseFingerprint = ""
			} // Compatible with historical runs before BaseFingerprint.
			prior.Status = "succeeded"
			if err := w.save(&prior); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(w.projectDir(prior.ProjectID), "cache", "materials", prior.Materials[0].Fingerprint+".mp4")
			if err := os.Remove(cache); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(cache + ".json"); err != nil {
				t.Fatal(err)
			}
			next, err := w.createTo(ctx, owner, uuid.MustParse(prior.ProjectID), "downstream-after-cache-loss", prior.Input.Revision, "F12", avatar.WorkflowEdits{BaseRunID: prior.ID, RerunFrom: "F10"})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.produceMaterial(ctx, &next, next.Needs[0], "F07"); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(w.dir(next.ID), "materials", "n1.mp4"))
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || string(body) != "version-1" {
				t.Fatalf("unaffected historical material regenerated: calls=%d content=%s", calls, body)
			}
		})
	}
}

func TestAdversarialNodeProposalChangedDuringAdmission(t *testing.T) {
	a, prior := completedNodeFixture(t)
	pool, _ := a.db.DB()
	pool.SetMaxOpenConns(1)
	reviewTextConfig(a)
	ctx := context.Background()
	h := avatarPackageHost{a}
	owner := a.avatarWorkflow.ownerID(&prior)
	_, err := h.Invoke(ctx, owner, prior.ProjectID, "avatar.edit-node", map[string]any{"node": "F03", "changes": "调整章节", "segments": []any{map[string]any{"id": "p1", "text": prior.Script, "chapter": "新的章节"}}}, "race-proposal")
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := h.project(ctx, owner, prior.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalOf(p)
	checked := make(chan struct{})
	var once sync.Once
	const hook = "node-adversarial-admission"
	if err := a.db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		// Confirm has already loaded its base snapshot before this idempotency query.
		if tx.Statement.Table == "workflow_runs" && strings.Contains(tx.Statement.SQL.String(), "input_hash") {
			once.Do(func() { close(checked) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer a.db.Callback().Query().Remove(hook)
	a.avatarMVP.mu.Lock() // Hold createTo at admission while another endpoint updates the base.
	type result struct {
		value any
		err   error
	}
	done := make(chan result, 1)
	go func() {
		v, e := h.Confirm(ctx, owner, prior.ProjectID, proposal.ID, proposal.Revision)
		done <- result{v, e}
	}()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		a.avatarMVP.mu.Unlock()
		t.Fatal("confirmation did not reach admission")
	}
	prior.Script = "用户刚确认的新文案。"
	prior.Segments[0].Text = prior.Script
	err = a.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id=? AND step_id=?", prior.ID, workflowMarker).Update("result", jsonMap(prior)).Error
	a.avatarMVP.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation deadlocked")
	}
	if r.err == nil {
		waitForPackageWorker(t, a, uuid.MustParse(owner), r.value.(gin.H)["run_id"].(string))
		t.Fatal("stale proposal was admitted after its base changed at the transaction boundary")
	}
	var count int64
	if err := a.db.Model(&models.WorkflowRun{}).Where("project_id=?", prior.ProjectID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stale proposal created %d runs", count)
	}
}

func TestAdversarialNodeRejectsMalformedEditsWithoutMutation(t *testing.T) {
	cases := []struct {
		name, pkg string
		args      map[string]any
	}{
		{"null subtitles", "avatar.edit-node", map[string]any{"node": "F09", "subtitles": nil}},
		{"wrong row type", "avatar.edit-node", map[string]any{"node": "F07", "needs": []any{"n1"}}},
		{"unknown node", "avatar.edit-node", map[string]any{"node": "F04/p1", "script": "bad"}},
		{"wrong node field", "avatar.edit-node", map[string]any{"node": "F07", "script": "bad"}},
		{"cross type material", "avatar.edit-node", map[string]any{"node": "F06", "needs": []any{map[string]any{"id": "n1", "prompt": "change"}}}},
		{"path injection", "avatar.edit-node", map[string]any{"node": "F07", "needs": []any{map[string]any{"id": "n1", "path": "../../private"}}}},
		{"duplicate ids", "avatar.edit-node", map[string]any{"node": "F10", "shots": []any{map[string]any{"id": "S1", "keyword": "one"}, map[string]any{"id": "S1", "keyword": "two"}}}},
		{"missing row", "avatar.edit-node", map[string]any{"node": "F10", "shots": []any{map[string]any{"id": "missing", "keyword": "x"}}}},
		{"empty script", "avatar.edit-node", map[string]any{"node": "F02", "script": "  \n"}},
		{"subtitle misalignment", "avatar.edit-node", map[string]any{"node": "F09", "subtitles": []any{map[string]any{"id": "p1", "text": "多出很多个字的字幕，不能继续使用已有对齐。"}}}},
		{"empty presentation", "avatar.edit-node", map[string]any{"node": "F11", "presentation": map[string]any{}}},
		{"oversized subtitle", "avatar.edit-node", map[string]any{"node": "F11", "presentation": map[string]any{"subtitle_scale": float64(100)}}},
		{"invalid color", "avatar.edit-node", map[string]any{"node": "F11", "presentation": map[string]any{"subtitle_color": "#zzzzzz"}}},
		{"invalid direction", "avatar.rerun", map[string]any{"from": "F10", "target": "F04"}},
		{"invalid target", "avatar.rerun", map[string]any{"from": "F04", "target": "F99"}},
		{"injected base", "avatar.rerun", map[string]any{"from": "F04", "base_run_id": uuid.NewString()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, prior := completedNodeFixture(t)
			ctx := context.Background()
			h := avatarPackageHost{a}
			owner := a.avatarWorkflow.ownerID(&prior)
			before, _, err := h.project(ctx, owner, prior.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			tc.args["changes"] = "malformed isolated edit"
			if _, err := h.Invoke(ctx, owner, prior.ProjectID, tc.pkg, tc.args, "invalid-edit"); err == nil {
				t.Fatal("invalid edit accepted")
			}
			after, _, err := h.project(ctx, owner, prior.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if string(before.ProjectMetadata) != string(after.ProjectMetadata) {
				t.Fatal("rejected edit changed project metadata")
			}
			var count int64
			a.db.Model(&models.WorkflowRun{}).Where("project_id=?", prior.ProjectID).Count(&count)
			if count != 1 {
				t.Fatal("invalid edit created a run")
			}
		})
	}
}

func TestAdversarialNodeConcurrentConfirmAdmitsOnce(t *testing.T) {
	a, prior := completedNodeFixture(t)
	pool, _ := a.db.DB()
	pool.SetMaxOpenConns(1)
	reviewTextConfig(a)
	h := avatarPackageHost{a}
	ctx := context.Background()
	owner := a.avatarWorkflow.ownerID(&prior)
	if _, err := h.Invoke(ctx, owner, prior.ProjectID, "avatar.rerun", map[string]any{"from": "F02", "target": "F02", "changes": "isolated repeat"}, "repeat-confirm"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := h.project(ctx, owner, prior.ProjectID)
	proposal := proposalOf(p)
	if _, err := h.Confirm(ctx, uuid.NewString(), prior.ProjectID, proposal.ID, proposal.Revision); err == nil {
		t.Fatal("cross-owner confirmation accepted")
	}
	if _, err := h.Confirm(ctx, owner, prior.ProjectID, proposal.ID, proposal.Revision+1); err == nil {
		t.Fatal("stale confirmation revision accepted")
	}
	type result struct {
		value any
		err   error
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			v, e := h.Confirm(ctx, owner, prior.ProjectID, proposal.ID, proposal.Revision)
			done <- result{v, e}
		}()
	}
	close(start)
	first, second := <-done, <-done
	if first.err != nil || second.err != nil {
		t.Fatalf("confirm failed: %v / %v", first.err, second.err)
	}
	id := first.value.(gin.H)["run_id"].(string)
	if id != second.value.(gin.H)["run_id"] {
		t.Fatal("concurrent confirms admitted different runs")
	}
	next := waitForPackageWorker(t, a, uuid.MustParse(owner), id)
	if next.Status != "paused" || next.TargetNode != "F02" {
		t.Fatalf("execution escaped target: %s %s", next.Status, next.Error)
	}
	for _, step := range next.Steps {
		if step.ProviderTaskID != "" || step.Submission != "" {
			t.Fatal("local-only confirmation submitted provider work")
		}
	}
	var count int64
	a.db.Model(&models.WorkflowRun{}).Where("project_id=?", prior.ProjectID).Count(&count)
	if count != 2 {
		t.Fatalf("expected old and one new run, got %d", count)
	}
}
