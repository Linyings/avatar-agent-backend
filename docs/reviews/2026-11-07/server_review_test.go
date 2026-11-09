// Isolated adversarial review. No production configuration or paid providers.
package server

import (
 "context"
 "net/http"
 "net/http/httptest"
 "strings"
 "sync/atomic"
 "testing"
 "time"

 "github.com/gin-gonic/gin"
 "github.com/narratoai/backend-go/internal/avatar"
 "github.com/narratoai/backend-go/internal/models"
)

func reviewTextConfig(a *App){
 a.cfg.OpenAIFlashAPIKey="test"
 a.cfg.OpenAIFlashModel="test"
 a.cfg.OpenAIFlashAPIBaseURL="http://127.0.0.1:1"
}

func TestAdversarialUnknownOrderAfterConfigure(t *testing.T){
 a,owner,p,s:=workflowFixture(t)
 pool,_:=a.db.DB();pool.SetMaxOpenConns(1)
 var submissions atomic.Int32
 provider:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  submissions.Add(1)
  w.WriteHeader(400)
  _,_=w.Write([]byte(`{"error":"isolated review: submission intercepted"}`))
 }))
 defer provider.Close()
 t.Setenv("RUNNINGHUB_BASE_URL",provider.URL)
 t.Setenv("RUNNINGHUB_API_KEY","test")
 a.cfg.TTSModel="doubao-seed-tts-2.0"
 reviewTextConfig(a)
 s.Status="submission_unknown"
 s.ScriptConfirmed=true
 s.Segments=[]avatar.Segment{{ID:"p1",Text:s.Input.Script}}
 s.Steps=[]avatar.WorkflowStep{{ID:"F04/p1",Status:"failed",Submission:"unknown",CostStatus:"unknown"}}
 if err:=a.avatarWorkflow.save(&s);err!=nil{t.Fatal(err)}
 ctx:=context.Background();h:=avatarPackageHost{a}
 if _,err:=h.Invoke(ctx,owner.String(),p.ID.String(),"avatar.voice",nil,"before-save");err==nil{t.Fatal("fixture: expected unknown order block before configure")}
 // Even an empty configuration patch increments revision without settling orders.
 if _,err:=h.Invoke(ctx,owner.String(),p.ID.String(),"avatar.configure",map[string]any{},"noop-save");err!=nil{t.Fatal(err)}
 result,err:=h.Invoke(ctx,owner.String(),p.ID.String(),"avatar.voice",nil,"after-save")
 if err!=nil{return}
 id:=result.(gin.H)["run_id"].(string)
 // Wait for the isolated worker to finish before its test database is closed.
 deadline:=time.Now().Add(5*time.Second)
 var next avatar.WorkflowState
 for time.Now().Before(deadline){
  next,err=a.avatarWorkflow.load(ctx,owner,id)
  if err!=nil{t.Fatal(err)}
  _,active:=workflowLaunchLocks.Load(id)
  if !active{break}
  time.Sleep(10*time.Millisecond)
 }
 t.Fatalf("unresolved order bypassed: new_run=%t new_revision=%d status=%s provider_requests=%d; error=%s",id!=s.ID,next.Input.Revision,next.Status,submissions.Load(),next.Error)
}

func TestAdversarialRenameThenSaveSettings(t *testing.T){
 a,owner,p,s:=workflowFixture(t)
 in:=s.Input;in.Title="旧名称"
 if err:=a.db.Model(&p).Updates(map[string]any{"name":in.Title,"project_metadata":jsonMap(map[string]any{"kind":"avatar_mvp","avatar_v2":in})}).Error;err!=nil{t.Fatal(err)}
 r:=gin.New()
 var user models.User;user.ID=owner
 r.Use(func(c *gin.Context){c.Set("user",user);c.Next()})
 r.PUT("/p/:project_id",a.updateProject)
 w:=httptest.NewRecorder()
 req:=httptest.NewRequest("PUT","/p/"+p.ID.String(),strings.NewReader(`{"name":"新名称"}`))
 req.Header.Set("Content-Type","application/json")
 r.ServeHTTP(w,req)
 if w.Code!=200{t.Fatalf("rename HTTP %d: %s",w.Code,w.Body.String())}
 h:=avatarPackageHost{a};ctx:=context.Background()
 renamed,draft,err:=h.project(ctx,owner.String(),p.ID.String());if err!=nil{t.Fatal(err)}
 if renamed.Name!="新名称"{t.Fatal("rename fixture did not apply")}
 _,err=h.Invoke(ctx,owner.String(),p.ID.String(),"avatar.configure",map[string]any{"title":draft.Title,"ratio":"16:9"},"save-settings")
 if err!=nil{t.Fatal(err)}
 after,_,_:=h.project(ctx,owner.String(),p.ID.String())
 if after.Name!="新名称"{t.Fatalf("rename reverted when saving ratio: draft title=%q final name=%q",draft.Title,after.Name)}
}

func TestAdversarialScriptProposalWithoutVideoPrerequisites(t *testing.T){
 a,owner,p,s:=workflowFixture(t)
 reviewTextConfig(a)
 a.cfg.DashScopeAPIKey="test";a.cfg.QwenTTSModel="test"
 in:=s.Input;in.UseAvatar=true
 if err:=a.db.Model(&p).Update("project_metadata",jsonMap(map[string]any{"kind":"avatar_mvp","avatar_v2":in})).Error;err!=nil{t.Fatal(err)}
 s.Input=in;s.Status="awaiting_confirmation";s.Confirmation="script"
 s.Segments=[]avatar.Segment{{ID:"p1",Text:in.Script}}
 if err:=a.avatarWorkflow.save(&s);err!=nil{t.Fatal(err)}
 h:=avatarPackageHost{a};ctx:=context.Background()
 if err:=a.avatarWorkflow.validatePackageInput(&in,"F03");err!=nil{t.Fatal(err)}
 _,err:=h.Invoke(ctx,owner.String(),p.ID.String(),"avatar.edit-script",map[string]any{"changes":"修改开头","segments":[]any{map[string]any{"id":"p1","text":"新开头。第二句。"}}},"edit-script")
 if err!=nil{t.Fatal(err)}
 stored,_,_:=h.project(ctx,owner.String(),p.ID.String());proposal:=proposalOf(stored)
 _,err=h.Confirm(ctx,owner.String(),p.ID.String(),proposal.ID,proposal.Revision)
 if err!=nil{t.Fatalf("script-only edit blocked by unrelated video prerequisite: %v",err)}
}

