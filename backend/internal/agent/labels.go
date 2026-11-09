// labels.go — 面向用户的文案标识辅助
package agent

import (
	"strconv"
	"strings"
)

// segmentLabel 把段落 ID 转成用户可读的称呼。
// avatar.WorkflowState 的段落 ID 形如 "p1"/"p2"，直接展示会露出内部编号，
// 因此统一渲染为「第 N 段」；不符合该格式的自定义 ID 原样返回。
func segmentLabel(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "该段"
	}
	// 取 ID 结尾的连续数字作为段序
	digits := trimmed
	if i := strings.IndexFunc(trimmed, func(r rune) bool { return r >= '0' && r <= '9' }); i > 0 {
		digits = trimmed[i:]
	}
	if n, err := strconv.Atoi(digits); err == nil && n > 0 {
		return "第 " + strconv.Itoa(n) + " 段"
	}
	return "段落 " + trimmed
}
