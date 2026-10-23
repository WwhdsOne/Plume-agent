package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"plume-agent/internal/app"
	"plume-agent/internal/model"
)

// TestAcceptedPendingInputCtrlCCancels 守住已准入 run 的回执迟到时，Ctrl+C 仍取消且不清原输入。
func TestAcceptedPendingInputCtrlCCancels(t *testing.T) {
	cancels := 0
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", RunID: "accepted-run", SessionID: "current"}
	}, Cancel: func() { cancels++ }})
	m.sessionID = "current"
	m.input.SetValue("ordinary message")
	m, submit := m.Update(specialKey(tea.KeyEnter))
	receipt := submit()
	m, _ = m.Update(AppEvent{Event: app.Event{SessionID: "current", RunID: "accepted-run", Kind: app.EventRunStarted}})
	m, cancel := m.Update(ctrlKey('c'))
	if cancel == nil {
		t.Fatal("ctrl+c must cancel the accepted pending input")
	}
	_ = cancel()
	if m.input.Value() != "ordinary message" {
		t.Fatal("ctrl+c treated accepted run as an idle draft")
	}
	m, _ = m.Update(receipt)
	if cancels != 1 {
		t.Fatalf("cancel calls = %d, want once", cancels)
	}
}

// TestPendingInputCancellationWaitsForAcceptance 守住准入前的取消意图只在本次普通输入被接受后执行。
func TestPendingInputCancellationWaitsForAcceptance(t *testing.T) {
	cancels := 0
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", RunID: "accepted-run", SessionID: "current"}
	}, Cancel: func() { cancels++ }})
	m.sessionID = "current"
	m.input.SetValue("ordinary message")
	m, submit := m.Update(specialKey(tea.KeyEnter))
	m, cancel := m.Update(ctrlKey('c'))
	if cancel != nil {
		_ = cancel()
	}
	if cancels != 0 || m.input.Value() != "ordinary message" {
		t.Fatal("unaccepted input was cancelled or erased")
	}
	m, cancel = m.Update(AppEvent{Event: app.Event{SessionID: "current", RunID: "accepted-run", Kind: app.EventRunStarted}})
	if cancel == nil {
		t.Fatal("acceptance should fulfill the pending cancel intent")
	}
	_ = cancel()
	m, _ = m.Update(submit())
	if cancels != 1 {
		t.Fatalf("cancel calls = %d", cancels)
	}
}

// TestReadonlyPendingCtrlCDoesNotCancelFutureRun 守住只读命令的 pending 状态不产生普通输入取消意图。
func TestReadonlyPendingCtrlCDoesNotCancelFutureRun(t *testing.T) {
	cancels := 0
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", Status: &app.CommandStatusInfo{}}
	}, Cancel: func() { cancels++ }})
	m.input.SetValue("/status")
	m, request := m.Update(specialKey(tea.KeyEnter))
	m, cancel := m.Update(ctrlKey('c'))
	if cancel != nil {
		_ = cancel()
	}
	m, _ = m.Update(request())
	if cancels != 0 {
		t.Fatal("read-only request set a model cancellation intent")
	}
}

// TestLatePreviewCannotReopenClosedSelector 守住 Esc 关闭后的只读回执不重新打开面板或清除原草稿。
func TestLatePreviewCannotReopenClosedSelector(t *testing.T) {
	// 选择器来源使用解析器允许的十六进制 ID，确保回执真正经过只读预览路径。
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", History: &app.HistoryList{SessionID: "aaaaaaaa"}}
	}})
	m.panel = &commandPanel{sessions: &app.SessionList{Items: []app.SessionSummary{{ID: "aaaaaaaa"}}}}
	m.input.SetValue("keep draft")
	m, preview := m.Update(specialKey(tea.KeyTab))
	m, _ = m.Update(specialKey(tea.KeyEsc))
	m, _ = m.Update(preview())
	if m.panel != nil || m.input.Value() != "keep draft" || m.commandPending {
		t.Fatal("late preview revived a closed presentation")
	}
}

