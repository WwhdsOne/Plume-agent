package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"plume-agent/internal/app"
	"plume-agent/internal/model"
)

// Update 处理按键、窗口尺寸与 app 事件。它是纯状态转移：
// 不调用网络、不阻塞等待模型——模型 run 的结果以 AppEvent 到达。
func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case statusClockTick:
		if !msg.sessionStarted.Equal(m.status.sessionStarted) {
			return *m, nil
		}
		m.syncLayout()
		return *m, m.nextStatusTick()
	case WorkspaceEvent:
		if msg.Status.Dir == m.options.Dir {
			m.status.workspace = msg.Status
		}
		m.syncLayout()
		return *m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return *m, nil

	case tea.KeyPressMsg:
		// 双击判定窗口只连续的 Ctrl+C 才有效，中间插入其他键即重置。
		if msg.String() != "ctrl+c" {
			m.lastCtrlC = time.Time{}
		}
		updated, cmd := m.handleKey(msg)
		updated.syncLayout()
		return updated, cmd

	case tea.MouseWheelMsg:
		// 滚轮滚动聊天记录（v2 MouseModeCellMotion，见 TeaModel.View）。
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.viewport.ScrollUp(3)
		case tea.MouseWheelDown:
			m.viewport.ScrollDown(3)
		}
		m.readingFrozen = !m.viewport.AtBottom()
		m.syncLayout()
		return *m, nil

	case AppEvent:
		wasIdle := m.state == stateIdle
		m.applyEvent(msg.Event)
		m.syncLayout()
		if wasIdle && m.state == stateRunning {
			return *m, nextStreamTick(m.runID)
		}
		return *m, nil

	case runStreamTick:
		if msg.runID != m.runID || m.state != stateRunning {
			return *m, nil
		}
		return m.Update(msg.tick)

	case streamTick:
		if m.state != stateRunning {
			return *m, nil
		}
		if m.options.Clock().Sub(m.lastSpinner) >= 100*time.Millisecond {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(thoughtSpinner)
			m.lastSpinner = m.options.Clock()
		}
		if m.dirty && m.options.Clock().Sub(m.lastFlush) >= 50*time.Millisecond {
			m.flushStream()
		} else if m.phase == app.PhaseThinking {
			m.syncViewport()
		}
		m.syncLayout()
		return *m, nextStreamTick(m.runID)

	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.syncLayout()
		return *m, cmd
	}
}

// handleKey 按 KeyMap（docs/specs/tui/keys.md §3/§4）分发按键。Ctrl+C 的三段
// 语义不走 Binding（契约的 KeyMap 字段表不含它），在此用键名判定。
func (m *Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	km := m.keys
	switch {
	case msg.String() == "ctrl+c":
		return m.handleCtrlC()

	case key.Matches(msg, km.NewlineShift, km.NewlineAlt):
		// 层级 1/2：kitty 协议识别 Shift+Enter；Meta 终端识别 Alt+Enter。
		m.input.InsertString("\n")
		return *m, nil

	case key.Matches(msg, km.Submit):
		return m.submitInput()

	case key.Matches(msg, km.ScrollUp):
		m.viewport.HalfPageUp()
		m.readingFrozen = !m.viewport.AtBottom()
		return *m, nil

	case key.Matches(msg, km.ScrollDown):
		m.viewport.HalfPageDown()
		m.readingFrozen = !m.viewport.AtBottom()
		return *m, nil

	case key.Matches(msg, km.Cancel):
		if m.state == stateRunning {
			return *m, cancelCmd(m.hooks.Cancel, m.runID)
		}
		return *m, nil

	case key.Matches(msg, km.Quit):
		// Ctrl+D 仅空输入退出；有草稿时忽略，防误触丢内容。
		if strings.TrimSpace(m.input.Value()) == "" {
			return *m, tea.Quit
		}
		return *m, nil

	case key.Matches(msg, km.ClearScreen):
		m.viewport.GotoBottom()
		m.readingFrozen = false
		return *m, tea.ClearScreen

	case key.Matches(msg, km.HistoryUp):
		// 边缘导航：光标在第一行才翻历史，否则在多行输入内上移光标。
		if m.input.Line() > 0 {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return *m, cmd
		}
		return m.historyPrev()

	case key.Matches(msg, km.HistoryDown):
		// 光标在最后一行才翻回历史，否则在多行输入内下移光标。
		if m.input.Line() < m.input.LineCount()-1 {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return *m, cmd
		}
		return m.historyNext()

	case key.Matches(msg, km.NewSession):
		if m.state == stateRunning {
			m.setNotice("A run is in progress; cancel it first (Esc), then press Ctrl+N.")
			return *m, nil
		}
		m.sessionReset()
		return *m, m.nextStatusTick()

	case key.Matches(msg, km.ToggleReasoning):
		for i := len(m.lines) - 1; i >= 0; i-- {
			if m.state == stateRunning && i != m.activeLine {
				continue
			}
			if sanitize(m.lines[i].reasoning) != "" {
				m.lines[i].expanded = !m.lines[i].expanded
				m.lines[i].manualFold = true
				m.syncViewport()
				break
			}
		}
		return *m, nil

	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return *m, cmd
	}
}

