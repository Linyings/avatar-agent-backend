package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/media"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm/clause"
)

// Model lab: admin-only side-by-side comparison of RunningHub media models.
// Each selected model receives exactly one order; runs live outside projects.
const (
	labMaxModels   = 8
	labConcurrency = 4
)

type labRequest struct {
	Kind       string   `json:"kind"`
	Prompt     string   `json:"prompt"`
	Negative   string   `json:"negative_prompt,omitempty"`
	Lyrics     string   `json:"lyrics,omitempty"`
	Images     []string `json:"images,omitempty"`
	Video      string   `json:"video,omitempty"`
	Audio      string   `json:"audio,omitempty"` // talking-avatar driving voice
	Duration   int      `json:"duration,omitempty"`
	Ratio      string   `json:"ratio,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	Models     []string `json:"models"`
	// Confirm must equal the number of orders the caller agreed to pay for.
	Confirm     int  `json:"confirm"`
	holdForTest bool `json:"-"`
}

type labResult struct {
	Model    string         `json:"model"`
	Name     string         `json:"name"`
	Family   string         `json:"family"`
	Status   string         `json:"status"` // queued submitting running succeeded failed unknown
	TaskID   string         `json:"task_id,omitempty"`
	Sent     map[string]any `json:"sent,omitempty"`
	File     string         `json:"file,omitempty"`
	Media    string         `json:"media,omitempty"`
	Seconds  float64        `json:"seconds"`
	Cost     string         `json:"cost,omitempty"`
	Usage    any            `json:"usage,omitempty"`
	Error    string         `json:"error,omitempty"`
	Score    int            `json:"score,omitempty"`
	Note     string         `json:"note,omitempty"`
	Started  *time.Time     `json:"started_at,omitempty"`
	Finished *time.Time     `json:"finished_at,omitempty"`
}

type labRun struct {
	ID        string      `json:"id"`
	Owner     string      `json:"owner"`
	Kind      string      `json:"kind"`
	Request   labRequest  `json:"request"`
	Status    string      `json:"status"` // running done
	CreatedAt time.Time   `json:"created_at"`
	Results   []labResult `json:"results"`
}

type modelLabService struct {
	app          *App
	mu           sync.Mutex
	download     func(context.Context, string, string) error
	pollInterval time.Duration
	pollLimit    time.Duration
}

func newModelLab(a *App) *modelLabService {
	s := &modelLabService{app: a, pollInterval: 4 * time.Second, pollLimit: 30 * time.Minute}
	s.download = func(ctx context.Context, url, path string) error { return s.client().Download(ctx, url, path) }
	return s
}

func (s *modelLabService) root() string {
	return filepath.Join(s.app.avatarMVP.options.DataRoot, "workflow-v2", "model-lab")
}
func (s *modelLabService) dir(id string) string { return filepath.Join(s.root(), id) }

func (s *modelLabService) client() *avatar.RunningHub {
	return s.app.avatarWorkflow.speechClient()
}

func mediaClass(k media.Kind) string {
	switch k {
	case media.KindMusic, media.KindSFX:
		return "audio"
	case media.KindT2I, media.KindI2I:
		return "image"
	}
	return "video"
}

func (s *modelLabService) create(ctx context.Context, owner string, in labRequest) (labRun, error) {
	kind := media.Kind(strings.TrimSpace(in.Kind))
	if len(in.Models) == 0 || len(in.Models) > labMaxModels {
		return labRun{}, fmt.Errorf("每次对比选择 1–%d 个模型", labMaxModels)
	}
	if in.Confirm != len(in.Models) {
		return labRun{}, fmt.Errorf("请确认将提交 %d 个付费订单", len(in.Models))
	}
	if len([]rune(in.Prompt)) > 4000 || len([]rune(in.Lyrics)) > 4000 || len(in.Images) > 10 {
		return labRun{}, fmt.Errorf("输入过长")
	}
	for _, raw := range append(append([]string{}, in.Images...), in.Video, in.Audio) {
		if raw != "" && !strings.HasPrefix(raw, "https://") {
			return labRun{}, fmt.Errorf("参考素材必须是 https 地址")
		}
	}
	run := labRun{ID: "lab-" + uuid.NewString()[:13], Owner: owner, Kind: string(kind), Request: in, Status: "running", CreatedAt: time.Now().UTC()}
	seen := map[string]bool{}
	for _, id := range in.Models {
		m, ok := media.Find(id)
		if !ok || m.Kind != kind || seen[m.ID] {
			return labRun{}, fmt.Errorf("模型 %s 不属于所选类别或重复", id)
		}
		seen[m.ID] = true
		sent, err := m.Body(s.mediaRequest(in))
		if err != nil {
			return labRun{}, err
		}
		run.Results = append(run.Results, labResult{Model: m.ID, Name: m.Name, Family: m.Family, Status: "queued", Sent: sent})
	}
	if err := ctx.Err(); err != nil {
		return labRun{}, err
	}
	if err := s.save(run); err != nil {
		return labRun{}, err
	}
	if !in.holdForTest {
		go s.execute(run.ID)
	}
	return run, nil
}

func (s *modelLabService) mediaRequest(in labRequest) media.Request {
	return media.Request{Prompt: in.Prompt, Negative: in.Negative, Lyrics: in.Lyrics, Images: in.Images, Video: in.Video, Audio: in.Audio, Duration: in.Duration, Ratio: in.Ratio, Resolution: in.Resolution}
}

func (s *modelLabService) execute(id string) {
	run, err := s.get(id)
	if err != nil {
		return
	}
	sem := make(chan struct{}, labConcurrency)
	var wg sync.WaitGroup
	for i := range run.Results {
		r := run.Results[i]
		if r.Status == "succeeded" || r.Status == "failed" || r.Status == "unknown" {
			continue
		}
		wg.Add(1)
		go func(index int, r labResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.runOne(id, index, r)
		}(i, r)
	}
	wg.Wait()
	_ = s.update(id, func(run *labRun) { run.Status = "done" })
}

func (s *modelLabService) runOne(id string, index int, r labResult) {
	m, ok := media.Find(r.Model)
	if !ok {
		s.finish(id, index, "failed", "模型已不在注册表", nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.pollLimit+5*time.Minute)
	defer cancel()
	client := s.client()
	started := time.Now().UTC()
	if r.TaskID == "" {
		if r.Status == "submitting" {
			// The previous process may have created the order; never pay twice.
			s.finish(id, index, "unknown", "提交状态待确认，未重新提交", nil)
			return
		}
		_ = s.update(id, func(run *labRun) {
			run.Results[index].Status, run.Results[index].Started = "submitting", &started
		})
		res, err := client.SubmitMedia(ctx, m.Path, r.Sent)
		if err != nil {
			status := "failed"
			if avatar.SubmissionUncertain(err) {
				status = "unknown"
			}
			s.finish(id, index, status, err.Error(), nil)
			return
		}
		r.TaskID = res.TaskID
		_ = s.update(id, func(run *labRun) { run.Results[index].Status, run.Results[index].TaskID = "running", res.TaskID })
	}
	deadline := time.Now().Add(s.pollLimit)
	for {
		res, err := client.QueryMedia(ctx, r.TaskID)
		if err == nil {
			switch res.Status {
			case "SUCCESS":
				url := res.MediaURL(mediaClass(m.Kind))
				if url == "" {
					s.finish(id, index, "failed", "订单成功但没有可用的结果文件", &res)
					return
				}
				name := fmt.Sprintf("%02d-%s%s", index, strings.ReplaceAll(m.Family, "/", "-"), extOf(url, mediaClass(m.Kind)))
				if err := s.download(ctx, url, filepath.Join(s.dir(id), name)); err != nil {
					s.finish(id, index, "failed", "结果下载失败，原订单已保留："+err.Error(), &res)
					return
				}
				_ = s.update(id, func(run *labRun) { run.Results[index].File, run.Results[index].Media = name, mediaClass(m.Kind) })
				s.finish(id, index, "succeeded", "", &res)
				return
			case "FAILED", "FAILURE", "CANCELED", "CANCELLED":
				s.finish(id, index, "failed", "模型返回失败"+codeSuffix(res.ErrorCode), &res)
				return
			}
		}
		if time.Now().After(deadline) {
			s.finish(id, index, "failed", "等待超时，原订单已保留（任务 "+r.TaskID+"）", nil)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.pollInterval):
		}
	}
}

func codeSuffix(code string) string {
	if code == "" {
		return ""
	}
	return "（错误码 " + code + "）"
}

func extOf(url, class string) string {
	path := strings.ToLower(strings.Split(url, "?")[0])
	if i := strings.LastIndex(path, "."); i > 0 && len(path)-i <= 5 {
		return path[i:]
	}
	return map[string]string{"audio": ".mp3", "image": ".png", "video": ".mp4"}[class]
}

func (s *modelLabService) finish(id string, index int, status, message string, res *avatar.ProviderResult) {
	now := time.Now().UTC()
	_ = s.update(id, func(run *labRun) {
		r := &run.Results[index]
		r.Status, r.Error, r.Finished = status, message, &now
		if r.Started != nil {
			r.Seconds = now.Sub(*r.Started).Seconds()
		}
		if res != nil && len(res.Usage) > 0 {
			var usage map[string]any
			if json.Unmarshal(res.Usage, &usage) == nil {
				r.Usage = usage
				if v, ok := usage["thirdPartyConsumeMoney"].(string); ok {
					r.Cost = v
				}
			}
		}
	})
}

func (s *modelLabService) path(id string) (string, error) {
	if !strings.HasPrefix(id, "lab-") || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("实验不存在")
	}
	return filepath.Join(s.dir(id), "run.json"), nil
}

func (s *modelLabService) save(run labRun) error {
	p, err := s.path(run.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(run, "", " ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *modelLabService) get(id string) (labRun, error) {
	p, err := s.path(id)
	if err != nil {
		return labRun{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return labRun{}, fmt.Errorf("实验不存在")
	}
	var run labRun
	return run, json.Unmarshal(data, &run)
}

func (s *modelLabService) update(id string, fn func(*labRun)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.get(id)
	if err != nil {
		return err
	}
	fn(&run)
	return s.save(run)
}

func (s *modelLabService) rate(id, model string, score int, note string) error {
	if score < 0 || score > 5 || len([]rune(note)) > 500 {
		return fmt.Errorf("评分范围 0–5，备注不超过 500 字")
	}
	found := false
	err := s.update(id, func(run *labRun) {
		for i := range run.Results {
			if run.Results[i].Model == model {
				run.Results[i].Score, run.Results[i].Note, found = score, note, true
			}
		}
	})
	if err == nil && !found {
		return fmt.Errorf("该实验中没有这个模型")
	}
	return err
}

func (s *modelLabService) list() []labRun {
	entries, _ := os.ReadDir(s.root())
	out := []labRun{}
	for _, e := range entries {
		if run, err := s.get(e.Name()); err == nil {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// resume continues polling orders accepted before a restart.
func (s *modelLabService) resume() {
	for _, run := range s.list() {
		if run.Status == "running" {
			go s.execute(run.ID)
		}
	}
}

type labSummary struct {
	Model    string  `json:"model"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	Runs     int     `json:"runs"`
	Success  int     `json:"success"`
	AvgScore float64 `json:"avg_score"`
	AvgSec   float64 `json:"avg_seconds"`
	Cost     string  `json:"last_cost,omitempty"`
}

