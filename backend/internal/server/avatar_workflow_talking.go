package server

import (
	"os"

	"github.com/narratoai/backend-go/internal/avatar"
)

// talkingClient returns a RunningHub client bound to the configured video
// account and site. Portrait preparation still uses it to upload the source
// photo and order the frontal reference image.
func (w *avatarWorkflowService) talkingClient() *avatar.RunningHub {
	o := w.app.avatarMVP.options.VideoDefaults()
	if os.Getenv("RUNNINGHUB_BASE_URL") == "" && o.Protocol == "runninghub" && o.BaseURL != "" && o.APIKey != "" {
		return avatar.NewRunningHub(o.BaseURL, o.APIKey)
	}
	return w.speechClient()
}

// avatarFootageConfig records which model produces the person footage. It is an
// audit key: changing it never restarts a run or invalidates cached plans.
func (w *avatarWorkflowService) avatarFootageConfig() string {
	o := w.app.avatarMVP.options.VideoDefaults()
	if o.Model == avatar.NativeSpeechModel && o.GenerationPath == avatar.SeedancePath {
		return "native:" + avatar.NativeSpeechModel
	}
	return "video:" + o.Model
}