// handleCtrlC 实现三段语义：running 取消 run；空闲有草稿清空草稿；
// 空草稿 1s 内两次退出（契约 §3.2）。
func (m *Model) handleCtrlC() (Model, tea.Cmd) {
	if m.state == stateRunning {
		m.lastCtrlC = time.Time{}
		return *m, cancelCmd(m.hooks.Cancel, m.runID)
	}
	if strings.TrimSpace(m.input.Value()) != "" {
		m.lastCtrlC = time.Time{}
		m.input.Reset()
		return *m, nil
	}
	now := time.Now()
	if !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) <= ctrlCDoublePress {
		return *m, tea.Quit
	}
	m.lastCtrlC = now
	return *m, nil
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
		m.setNotice("A run is in progress; wait for it to finish or press Esc to cancel.")
		return *m, nil
	}
	raw := m.input.Value()
	// 续行兜底（契约 §3.1 层级 3）：行尾 `\` 的 Enter 不发送，
	// 去掉 `\` 换行。判定只看原始值末尾，不做 trim。
	if before, ok := strings.CutSuffix(raw, "\\"); ok {
		m.input.SetValue(before)
		m.input.InsertString("\n")
		return *m, nil
	}
	text := strings.TrimSpace(raw)
	if text == "" {
		return *m, nil // 空输入不产生请求
	}
	if m.hooks.Submit == nil {
		m.setNotice("chat is not wired to a backend")
		return *m, nil
	}
	runID, err := m.hooks.Submit(text)
	if err != nil {
		m.appendCriticalLine(lineError, "submit rejected: "+err.Error())
		return *m, nil
	}
	m.appendLine(lineUser, text)
	m.pushHistory(text)
	m.input.Reset()
	m.startRun(runID)
	return *m, nextStreamTick(m.runID)
}

// historyPrev 实现 Up：从编辑中草稿进入历史最末一条，再往旧翻。
func (m *Model) historyPrev() (Model, tea.Cmd) {
	if len(m.history) == 0 {
		return *m, nil
	}
	if m.historyIdx == -1 {
		m.draft = m.input.Value()
		m.historyIdx = len(m.history) - 1
	} else if m.historyIdx > 0 {
		m.historyIdx--
	} else {
		return *m, nil // 已到最旧一条
	}
	m.input.SetValue(m.history[m.historyIdx])
	return *m, nil
}

// historyNext 实现 Down：往新翻，翻过最新一条时恢复编辑中的草稿。
func (m *Model) historyNext() (Model, tea.Cmd) {
	if m.historyIdx == -1 {
		return *m, nil
	}
	if m.historyIdx < len(m.history)-1 {
		m.historyIdx++
		m.input.SetValue(m.history[m.historyIdx])
		return *m, nil
	}
	m.historyIdx = -1
	m.input.SetValue(m.draft)
	m.draft = ""
	return *m, nil
}

// pushHistory 在成功提交后记录输入；连续重复只记一次，历史上限 200 条。
func (m *Model) pushHistory(text string) {
	if len(m.history) > 0 && m.history[len(m.history)-1] == text {
		m.historyIdx = -1
		m.draft = ""
		return
	}
	m.history = append(m.history, text)
	if len(m.history) > 200 {
		m.history = m.history[len(m.history)-200:]
	}
	m.historyIdx = -1
	m.draft = ""
}

// sessionReset 实现 Ctrl+N：清空上下文、聊天记录与输入草稿历史。
// 新会话不串上下文。
func (m *Model) sessionReset() {
	if m.hooks.ResetSession != nil {
		m.hooks.ResetSession()
	}
	m.lines = nil
	m.readingFrozen = false
	m.activeLine = -1
	m.firstAnswerRun = ""
	m.lastUsage = ""
	m.status = statusState{sessionStarted: m.options.Clock(), workspace: m.status.workspace}
	m.initializeEmptyContext()
	m.notice = ""
	m.history = nil
	m.historyIdx = -1
	m.draft = ""
	m.input.Reset()
	m.syncViewport()
	m.appendLine(lineSystem, "New session started. Context cleared.")
}

