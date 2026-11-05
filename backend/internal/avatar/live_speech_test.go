package avatar

import (
	"context"
	"os"
	"testing"
)

func TestLiveDoubaoSpeech(t *testing.T) {
	if os.Getenv("TTS_LIVE") != "1" {
		t.Skip()
	}
	client := NewRunningHub(os.Getenv("VIDEO_API_BASE_URL"), os.Getenv("VIDEO_API_KEY"))
	audio, chars, err := client.Synthesize(context.Background(), SpeechRequest{
		Model: doubaoTTSModel, Speaker: os.Getenv("TTS_SPEAKER"), Language: "zh-cn", Text: "你好。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(audio) < 16 || chars != 3 {
		t.Fatalf("audio=%d chars=%d", len(audio), chars)
	}
}
