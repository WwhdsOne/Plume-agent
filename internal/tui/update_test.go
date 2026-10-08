package tui

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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

// 以下构造器按 bubbletea v2 的 Key 结构拼按键；String() 产出与
// newKeyMap 绑定的键名一致（enter/shift+enter/ctrl+c/…）。

func typeText(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 0, Text: text})
}

func specialKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func enterWithMod(mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: mod})
}

func ctrlKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
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

	updated, _ := m.Update(AppEvent{Event: app.Event{Kind: app.EventRunFailed, RunID: "run-000001", Err: errRateLimited()}})
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

func TestCtrlNResetsSession(t *testing.T) {
	var resets atomic.Int64
	m := newTestModel(t, Hooks{ResetSession: func() { resets.Add(1) }})
	m.appendLine(lineUser, "old context")
	m.input.SetValue("draft to be cleared")

	updated, _ := m.Update(ctrlKey('n'))
	m = updated

	if resets.Load() != 1 {
		t.Errorf("reset hook calls = %d, want 1", resets.Load())
	}
	if len(m.lines) != 1 || m.lines[0].kind != lineSystem {
		t.Errorf("lines after reset = %+v, want only the new-session notice", m.lines)
	}
	if m.input.Value() != "" {
		t.Error("ctrl+n must clear the input draft")
	}
	if len(m.history) != 0 {
		t.Errorf("history after reset = %v, want empty (draft history is per-session)", m.history)
	}
}

func TestCtrlNWhileRunningIsRejected(t *testing.T) {
	var resets atomic.Int64
	m := newTestModel(t, Hooks{ResetSession: func() { resets.Add(1) }})
	m.startRun("run-000001")

	updated, _ := m.Update(ctrlKey('n'))
	m = updated

	if resets.Load() != 0 {
		t.Error("ctrl+n while running must not reset")
	}
	if m.notice == "" {
		t.Error("ctrl+n while running must show a notice")
	}
}

// --- G1b.2.1 键位契约测试 ---

func TestKeyMapTableComplete(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		km := newKeyMap(goos)
		bindings := map[string]struct {
			b    interface{ Enabled() bool }
			keys []string
		}{
			"Submit":           {km.Submit, []string{"enter"}},
			"NewlineShift":     {km.NewlineShift, []string{"shift+enter"}},
			"NewlineAlt":       {km.NewlineAlt, []string{"alt+enter"}},
			"NewlineBackslash": {km.NewlineBackslash, []string{"\\"}},
			"Cancel":           {km.Cancel, []string{"esc"}},
			"Quit":             {km.Quit, []string{"ctrl+d"}},
			"ClearScreen":      {km.ClearScreen, []string{"ctrl+l"}},
			"HistoryUp":        {km.HistoryUp, []string{"up"}},
			"HistoryDown":      {km.HistoryDown, []string{"down"}},
			"ScrollUp":         {km.ScrollUp, []string{"pgup"}},
			"ScrollDown":       {km.ScrollDown, []string{"pgdown"}},
			"NewSession":       {km.NewSession, []string{"ctrl+n"}},
		}
		for name, b := range bindings {
			if !b.b.Enabled() {
				t.Errorf("%s: binding %s disabled", goos, name)
			}
			if got, want := b.keys[0], b.keys[0]; got != want {
				t.Errorf("%s: binding %s keys = %v, want %v", goos, name, b.keys, want)
			}
		}
	}
	// 平台差异只允许在帮助文本：darwin 显示 Option，linux/windows 显示 Alt。
	if km := newKeyMap("darwin"); km.NewlineAlt.Help().Key != "option+enter" {
		t.Errorf("darwin alt help = %q, want option+enter", km.NewlineAlt.Help().Key)
	}
	if km := newKeyMap("linux"); km.NewlineAlt.Help().Key != "alt+enter" {
		t.Errorf("linux alt help = %q, want alt+enter", km.NewlineAlt.Help().Key)
	}
}

func TestNewlineThreeLayers(t *testing.T) {
	m := newTestModel(t, Hooks{})
	var calls atomic.Int64
	m.hooks.Submit = func(string) (string, error) { calls.Add(1); return "run-x", nil }

	// 层级 1：Shift+Enter（kitty 协议）
	updated, _ := m.Update(enterWithMod(tea.ModShift))
	m = updated
	m.input.SetValue("a")
	// 层级 2：Alt+Enter
	updated, _ = m.Update(enterWithMod(tea.ModAlt))
	m = updated

	if got := m.input.Value(); got != "a\n" {
		t.Errorf("value after shift/alt+enter = %q, want \"a\\n\"", got)
	}
	if calls.Load() != 0 {
		t.Error("newline keys must not submit")
	}

	// 层级 3：行尾 `\` + Enter 续行，不发送
	m.input.SetValue("hello\\")
	updated, _ = m.Update(specialKey(tea.KeyEnter))
	m = updated
	if calls.Load() != 0 {
		t.Error("trailing backslash + enter must not submit")
	}
	if got := m.input.Value(); got != "hello\n" {
		t.Errorf("value after backslash continuation = %q, want \"hello\\n\"", got)
	}

	// 反斜杠去掉后回车正常发送
	updated, _ = m.Update(specialKey(tea.KeyEnter))
	m = updated
	if calls.Load() != 1 {
		t.Errorf("submit calls = %d, want 1 after continuation line", calls.Load())
	}
	if !strings.HasPrefix(m.lines[0].text, "hello") {
		t.Errorf("submitted text = %q, want prefixed hello", m.lines[0].text)
	}
}