// applyEvent 把 app 事件落到界面状态。终态事件让 run 收尾；
// 失败不污染聊天历史，只追加错误行。
func (m *Model) applyEvent(event app.Event) {
	if event.Kind == app.EventRunStarted {
		if m.state == stateIdle {
			return
		} // 旧 run 的迟到 started 不复活会话
		if event.RunID == m.runID {
			m.startRun(event.RunID)
			if !event.AcceptedAt.IsZero() {
				m.startedAt = event.AcceptedAt
				if m.firstAnswerRun == event.RunID {
					m.firstAnswerAt = event.AcceptedAt
				}
			}
		}
		return
	}
	if m.state != stateRunning || event.RunID != m.runID {
		return
	}
	m.updateStatus(event)
	switch event.Kind {
	case app.EventToolUpdate:
		tool := event.Tool
		text := sanitize(tool.Name + " · " + tool.Status)
		if tool.Summary != "" {
			text += " · " + sanitize(tool.Summary)
		}
		if index, ok := m.toolLines[tool.CallID]; ok && index < len(m.lines) {
			m.lines[index].text = text
		} else {
			if m.toolLines == nil {
				m.toolLines = make(map[string]int)
			}
			m.toolLines[tool.CallID] = len(m.lines)
			m.lines = append(m.lines, chatLine{kind: lineTool, text: text})
		}
		m.syncViewport()
	case app.EventRunPhase:
		if event.Phase == app.PhaseWaiting && event.ModelCall > 1 && m.activeLine >= 0 {
			// 每个模型步骤独立展示，保持思考→工具→最终答案的时间顺序。
			if !m.lines[m.activeLine].manualFold {
				m.lines[m.activeLine].expanded = false
			}
			m.flushStream()
			m.activeLine = -1
		}
		// 首答案之后即使供应商继续发思考，也不退回 thinking。
		if m.phase == app.PhaseResponding && event.Phase == app.PhaseThinking {
			return
		}
		m.selectPhase(event.Phase)
		m.syncViewport()
	case app.EventReasoningDelta:
		if event.ReasoningDelta == "" {
			return
		}
		line := m.ensureAssistant()
		line.reasoning += event.ReasoningDelta
		if m.phase != app.PhaseResponding {
			m.selectPhase(app.PhaseThinking)
		}
		m.dirty = true
		// 思考预览也合并刷新，首片段立即显示。
		if m.lastFlush.IsZero() || m.options.Clock().Sub(m.lastFlush) >= 50*time.Millisecond {
			m.flushStream()
		}
	case app.EventTextDelta:
		if event.TextDelta == "" {
			return
		}
		line := m.ensureAssistant()
		first := line.text == ""
		line.text += event.TextDelta
		m.selectPhase(app.PhaseResponding)
		if !line.manualFold {
			line.expanded = false
		}
		m.dirty = true
		if first || m.options.Clock().Sub(m.lastFlush) >= 50*time.Millisecond {
			m.flushStream()
		}
	case app.EventRunCompleted:
		line := m.ensureAssistant()
		line.text = event.Reply
		if event.Reasoning != "" {
			line.reasoning = event.Reasoning
		}
		if !line.manualFold {
			line.expanded = false
		}
		m.lastUsage = usageLabel(event.Usage)
		m.notice = ""
		m.selectPhase(app.PhaseResponding)
		m.flushStream()
		m.endRun()
		if event.Duration > 0 {
			m.status.runDuration = event.Duration
		}
		m.syncViewport()
	case app.EventRunFailed:
		if event.Reply != "" || event.Reasoning != "" || m.activeLine >= 0 {
			line := m.ensureAssistant()
			if event.Reply != "" {
				line.text = event.Reply
			}
			if event.Reasoning != "" {
				line.reasoning = event.Reasoning
			}
			line.terminal = "incomplete"
			var modelErr *model.Error
			if errors.Is(event.Err, context.Canceled) || (errors.As(event.Err, &modelErr) && modelErr.Code == model.ErrCancelled) {
				line.terminal = "cancelled"
			}
		}
		m.appendCriticalLine(lineError, classifyFailure(event.Err))
		m.notice = ""
		m.flushStream()
		m.endRun()
		if event.Duration > 0 {
			m.status.runDuration = event.Duration
		}
		m.syncViewport()
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

// resize 依据窗口大小与输入区当前高度重排组件。
func (m *Model) resize() {
	// 颜色能力、环境快照等启动消息可能先到，拿到有效尺寸后再消耗开屏。
	if m.width <= 0 || m.height <= 0 {
		return
	}
	if m.splash != nil {
		// 开屏按当前宽度一次性渲染（窄端降级为纯文本标题），之后不再重排。
		m.lines = append(buildSplashLines(m.width, *m.splash), m.lines...)
		m.splash = nil
	}
	if !m.viewport.AtBottom() {
		m.readingFrozen = true
	}
	follow := !m.readingFrozen
	offset := m.viewport.YOffset()
	layout := func(rows int) {
		m.input.MaxHeight = max(min(maxInputLines, m.height-3-rows), 1)
		m.input.SetWidth(m.width)
		logHeight := max(m.height-m.input.Height()-2-rows, 1)
		m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(logHeight))
		m.viewport.SetContent(m.renderHistory())
		if follow {
			m.viewport.GotoBottom()
		} else {
			m.viewport.SetYOffset(offset)
		}
	}
	// Thought 可见性会随 viewport 高度变化，先收敛布局再保存实际预留的行数。
	rows := m.desiredStatusRows()
	tried := [3]bool{}
	for {
		tried[rows] = true
		layout(rows)
		next := m.desiredStatusRows()
		if next == rows {
			break
		}
		if tried[next] {
			rows = max(rows, next)
			layout(rows)
			break
		}
		rows = next
	}
	m.lastInputHeight = m.input.Height()
	m.lastStatusRows = rows
	m.layoutReady = true
}

// elapsed 是状态栏的运行耗时。
func (m *Model) elapsed() time.Duration {
	if m.state != stateRunning {
		return 0
	}
	return m.options.Clock().Sub(m.startedAt).Round(time.Millisecond)
}
