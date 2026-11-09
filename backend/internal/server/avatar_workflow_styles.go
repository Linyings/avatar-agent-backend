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
	return styles, nil
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
		if !s.Legacy && s.Published {
			return s.ID
		}
	}
	return "flat-vector"
}
