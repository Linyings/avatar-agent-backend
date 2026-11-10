package agent

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/narratoai/backend-go/internal/agentpackages"
)

// Ship the reviewed project copy inside the API binary. Reads never depend on
// a developer's home directory, container working directory or writable media.
//
//go:embed skills
var skillDocuments embed.FS

type SkillDefinition struct {
	Name                    string   `json:"name"`
	Title                   string   `json:"title"`
	Description             string   `json:"description"`
	DefaultEnabled          bool     `json:"default_enabled"`
	AllowImplicitInvocation bool     `json:"allow_implicit_invocation"`
	Resources               []string `json:"resources"`
}

type SkillDocument struct {
	Name      string   `json:"name"`
	Resource  string   `json:"resource"`
	Content   string   `json:"content"`
	Resources []string `json:"resources"`
}

// Return fresh descriptors so callers cannot mutate the process-wide catalog.
func DefaultSkills() []SkillDefinition {
	return []SkillDefinition{{
		Name: "talking-head-storyboard", Title: "口播与图解视频分镜设计",
		Description:    "分析参考视频、设计或审查口播与数字人分镜：按讲解目标切分场景，安排人物全屏/小窗/隐藏、图层构图、语义动效、时间对齐及转场。纯配音、只生成口播素材、格式转换和普通状态查询不适用。",
		DefaultEnabled: true, AllowImplicitInvocation: true,
		Resources: []string{"references/scene-design.md", "references/storyboard-format.md", "references/reference-cases.md", "references/project-integration.md"},
	}, {
		Name: "portrait-realism", Title: "正脸讲解与面部真实感",
		Description:    "准备正脸参考图、生成写实口播人物，保持身份、真实皮肤细节、直视镜头和跨帧稳定性。F06 后端固定注入。",
		DefaultEnabled: true, AllowImplicitInvocation: true, Resources: []string{},
	}}
}

func ReadSkill(name, resource string) (SkillDocument, error) {
	if resource == "" {
		resource = "SKILL.md"
	}
	for _, skill := range DefaultSkills() {
		if skill.Name != name || !skill.DefaultEnabled {
			continue
		}
		if resource != "SKILL.md" && !slices.Contains(skill.Resources, resource) {
			return SkillDocument{}, fmt.Errorf("技能资源不存在；使用 SKILL.md 或目录列出的相对资源名")
		}
		data, err := skillDocuments.ReadFile("skills/" + name + "/" + resource)
		if err != nil {
			return SkillDocument{}, fmt.Errorf("技能资源暂不可用")
		}
		if len(data) > 24*1024 {
			return SkillDocument{}, fmt.Errorf("技能资源过大，需要拆分参考文件后读取")
		}
		return SkillDocument{Name: name, Resource: resource, Content: string(data), Resources: skill.Resources}, nil
	}
	return SkillDocument{}, fmt.Errorf("技能不存在或未启用")
}

var skillDigestFiles = func(name string) ([][]byte, error) {
	for _, skill := range DefaultSkills() {
		if skill.Name != name {
			continue
		}
		files := [][]byte{}
		for _, resource := range append([]string{"SKILL.md"}, skill.Resources...) {
			data, err := skillDocuments.ReadFile("skills/" + name + "/" + resource)
			if err != nil {
				return nil, err
			}
			files = append(files, []byte(resource), data)
		}
		return files, nil
	}
	return nil, fmt.Errorf("技能不存在")
}

// SkillsDigest names the exact embedded skill text a run was produced with,
// e.g. "talking-head-storyboard@3f2a9c1b04de", so results stay attributable
// after the guidance changes.
func SkillsDigest() string {
	parts := []string{}
	for _, skill := range DefaultSkills() {
		if !skill.DefaultEnabled {
			continue
		}
		files, err := skillDigestFiles(skill.Name)
		if err != nil {
			parts = append(parts, skill.Name+"@unavailable")
			continue
		}
		h := sha256.New()
		for _, f := range files {
			_, _ = fmt.Fprintf(h, "%d:", len(f))
			_, _ = h.Write(f)
		}
		parts = append(parts, skill.Name+"@"+hex.EncodeToString(h.Sum(nil))[:12])
	}
	return strings.Join(parts, ",")
}

func defaultSkillGuidance() string {
	var b strings.Builder
	b.WriteString("## 默认可用技能\n以下技能默认可发现，由你根据本轮任务自行判断是否调用，不要求用户手动开启或指定名称。相关时先调用 read_skill 读取 SKILL.md，再按需读取其引用资源；不相关时直接处理原任务，不必调用。技能是方法指导，不覆盖用户要求、现有参数和工具契约；读取技能不代表已经修改项目或完成制作。只分析时不启动制作。长任务按场景分批，只读取需要的参考资料。\n")
	for _, skill := range DefaultSkills() {
		if skill.DefaultEnabled && skill.AllowImplicitInvocation {
			fmt.Fprintf(&b, "- %s（%s）：%s\n", skill.Name, skill.Title, skill.Description)
		}
	}
	return b.String()
}

func RegisterSkillTools(reg *ToolRegistry) {
	names := []any{}
	for _, skill := range DefaultSkills() {
		if skill.DefaultEnabled {
			names = append(names, skill.Name)
		}
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"name"},
		"properties": map[string]any{
			"name":     map[string]any{"type": "string", "enum": names, "description": "默认技能目录中的名称"},
			"resource": map[string]any{"type": "string", "description": "省略时读取 SKILL.md；参考文件使用上次返回的 resources 中的相对名称，每次只读取一个文件", "maxLength": float64(200)},
		},
	}
	reg.Register(&Tool{
		Name: "read_skill", Description: "按任务需要读取默认技能及参考模板，只读，不生成素材、不修改项目、不启动工作流。由 AI 判断是否适用；先读主文件，再按需读取参考。",
		Parameters: schema,
		Check: func(args map[string]any) *Issue {
			err := agentpackages.Validate(schema, args)
			if err == nil {
				name, _ := args["name"].(string)
				resource, _ := args["resource"].(string)
				_, err = ReadSkill(name, resource)
			}
			if err == nil {
				return nil
			}
			issue := NewIssue(IssueKindFormat, "read_skill", "invalid_skill_resource", err.Error(), true, ResolutionRepairContent)
			return &issue
		},
		Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			name, _ := args["name"].(string)
			resource, _ := args["resource"].(string)
			doc, err := ReadSkill(name, resource)
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(doc)
			return string(raw), err
		},
	})
}
