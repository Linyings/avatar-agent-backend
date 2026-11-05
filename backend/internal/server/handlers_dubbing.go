package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/providers"
)

func vendorASRServiceKind(vendor string) string {
	if vendor == "paraformer" {
		return "dubbing_asr"
	}
	return "asr"
}

func (a *App) dubbingProvider() providers.DubbingClient {
	if a.dubbing != nil {
		return *a.dubbing
	}
	return providers.NewDubbingClient(a.cfg)
}

func (a *App) dubbingCapabilities(c *gin.Context) {
	voices, err := a.enabledQwenVoices()
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取配音音色设置失败")
		return
	}
	separation, sepOK, free := a.modeBillingRate("dubbing", "aliyun-mps", "audio_separation", "MusicDemix")
	asr, asrOK, _ := a.modeBillingRate("dubbing", "dashscope", "dubbing_asr", a.cfg.DubbingASRModel)
	serviceReady := providers.NewDubbingClient(a.cfg).Configured() && a.dashscope.Configured() && a.dashscope.DubbingTTSModel() != ""
	billingReady := free || (sepOK && asrOK && separation.CreditsPerMinute > 0 && asr.CreditsPerMinute > 0)
	ok(c, gin.H{"enabled": serviceReady && billingReady, "service_ready": serviceReady, "billing_ready": billingReady, "free_experience": free,
		"asr_model": a.cfg.DubbingASRModel, "separation_model": "MusicDemix", "max_window_seconds": 300,
		"tts_model": a.dashscope.DubbingTTSModel(), "tts_voices": voices,
		"separation_credits_per_minute": separation.CreditsPerMinute, "asr_credits_per_minute": asr.CreditsPerMinute,
		"message": "按 ASR 时长、人声分离区间时长及配音用量分别计费；每次操作向上取整到积分"})
}

func (a *App) submitDubbingSeparation(c *gin.Context) {
	client := a.dubbingProvider()
	if !client.Configured() {
		fail(c, 503, "搞笑配音人声分离服务未配置")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 42<<20)
	var payload map[string]any
	if !bindJSON(c, &payload) {
		return
	}
	identity, valid := extractJSONWorkflowIdentity(c, payload)
	if !valid {
		return
	}
	if identity == nil {
		fail(c, 400, "缺少工作流标识")
		return
	}
	if !strings.HasPrefix(identity.WorkflowStepID, "dubbing-separation-") {
		fail(c, 400, "人声分离步骤标识无效")
		return
	}
	encoded, _ := payload["audio_base64"].(string)
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		fail(c, 400, "音频编码无效")
		return
	}
	duration, err := providers.DubbingWAVDuration(audio)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	digest := sha256.Sum256(audio)
	fingerprint := workflowInputFingerprint(gin.H{"audio_sha256": hex.EncodeToString(digest[:]), "model": "MusicDemix", "bucket": a.cfg.DubbingOSSBucket})
	user := getUser(c)
	if _, taskID, found, err := a.findWorkflowProviderSubmissionReplay(c.Request.Context(), user.ID, nil, identity, fingerprint); err != nil {
		respondWorkflowRequestError(c, err)
		return
	} else if found {
		ok(c, gin.H{"task_id": taskID, "status": "PENDING"})
		return
	}
	execution, valid := a.resolveJSONWorkflowExecutionForProject(c, user.ID, nil, identity)
	if !valid {
		return
	}
	consumption := creditConsumption{UserID: user.ID, Provider: "aliyun-mps", ServiceKind: "audio_separation", ModelID: "MusicDemix", APIEndpoint: "/api/v1/tools/dubbing/separation/query", DurationSeconds: &duration, RequestMetadata: gin.H{workflowInputFingerprintMetadataKey: fingerprint}}
	if !a.checkJSONWorkflowCreditBalance(c, user.ID, consumption, execution) {
		return
	}
	claim, err := a.claimWorkflowProviderSubmission(c.Request.Context(), execution, fingerprint)
	if err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	if claim.Replayed {
		ok(c, gin.H{"task_id": claim.ProviderTaskID, "status": "PENDING"})
		return
	}
	key := "dubbing/" + user.ID.String() + "/" + fingerprint + "/input.wav"
	taskID, err := client.Submit(c.Request.Context(), key, audio)
	if err != nil {
		if errors.Is(err, providers.ErrDubbingUpload) {
			a.respondASRWorkflowSubmissionFailure(c, claim.Execution, claim.ClaimID, err)
			return
		}
		// Retain the durable claim on an ambiguous network failure: never blindly repeat a paid job.
		fail(c, 502, err.Error())
		return
	}
	if _, err = a.persistWorkflowProviderSubmission(c.Request.Context(), claim.Execution, claim.ClaimID, taskID, fingerprint); err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	ok(c, gin.H{"task_id": taskID, "status": "PENDING", "duration_seconds": duration})
}

