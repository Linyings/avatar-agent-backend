package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

func (w *avatarWorkflowService) configSnapshot() map[string]string {
	c := w.app.cfg
	if !c.OpenAIFlashConfigured() && c.AgentConfigured() {
		c.OpenAIFlashModel = c.AgentModel
		c.OpenAIFlashAPIBaseURL = c.AgentAPIBaseURL
	}
	visionModel, visionEndpoint := c.MovieVisionModel, c.MovieVisionAPIBaseURL
	if os.Getenv("AVATAR_VISION_USE_FLASH") == "true" && !c.MovieVisionConfigured() {
		visionModel, visionEndpoint = c.OpenAIFlashModel, c.OpenAIFlashAPIBaseURL
	}
	return map[string]string{"workflow": avatar.WorkflowVersion, "video": w.app.avatarMVP.options.VideoConfigID(), "tts_model": sTTSModel(w.app), "tts_language": c.TTSLanguage, "flash_model": c.OpenAIFlashModel, "flash_endpoint": c.OpenAIFlashAPIBaseURL, "vision_model": visionModel, "vision_endpoint": visionEndpoint, "vision_sampling": "1fps/batch20s", "style_version": avatar.MotionPlanVersion + "+legacy/2", "renderer": "talkcraft-workflow/4", "presentation_version": avatar.PresentationVersion}
}

func workflowProviderConfigCompatible(old, next map[string]string) bool {
	withoutLocalDesign := func(in map[string]string) map[string]string {
		out := map[string]string{}
		for key, value := range in {
			if key != "renderer" && key != "style_version" && key != "presentation_version" && key != "workflow" {
				out[key] = value
			}
		}
		return out
	}
	return avatar.Digest(withoutLocalDesign(old)) == avatar.Digest(withoutLocalDesign(next))
}
func textSegments(in []avatar.Segment) []avatar.Segment {
	out := make([]avatar.Segment, len(in))
	for i, p := range in {
		out[i] = avatar.Segment{ID: p.ID, Chapter: p.Chapter, Text: p.Text, DirectorSceneID: p.DirectorSceneID}
	}
	return out
}

// A style/ratio revision must not silently discard diagnostics for footage it
// reuses. Do not carry over render warnings or checks for changed materials.
func replayWorkflowMaterialWarnings(next *avatar.WorkflowState, prior avatar.WorkflowState, material avatar.Material) {
	for _, old := range prior.Materials {
		if old.NeedID != material.NeedID || old.Fingerprint != material.Fingerprint {
			continue
		}
		node := map[string]string{"avatar": "F06", "animation": "F07", "real": "F08"}[old.Kind]
		if node == "" {
			return
		}
		prefix := node + "/" + old.NeedID
		for _, event := range prior.Events {
			if event.Type == "quality" && (event.Node == prefix || strings.HasPrefix(event.Node, prefix+"-")) {
				next.AddWarning(event.Node, event.Type, event.Message)
			}
		}
		return
	}
}

