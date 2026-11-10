package media

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRegistryLoadsEveryKindWithUniqueStandardPaths(t *testing.T) {
	all := Models()
	if len(all) < 200 {
		t.Fatalf("registry unexpectedly small: %d", len(all))
	}
	counts := map[Kind]int{}
	seen := map[string]bool{}
	for _, m := range all {
		counts[m.Kind]++
		if m.ID == "" || m.Name == "" || !strings.HasPrefix(m.Path, "/openapi/v2/") || seen[m.ID] {
			t.Fatalf("invalid or duplicate model %#v", m)
		}
		seen[m.ID] = true
		for _, r := range m.Required {
			if strings.Contains(r, "##") {
				t.Fatalf("%s: workflow-style endpoint leaked into registry", m.ID)
			}
		}
		if got, ok := Find(m.ID); !ok || got.Path != m.Path {
			t.Fatalf("%s not resolvable", m.ID)
		}
	}
	for _, k := range []Kind{KindMusic, KindSFX, KindT2I, KindI2I, KindT2V, KindI2V, KindR2V, KindEnhance} {
		if counts[k] == 0 {
			t.Fatalf("kind %s has no models", k)
		}
	}
	if _, ok := Find("fish-audio/s2"); ok {
		t.Fatal("unregistered model resolved")
	}
	if len(ByKind(KindT2V)) != counts[KindT2V] {
		t.Fatal("ByKind disagrees with Models")
	}
}

func TestRegistryExcludesModelsRunningHubNoLongerServes(t *testing.T) {
	data, err := os.ReadFile("../../scripts/media_unavailable.json")
	if err != nil {
		t.Fatal(err)
	}
	var unavailable map[string]string
	if err := json.Unmarshal(data, &unavailable); err != nil || len(unavailable) == 0 {
		t.Fatalf("unavailable list unreadable: %v", err)
	}
	for id := range unavailable {
		if _, ok := Find(id); ok {
			t.Fatalf("%s is discontinued or unknown at RunningHub but still selectable", id)
		}
	}
}

func TestCapabilitiesAreDerivedFromSchema(t *testing.T) {
	kling, _ := Find("kling-v3.0-pro/image-to-video")
	c := kling.Capabilities()
	if !c.FirstFrame || !c.LastFrame || !c.NativeAudio || len(c.Durations) == 0 || c.Durations[0] != 3 || c.MaxDuration != 15 {
		t.Fatalf("kling caps wrong: %#v", c)
	}
	veo, _ := Find("rhart-video-v3.1-fast/text-to-video")
	if c := veo.Capabilities(); len(c.Ratios) == 0 || c.FirstFrame {
		t.Fatalf("veo t2v caps wrong: %#v", c)
	}
}