func (s *modelLabService) summary() []labSummary {
	agg := map[string]*labSummary{}
	scored := map[string]int{}
	for _, run := range s.list() {
		for _, r := range run.Results {
			a := agg[r.Model]
			if a == nil {
				a = &labSummary{Model: r.Model, Name: r.Name, Kind: run.Kind}
				agg[r.Model] = a
			}
			a.Runs++
			if r.Status == "succeeded" {
				a.Success++
				a.AvgSec += r.Seconds
				if r.Cost != "" {
					a.Cost = r.Cost
				}
			}
			if r.Score > 0 {
				a.AvgScore += float64(r.Score)
				scored[r.Model]++
			}
		}
	}
	out := []labSummary{}
	for id, a := range agg {
		if a.Success > 0 {
			a.AvgSec /= float64(a.Success)
		}
		if scored[id] > 0 {
			a.AvgScore /= float64(scored[id])
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AvgScore > out[j].AvgScore || out[i].AvgScore == out[j].AvgScore && out[i].Model < out[j].Model
	})
	return out
}

// ---- HTTP ----

type catalogModel struct {
	media.Model
	Capabilities media.Capabilities `json:"capabilities"`
}

func (a *App) mediaModels(c *gin.Context) {
	kind := media.Kind(c.Query("kind"))
	out := []catalogModel{}
	for _, m := range media.Models() {
		if kind != "" && m.Kind != kind {
			continue
		}
		m.Fields = nil
		out = append(out, catalogModel{Model: m, Capabilities: m.Capabilities()})
	}
	ok(c, gin.H{"models": out, "defaults": a.mediaDefaults()})
}

