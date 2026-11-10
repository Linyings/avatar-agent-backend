package avatar

import (
	"strings"
	"testing"
)

func TestTalkingDirectionCannotOverridePoseGazeOrFraming(t *testing.T) {
	p := TalkingPrompt("保持黑色衣服。身体与面部略朝画面右侧。自然看向镜头附近。面部略朝右。头肩中近景。16:9。turn head right; look away; close-up。柔和自然光")
	for _, bad := range []string{"略朝", "镜头附近", "中近景", "16:9", "turn head", "look away", "close-up"} {
		if strings.Contains(p, bad) {
			t.Fatalf("conflicting direction survived: %s", p)
		}
	}
	for _, good := range []string{"黑色衣服", "柔和自然光", "直视镜头", "固定焦距"} {
		if !strings.Contains(p, good) {
			t.Fatalf("missing %s: %s", good, p)
		}
	}
}
