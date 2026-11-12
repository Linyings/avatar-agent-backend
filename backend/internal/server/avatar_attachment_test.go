package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

// Files the system cannot analyse (PDF, Word, archives ...) are kept as project
// attachments. Reference analysis lists them so the Agent knows they exist,
// but never sends their bytes to a model as an image.
func TestAttachmentIsListedButNeverSentToModel(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	dir := w.projectDir(s.ProjectID)
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "f1"), []byte("%PDF-1.7"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Input.Assets = []avatar.SourceAsset{{ID: "f1", Kind: "file", Name: "brief.pdf", Path: "assets/f1", SHA256: "x"}}
	os.MkdirAll(w.dir(s.ID), 0700)
	w.save(&s)
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		return nil, fmt.Errorf("an attachment must not reach the model")
	}
	if err := w.analyzeReferences(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	var found *avatar.ReferenceAnalysis
	for i := range s.References {
		if s.References[i].ID == "f1" {
			found = &s.References[i]
		}
	}
	if found == nil || found.Kind != "file" || found.Status != "stored" {
		t.Fatalf("attachment not listed as stored reference: %#v", s.References)
	}
}

func TestUploadKindAcceptsAttachment(t *testing.T) {
	for _, kind := range []string{"photo", "image", "video", "audio", "text", "file"} {
		if !validUploadKind(kind) {
			t.Fatalf("%s rejected", kind)
		}
	}
	if validUploadKind("exe") {
		t.Fatal("unknown kind accepted")
	}
}
