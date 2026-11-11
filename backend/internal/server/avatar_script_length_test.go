package server

import (
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

// The script writer used to ignore the requested length ("不限制整片时长"), so a
// "two-minute" request produced ~1000 characters, about four minutes at the
// brisk native-speech pace. The prompt must carry a character budget derived
// from the requested duration.
func TestScriptLengthBudgetFollowsRequestedDuration(t *testing.T) {
	cases := []struct {
		req      avatar.RequirementsAnalysis
		min, max int
	}{
		{avatar.RequirementsAnalysis{Constraints: []string{"时长约 2 分钟", "画幅 16:9"}}, 400, 560},
		{avatar.RequirementsAnalysis{Constraints: []string{"控制在 90 秒左右"}}, 300, 420},
		{avatar.RequirementsAnalysis{Constraints: []string{"30秒"}}, 100, 140},
		{avatar.RequirementsAnalysis{Constraints: []string{"1分30秒"}}, 300, 420},
	}
	for _, c := range cases {
		lo, hi, ok := scriptCharBudget(&c.req, "")
		if !ok || lo < c.min || hi > c.max || lo >= hi {
			t.Fatalf("%v -> %d-%d ok=%v", c.req.Constraints, lo, hi, ok)
		}
	}
	if _, _, ok := scriptCharBudget(&avatar.RequirementsAnalysis{Constraints: []string{"画幅 16:9"}}, "讲清楚即可"); ok {
		t.Fatal("budget invented without a requested duration")
	}
	// The topic alone may carry the duration.
	if _, _, ok := scriptCharBudget(nil, "做一期两分钟左右的解读"); !ok {
		t.Fatal("Chinese numeral duration in topic not recognised")
	}
	if !strings.Contains(scriptLengthRule(&cases[0].req, ""), "字") {
		t.Fatal("length rule missing from prompt text")
	}
}
