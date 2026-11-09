// Review reproductions. Copy into backend/internal/agent to run; assertions
// describe the required safe behavior and intentionally fail on the reviewed tree.
package agent

import (
 "context"
 "encoding/json"
 "net/http/httptest"
 "sync/atomic"
 "testing"

 "github.com/gin-gonic/gin"
 "github.com/google/uuid"
)

type reviewMessageStore struct{ loads int }
func (s *reviewMessageStore) Load(context.Context, string) ([]ChatMessage,error) {
 s.loads++
 return []ChatMessage{{ID:"saved",Role:"assistant",Content:"已保存的聊天记录"}},nil
}
func (*reviewMessageStore) Append(context.Context,string,string,ChatMessage) error {return nil}

func TestAdversarialHistoryWithoutMemorySession(t *testing.T) {
 old:=globalDeps
 defer func(){globalDeps=old}()
 store:=&reviewMessageStore{}
 globalDeps=Deps{Messages:store,Authorize:func(context.Context,uuid.UUID,string)error{return nil}}
 r:=gin.New()
 r.GET("/history",GinHandleHistory)
 w:=httptest.NewRecorder()
 r.ServeHTTP(w,httptest.NewRequest("GET","/history?projectId="+uuid.NewString(),nil))
 var body struct{Messages []ChatMessage `json:"messages"`}
 if err:=json.Unmarshal(w.Body.Bytes(),&body);err!=nil{t.Fatal(err)}
 if w.Code!=200||store.loads!=1||len(body.Messages)!=1 {
  t.Fatalf("cold history: HTTP=%d store.Load=%d messages=%d; persisted history must be returned",w.Code,store.loads,len(body.Messages))
 }
}

type reviewPackageHost struct{ calls []string; requests []string; onInvoke func() }
func (h *reviewPackageHost) Invoke(ctx context.Context,owner,project,id string,args map[string]any,request string)(any,error){
 h.calls=append(h.calls,id)
 h.requests=append(h.requests,request)
 if h.onInvoke!=nil{h.onInvoke()}
 return map[string]any{"saved":true},nil
}
func (*reviewPackageHost) Confirm(context.Context,string,string,string,int)(any,error){return nil,nil}

func TestAdversarialAbortBeforeLLMReturns(t *testing.T){
 host:=&reviewPackageHost{}
 s:=NewSession(SessionConfig{ProjectID:uuid.NewString(),Packages:host})
 s.SetEmitter(func(any){})
 llm:=&fakeLLM{responses:[]string{`{"tool_calls":[{"id":"write-after-stop","name":"avatar_configure","arguments":{"title":"不应保存"}}]}`},onCall:func(int){s.Abort()}}
 s.config.LLM=llm
 out,err:=s.Chat(context.Background(),ChatInput{Message:"修改标题",TurnID:uuid.NewString(),RequestID:uuid.NewString()},&stubState{})
 if err!=nil{t.Fatal(err)}
 if len(host.calls)!=0{t.Fatalf("stopped turn still invoked %v; reply=%q",host.calls,out.Reply)}
}

func TestAdversarialAbortBetweenTools(t *testing.T){
 host:=&reviewPackageHost{}
 s:=NewSession(SessionConfig{ProjectID:uuid.NewString(),Packages:host})
 s.SetEmitter(func(any){})
 host.onInvoke=func(){s.Abort()}
 s.config.LLM=&fakeLLM{responses:[]string{`{"tool_calls":[{"id":"first","name":"avatar_inspect","arguments":{}},{"id":"second","name":"avatar_configure","arguments":{"title":"不应保存"}}]}`}}
 _,err:=s.Chat(context.Background(),ChatInput{Message:"读取再修改",TurnID:uuid.NewString(),RequestID:uuid.NewString()},&stubState{})
 if err!=nil{t.Fatal(err)}
 if len(host.calls)!=1{t.Fatalf("abort during first tool did not stop batch: %v",host.calls)}
}

type reviewConcurrentLLM struct{calls atomic.Int32;entered chan struct{};release chan struct{}}
func (l *reviewConcurrentLLM) Complete(context.Context,LLMRequest)(string,error){
 if l.calls.Add(1)==1{
  close(l.entered);<-l.release
  return `{"tool_calls":[{"id":"first-turn-tool","name":"avatar_configure","arguments":{"title":"first turn"}}]}`,nil
 }
 return `{"reply":"完成"}`,nil
}
func TestAdversarialConcurrentTurnIdentity(t *testing.T){
 llm:=&reviewConcurrentLLM{entered:make(chan struct{}),release:make(chan struct{})}
 host:=&reviewPackageHost{}
 s:=NewSession(SessionConfig{ProjectID:uuid.NewString(),Packages:host,LLM:llm})
 s.SetEmitter(func(any){})
 done:=make(chan error,1)
 go func(){_,err:=s.Chat(context.Background(),ChatInput{Message:"修改标题",TurnID:"turn-A",RequestID:"request-A"},&stubState{});done<-err}()
 <-llm.entered
 _,err:=s.Chat(context.Background(),ChatInput{Message:"另一标签页的消息",TurnID:"turn-B",RequestID:"request-B"},&stubState{})
 close(llm.release)
 firstErr:=<-done
 if err!=nil||firstErr!=nil{t.Fatalf("unexpected turn error: %v %v",err,firstErr)}
 if len(host.requests)!=1||host.requests[0]!="turn-A/avatar.configure"{
  t.Fatalf("first turn used another turn's package identity: %v",host.requests)
 }
}
