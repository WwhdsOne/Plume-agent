package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"plume-agent/internal/app"
	"plume-agent/internal/model"
)

func newTestModel(t *testing.T, hooks Hooks) Model {
	t.Helper()
	m := New("test/model", hooks)
	m.width, m.height = 80, 24
	m.resize()
	return m
}

func keyPress(key string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

func specialKey(keyType tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: keyType}
}

func TestSubmitSendsInputAndShowsUserLine(t *testing.T) {
	var submitted atomic.Value
	hooks := Hooks{Submit: func(input string) (string, error) {
		submitted.Store(input)
		return "run-000001", nil
	}}
	m := newTestModel(t, hooks)
	m.input.SetValue("  hello plume  ")

	updated, _ := m.Update(specialKey(tea.KeyEnter))
	m = updated

	if got, _ := submitted.Load().(string); got != "hello plume" {
		t.Errorf("submit input = %q, want trimmed text", got)
	}
	if m.state != stateRunning || m.runID != "run-000001" {
		t.Errorf("state = %s runID = %q, want running", m.state, m.runID)
	}
	if len(m.lines) != 1 || m.lines[0].kind != lineUser || m.lines[0].text != "hello plume" {
		t.Errorf("lines = %+v, want one user line", m.lines)
	}
	if m.input.Value() != "" {
		t.Error("input must be cleared after submit")
	}
}

func TestSubmitEmptyInputDoesNothing(t *testing.T) {
	var calls atomic.Int64
	m := newTestModel(t, Hooks{Submit: func(string) (string, error) {
		calls.Add(1)
		return "run-x", nil
	}})
	m.input.SetValue("   ")

	updated, _ := m.Update(specialKey(tea.KeyEnter))
	m = updated

	if calls.Load() != 0 {
		t.Error("empty input must not produce a request")
	}
	if len(m.lines) != 0 {
		t.Errorf("lines = %d, want none", len(m.lines))
	}
}

func TestSubmitWhileBusyIsRejected(t *testing.T) {
	m := newTestModel(t, Hooks{Submit: func(string) (string, error) { return "run-x", nil }})
	m.startRun("run-000001")
	m.input.SetValue("second")

	updated, _ := m.Update(specialKey(tea.KeyEnter))
	m = updated

	if m.notice == "" {
		t.Error("busy submit must show a notice")
	}
	if len(m.lines) != 0 {
		t.Errorf("busy submit must not add lines, got %d", len(m.lines))
	}
}

func TestCompletedEventAppendsAssistantAndUsage(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.startRun("run-000001")

	event := app.Event{
		Kind:         app.EventRunCompleted,
		RunID:        "run-000001",
		Reply:        "the answer",
		FinishReason: model.FinishStop,
		Usage:        model.Usage{OK: true, PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}
	updated, _ := m.Update(AppEvent{Event: event})
	m = updated

	if m.state != stateIdle {
		t.Errorf("state = %s, want idle after terminal event", m.state)
	}
	if len(m.lines) != 1 || m.lines[0].kind != lineAssistant || m.lines[0].text != "the answer" {
		t.Fatalf("lines = %+v, want one assistant line", m.lines)
	}
	if !strings.Contains(m.lastUsage, "3/2/5") {
		t.Errorf("usage label = %q, want 3/2/5", m.lastUsage)
	}
}

func TestFailedEventAddsErrorLineOnly(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.startRun("run-000001")

	updated, _ := m.Update(AppEvent{Event: app.Event{Kind: app.EventRunFailed, Err: errRateLimited()}})
	m = updated

	if m.state != stateIdle {
		t.Errorf("state = %s, want idle", m.state)
	}
	if len(m.lines) != 1 || m.lines[0].kind != lineError {
		t.Fatalf("lines = %+v, want one error line", m.lines)
	}
	if !strings.Contains(m.lines[0].text, "rate_limited") {
		t.Errorf("error line = %q, want classified code", m.lines[0].text)
	}
}

func TestCancelRequestedWhileRunning(t *testing.T) {
	var cancels atomic.Int64
	m := newTestModel(t, Hooks{Cancel: func() { cancels.Add(1) }})
	m.startRun("run-000001")

	updated, cmd := m.Update(specialKey(tea.KeyEsc))
	_ = updated
	if cmd == nil {
		t.Fatal("esc while running must produce a cancel cmd")
	}
	_ = cmd() // 执行 cmd：钩子应被调用
	if cancels.Load() != 1 {
		t.Errorf("cancel hook calls = %d, want 1", cancels.Load())
	}
}

func TestCtrlNResetsAndCtrlCQuits(t *testing.T) {
	var resets atomic.Int64
	m := newTestModel(t, Hooks{ResetSession: func() { resets.Add(1) }})
	m.appendLine(lineUser, "old context")

	updated, _ := m.Update(keyPress("ctrl+n"))
	m = updated

	if resets.Load() != 1 {
		t.Errorf("reset hook calls = %d, want 1", resets.Load())
	}
	if len(m.lines) != 1 || m.lines[0].kind != lineSystem {
		t.Errorf("lines after reset = %+v, want only the new-session notice", m.lines)
	}

	_, cmd := m.Update(specialKey(tea.KeyCtrlC))
	if cmd == nil {
		t.Fatal("ctrl+c while idle must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("idle ctrl+c must return quit")
	}
}

func TestCtrlNWhileRunningIsRejected(t *testing.T) {
	var resets atomic.Int64
	m := newTestModel(t, Hooks{ResetSession: func() { resets.Add(1) }})
	m.startRun("run-000001")

	updated, _ := m.Update(keyPress("ctrl+n"))
	m = updated

	if resets.Load() != 0 {
		t.Error("ctrl+n while running must not reset")
	}
	if m.notice == "" {
		t.Error("ctrl+n while running must show a notice")
	}
}

func TestSanitizeStripsTerminalSequences(t *testing.T) {
	cases := map[string]string{
		"\x1b]8;;http://evil\x07click\x1b]8;;\x07": "click",             // OSC 超链接
		"\x1b[2J\x1b[Hhello":                       "hello",             // CSI 清屏/移光标
		"line\x00\x07break":                        "linebreak",         // C0 控制
		"multi\nline\nkeep":                        "multi\nline\nkeep", // 换行保留
	}
	for input, want := range cases {
		if got := sanitize(input); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", input, got, want)
		}
	}
}

// errRateLimited 构造一个分类错误供失败事件测试。
func errRateLimited() error { return model.NewError(model.ErrRateLimited).WithSummary("too many") }