func sameSourceInputs(a, b avatar.WorkflowInput) bool {
	selectSources := func(in avatar.WorkflowInput) []avatar.SourceAsset {
		out := []avatar.SourceAsset{}
		for _, s := range in.Assets {
			if s.Kind != "photo" && !s.Excluded {
				out = append(out, s)
			}
		}
		return out
	}
	return avatar.Digest(selectSources(a)) == avatar.Digest(selectSources(b))
}
func (w *avatarWorkflowService) inherit(s *avatar.WorkflowState, previous avatar.WorkflowState, edits avatar.WorkflowEdits) error {
	if edits.RerunFrom != "" && (!isKnownWorkflowNode(edits.RerunFrom) || (s.TargetNode != "" && edits.RerunFrom > s.TargetNode)) {
		return fmt.Errorf("重做节点范围无效")
	}
	s.ReusedFrom = previous.ID
	s.Edits = edits
	edits.ApplyPresentation(&s.Input)
	edits.ApplyVoice(&s.Input)
	sourceSame := sameSourceInputs(s.Input, previous.Input)
	activeReferences := func(in avatar.WorkflowInput) []avatar.SourceAsset {
		out := []avatar.SourceAsset{}
		for _, asset := range in.Assets {
			if !asset.Excluded {
				out = append(out, asset)
			}
		}
		return out
	}
	referencesSame := avatar.Digest(activeReferences(s.Input)) == avatar.Digest(activeReferences(previous.Input)) && s.Input.Script == previous.Input.Script && slices.Equal(s.Input.ReferenceLinks, previous.Input.ReferenceLinks)
	contentSame := sourceSame && s.Input.Script == previous.Input.Script && s.Input.Topic == previous.Input.Topic && slices.Equal(s.Input.ReferenceLinks, previous.Input.ReferenceLinks)
	if avatar.DirectorWorkflow(s) && avatar.DirectorWorkflow(&previous) {
		if contentSame && s.Input.Direction == previous.Input.Direction && s.Input.Style == previous.Input.Style && s.Input.Ratio == previous.Input.Ratio && s.Input.UseAvatar == previous.Input.UseAvatar && avatar.AnimationEnabled(s.Input) == avatar.AnimationEnabled(previous.Input) {
			s.Requirements = previous.Requirements
		}
		if referencesSame {
			s.References = append([]avatar.ReferenceAnalysis{}, previous.References...)
			s.ReferencesAnalyzed = previous.ReferencesAnalyzed
		}
		if referencesSame && contentSame && s.Input.Direction == previous.Input.Direction && s.Input.Style == previous.Input.Style && s.Input.Ratio == previous.Input.Ratio && s.Input.UseAvatar == previous.Input.UseAvatar && avatar.AnimationEnabled(s.Input) == avatar.AnimationEnabled(previous.Input) {
			data, _ := json.Marshal(previous.Director)
			_ = json.Unmarshal(data, &s.Director)
		}
		if edits.RerunFrom != "" && edits.RerunFrom <= "F01.6" {
			s.Director = nil
			if edits.RerunFrom <= "F01.4" {
				s.References = nil
				s.ReferencesAnalyzed = false
			}
			if edits.RerunFrom == "F01.2" {
				s.Requirements = nil
			}
		}
	}
	// Explicit corrections are authoritative for unchanged source files, even
	// when the default analysis model changed since the original run.
	if len(edits.Descriptions) > 0 || (previous.ConfigSnapshot["vision_model"] == s.ConfigSnapshot["vision_model"] && previous.ConfigSnapshot["vision_endpoint"] == s.ConfigSnapshot["vision_endpoint"] && previous.ConfigSnapshot["vision_sampling"] == s.ConfigSnapshot["vision_sampling"]) {
		unchanged := map[string]bool{}
		for _, current := range s.Input.Assets {
			for _, old := range previous.Input.Assets {
				if current.Kind == "video" && !current.Excluded && !old.Excluded && current.ID == old.ID && current.SHA256 == old.SHA256 && current.Kind == old.Kind {
					unchanged[current.ID] = true
				}
			}
		}
		for _, description := range previous.Descriptions {
			if unchanged[description.AssetID] {
				s.Descriptions = append(s.Descriptions, description)
			}
		}
		for _, step := range previous.Steps {
			matches := false
			for id := range unchanged {
				if step.ID == "F01.4/"+id || strings.HasPrefix(step.ID, "F01.4/"+id+"-b") {
					matches = true
				}
			}
			if matches && step.Status == "succeeded" {
				if len(edits.Descriptions) > 0 {
					for _, source := range s.Input.Assets {
						if step.ID == "F01.4/"+source.ID {
							step.InputHash = avatar.Digest([]any{source.SHA256, s.ConfigSnapshot["vision_model"]})
						}
					}
				}
				step.Seconds = 0
				step.Attempt = 0
				step.CostStatus = "reused"
				step.Usage = nil
				step.Orders = nil
				step.Attempts = nil
				step.ProviderTaskID = ""
				step.Submission = ""
				step.StartedAt = nil
				step.CompletedAt = nil
				s.Steps = append(s.Steps, step)
			}
		}
	}
	if !contentSame && edits.BaseRunID != "" {
		return fmt.Errorf("修改基础文案/主题或源文件后请先建立新的内容版本，再做局部修改")
	}
	if contentSame && (previous.ScriptConfirmed || len(edits.Segments) > 0) {
		s.Script = previous.Script
		s.ProductionNotes = append([]string(nil), previous.ProductionNotes...)
		s.ResolvedAngles = append([]string(nil), previous.ResolvedAngles...)
		s.Segments = textSegments(previous.Segments)
		s.ScriptConfirmed = true
		if len(edits.Segments) > 0 {
			var body strings.Builder
			for _, p := range edits.Segments {
				body.WriteString(p.Text)
			}
			if err := avatar.ValidateSegments(body.String(), edits.Segments); err != nil {
				return err
			}
			s.Script = body.String()
			s.Segments = textSegments(edits.Segments)
			s.ScriptConfirmed = false
			// Subtitle corrections are tied to the exact narration and alignment.
			// Retain only bindings whose paragraph identity and spoken text survive.
			var subtitles map[string]string
			for _, seg := range s.Segments {
				for _, old := range previous.Segments {
					if seg.ID == old.ID && seg.Text == old.Text {
						if text, ok := s.Input.SubtitleEdits[seg.ID]; ok {
							if subtitles == nil {
								subtitles = map[string]string{}
							}
							subtitles[seg.ID] = text
						}
					}
				}
			}
			s.Input.SubtitleEdits = subtitles
		}
		sameSegments := avatar.Digest(textSegments(s.Segments)) == avatar.Digest(textSegments(previous.Segments))
		sameRequirements := s.Input.UseAvatar == previous.Input.UseAvatar && avatar.Digest(s.Input.UseAnimation) == avatar.Digest(previous.Input.UseAnimation) && s.Input.Direction == previous.Input.Direction && avatar.Digest(s.Input.Angles) == avatar.Digest(previous.Input.Angles)
		if sameSegments && sameRequirements {
			s.Needs = append([]avatar.MaterialNeed(nil), previous.Needs...)
			if s.Input.Style == previous.Input.Style && s.Input.Ratio == previous.Input.Ratio && s.ConfigSnapshot["presentation_version"] == previous.ConfigSnapshot["presentation_version"] {
				s.Shots = append([]avatar.WorkflowShot(nil), previous.Shots...)
			}
		}
		if s.Input.Voice == previous.Input.Voice && s.ConfigSnapshot["tts_model"] == previous.ConfigSnapshot["tts_model"] && s.ConfigSnapshot["tts_language"] == previous.ConfigSnapshot["tts_language"] {
			for i, p := range s.Segments {
				for _, old := range previous.Segments {
					if old.ID == p.ID && old.Text == p.Text && avatar.EffectiveVoiceSpeed(s.Input, p.ID) == avatar.EffectiveVoiceSpeed(previous.Input, p.ID) {
						s.Segments[i].Fingerprint = old.Fingerprint
					}
				}
			}
		}
	}
	if avatar.DirectorWorkflow(s) && s.Director == nil {
		s.Needs = nil
		s.Shots = nil
	}
	if len(edits.RegenerateSegments) > 0 {
		s.Shots = nil
		s.Needs = nil
	}
	for _, seg := range s.Segments {
		if avatar.EffectiveVoiceSpeed(s.Input, seg.ID) != avatar.EffectiveVoiceSpeed(previous.Input, seg.ID) {
			s.Shots = nil
			s.Needs = nil
		}
	}
	if edits.Director != nil {
		if err := avatar.ValidateDirector(*edits.Director); err != nil {
			return err
		}
		data, _ := json.Marshal(edits.Director)
		_ = json.Unmarshal(data, &s.Director)
		s.Needs = nil
		s.Shots = nil
	}
	if len(edits.Descriptions) > 0 {
		s.ReferencesAnalyzed = false
		s.Director = nil
		old := map[string]avatar.SourceDescription{}
		for _, d := range previous.Descriptions {
			old[d.ID] = d
		}
		for _, d := range edits.Descriptions {
			prior, ok := old[d.ID]
			if !ok || prior.AssetID != d.AssetID || prior.Start != d.Start || prior.End != d.End || strings.TrimSpace(d.Text) == "" {
				return fmt.Errorf("只允许修订已有源区间文案")
			}
		}
		for i, d := range s.Descriptions {
			for _, edit := range edits.Descriptions {
				if edit.ID == d.ID {
					s.Descriptions[i].Text = edit.Text
					s.Descriptions[i].Coverage = d.Coverage + "；用户修订"
				}
			}
		}
		// Explicit source corrections require a fresh content review.
		s.ScriptConfirmed = false
		// Keep the user's confirmed narration, but require explicit re-confirmation.
		// Correcting a source description only replans its downstream matching.
		s.Needs = nil
		s.Shots = nil
	}
	if len(edits.Needs) > 0 {
		s.Needs = append([]avatar.MaterialNeed(nil), edits.Needs...)
		s.Shots = nil
	}
	if len(edits.Shots) > 0 {
		s.Shots = append([]avatar.WorkflowShot(nil), edits.Shots...)
	}
	if edits.Script != nil {
		if strings.TrimSpace(*edits.Script) == "" || len([]rune(*edits.Script)) > 10000 {
			return fmt.Errorf("口播稿为空或过长")
		}
		s.Script = *edits.Script
		s.Segments = nil
		s.ScriptConfirmed = false
		s.AudioConfirmed = false
		s.Needs = nil
		s.Shots = nil
		s.ProductionNotes = nil
		s.ResolvedAngles = nil
		s.Input.SubtitleEdits = nil
	}
	if from := edits.RerunFrom; from != "" {
		if from == "F01.4" {
			s.Descriptions = nil
			s.Steps = nil
		}
		if from <= "F03" {
			if from == "F03" {
				s.Script = previous.Script
				// F03 already removed these instructions from the spoken script.
				// Re-segmenting that cleaned script cannot extract them again.
				s.ProductionNotes = append([]string(nil), previous.ProductionNotes...)
				s.ResolvedAngles = append([]string(nil), previous.ResolvedAngles...)
			} else {
				s.Script = s.Input.Script
				s.ProductionNotes = nil
				s.ResolvedAngles = nil
			}
			s.Segments = nil
			s.ScriptConfirmed = false
			s.Input.SubtitleEdits = nil
		}
		if from <= "F04" {
			edits.RegenerateSegments = nil
			for _, seg := range s.Segments {
				edits.RegenerateSegments = append(edits.RegenerateSegments, seg.ID)
			}
			s.AudioConfirmed = false
		}
		if from <= "F05" {
			s.Needs = nil
		}
		if from <= "F10" {
			s.Shots = nil
		}
		s.Edits = edits
	}
	for _, id := range edits.RegenerateSegments {
		found := false
		for _, p := range s.Segments {
			if p.ID == id {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("重配音段落不存在")
		}
	}
	// Reuse the actual confirmed files and alignment for unchanged paragraphs.
	// Styling and subtitles must not re-run TTS or ask to approve the same sound.
	allAudio := len(s.Segments) > 0
	for i := range s.Segments {
		p := &s.Segments[i]
		var old avatar.Segment
		for _, candidate := range previous.Segments {
			if candidate.ID == p.ID && candidate.Text == p.Text && candidate.Fingerprint == p.Fingerprint {
				old = candidate
			}
		}
		usable := old.Audio != "" && old.Duration > 0 && old.Fingerprint != ""
		for _, id := range edits.RegenerateSegments {
			if id == p.ID {
				usable = false
			}
		}
		for _, file := range []string{"audio.wav", "timestamps.json", "alignment-cache.json"} {
			if _, err := os.Stat(filepath.Join(w.dir(previous.ID), p.ID, file)); err != nil {
				usable = false
			}
		}
		if !usable {
			allAudio = false
			continue
		}
		for _, file := range []string{"audio.wav", "timestamps.json", "alignment-cache.json"} {
			if err := copyWorkflowFile(filepath.Join(w.dir(previous.ID), p.ID, file), filepath.Join(w.dir(s.ID), p.ID, file)); err != nil {
				return err
			}
		}
		*p = old
		s.Steps = append(s.Steps, avatar.WorkflowStep{ID: "F04/" + p.ID, Status: "succeeded", InputHash: avatar.Digest(w.audioFingerprint(s, *p)), CostStatus: "reused"})
		w.addArtifacts(s, []avatar.WorkflowArtifact{{ID: "audio-" + p.ID, Name: p.ID + ".wav", Path: p.Audio, MediaType: "audio/wav"}})
	}
	s.AudioConfirmed = allAudio && previous.AudioConfirmed && s.ScriptConfirmed
	if edits.MaterialOnly != "" {
		return w.inheritMaterialNodes(s, previous)
	}
	return nil
}

func (w *avatarWorkflowService) audioFingerprint(s *avatar.WorkflowState, seg avatar.Segment) string {
	audioInput := s.Input
	audioInput.VoiceSpeed = avatar.EffectiveVoiceSpeed(s.Input, seg.ID)
	values := []any{avatar.AudioFingerprint(audioInput, seg.Text), sTTSModel(w.app), w.app.cfg.TTSLanguage}
	if workflowRerunsNode(s, "F04") {
		return avatar.Digest(append(values, s.ID))
	}
	for _, id := range s.Edits.RegenerateSegments {
		if id == seg.ID {
			values = append(values, s.ID)
			return avatar.Digest(values)
		}
	}
	if seg.Fingerprint != "" {
		return seg.Fingerprint
	}
	return avatar.Digest(values)
}

func (w *avatarWorkflowService) reuseStableNeeds(ctx context.Context, s *avatar.WorkflowState) {
	if s.ReusedFrom == "" || len(s.Edits.Needs) > 0 || workflowRerunsNode(s, "F05") {
		return
	}
	prior, err := w.load(ctx, uuid.MustParse(w.ownerID(s)), s.ReusedFrom)
	if err != nil {
		return
	}
	if s.Input.Direction != prior.Input.Direction || avatar.Digest(s.Input.Angles) != avatar.Digest(prior.Input.Angles) {
		return
	}
	for i, n := range s.Needs {
		var current, old avatar.Segment
		for _, p := range s.Segments {
			if p.ID == n.SegmentID {
				current = p
			}
		}
		for _, p := range prior.Segments {
			if p.ID == n.SegmentID {
				old = p
			}
		}
		if current.Text != old.Text || current.ID == "" {
			continue
		}
		for _, pn := range prior.Needs {
			if pn.ID == n.ID && pn.Kind == n.Kind && pn.SegmentID == n.SegmentID {
				if n.Kind == "avatar" {
					pn.Duration = current.Duration
				} else if old.Duration > 0 && current.Duration != old.Duration {
					pn.Duration = pn.Duration * current.Duration / old.Duration
					if pn.NarrationStart != nil {
						start := *pn.NarrationStart * current.Duration / old.Duration
						pn.NarrationStart = &start
					}
				}
				s.Needs[i] = pn
			}
		}
	}
}

// A deliberate rerun invalidates its start and every later production step.
// The end target limits execution, not invalidation, so later resume is safe.
func workflowRerunsNode(s *avatar.WorkflowState, node string) bool {
	return s.Edits.RerunFrom != "" && node >= s.Edits.RerunFrom
}

func canonicalizeWorkflowNeeds(needs []avatar.MaterialNeed) {
	counts := map[string]int{}
	for i, n := range needs {
		key := n.SegmentID + "_" + n.Kind
		if n.Kind == "avatar" {
			key += "_" + avatar.Digest(n.Angle)[:8]
		}
		counts[key]++
		needs[i].ID = fmt.Sprintf("n_%s_%d", avatar.Digest(key)[:16], counts[key])
	}
}
