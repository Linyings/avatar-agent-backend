package server

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type dubbingTestTransport func(*http.Request) (*http.Response, error)

func (fn dubbingTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestDubbingHandlersPersistJobAndPreventDuplicateCharges(t *testing.T) {
	app, user, run, _ := newWorkflowRunBillingFixture(t, "dubbing-handler@example.com", nil)
	app.cfg.DubbingAccessKeyID = "test"
	app.cfg.DubbingAccessKeySecret = "test"
	app.cfg.DubbingOSSBucket = "test-bucket"
	app.cfg.DubbingSeparationCreditsPerMinute = 20
	client := providers.NewDubbingClient(app.cfg)
	submissions := 0
	uploadFailure := true
	prefix := ""
	client.HTTP = &http.Client{Transport: dubbingTestTransport(func(r *http.Request) (*http.Response, error) {
		response := map[string]any{}
		if r.Method == "PUT" {
			if uploadFailure {
				uploadFailure = false
				return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("upload failed")), Header: make(http.Header)}, nil
			}
			prefix = strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "input.wav"), "/")
		} else {
			r.ParseForm()
			if r.Form.Get("Action") == "SubmitIProductionJob" {
				submissions++
				response["JobId"] = "abcdef1234567890abcdef1234567890"
			} else {
				data, _ := json.Marshal(map[string]any{"result": []map[string]string{{"file": prefix + "output-accompaniment.wav"}}})
				details, _ := json.Marshal(map[string]any{"Code": "Success", "Duration": 3, "Data": string(data)})
				response = map[string]any{"State": "Success", "FunctionName": "MusicDemix", "Input": "oss://test-bucket.oss-cn-shanghai.aliyuncs.com/" + prefix + "input.wav", "Result": string(details)}
			}
		}
		raw, _ := json.Marshal(response)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}, nil
	})}
	app.dubbing = &client
	wav := make([]byte, 44+96000)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 96000)
	identity := url.Values{"workflow_run_id": {run.ID.String()}, "workflow_step_id": {"dubbing-separation-test"}, "workflow_input_hash": {run.InputHash}, "client_instance_id": {"billing-client"}}
	payload := map[string]any{"audio_base64": base64.StdEncoding.EncodeToString(wav)}
	for key, values := range identity {
		payload[key] = values[0]
	}
	call := func(query bool, user models.User) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("user", user)
		if query {
			c.Request = httptest.NewRequest("GET", "/?"+identity.Encode(), nil)
			c.Params = gin.Params{{Key: "task_id", Value: "abcdef1234567890abcdef1234567890"}}
			app.queryDubbingSeparation(c)
		} else {
			raw, _ := json.Marshal(payload)
			c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(raw))
			c.Request.Header.Set("Content-Type", "application/json")
			app.submitDubbingSeparation(c)
		}
		return recorder
	}
	if response := call(false, user); response.Code != 502 {
		t.Fatal("expected retriable upload failure", response.Code)
	}
	for i := 0; i < 2; i++ {
		if response := call(false, user); response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if submissions != 1 {
		t.Fatal("duplicate provider submit", submissions)
	}
	for i := 0; i < 2; i++ {
		if response := call(true, user); response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	var count int64
	app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", run.ID).Count(&count)
	if count != 1 {
		t.Fatal("duplicate reservation", count)
	}
	other := user
	other.ID = [16]byte{1}
	if response := call(true, other); response.Code == 200 {
		t.Fatal("cross-user task lookup succeeded")
	}
}