func (a *App) labList(c *gin.Context) {
	ok(c, gin.H{"runs": a.modelLab.list(), "summary": a.modelLab.summary()})
}

func (a *App) labCreate(c *gin.Context) {
	var in labRequest
	if !bindJSON(c, &in) {
		return
	}
	run, err := a.modelLab.create(c.Request.Context(), getUser(c).ID.String(), in)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	ok(c, run)
}

func (a *App) labGet(c *gin.Context) {
	run, err := a.modelLab.get(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, err.Error())
		return
	}
	ok(c, run)
}

func (a *App) labRate(c *gin.Context) {
	var in struct {
		Model string `json:"model"`
		Score int    `json:"score"`
		Note  string `json:"note"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if err := a.modelLab.rate(c.Param("id"), in.Model, in.Score, in.Note); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	ok(c, gin.H{"saved": true})
}

func (a *App) labFile(c *gin.Context) {
	run, err := a.modelLab.get(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, err.Error())
		return
	}
	name := c.Param("file")
	for _, r := range run.Results {
		if r.File != "" && r.File == name {
			c.Header("Cache-Control", "private, max-age=3600")
			c.Header("X-Content-Type-Options", "nosniff")
			c.File(filepath.Join(a.modelLab.dir(run.ID), r.File))
			return
		}
	}
	fail(c, http.StatusNotFound, "结果文件不存在")
}

// Defaults chosen in the lab are stored server-side and used by new projects.
const mediaDefaultsKey = "media_default_models"

func (a *App) mediaDefaults() map[string]string {
	out := map[string]string{}
	var setting models.SystemSetting
	if a.db.Where("key = ?", mediaDefaultsKey).First(&setting).Error == nil {
		_ = json.Unmarshal(setting.Value, &out)
	}
	return out
}

func (a *App) saveMediaDefaults(c *gin.Context, defaults map[string]string) error {
	user := getUser(c)
	setting := models.SystemSetting{Key: mediaDefaultsKey, Value: jsonMap(defaults), UpdatedBy: &user.ID}
	return a.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_by", "updated_at"})}).Create(&setting).Error
}

func (a *App) labSetDefault(c *gin.Context) {
	var in struct {
		Kind  string `json:"kind"`
		Model string `json:"model"`
	}
	if !bindJSON(c, &in) {
		return
	}
	m, found := media.Find(in.Model)
	if in.Model != "" && (!found || string(m.Kind) != in.Kind) {
		fail(c, http.StatusBadRequest, "模型与类别不匹配")
		return
	}
	defaults := a.mediaDefaults()
	if in.Model == "" {
		delete(defaults, in.Kind)
	} else {
		defaults[in.Kind] = m.ID
	}
	if err := a.saveMediaDefaults(c, defaults); err != nil {
		fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	ok(c, gin.H{"defaults": defaults})
}

func (a *App) registerModelLab(admin *gin.RouterGroup, avatarRoutes *gin.RouterGroup) {
	avatarRoutes.GET("/media/models", a.mediaModels)
	admin.GET("/model-lab/runs", a.labList)
	admin.POST("/model-lab/runs", a.labCreate)
	admin.GET("/model-lab/runs/:id", a.labGet)
	admin.POST("/model-lab/runs/:id/rate", a.labRate)
	admin.GET("/model-lab/runs/:id/files/:file", a.labFile)
	admin.PUT("/model-lab/defaults", a.labSetDefault)
}
