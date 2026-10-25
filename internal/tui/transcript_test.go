package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"plume-agent/internal/app"
	"plume-agent/internal/store"
)

// TestEscapeHistoryInput 守住回填转义顺序：净化控制序列与发送端空白 →
// 只还原顶层 slash 转义 → 4096 字符校验；正文内部与正常换行保持不变。
func TestEscapeHistoryInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		want  string
		valid bool
	}{
		// case: 字面 /exit 回填后必须仍是普通消息。
		{"literal exit", "/exit", "//exit", true},
		// case: 空格后的普通路径不补转义——Enter 重发保持原文不变形。
		{"plain path after space", "see /tmp/a.txt", "see /tmp/a.txt", true},
		// case: 行中指令字面量原本就是正文，回填不能增加 slash。
		{"command after space", "see /exit", "see /exit", true},
		// case: 后续行指令字面量也不参与顶层解析。
		{"command after newline", "first\n/exit", "first\n/exit", true},
		// case: 发送端会裁掉前导空白，路径仍须作为顶层 slash 字面量转义。
		{"path after spaces", "  /tmp/a.txt", "//tmp/a.txt", true},
		// case: 前导 Tab 与空格遵循同一发送规则。
		{"path after tab", "\t/tmp/a.txt", "//tmp/a.txt", true},
		// case: Unicode 首尾空白也会被发送端裁掉。
		{"path after unicode whitespace", "\u3000/tmp/a.txt\u00a0", "//tmp/a.txt", true},
		// case: 净化揭露的顶层指令同样转义，不得在再次发送时执行。
		{"sanitize reveals command", "\x1b[2K /exit", "//exit", true},
		// case: 多重 slash 逐次回填保持解析不变量。
		{"multi slash", "//x", "///x", true},
		// case: 非空白后的斜杠（路径分隔）不补。
		{"plain path", "src/pkg/main.go", "src/pkg/main.go", true},
		// case: 多行输入保留换行；非指令的行首斜杠不变形。
		{"multiline", "first\n/second", "first\n/second", true},
		// case: 净化剥离控制序列后，非指令斜杠原样保留。
		{"sanitize reveals slash", "a\x1b[2K /hidden", "a /hidden", true},
		// case: 控制序列被剥离，不进入输入框。
		{"sanitize strips ansi", "text\x1b]0;title\x07", "text", true},
		// case: 按 Unicode 字符计数，普通正文恰好 4096 字符可完整回填。
		{"unicode exact limit", strings.Repeat("字", 4096), strings.Repeat("字", 4096), true},
		// case: 顶层 slash 补转义后恰好 4096 字符仍可回填。
		{"escaped exact limit", "/" + strings.Repeat("字", 4094), "//" + strings.Repeat("字", 4094), true},
		// case: 转义后超限拒绝，不静默裁剪。
		{"over limit", strings.Repeat("/x", 2048), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := escapeHistoryInput(tc.text)
			if ok != tc.valid || (ok && got != tc.want) {
				t.Fatalf("escape(%q) = (%q,%v), want (%q,%v)", tc.text, got, ok, tc.want, tc.valid)
			}
		})
	}
}

// TestHistoryPrevEscapesAndDraftKept 守住 ↑ 回填转义、↓ 原样恢复草稿
// （不再转义一次）；超限时拒绝回填且草稿/游标不变。
func TestHistoryPrevEscapesAndDraftKept(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.input.SetValue("wip draft")
	m.history = []string{"/exit", "normal"}
	m, _ = m.Update(specialKey(tea.KeyUp))
	if m.input.Value() != "normal" || m.historyIdx != 1 || m.draft != "wip draft" {
		t.Fatalf("prev = %q idx=%d draft=%q", m.input.Value(), m.historyIdx, m.draft)
	}
	m, _ = m.Update(specialKey(tea.KeyUp))
	if m.input.Value() != "//exit" || m.historyIdx != 0 {
		t.Fatalf("prev older = %q idx=%d", m.input.Value(), m.historyIdx)
	}
	m, _ = m.Update(specialKey(tea.KeyDown))
	if m.input.Value() != "normal" {
		t.Fatalf("next = %q", m.input.Value())
	}
	m, _ = m.Update(specialKey(tea.KeyDown))
	if m.input.Value() != "wip draft" || m.historyIdx != -1 {
		t.Fatalf("draft restore = %q idx=%d", m.input.Value(), m.historyIdx)
	}
}

