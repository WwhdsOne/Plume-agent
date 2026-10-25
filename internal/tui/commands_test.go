package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"plume-agent/internal/app"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// TestCommandsRouteBeforeBusy 守住运行时的管理指令仍可到统一处理器，且不追加用户正文。
func TestCommandsRouteBeforeBusy(t *testing.T) {
	var received string
	m := newTestModel(t, Hooks{Command: func(text string) app.CommandResult {
		received = text
		return app.CommandResult{Code: "ok", Message: "cancel_requested"}
	}})
	m.startRun("run-current")
	m.input.SetValue("/cancel")
	m, cmd := m.Update(specialKey(tea.KeyEnter))
	if cmd == nil || received != "" {
		t.Fatal("management command must be scheduled asynchronously")
	}
	m, _ = m.Update(cmd())
	if received != "/cancel" || len(m.lines) != 0 || m.input.Value() != "" || m.state != stateRunning {
		t.Fatalf("command was blocked or entered transcript: input=%q lines=%+v state=%s", received, m.lines, m.state)
	}
}

// TestCommandFailurePreservesOriginalDisplay 守住恢复拒绝时原记录、输入草稿和统计原封不动。
func TestCommandFailurePreservesOriginalDisplay(t *testing.T) {
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "resume_incompatible", Message: "model config unavailable"}
	}})
	m.appendLine(lineUser, "old transcript")
	m.input.SetValue("/resume abcdef01")
	m.status.stats.Calls = 3
	m, cmd := m.Update(specialKey(tea.KeyEnter))
	m, _ = m.Update(cmd())
	if len(m.lines) != 1 || m.lines[0].text != "old transcript" || m.input.Value() != "/resume abcdef01" || m.status.stats.Calls != 3 {
		t.Fatal("failed command erased current session")
	}
	if !strings.Contains(m.notice, "resume_incompatible") {
		t.Fatalf("failure notice = %q", m.notice)
	}
}

// TestCompletionEnterDoesNotExecuteCandidate 守住前缀回车只执行已输入文本；Tab 才补全所选候选。
func TestCompletionEnterDoesNotExecuteCandidate(t *testing.T) {
	var received string
	m := newTestModel(t, Hooks{Command: func(text string) app.CommandResult {
		received = text
		return app.CommandResult{Code: "unknown_command", Message: "use /help"}
	}})
	m, _ = m.Update(typeText("/res"))
	if len(m.completions) == 0 {
		t.Fatal("prefix should show command suggestions")
	}
	m, cmd := m.Update(specialKey(tea.KeyEnter))
	m, _ = m.Update(cmd())
	if received != "/res" {
		t.Fatalf("enter executed candidate %q", received)
	}
	m, _ = m.Update(specialKey(tea.KeyTab))
	if m.input.Value() != "/resume" {
		t.Fatalf("tab value = %q", m.input.Value())
	}
}

// TestEscapePanelBeforeCancelAndCtrlCAlwaysCancel 守住 Esc 先关闭建议，running Ctrl+C 始终取消。
func TestEscapePanelBeforeCancelAndCtrlCAlwaysCancel(t *testing.T) {
	cancels := 0
	m := newTestModel(t, Hooks{Cancel: func() { cancels++ }})
	m.startRun("run-active")
	m, _ = m.Update(typeText("/"))
	m, cmd := m.Update(specialKey(tea.KeyEsc))
	if cmd != nil || len(m.completions) != 0 {
		t.Fatal("first escape must close suggestions only")
	}
	m, cmd = m.Update(specialKey(tea.KeyEsc))
	if cmd == nil {
		t.Fatal("second escape must cancel")
	}
	_ = cmd()
	m.input.Reset()
	m, _ = m.Update(typeText("/"))
	_, cmd = m.Update(ctrlKey('c'))
	if cmd == nil {
		t.Fatal("running ctrl+c must cancel through suggestions")
	}
	_ = cmd()
	if cancels != 2 {
		t.Fatalf("cancel count = %d", cancels)
	}
}

