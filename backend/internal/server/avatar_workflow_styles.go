package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

type workflowStyle struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Version        string         `json:"version"`
	Source         string         `json:"source"`
	Entry          string         `json:"entry,omitempty"`
	Signature      string         `json:"signature,omitempty"`
	Design         map[string]any `json:"design,omitempty"`
	Description    string         `json:"description,omitempty"`
	Ratios         []string       `json:"ratios"`
	Templates      []string       `json:"templates"`
	Published      bool           `json:"published"`
	Legacy         bool           `json:"legacy"`
	Archived       bool           `json:"archived,omitempty"`
	OriginalPost   string         `json:"original_post,omitempty"`
	PreviewPage    string         `json:"preview_page,omitempty"`
	Preview        string         `json:"preview"`
	CardPreview    string         `json:"card_preview,omitempty"`
	CardPoster     string         `json:"card_poster,omitempty"`
	PreviewKind    string         `json:"preview_kind,omitempty"`
	PreviewWidth   int            `json:"preview_width,omitempty"`
	PreviewHeight  int            `json:"preview_height,omitempty"`
	ProductionMode string         `json:"production_mode,omitempty"`
	Reference      string         `json:"reference,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	SourceDownload string         `json:"source_download,omitempty"`
}

func workflowStyleFilesExist(root string, names ...string) bool {
	for _, name := range names {
		s, e := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if e != nil || s.IsDir() {
			return false
		}
	}
	return true
}

func loadWorkflowStyles(root string) ([]workflowStyle, error) {
	styles := []workflowStyle{}
	data, err := os.ReadFile(filepath.Join(root, "native-styles.json"))
	if err == nil {
		if err = json.Unmarshal(data, &styles); err != nil {
			return nil, fmt.Errorf("动效风格目录格式无效")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range styles {
		s := &styles[i]
		if !avatar.SafeID.MatchString(s.ID) || !strings.HasPrefix(s.ID, "mg-") || seen[s.ID] || !strings.HasPrefix(s.Entry, "demos/") || strings.Contains(s.Entry, "..") || strings.Contains(s.Entry, "\\") || filepath.IsAbs(s.Entry) {
			return nil, fmt.Errorf("动效风格目录路径无效")
		}
		seen[s.ID] = true
		s.Published = workflowStyleFilesExist(root, "catalog/"+s.ID+".mp4", "catalog/"+s.ID+".jpg", "catalog/"+s.ID+"-reference.mp4", "catalog/"+s.ID+"-source.zip", "templates/"+s.ID+"/original-prompt.md", "templates/"+s.ID+"/SOURCE.json", "mg-upstream/"+s.Entry)
		s.Preview = "/api/v1/avatar-mvp/catalog/" + s.ID + "/preview"
		if workflowStyleFilesExist(root, "catalog/"+s.ID+"-card.mp4", "catalog/"+s.ID+"-card.jpg") {
			s.CardPreview = "/api/v1/avatar-mvp/catalog/" + s.ID + "/card-preview"
			s.CardPoster = "/api/v1/avatar-mvp/catalog/" + s.ID + "/card-poster"
		}
		s.Reference = "/api/v1/avatar-mvp/catalog/" + s.ID + "/reference"
		s.Prompt = "/api/v1/avatar-mvp/catalog/" + s.ID + "/prompt"
		s.SourceDownload = "/api/v1/avatar-mvp/catalog/" + s.ID + "/source"
	}
	for _, old := range []workflowStyle{{ID: "flat-vector", Name: "扁平图解（旧版简化）"}, {ID: "line-art", Name: "线条图解（旧版简化）"}} {
		old.Version = "2"
		old.Source = "mg-styles-15"
		old.Legacy = true
		old.Description = "历史任务兼容的简化卡片组件；新项目建议选择原生动效。"
		old.Ratios = []string{"9:16", "16:9", "1:1"}
		old.Templates = []string{"steps", "compare", "relationship", "highlight", "summary"}
		old.Published = workflowStyleFilesExist(root, "catalog/"+old.ID+".mp4")
		old.Preview = "/api/v1/avatar-mvp/catalog/" + old.ID + "/preview"
		styles = append(styles, old)
	}
	// Keep historical IDs and their real production behavior. Archiving removes
	// choices from new-project pickers without turning them into legacy renderers.
	newStyles, err := loadExplainerStyles(root)
	if err != nil {
		return nil, err
	}
	if len(newStyles) > 0 {
		for i := range styles {
			styles[i].Archived = true
		}
		styles = append(newStyles, styles...)
	}
	return styles, nil
}

func loadExplainerStyles(root string) ([]workflowStyle, error) {
	data, err := os.ReadFile(filepath.Join(root, "explainer-styles.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var styles []workflowStyle
	if err := json.Unmarshal(data, &styles); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range styles {
		s := &styles[i]
		if !avatar.SafeID.MatchString(s.ID) || !strings.HasPrefix(s.ID, "explain-") || seen[s.ID] {
			return nil, fmt.Errorf("解释视频方案 ID 无效或重复")
		}
		seen[s.ID] = true
		s.Published = len(s.Design) > 0 && workflowStyleFilesExist(root,
			"catalog/"+s.ID+"-reference.jpg", "templates/"+s.ID+"/production.md",
			"templates/"+s.ID+"/original-prompt.md", "templates/"+s.ID+"/SOURCE.json")
		base := "/api/v1/avatar-mvp/catalog/" + s.ID
		s.CardPoster = base + "/poster"
		s.Prompt = base + "/prompt"
		if workflowStyleFilesExist(root, "catalog/"+s.ID+"-reference.mp4") {
			s.Preview, s.Reference = base+"/reference", base+"/reference"
		}
		if workflowStyleFilesExist(root, "catalog/"+s.ID+"-card.mp4", "catalog/"+s.ID+"-card.jpg") {
			s.CardPreview, s.CardPoster = base+"/card-preview", base+"/card-poster"
		}
	}
	return styles, nil
}

// Curated production instructions, never raw social posts or example stories.
func workflowStyleProductionContext(root, id string) map[string]any {
	s, found := findWorkflowStyle(root, id)
	if !found {
		return nil
	}
	result := map[string]any{"id": s.ID, "version": s.Version, "name": s.Name, "profile": s.Design}
	if strings.HasPrefix(id, "explain-") {
		if data, err := os.ReadFile(filepath.Join(root, "templates", id, "production.md")); err == nil {
			result["method"] = string(data)
		}
	}
	return result
}

func findWorkflowStyle(root, id string) (workflowStyle, bool) {
	styles, err := loadWorkflowStyles(root)
	if err != nil {
		return workflowStyle{}, false
	}
	for _, s := range styles {
		if s.ID == id {
			return s, true
		}
	}
	return workflowStyle{}, false
}

func defaultWorkflowStyle(root string) string {
	styles, _ := loadWorkflowStyles(root)
	for _, s := range styles {
		if !s.Legacy && !s.Archived && s.Published {
			return s.ID
		}
	}
	return "flat-vector"
}