func parseDubbingResult(result map[string]any, expectedPrefix string) (float64, string, error) {
	var details struct {
		Code     string
		Duration float64
		Data     string
	}
	raw, _ := result["Result"].(string)
	if json.Unmarshal([]byte(raw), &details) != nil || details.Code != "Success" || math.IsNaN(details.Duration) || math.IsInf(details.Duration, 0) || details.Duration <= 0 || details.Duration > 301 {
		return 0, "", errors.New("人声分离结果缺少有效时长")
	}
	var data struct {
		Result []struct {
			File string `json:"file"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(details.Data), &data) != nil {
		return 0, "", errors.New("人声分离结果缺少音频")
	}
	for _, file := range data.Result {
		if file.File == expectedPrefix+"output-accompaniment.wav" {
			return details.Duration, file.File, nil
		}
	}
	return 0, "", errors.New("人声分离返回的伴奏路径不属于当前任务")
}

func (a *App) queryDubbingSeparation(c *gin.Context) {
	user := getUser(c)
	taskID := c.Param("task_id")
	identity, err := extractWorkflowRequestIdentityFromValues(c.Request.URL.Query())
	if err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	if identity == nil {
		fail(c, 400, "缺少工作流标识")
		return
	}
	execution, err := a.resolveWorkflowProviderQuery(c.Request.Context(), user.ID, nil, identity, taskID, "")
	if err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	fingerprint := valueOrEmpty(execution.Step.ProviderInputHash)
	if len(fingerprint) != 64 {
		fail(c, 409, "任务缺少音频指纹")
		return
	}
	client := a.dubbingProvider()
	if !client.Configured() {
		fail(c, 503, "人声分离服务未配置")
		return
	}
	result, err := client.Query(c.Request.Context(), taskID)
	if err != nil {
		fail(c, 502, err.Error())
		return
	}
	state, _ := result["State"].(string)
	switch state {
	case "Queuing", "Analysing":
		ok(c, gin.H{"status": "RUNNING", "task_id": taskID})
		return
	case "Fail":
		a.respondJSONWorkflowProviderFailure(c, execution, errors.New("人声分离失败"))
		return
	case "Success":
	default:
		fail(c, 502, "未知的人声分离任务状态")
		return
	}
	prefix := "dubbing/" + user.ID.String() + "/" + fingerprint + "/"
	if !strings.HasSuffix(fmt.Sprint(result["Input"]), "/"+prefix+"input.wav") || result["FunctionName"] != "MusicDemix" {
		fail(c, 502, "人声分离任务不匹配")
		return
	}
	duration, key, err := parseDubbingResult(result, prefix)
	if err != nil {
		fail(c, 502, err.Error())
		return
	}
	consumption := creditConsumption{UserID: user.ID, Provider: "aliyun-mps", ServiceKind: "audio_separation", ModelID: "MusicDemix", APIEndpoint: "/api/v1/tools/dubbing/separation/query", DurationSeconds: &duration, RequestMetadata: gin.H{workflowInputFingerprintMetadataKey: fingerprint, "task_id": taskID}}
	if err = a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{ProviderRequestID: &taskID, Consumption: consumption}); err != nil {
		a.respondASRWorkflowFinalizationError(c, execution, err)
		return
	}
	ok(c, gin.H{"status": "SUCCEEDED", "task_id": taskID, "duration_seconds": duration, "accompaniment_url": client.DownloadURL(key)})
}