// TestSessionSwitchReplacesStateAfterSuccess 守住成功恢复才替换转录、统计和模型，旧会话事件不能污染新会话。
func TestSessionSwitchReplacesStateAfterSuccess(t *testing.T) {
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	capacity := int64(32000)
	m := NewWithOptions("old/model", Hooks{}, Options{InitialSessionID: "old-session", Clock: func() time.Time { return now }})
	m.width, m.height = 80, 24
	m.SetSplash(Splash{Version: "should disappear"})
	m.appendLine(lineUser, "old conversation")
	m.input.SetValue("draft")
	m.history = []string{"old input"}
	snapshot := &app.SessionSnapshot{SessionID: "new-session", Model: app.CommandModelInfo{Provider: "new", Model: "restored", Reasoning: "high", ContextWindowTokens: &capacity},
		Stats: app.SessionStats{StartedAt: now, Calls: 1, KnownCalls: 1, LastUsage: model.Usage{OK: true, PromptTokens: 7}},
		Runs:  []store.RunRecord{{Input: "/new is saved text", Reply: "```go\nfmt.Println(1)\n```\x1b]0;evil\x07", Reasoning: "thought", Status: store.RunCancelled}}, Resumed: true}
	m, _ = m.Update(commandResultMsg{result: app.CommandResult{Code: "ok", Action: app.ActionSwitchSession, Switch: snapshot}})
	if m.sessionID != "new-session" || m.input.Value() != "" || len(m.history) != 0 || m.splash != nil || m.options.ModelID != "restored" || m.status.stats.Calls != 1 {
		t.Fatalf("switch did not replace complete display state: %+v", m)
	}
	if len(m.lines) != 3 || m.lines[0].text != "/new is saved text" || m.lines[1].reasoning != "thought" || m.lines[1].terminal != "cancelled" || strings.Contains(m.lines[1].text, "\x1b") {
		t.Fatalf("restored transcript = %+v", m.lines)
	}
	if m.lines[2].kind != lineSystem || !strings.Contains(m.lines[2].text, "Session restored") {
		t.Fatalf("restore hint missing: %+v", m.lines)
	}
	m.startRun("same-run")
	m, _ = m.Update(AppEvent{Event: app.Event{SessionID: "old-session", RunID: "same-run", Kind: app.EventTextDelta, TextDelta: "stale"}})
	if len(m.lines) != 3 {
		t.Fatal("old session event mutated restored transcript")
	}
	// case: /new 新建会话不冒充恢复，转录保持干净。
	fresh := &app.SessionSnapshot{SessionID: "fresh-session", Model: app.CommandModelInfo{Provider: "new", Model: "restored"}}
	m.replaceSession(fresh)
	if len(m.lines) != 0 {
		t.Fatalf("new session must stay clean of restore hints: %+v", m.lines)
	}
}

// TestSelectorPreviewDoesNotSwitchAndEscapeRestoresDraft 守住预览使用只读 history，Esc 关闭列表不替换原草稿。
func TestSelectorPreviewDoesNotSwitchAndEscapeRestoresDraft(t *testing.T) {
	var received string
	m := newTestModel(t, Hooks{Command: func(text string) app.CommandResult {
		received = text
		return app.CommandResult{Code: "ok", History: &app.HistoryList{SessionID: "target-session", Page: 1, PageSize: 20, Total: 1, Runs: []store.RunRecord{{Input: "preview", Reply: "answer"}}}}
	}})
	m.sessionID = "current-session"
	m.input.SetValue("keep draft")
	m.appendLine(lineUser, "keep transcript")
	m, _ = m.Update(commandResultMsg{result: app.CommandResult{Code: "ok", Action: app.ActionSelectSession, Sessions: &app.SessionList{Page: 1, PageSize: 20, Total: 1, Items: []app.SessionSummary{{ID: "target-session", Title: "target", Reason: "model unavailable"}}}}})
	m, cmd := m.Update(specialKey(tea.KeyTab))
	if cmd == nil {
		t.Fatal("tab on selector should request preview")
	}
	m, _ = m.Update(cmd())
	if received != "/history --session target-session" || m.sessionID != "current-session" || m.input.Value() != "keep draft" || len(m.lines) != 1 {
		t.Fatal("preview changed current conversation")
	}
	m, _ = m.Update(specialKey(tea.KeyEsc))
	if m.input.Value() != "keep draft" || m.panel != nil {
		t.Fatal("escape did not return to original draft")
	}
}