// TestHistoryPrevOverlimitRejected 守住超限候选不覆盖草稿或推进历史游标。
func TestHistoryPrevOverlimitRejected(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.input.SetValue("keep me")
	m.history = []string{strings.Repeat("/x", 2048)}
	m, _ = m.Update(specialKey(tea.KeyUp))
	if m.input.Value() != "keep me" || m.historyIdx != -1 {
		t.Fatalf("rejected backfill = %q idx=%d", m.input.Value(), m.historyIdx)
	}
}

// TestInProcessSlashEscapeRoundTrip 守住同进程 // 修复：Command 回执把解码
// 正文放入历史后，↑ 取回的输入再次解析仍是普通消息，Enter 不执行管理动作。
func TestInProcessSlashEscapeRoundTrip(t *testing.T) {
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", RunID: "run-1", SessionID: "sess-1"}
	}})
	m, _ = m.Update(typeText("//exit"))
	m, cmd := m.Update(specialKey(tea.KeyEnter))
	if cmd != nil {
		m, _ = m.Update(cmd())
	}
	if len(m.history) != 1 || m.history[0] != "/exit" {
		t.Fatalf("stored history = %#v", m.history)
	}
	m, _ = m.Update(specialKey(tea.KeyUp))
	if m.input.Value() != "//exit" {
		t.Fatalf("backfilled = %q, want //exit", m.input.Value())
	}
	parsed, err := app.ParseInput(m.input.Value())
	if err != nil || parsed.Kind != app.InputMessage || parsed.Text != "/exit" {
		t.Fatalf("parsed = %+v err=%v", parsed, err)
	}
}

// TestInitialResendCandidateNotAutoFilled 守住恢复候选：只进输入历史供 ↑
// 取回，不自动填入输入框、不发送。
func TestInitialResendCandidateNotAutoFilled(t *testing.T) {
	m := NewWithOptions("fake/offline", Hooks{}, Options{InitialResend: "/tmp/a.txt"})
	if m.input.Value() != "" {
		t.Fatalf("input auto-filled = %q", m.input.Value())
	}
	if len(m.history) != 1 || m.history[0] != "/tmp/a.txt" {
		t.Fatalf("history = %#v", m.history)
	}
	m, _ = m.Update(specialKey(tea.KeyUp))
	if m.input.Value() != "//tmp/a.txt" {
		t.Fatalf("prev = %q", m.input.Value())
	}
	// case: 超限或空候选不产生历史项。
	big := NewWithOptions("fake/offline", Hooks{}, Options{InitialResend: strings.Repeat("/x", 2048)})
	if len(big.history) != 0 {
		t.Fatalf("over-limit candidate = %#v", big.history)
	}
}

// TestReplaceSessionInjectsRecovery 守住 /resume 恢复：转录合并检查点视图、
// 未确认工具带标记、提示行进转录、候选进历史；重复恢复不重复候选。
func TestReplaceSessionInjectsRecovery(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.width, m.height = 80, 24
	snapshot := &app.SessionSnapshot{SessionID: "sess-r", Model: app.CommandModelInfo{Provider: "p", Model: "m"},
		Turns: 2, Resumed: true,
		Runs: []store.RunRecord{{ID: "r1", Input: "task", Status: store.RunInterrupted}},
		Recovery: &app.SessionRecovery{LastRunID: "r1", LastStatus: store.RunInterrupted, Reply: "half done",
			Tools:       []app.RecoveryToolView{{Name: "bash", Status: "running", Unconfirmed: true}, {Name: "read", Status: "completed", Summary: "file"}},
			ResendInput: "task"}}
	m.replaceSession(snapshot)
	toolSeen, hintSeen, toolWarningSeen := false, false, false
	for _, line := range m.lines {
		if line.kind == lineTool && strings.Contains(line.text, "result unconfirmed") {
			toolSeen = true
		}
		if line.kind == lineSystem && strings.Contains(line.text, "Last task: interrupted") {
			hintSeen = true
		}
		if line.kind == lineSystem && strings.Contains(line.text, "Check prior tool results before resending") {
			toolWarningSeen = true
		}
	}
	if !toolSeen || !hintSeen || !toolWarningSeen || m.input.Value() != "" {
		t.Fatalf("lines = %#v", m.lines)
	}
	if len(m.history) != 1 || m.history[0] != "task" {
		t.Fatalf("history = %#v", m.history)
	}
	m, _ = m.Update(specialKey(tea.KeyUp))
	if m.input.Value() != "task" {
		t.Fatalf("resend backfill = %q", m.input.Value())
	}
	// 再次恢复同一会话：候选不重复。
	m.replaceSession(snapshot)
	if len(m.history) != 1 {
		t.Fatalf("duplicated candidates = %#v", m.history)
	}
}

