package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/models"
)

func TestAvatarWorkflowReferenceLinksPersistAndRemainVisibleToAgent(t *testing.T) {
	a := newTestApp(t)
	token := registerAndLogin(t, a, "reference-links@example.com")
	response := request(a, "POST", "/api/v1/avatar-mvp/projects", map[string]any{"title": "参考网页", "topic": "制作视频", "reference_links": []string{"https://example.com/a", "https://example.com/b"}}, token)
	if response.Code != 200 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		ID   string `json:"id"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID == "" {
		body.ID = body.Data.ID
	}
	var project models.Project
	if err := a.db.First(&project, "id=?", body.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := (avatarPackageHost{a}).snapshot(context.Background(), project.OwnerID.String(), project.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(snapshot)
	if !strings.Contains(string(data), `"reference_links":["https://example.com/a","https://example.com/b"]`) {
		t.Fatalf("links missing from persisted agent input: %s", data)
	}
}

func TestAvatarWorkflowReferenceLinksRejectInvalidInput(t *testing.T) {
	a := newTestApp(t)
	token := registerAndLogin(t, a, "reference-validation@example.com")
	for _, link := range []string{"javascript:alert(1)", "file:///etc/passwd", "https://user:secret@example.com", "https://", "https://example.com/" + strings.Repeat("x", 2048)} {
		response := request(a, "POST", "/api/v1/avatar-mvp/projects", map[string]any{"title": "invalid", "reference_links": []string{link}}, token)
		if response.Code != 400 {
			t.Errorf("invalid reference accepted: status=%d", response.Code)
		}
	}
	var count int64
	a.db.Model(&models.Project{}).Where("name=?", "invalid").Count(&count)
	if count != 0 {
		t.Fatalf("invalid links created %d projects", count)
	}
}