// TestAsyncInputBuffersEarlyRunEvents 守住极快离线 run 的事件先到回执时，正文与终态不丢失。
func TestAsyncInputBuffersEarlyRunEvents(t *testing.T) {
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", RunID: "run-fast", SessionID: "session-fast"}
	}})
	m.sessionID = "session-fast"
	m.input.SetValue("message")
	m, cmd := m.Update(specialKey(tea.KeyEnter))
	m, _ = m.Update(AppEvent{Event: app.Event{SessionID: "session-fast", RunID: "run-fast", Kind: app.EventRunCompleted, Reply: "fast answer"}})
	m, _ = m.Update(cmd())
	if m.state != stateIdle || len(m.lines) != 2 || m.lines[0].text != "message" || m.lines[1].text != "fast answer" {
		t.Fatalf("early events lost: state=%s lines=%+v", m.state, m.lines)
	}
}

// TestCtrlNUsesUnifiedCommandAndClearRetainsContext 守住快捷新建复用 /new，清屏动作只重绘且不清转录。
func TestCtrlNUsesUnifiedCommandAndClearRetainsContext(t *testing.T) {
	var received string
	m := newTestModel(t, Hooks{Command: func(text string) app.CommandResult {
		received = text
		return app.CommandResult{Code: "busy", Message: "cancel first"}
	}})
	m.input.SetValue("keep draft")
	m.appendLine(lineUser, "keep transcript")
	m.startRun("active")
	m, cmd := m.Update(ctrlKey('n'))
	if cmd == nil {
		t.Fatal("ctrl+n should use unified command even while busy")
	}
	m, _ = m.Update(cmd())
	if received != "/new" || m.input.Value() != "keep draft" || len(m.lines) != 1 {
		t.Fatal("shortcut bypassed command handler or erased draft")
	}
	m, cmd = m.Update(commandResultMsg{result: app.CommandResult{Code: "ok", Action: app.ActionClear}})
	if cmd == nil || len(m.lines) != 1 || m.state != stateRunning {
		t.Fatal("clear must retain conversation and running state")
	}
}

// TestCancelStatusIsSuccessfulManagementResult 守住取消请求的目标 run ID 只用于回执，不能当成新普通消息启动。
func TestCancelStatusIsSuccessfulManagementResult(t *testing.T) {
	for _, code := range []string{"cancel_requested", "no_active_run"} {
		// case: cancel_requested 与空闲无目标都是已处理回执，不属于失败提示。
		t.Run(code, func(t *testing.T) {
			m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
				return app.CommandResult{Command: app.CommandCancel, Code: code, RunID: "active", Message: "Cancellation requested."}
			}})
			m.startRun("active")
			m.input.SetValue("/cancel")
			m, cmd := m.Update(specialKey(tea.KeyEnter))
			m, _ = m.Update(cmd())
			if m.input.Value() != "" || len(m.lines) != 0 || strings.Contains(m.notice, code+":") {
				t.Fatalf("cancel mishandled: lines=%+v draft=%q notice=%q", m.lines, m.input.Value(), m.notice)
			}
		})
	}
}

// TestSelectorPreviewCanPageWithoutSwitching 守住历史预览的翻页请求只读且不改变当前会话。
func TestSelectorPreviewCanPageWithoutSwitching(t *testing.T) {
	var received string
	m := newTestModel(t, Hooks{Command: func(text string) app.CommandResult {
		received = text
		return app.CommandResult{Code: "ok", History: &app.HistoryList{SessionID: "preview-session", Page: 2, PageSize: 1, Total: 2}}
	}})
	m.sessionID = "current-session"
	m.panel = &commandPanel{sessions: &app.SessionList{Items: []app.SessionSummary{{ID: "preview-session"}}},
		history: &app.HistoryList{SessionID: "preview-session", Page: 1, PageSize: 1, Total: 2}}
	m, cmd := m.Update(specialKey(tea.KeyPgDown))
	if cmd == nil {
		t.Fatal("preview must be pageable")
	}
	m, _ = m.Update(cmd())
	if received != "/history 2 --session preview-session" || m.sessionID != "current-session" || m.panel.history.Page != 2 {
		t.Fatalf("preview page = %q current=%q panel=%+v", received, m.sessionID, m.panel)
	}
}

