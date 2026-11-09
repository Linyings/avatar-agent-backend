package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/agent"
)

func TestProjectSnapshotAdvertisesDefaultSkillsWithoutDocumentBodies(t *testing.T) {
	a, owner, project, _ := workflowFixture(t)
	result, err := (avatarPackageHost{a}).snapshot(context.Background(), owner.String(), project.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Skills []agent.SkillDefinition `json:"skills"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Skills) == 0 || out.Skills[0].Name != "talking-head-storyboard" || !out.Skills[0].DefaultEnabled || !out.Skills[0].AllowImplicitInvocation {
		t.Fatalf("default skill missing from deployed project snapshot: %s", raw)
	}
	if strings.Contains(string(raw), "区分五种粒度") {
		t.Fatal("project inspection preloaded the full skill")
	}
}
