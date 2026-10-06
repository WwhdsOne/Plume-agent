package tui

import "regexp"

// 模型输出在进入渲染层前先剥离终端控制序列：模型若被诱导输出 ANSI/OSC，
// 不得借聊天窗口执行 OSC（改剪贴板/标题）、移动光标或隐藏文本
// （阶段计划 §2：模型输出过滤终端控制序列）。
var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
	ansiEsc = regexp.MustCompile(`\x1b[@-_]`)
	c0Ctrl  = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f]`)
)

// sanitize 清理模型输出的控制序列；换行保留用于正常分段。
func sanitize(text string) string {
	text = ansiCSI.ReplaceAllString(text, "")
	text = ansiOSC.ReplaceAllString(text, "")
	text = ansiEsc.ReplaceAllString(text, "")
	return c0Ctrl.ReplaceAllString(text, "")
}