// TestLatePreviewCannotAttachToAnotherSelection 守住选 A 的预览迟到时，不能附着到已选 B 的恢复按钮。
func TestLatePreviewCannotAttachToAnotherSelection(t *testing.T) {
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", History: &app.HistoryList{SessionID: "aaaaaaaa"}}
	}})
	m.panel = &commandPanel{sessions: &app.SessionList{Items: []app.SessionSummary{{ID: "aaaaaaaa"}, {ID: "bbbbbbbb"}}}}
	m, preview := m.Update(specialKey(tea.KeyTab))
	m, _ = m.Update(specialKey(tea.KeyDown))
	m, _ = m.Update(preview())
	if m.panel == nil || m.panel.selected != 1 || m.panel.history != nil {
		t.Fatal("late A preview attached to B selection")
	}
}

// TestLateCommittedSwitchStillReplacesSession 守住 Esc 只撤销展示意图，不撤销已经成功提交的会话切换。
func TestLateCommittedSwitchStillReplacesSession(t *testing.T) {
	m := newTestModel(t, Hooks{Command: func(string) app.CommandResult {
		return app.CommandResult{Code: "ok", Action: app.ActionSwitchSession, Switch: &app.SessionSnapshot{SessionID: "target"}}
	}})
	m.panel = &commandPanel{sessions: &app.SessionList{Items: []app.SessionSummary{{ID: "target"}}}}
	m, resume := m.Update(specialKey(tea.KeyEnter))
	m, _ = m.Update(specialKey(tea.KeyEsc))
	m, _ = m.Update(resume())
	if m.sessionID != "target" {
		t.Fatal("presentation cancellation discarded a committed session change")
	}
}

// TestCoveredAnswerReportsOnlyAfterPanelCloses 守住管理面板遮挡的答案不计可见首帧，关闭后只计一次。
func TestCoveredAnswerReportsOnlyAfterPanelCloses(t *testing.T) {
	answers := 0
	m := newTestModel(t, Hooks{FirstAnswer: func(string, time.Duration) { answers++ }})
	m.startRun("active")
	m.panel = &commandPanel{title: "Status", text: "status covers transcript"}
	m, _ = m.Update(AppEvent{Event: app.Event{RunID: "active", Kind: app.EventTextDelta, TextDelta: "hidden answer"}})
	m.reportFirstAnswer()
	if answers != 0 {
		t.Fatal("covered answer was counted as visible")
	}
	m, _ = m.Update(specialKey(tea.KeyEsc))
	m.reportFirstAnswer()
	m.reportFirstAnswer()
	if answers != 1 {
		t.Fatalf("visible first answer reports = %d", answers)
	}
}

// TestSwitchSummaryDoesNotHideRestoredStatus 守住恢复摘要不会永久占据状态栏，首次恢复帧就展示缓存和上下文。
func TestSwitchSummaryDoesNotHideRestoredStatus(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.width, m.height = 180, 30
	m.resize()
	capacity, cached := int64(1000000), int64(4)
	// 默认 cache_scope=session，恢复快照必须同时携带会话聚合，不能只填写末次 usage。
	m, _ = m.Update(commandResultMsg{result: app.CommandResult{Code: "ok", Message: "Session resumed.", Action: app.ActionSwitchSession,
		Switch: &app.SessionSnapshot{SessionID: "restored", Model: app.CommandModelInfo{Provider: "test", Model: "restored", ContextWindowTokens: &capacity},
			Stats: app.SessionStats{StartedAt: time.Now(), Calls: 1, KnownCalls: 1, CacheKnownCalls: 1, CachePromptTokens: 10, CachedPromptTokens: 4,
				LastUsage: model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &cached}}}}})
	view := m.View()
	if m.notice != "" || !strings.Contains(view, "40%") || !strings.Contains(view, "10/1M") {
		t.Fatalf("switch summary hides statistics: notice=%q view=%q", m.notice, view)
	}
}