// TestSuggestionsCanReopenAfterEditing 守住 Esc 只压住当前候选，再编辑输入后可以重新显示建议。
func TestSuggestionsCanReopenAfterEditing(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m, _ = m.Update(typeText("/"))
	m, _ = m.Update(specialKey(tea.KeyEsc))
	m, _ = m.Update(specialKey(tea.KeyBackspace))
	m, _ = m.Update(typeText("/"))
	if len(m.completions) == 0 {
		t.Fatal("suggestions did not reopen after editing")
	}
}

// TestNarrowSelectorKeepsSelectedTitleAndReasonVisible 守住中文长标题、多个不可继续条目在窄窗口内仍能看到选中项和原因。
func TestNarrowSelectorKeepsSelectedTitleAndReasonVisible(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.width, m.height = 24, 12
	m.resize()
	items := make([]app.SessionSummary, 20)
	for i := range items {
		items[i] = app.SessionSummary{ID: "long-session-id", Title: strings.Repeat("长标题", 15), Reason: "模型配置已删除"}
	}
	items[19].Title = "最终选中会话"
	m.panel = &commandPanel{title: "Sessions", sessions: &app.SessionList{Items: items, Page: 1, PageSize: 20, Total: 20}, selected: 19}
	view := m.panelView()
	if !strings.Contains(view, "最终选中") || !strings.Contains(view, "模型配置已删除") {
		t.Fatalf("selected session is hidden in narrow view: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if displayWidth := ansi.StringWidth(line); displayWidth > m.width {
			t.Fatalf("panel width = %d, want <= %d", displayWidth, m.width)
		}
	}
}

// TestManagementPendingKeepsStreamingEvents 守住只读管理回执到达前的流式正文与终态照常应用。
func TestManagementPendingKeepsStreamingEvents(t *testing.T) {
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Command: app.CommandStatus, Code: "ok", Status: &app.CommandStatusInfo{SessionID: "current", Running: true}}
	}})
	m.sessionID = "current"
	m.startRun("streaming")
	m.input.SetValue("/status")
	m, cmd := m.Update(specialKey(tea.KeyEnter))
	m, _ = m.Update(AppEvent{Event: app.Event{SessionID: "current", RunID: "streaming", Kind: app.EventTextDelta, TextDelta: "first"}})
	m, _ = m.Update(AppEvent{Event: app.Event{SessionID: "current", RunID: "streaming", Kind: app.EventRunCompleted, Reply: "complete"}})
	m, _ = m.Update(cmd())
	if m.state != stateIdle || len(m.lines) != 1 || m.lines[0].text != "complete" || m.panel == nil {
		t.Fatal("read-only command response discarded streaming terminal")
	}
}

// TestMouseWheelUsesPreviewScroll 守住打开只读面板时滚轮只滚预览，不改变底下聊天转录的跟随状态。
func TestMouseWheelUsesPreviewScroll(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.panel = &commandPanel{history: &app.HistoryList{}}
	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.panel.scroll != 3 || m.readingFrozen {
		t.Fatal("mouse wheel scrolled hidden chat instead of preview")
	}
}

// TestStatusPanelShowsCreationAndCompletedRunDuration 守住创建时间与实际成功 run 耗时分别显示，
// 不把创建距今或本次打开时长当成执行耗时，未知用量与保存状态仍保持原口径。
func TestStatusPanelShowsCreationAndCompletedRunDuration(t *testing.T) {
	created := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	m := newTestModel(t, Hooks{})
	m.width, m.height = 100, 30
	m.resize()
	m, _ = m.Update(commandResultMsg{result: app.CommandResult{Code: "ok", Status: &app.CommandStatusInfo{
		SessionID: "session-status", Title: "status fixture", CreatedAt: created,
		CompletedRunCount: 2, CompletedRunDuration: 2500 * time.Millisecond,
		Persistence: "persistent", SaveStatus: "saved",
	}}})
	view := m.panelView()
	for _, want := range []string{"Created: " + created.Local().Format(time.RFC3339), "Completed runs: 2", "Completed run duration: 2.5s", "Save status: saved"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status panel missing %q: %q", want, view)
		}
	}
}
