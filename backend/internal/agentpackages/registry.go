// Package agentpackages defines reviewed, versioned production plugins.
// Manifests are embedded at build time; request data can never select shell code.
package agentpackages

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

//go:embed packages/*/package.json
var files embed.FS

type Manifest struct {
	ID           string         `json:"id"`
	Version      string         `json:"version"`
	Tool         string         `json:"tool"`
	Title        string         `json:"title"`
	Node         string         `json:"node,omitempty"`
	Entrypoint   string         `json:"entrypoint"`
	Dependencies []string       `json:"dependencies"`
	Outputs      []string       `json:"outputs"`
	Tools        []string       `json:"tools"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema"`
}

func Load() ([]Manifest, error) {
	paths, err := fs.Glob(files, "packages/*/package.json")
	if err != nil {
		return nil, err
	}
	out := []Manifest{}
	seen := map[string]bool{}
	for _, path := range paths {
		data, err := files.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var p Manifest
		if err = json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if p.ID == "" || p.Tool == "" || seen[p.ID] || seen[p.Tool] {
			return nil, fmt.Errorf("invalid or duplicate package %s", path)
		}
		seen[p.ID] = true
		seen[p.Tool] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
func Find(id string) (Manifest, bool) {
	all, err := Load()
	if err != nil {
		return Manifest{}, false
	}
	for _, p := range all {
		if p.ID == id || p.Tool == id {
			return p, true
		}
	}
	return Manifest{}, false
}

func Validate(schema map[string]any, value any) error { return validate(schema, value, "input") }
func validate(s map[string]any, v any, path string) error {
	bad := func() error { return fmt.Errorf("%s 参数类型或取值无效", path) }
	switch s["type"] {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		props, _ := s["properties"].(map[string]any)
		required, _ := s["required"].([]any)
		for _, r := range required {
			key, _ := r.(string)
			if _, ok := m[key]; !ok {
				return fmt.Errorf("%s 缺少 %s", path, key)
			}
		}
		for k, x := range m {
			raw, ok := props[k]
			if !ok {
				return fmt.Errorf("%s 不支持字段 %s", path, k)
			}
			sub, ok := raw.(map[string]any)
			if !ok {
				return bad()
			}
			if err := validate(sub, x, path+"."+k); err != nil {
				return err
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return bad()
		}
		if max, ok := s["maxItems"].(float64); ok && len(a) > int(max) {
			return bad()
		}
		sub, _ := s["items"].(map[string]any)
		for _, x := range a {
			if err := validate(sub, x, path+"[]"); err != nil {
				return err
			}
		}
	case "string":
		x, ok := v.(string)
		if !ok {
			return bad()
		}
		if max, ok := s["maxLength"].(float64); ok && utf8.RuneCountInString(x) > int(max) {
			return bad()
		}
		if choices, ok := s["enum"].([]any); ok {
			found := false
			for _, c := range choices {
				if x == c {
					found = true
				}
			}
			if !found {
				return bad()
			}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return bad()
		}
	case "number", "integer":
		x, ok := v.(float64)
		if !ok {
			return bad()
		}
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return bad()
		}
		if s["type"] == "integer" && math.Trunc(x) != x {
			return bad()
		}
		if min, ok := s["minimum"].(float64); ok && x < min {
			return bad()
		}
	default:
		return fmt.Errorf("%s 未知 schema 类型 %s", path, strings.TrimSpace(fmt.Sprint(s["type"])))
	}
	return nil
}