// TestRecoveryHintsWithoutToolsStayConcise 守住工具副作用提示的范围：
// 没有恢复工具记录时不增加警告；提示只生成展示行，不创建回填或发送动作。
func TestRecoveryHintsWithoutToolsStayConcise(t *testing.T) {
	for _, recovery := range []*app.SessionRecovery{nil, {LastStatus: store.RunInterrupted, ResendInput: "task"}} {
		// case: 无恢复任务和无工具记录的恢复任务都不虚构工具副作用。
		for _, hint := range RecoveryHints(recovery, 0) {
			if strings.Contains(hint, "Check prior tool results") {
				t.Fatalf("tool warning without tool record: %q", hint)
			}
		}
	}
}

// TestRunTranscriptLinesMapsToolsAndTerminal 守住共用转换：-c 与 /resume 的
// 转录都映射工具摘要与终态标记；未确认工具行独立标注。
func TestRunTranscriptLinesMapsToolsAndTerminal(t *testing.T) {
	runs := []store.RunRecord{
		{ID: "r1", Input: "q1", Reply: "a1", Status: store.RunCompleted, ToolEvents: []store.ToolEventRecord{{Name: "read", Status: "completed", Summary: "ok"}}},
		{ID: "r2", Input: "q2", Status: store.RunInterrupted},
	}
	lines := RunTranscriptLines(runs, nil)
	kinds := make([]TranscriptKind, 0, len(lines))
	for _, line := range lines {
		kinds = append(kinds, line.Kind)
	}
	// user, tool, assistant, user, assistant(interrupted)
	want := []TranscriptKind{TranscriptUser, TranscriptTool, TranscriptAssistant, TranscriptUser, TranscriptAssistant}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v", kinds)
		}
	}
	if !strings.Contains(lines[1].Text, "read · completed · ok") {
		t.Fatalf("tool line = %q", lines[1].Text)
	}
	if lines[4].Terminal != string(store.RunInterrupted) {
		t.Fatalf("terminal = %q", lines[4].Terminal)
	}

	recovery := &app.SessionRecovery{LastRunID: "r2", LastStatus: store.RunInterrupted, Reply: "partial",
		Tools: []app.RecoveryToolView{{Name: "bash", Status: "running", Unconfirmed: true}}, ResendInput: "q2"}
	lines = RunTranscriptLines(runs, recovery)
	last := lines[len(lines)-1]
	if last.Kind != TranscriptAssistant || last.Text != "partial" || last.Terminal != "interrupted" {
		t.Fatalf("recovery assistant = %+v", last)
	}
	tool := lines[len(lines)-2]
	if tool.Kind != TranscriptTool || !strings.Contains(tool.Text, "bash · running · result unconfirmed") {
		t.Fatalf("unconfirmed tool = %+v", tool)
	}
}

// TestEarlierInterruptedToolsRemainUnconfirmed 守住旧中断 run 的工具状态：
// 即使尾 run 成功且没有回填候选，旧 running 工具仍标记结果未确认。
func TestEarlierInterruptedToolsRemainUnconfirmed(t *testing.T) {
	runs := []store.RunRecord{
		{ID: "old", Input: "old task", Status: store.RunInterrupted, ToolEvents: []store.ToolEventRecord{{Name: "bash", Status: "running"}}},
		{ID: "new", Input: "new task", Status: store.RunCompleted, Reply: "done"},
	}
	lines := RunTranscriptLines(runs, nil)
	if len(lines) < 2 || lines[1].Kind != TranscriptTool || !strings.Contains(lines[1].Text, "result unconfirmed") {
		t.Fatalf("old interrupted tool lost uncertainty: %+v", lines)
	}
}

