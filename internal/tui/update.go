package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"herald-agent/internal/app"
	"herald-agent/internal/model"
)

// Update 处理按键、窗口尺寸与 app 事件。它是纯状态转移：
// 不调用网络、不阻塞等待模型——模型 run 的结果以 AppEvent 到达。
func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return *m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case AppEvent:
		m.applyEvent(msg.Event)
		return *m, nil

	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return *m, cmd
	}
}

// handleKey 实现阶段计划 §2 的键位：Enter 发送、Ctrl+J 换行、
// PgUp/PgDn 滚动、Esc 取消、Ctrl+C 运行中取消/空闲退出、Ctrl+N 新会话。
func (m *Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.submitInput()
	case "ctrl+j":
		m.input.InsertString("\n")
		return *m, nil
	case "pgup":
		m.viewport.HalfViewUp()
		return *m, nil
	case "pgdown":
		m.viewport.HalfViewDown()
		return *m, nil
	case "esc":
		if m.state == stateRunning {
			return *m, cancelCmd(m.hooks.Cancel, m.runID)
		}
		return *m, nil
	case "ctrl+c":
		if m.state == stateRunning {
			return *m, cancelCmd(m.hooks.Cancel, m.runID)
		}
		return *m, tea.Quit
	case "ctrl+n":
		if m.state == stateRunning {
			m.notice = "A run is in progress; cancel it first (Esc), then press Ctrl+N."
			return *m, nil
		}
		m.sessionReset()
		return *m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return *m, cmd
	}
}

type cancelRequestedMsg struct{ runID string }

// cancelCmd 触发取消钩子并返回一条本地确认消息（不依赖 app 事件到达）。
func cancelCmd(cancel func(), runID string) tea.Cmd {
	return func() tea.Msg {
		if cancel != nil {
			cancel()
		}
		return cancelRequestedMsg{runID}
	}
}

func (m *Model) submitInput() (Model, tea.Cmd) {
	if m.state == stateRunning {
		m.notice = "A run is in progress; wait for it to finish or press Esc to cancel."
		return *m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return *m, nil // 空输入不产生请求
	}
	if m.hooks.Submit == nil {
		m.notice = "chat is not wired to a backend"
		return *m, nil
	}
	runID, err := m.hooks.Submit(text)
	if err != nil {
		m.appendLine(lineError, "submit rejected: "+err.Error())
		return *m, nil
	}
	m.appendLine(lineUser, text)
	m.input.Reset()
	m.startRun(runID)
	return *m, nil
}

// sessionReset 实现 Ctrl+N：清空上下文与聊天记录。新会话不串上下文。
func (m *Model) sessionReset() {
	if m.hooks.ResetSession != nil {
		m.hooks.ResetSession()
	}
	m.lines = nil
	m.lastUsage = ""
	m.syncViewport()
	m.appendLine(lineSystem, "New session started. Context cleared.")
}

// applyEvent 把 app 事件落到界面状态。终态事件让 run 收尾；
// 失败不污染聊天历史，只追加错误行。
func (m *Model) applyEvent(event app.Event) {
	switch event.Kind {
	case app.EventRunStarted:
		m.startRun(event.RunID)
	case app.EventRunCompleted:
		m.appendLine(lineAssistant, sanitize(event.Reply))
		m.lastUsage = usageLabel(event.Usage)
		m.notice = ""
		m.endRun()
	case app.EventRunFailed:
		m.appendLine(lineError, classifyFailure(event.Err))
		m.notice = ""
		m.endRun()
	}
}

// usageLabel 把 usage 转为状态栏文本；未知 usage 显示 unknown，不当 0。
func usageLabel(usage model.Usage) string {
	if !usage.OK {
		return "usage unknown"
	}
	return fmt.Sprintf("tokens %d/%d/%d", usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
}

// classifyFailure 把失败转换为用户可读的一行（错误分类可见，Key 不可见）。
func classifyFailure(err error) string {
	if err == nil {
		return "run failed"
	}
	return "run failed: " + err.Error()
}

// resize 依据窗口大小重排组件。
func (m *Model) resize() {
	inputHeight := m.input.Height()
	logHeight := max(
		// 状态栏一行
		m.height-inputHeight-1, 1)
	m.viewport = viewport.New(m.width, logHeight)
	m.viewport.SetContent(renderLines(m.lines, m.width))
	m.viewport.GotoBottom()
	m.input.SetWidth(m.width)
}

// elapsed 是状态栏的运行耗时。
func (m *Model) elapsed() time.Duration {
	if m.state != stateRunning {
		return 0
	}
	return time.Since(m.startedAt).Round(time.Millisecond)
}