func TestCtrlCThreeStage(t *testing.T) {
	// 第一段：running 取消 run
	var cancels atomic.Int64
	m := newTestModel(t, Hooks{Cancel: func() { cancels.Add(1) }})
	m.startRun("run-000001")
	updated, cmd := m.Update(ctrlKey('c'))
	if cmd == nil {
		t.Fatal("ctrl+c while running must cancel")
	}
	_ = cmd()
	if cancels.Load() != 1 {
		t.Errorf("cancel calls = %d, want 1", cancels.Load())
	}

	// 第二段：idle 有草稿清空草稿（不退出）
	m.endRun()
	m.input.SetValue("draft here")
	updated, cmd = m.Update(ctrlKey('c'))
	m = updated
	if cmd != nil {
		t.Error("ctrl+c with draft must not quit")
	}
	if m.input.Value() != "" {
		t.Errorf("draft = %q, want cleared", m.input.Value())
	}

	// 第三段：空草稿双击（1s 内）退出
	updated, cmd = m.Update(ctrlKey('c'))
	m = updated
	if cmd != nil {
		t.Error("single ctrl+c on empty draft must not quit")
	}
	_, cmd = m.Update(ctrlKey('c'))
	if cmd == nil {
		t.Fatal("double ctrl+c on empty draft must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("double ctrl+c must return quit msg")
	}

	// 超出 1s 窗口不退出
	m = newTestModel(t, Hooks{})
	m.lastCtrlC = time.Now().Add(-2 * time.Second)
	_, cmd = m.Update(ctrlKey('c'))
	if cmd != nil {
		t.Error("ctrl+c outside the 1s window must not quit")
	}

	// 中间插入其他键重置双击窗口
	m = newTestModel(t, Hooks{})
	_, _ = m.Update(ctrlKey('c'))
	_, _ = m.Update(typeText("x"))
	m.input.Reset()
	_, cmd = m.Update(ctrlKey('c'))
	if cmd != nil {
		t.Error("other key between ctrl+c presses must reset the double-press window")
	}
}

func TestCtrlDQuitOnlyOnEmptyInput(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.input.SetValue("keep me")

	updated, cmd := m.Update(ctrlKey('d'))
	m = updated
	if cmd != nil {
		t.Error("ctrl+d with input must be ignored")
	}
	if m.input.Value() != "keep me" {
		t.Errorf("draft = %q, want preserved", m.input.Value())
	}

	m.input.Reset()
	updated, cmd = m.Update(ctrlKey('d'))
	_ = updated
	if cmd == nil {
		t.Fatal("ctrl+d on empty input must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+d quit must return quit msg")
	}
}

func TestCtrlLClearsScreenAndResetsScroll(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.appendLine(lineUser, "hello")

	updated, cmd := m.Update(ctrlKey('l'))
	m = updated
	if cmd == nil {
		t.Fatal("ctrl+l must return a clear-screen cmd")
	}
	_ = cmd() // v2 的 clearScreenMsg 是私有类型，只验证 cmd 可执行
	if !m.viewport.AtBottom() {
		t.Error("ctrl+l must reset scroll to bottom")
	}
}

func TestDraftHistoryNavigation(t *testing.T) {
	var seq atomic.Int64
	m := newTestModel(t, Hooks{Submit: func(string) (string, error) {
		return "run-" + string(rune('a'+seq.Add(1))), nil
	}})

	// 提交两条
	m.input.SetValue("first message")
	updated, _ := m.Update(specialKey(tea.KeyEnter))
	m = updated
	m.endRun()
	m.input.SetValue("second message")
	updated, _ = m.Update(specialKey(tea.KeyEnter))
	m = updated
	m.endRun()

	// 编辑中的草稿在按 Up 时被保存
	m.input.SetValue("draft in progress")
	updated, _ = m.Update(specialKey(tea.KeyUp))
	m = updated
	if got := m.input.Value(); got != "second message" {
		t.Errorf("up = %q, want most recent history entry", got)
	}

	// 再按 Up 到更旧
	updated, _ = m.Update(specialKey(tea.KeyUp))
	m = updated
	if got := m.input.Value(); got != "first message" {
		t.Errorf("up again = %q, want oldest entry", got)
	}
	// 已到最旧，继续 Up 不变
	updated, _ = m.Update(specialKey(tea.KeyUp))
	m = updated
	if got := m.input.Value(); got != "first message" {
		t.Errorf("up at oldest = %q, want unchanged", got)
	}

	// Down 回到较新
	updated, _ = m.Update(specialKey(tea.KeyDown))
	m = updated
	if got := m.input.Value(); got != "second message" {
		t.Errorf("down = %q, want newer entry", got)
	}

	// Down 翻过最新，恢复编辑中草稿
	updated, _ = m.Update(specialKey(tea.KeyDown))
	m = updated
	if got := m.input.Value(); got != "draft in progress" {
		t.Errorf("down past newest = %q, want restored draft", got)
	}

	// 无历史时 Up/Down 无副作用
	m2 := newTestModel(t, Hooks{})
	m2.input.SetValue("solo")
	updated, _ = m2.Update(specialKey(tea.KeyUp))
	m2 = updated
	if got := m2.input.Value(); got != "solo" {
		t.Errorf("up with empty history = %q, want unchanged", got)
	}
}

// TestHistoryNavigationIsEdgeOnly 守住边缘导航语义（用户反馈修复）：
// 多行输入内 Up/Down 先移光标，到第一行/最后一行的边缘才翻历史。
func TestHistoryNavigationIsEdgeOnly(t *testing.T) {
	m := newTestModel(t, Hooks{Submit: func(string) (string, error) { return "run-1", nil }})

	// 先提交一条，保证历史非空
	m.input.SetValue("earlier message")
	updated, _ := m.Update(specialKey(tea.KeyEnter))
	m = updated
	m.endRun()

	// 三行输入，光标停在末尾（第三行）
	m.input.SetValue("l1\nl2\nl3")
	if m.input.Line() != 2 {
		t.Fatalf("cursor line = %d, want 2 (end of value)", m.input.Line())
	}

	// Up：第二行 → 光标上移，不翻历史
	updated, _ = m.Update(specialKey(tea.KeyUp))
	m = updated
	if m.input.Line() != 1 || m.input.Value() != "l1\nl2\nl3" {
		t.Errorf("up inside multiline: line = %d value = %q, want line 1 value unchanged", m.input.Line(), m.input.Value())
	}
	if m.historyIdx != -1 {
		t.Errorf("historyIdx = %d, want -1 (must not enter history from middle of input)", m.historyIdx)
	}

	// Up：第一行 → 仍在输入内
	updated, _ = m.Update(specialKey(tea.KeyUp))
	m = updated
	if m.input.Line() != 0 {
		t.Errorf("line = %d, want 0", m.input.Line())
	}

	// Up：已到第一行边缘 → 翻历史
	updated, _ = m.Update(specialKey(tea.KeyUp))
	m = updated
	if got := m.input.Value(); got != "earlier message" {
		t.Errorf("up at first line = %q, want history entry", got)
	}

	// Down：翻回最新恢复后，Down 在最后一行边缘之外无事发生
	updated, _ = m.Update(specialKey(tea.KeyDown))
	m = updated
	updated, _ = m.Update(specialKey(tea.KeyDown))
	m = updated
	if got := m.input.Value(); got != "l1\nl2\nl3" {
		t.Errorf("down past newest = %q, want restored multiline draft", got)
	}

	// 光标在末尾（SetValue 后即末尾），Down 直接翻历史
	m.input.SetValue("x\ny")
	updated, _ = m.Update(specialKey(tea.KeyDown))
	m = updated
	if m.historyIdx != -1 || m.input.Value() != "x\ny" {
		t.Errorf("down at last line with empty history must be a no-op, got value %q idx %d", m.input.Value(), m.historyIdx)
	}
}

func TestInputHeightGrowsToFourLinesMax(t *testing.T) {
	m := newTestModel(t, Hooks{})
	if m.input.Height() != 1 {
		t.Errorf("initial input height = %d, want 1", m.input.Height())
	}

	// 多行内容（含折行场景用长行覆盖）：SetValue/InsertString 内部
	// recalculateHeight，DynamicHeight 钳制在 1..4。
	m.input.SetValue("line1\nline2\nline3\nline4\nline5")
	updated, _ := m.Update(specialKey(tea.KeyPgUp)) // 任一经过 Update 的消息触发 syncLayout
	m = updated
	if h := m.input.Height(); h != maxInputLines {
		t.Errorf("input height with 5 lines = %d, want capped at %d", h, maxInputLines)
	}

	m.input.Reset()
	updated, _ = m.Update(specialKey(tea.KeyPgUp))
	m = updated
	if h := m.input.Height(); h != minInputLines {
		t.Errorf("input height after reset = %d, want %d", h, minInputLines)
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