// TestHistoryNextAlsoEscapesLiteralCommands 守住 ↓ 路径：翻回历史条目同样
// 走转义，/exit 字面量不会因 ↓ 直接回填变成可执行指令；草稿恢复原样。
func TestHistoryNextAlsoEscapesLiteralCommands(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.input.SetValue("wip")
	m.history = []string{"older", "/exit"}
	m, _ = m.Update(specialKey(tea.KeyUp)) // -> "/exit" 转义为 //exit
	m, _ = m.Update(specialKey(tea.KeyUp)) // -> older
	if m.input.Value() != "older" {
		t.Fatalf("second prev = %q", m.input.Value())
	}
	m, _ = m.Update(specialKey(tea.KeyDown)) // 翻回 /exit：必须仍是转义形态
	if m.input.Value() != "//exit" {
		t.Fatalf("next = %q, want escaped //exit", m.input.Value())
	}
	m, _ = m.Update(specialKey(tea.KeyDown)) // 翻过最末恢复草稿，原样
	if m.input.Value() != "wip" {
		t.Fatalf("draft restore = %q", m.input.Value())
	}
}

// TestEscapeRoundTripResendsOriginalText 守住重发不变量：非指令正文回填后
// 再次解析得到的正文与净化后原文一致（Enter 重发不变形）；开头斜杠条目
// 补一个转义后由解析器剥回，同样还原。
func TestEscapeRoundTripResendsOriginalText(t *testing.T) {
	for _, text := range []string{
		// case: 行中已知指令与普通路径都不参与顶层解析。
		"see /exit and /new",
		// case: 普通路径按原文重发。
		"see /tmp/a.txt and /var/log too",
		// case: 多行正文中的指令字面量不能因回填增加 slash。
		"hello\n/exit",
		// case: 后续行普通路径同样保留。
		"first\n/second line",
		// case: 无 slash 的正文保持不变。
		"plain message without slashes",
	} {
		escaped, ok := escapeHistoryInput(text)
		if !ok {
			t.Fatalf("escape(%q) rejected", text)
		}
		parsed, err := app.ParseInput(strings.TrimSpace(escaped))
		if err != nil || parsed.Kind != app.InputMessage || parsed.Text != text {
			// 重发须经过与 submitInput 一致的 TrimSpace，再还原已接受的正文。
			t.Fatalf("round trip %q -> %q parsed=%+v err=%v", text, escaped, parsed, err)
		}
	}
	// 开头斜杠条目：补一个后解析剥回，还原解码正文。
	escaped, ok := escapeHistoryInput("/exit is literal")
	if !ok || escaped != "//exit is literal" {
		t.Fatalf("escape = %q ok=%v", escaped, ok)
	}
	parsed, err := app.ParseInput(escaped)
	if err != nil || parsed.Kind != app.InputMessage || parsed.Text != "/exit is literal" {
		t.Fatalf("round trip parsed=%+v err=%v", parsed, err)
	}
}

// TestHistoryBackfillUsesSendPrefixRules 守住回填与真实发送链一致：前导
// 空白与终端控制净化后若露出 slash，Enter 仍发送普通正文，不执行指令。
func TestHistoryBackfillUsesSendPrefixRules(t *testing.T) {
	for _, text := range []string{
		// case: ASCII 空格被发送端去掉后，路径仍须作为正文。
		"  /tmp/a.txt",
		// case: Tab 被发送端去掉后，路径仍须作为正文。
		"\t/tmp/a.txt",
		// case: Unicode 空白被发送端去掉后，指令字面量仍须作为正文。
		"\u3000/exit\u00a0",
		// case: 净化移除终端控制序列后，原先被遮住的顶层 slash 仍安全。
		"\x1b[2K /new",
	} {
		escaped, ok := escapeHistoryInput(text)
		if !ok {
			t.Fatalf("escape(%q) rejected", text)
		}
		parsed, err := app.ParseInput(strings.TrimSpace(escaped))
		want := strings.TrimSpace(sanitize(text))
		if err != nil || parsed.Kind != app.InputMessage || parsed.Text != want {
			t.Fatalf("round trip %q -> %q parsed=%+v err=%v, want body %q", text, escaped, parsed, err, want)
		}
	}
}
