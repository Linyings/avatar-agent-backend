package agentpackages

import "testing"

func TestPackagesCoverWorkflowAndRejectInvalidArguments(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]bool{}
	for _, p := range registry {
		if p.Node != "" {
			nodes[p.Node] = true
		}
		if p.Version == "" || p.Entrypoint == "" || len(p.Outputs) == 0 {
			t.Fatalf("incomplete manifest: %s", p.ID)
		}
	}
	for _, node := range []string{"F01", "F01.4", "F02", "F03", "F04", "F05", "F06", "F07", "F08", "F09", "F10", "F11", "F12"} {
		if !nodes[node] {
			t.Errorf("missing %s", node)
		}
	}
	p, _ := Find("avatar.configure")
	for _, args := range []map[string]any{{"ratio": "4:3"}, {"use_avatar": "yes"}, {"photo": "/etc/passwd"}, {"revision": 1.5}} {
		if Validate(p.InputSchema, args) == nil {
			t.Errorf("accepted unsafe/invalid input: %v", args)
		}
	}
	if err := Validate(p.InputSchema, map[string]any{"topic": "介绍产品", "ratio": "9:16", "use_avatar": false}); err != nil {
		t.Fatal(err)
	}
}
