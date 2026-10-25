package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"plume-agent/internal/app"
	"plume-agent/internal/store"
)

// maxHistoryChars 是回填候选与输入框一致的字符上限；超限明确拒绝，
// 不静默裁剪（textarea 的 CharLimit 会截断，所以放回输入框前显式校验）。
const maxHistoryChars = 4096

// escapeHistoryInput 把已接受的消息正文转回可编辑输入：净化终端控制
// 序列后，与 submitInput 一样裁掉首尾空白，再给顶层 slash 补一个转义符。
// ParseInput 只识别整段输入的开头，行中与后续行的 slash 不参与指令解析，
// 不能多加转义而改变再次发送的正文；前导 Unicode 空白也不能遮住顶层
// slash。转义后超限返回 false，调用方保持原草稿与历史游标不变。
func escapeHistoryInput(text string) (string, bool) {
	cleaned := strings.TrimSpace(sanitize(text))
	if strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	if utf8.RuneCountInString(cleaned) > maxHistoryChars {
		return "", false
	}
	return cleaned, true
}

// RecoveryHints 生成恢复成功后的 system 提示行；recovery 非空时补充最后
// 未完成任务的状态与回填指引。这些展示行不进入模型历史。
func RecoveryHints(recovery *app.SessionRecovery, completedTurns int) []string {
	lines := []string{fmt.Sprintf("Session restored · %d completed turns", completedTurns)}
	if recovery != nil {
		lines = append(lines, fmt.Sprintf("Last task: %s. It was not resent.", recovery.LastStatus))
		if len(recovery.Tools) > 0 {
			// 新 run 可能再次执行工具；恢复只展示既有结果，不承诺回滚。
			lines = append(lines, "Check prior tool results before resending; changes are not rolled back.")
		}
		lines = append(lines, "Press ↑ to edit its input; Enter sends a new request.")
	}
	return lines
}

// RunTranscriptLines 把持久 run 记录与恢复信息转换为展示行；`-c` 预置
// 转录与 `/resume`/选择器切换共用本转换，保证工具摘要映射与"结果未确认"
// 标记一致。最后一条 run 携带 recovery 时使用合并后的检查点视图。
func RunTranscriptLines(runs []store.RunRecord, recovery *app.SessionRecovery) []TranscriptLine {
	lines := []TranscriptLine(nil)
	for index, run := range runs {
		view, tools := run, false
		if recovery != nil && index == len(runs)-1 && recovery.LastRunID == run.ID {
			view = store.RunRecord{ID: run.ID, Input: run.Input, Status: recovery.LastStatus,
				Reply: recovery.Reply, Reasoning: recovery.Reasoning}
			tools = true
		}
		lines = append(lines, TranscriptLine{Kind: TranscriptUser, Text: view.Input})
		if tools {
			for _, tool := range recovery.Tools {
				lines = append(lines, TranscriptLine{Kind: TranscriptTool, Text: toolLineText(tool.Name, tool.Status, tool.Summary, tool.Unconfirmed)})
			}
		} else {
			for _, tool := range app.RecoveryTools(run) {
				lines = append(lines, TranscriptLine{Kind: TranscriptTool, Text: toolLineText(tool.Name, tool.Status, tool.Summary, tool.Unconfirmed)})
			}
		}
		if view.Reply != "" || view.Reasoning != "" || view.Status != store.RunCompleted {
			terminal := ""
			if view.Status != store.RunCompleted {
				terminal = string(view.Status)
			}
			lines = append(lines, TranscriptLine{Kind: TranscriptAssistant, Text: view.Reply, Reasoning: view.Reasoning, Terminal: terminal})
		}
	}
	return lines
}

func toolLineText(name, status, summary string, unconfirmed bool) string {
	if unconfirmed {
		// 只有准备/运行记录而无完成结果：不能猜成未执行或已成功。
		status = "running · result unconfirmed"
	}
	text := name + " · " + status
	if summary != "" {
		text += " · " + summary
	}
	return text
}
