package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"plume-agent/internal/app"
)

// commandResultMsg 将慢存储/运行时装配的回执带回主循环。sequence 防止旧回执
// 覆盖后来的输入；raw 只用于成功后清理本次输入，不擦除等待期间编辑的新草稿。
type commandResultMsg struct {
	result                 app.CommandResult
	sequence               uint64
	raw                    string
	text                   string
	presentationGeneration uint64
	presentationOnly       bool
	previewTarget          string
}

// commandPanel 保留目录与可选预览；它不替换当前会话，也不把管理回执放入转录。
type commandPanel struct {
	sessions *app.SessionList
	history  *app.HistoryList
	selected int
	scroll   int
	details  bool
	title    string
	text     string
}

// routeInput 仅安排钩子；相同输入的连按 Enter 不建立第二个异步请求。
func (m *Model) routeInput(text string, fromInput bool) tea.Cmd {
	if m.hooks.Command == nil {
		m.setNotice("commands are not wired to a backend")
		return nil
	}
	if m.commandPending {
		m.setNotice("An input is being processed; wait for its result.")
		return nil
	}
	m.commandPending = true
	m.pendingMessage = false
	m.pendingAcceptedRunID = ""
	m.pendingCancel, m.pendingCancelSent = false, false
	presentationOnly, previewTarget := false, ""
	if parsed, err := app.ParseInput(text); err == nil {
		m.pendingMessage = parsed.Kind == app.InputMessage
		if parsed.Kind == app.InputCommand {
			definition, _ := app.LookupCommand(string(parsed.Command.Name))
			presentationOnly = definition.ReadOnly || (parsed.Command.Name == app.CommandResume && parsed.Command.SessionID == "")
			if parsed.Command.Name == app.CommandHistory && m.panel != nil && m.panel.sessions != nil {
				previewTarget = parsed.Command.SessionID
			}
		}
	} else if m.modelLabel == "fake/offline" {
		// 离线演示由 CLI 显式归一化为普通正文；这里只识别其等待取消意图，
		// 不执行或泛化未知指令，也不影响在线命令错误路径。
		trimmed := strings.TrimSpace(text)
		m.pendingMessage = (trimmed == "/demo" || strings.HasPrefix(trimmed, "/demo ")) && !strings.ContainsAny(trimmed, "\r\n")
	}
	m.commandSequence++
	sequence := m.commandSequence
	presentationGeneration := m.presentationGeneration
	raw := ""
	if fromInput {
		raw = m.input.Value()
	}
	hook := m.hooks.Command
	return func() tea.Msg {
		return commandResultMsg{result: hook(text), sequence: sequence, raw: raw, text: text,
			presentationGeneration: presentationGeneration, presentationOnly: presentationOnly, previewTarget: previewTarget}
	}
}

// applyCommandResult 成功才消费输入与会话快照；失败保持原转录、统计、草稿和面板。
func (m *Model) applyCommandResult(msg commandResultMsg) tea.Cmd {
	if msg.sequence != 0 && msg.sequence != m.commandSequence {
		return nil
	}
	m.commandPending = false
	result := msg.result
	buffered := m.pendingEvents
	m.pendingEvents = nil
	cancelOnAccepted := m.pendingMessage && m.pendingCancel && !m.pendingCancelSent
	m.pendingMessage, m.pendingCancel, m.pendingCancelSent = false, false, false
	m.pendingAcceptedRunID = ""
	// Esc 或目录选中项变化只撤销展示意图。真正已提交的会话切换必须同步，
	// 否则 UI 与应用层会分叉；只读预览还要校验目标，不能把 A 附到 B 按钮下。
	if msg.sequence != 0 && result.Action != app.ActionSwitchSession && msg.presentationOnly {
		stale := msg.presentationGeneration != m.presentationGeneration
		if msg.previewTarget != "" {
			stale = stale || m.panel == nil || m.panel.sessions == nil || len(m.panel.sessions.Items) == 0
			if !stale {
				stale = m.panel.sessions.Items[m.panel.selected].ID != msg.previewTarget
			}
		}
		if stale {
			return nil
		}
	}
	if result.Code != "ok" && result.Code != "cancel_requested" && result.Code != "no_active_run" {
		m.setNotice(result.Code + ": " + result.Message)
		return nil
	}
	m.notice = sanitize(result.Message)
	if result.RunID != "" && result.Command == "" {
		text := msg.text
		if parsed, err := app.ParseInput(text); err == nil && parsed.Kind == app.InputMessage {
			text = parsed.Text
		}
		m.panel = nil
		m.completions = nil
		m.sessionID = result.SessionID
		m.appendLine(lineUser, text)
		m.pushHistory(text)
		if msg.raw != "" && m.input.Value() == msg.raw {
			m.input.Reset()
		}
		m.startRun(result.RunID)
		for _, event := range buffered {
			m.applyEvent(event)
		}
		m.resize()
		if cancelOnAccepted {
			cancel := cancelCmd(m.hooks.Cancel, result.RunID)
			if m.state == stateRunning {
				return tea.Batch(cancel, nextStreamTick(m.runID))
			}
			return cancel
		}
		if m.state == stateRunning {
			return nextStreamTick(m.runID)
		}
		return nil
	}
	if result.Action == app.ActionSwitchSession && result.Switch != nil {
		m.replaceSession(result.Switch)
		if result.Message != "" {
			m.appendLine(lineSystem, sanitize(result.Message))
		}
		return m.nextStatusTick()
	}
	// 选择器的 Esc 要返回打开前的输入，不能在打开时先清掉 /resume 草稿。
	if msg.raw != "" && m.input.Value() == msg.raw && result.Action != app.ActionSelectSession {
		m.input.Reset()
	}
	m.completions = nil
	switch result.Action {
	case app.ActionClear:
		m.panel = nil
		m.viewport.GotoBottom()
		m.readingFrozen = false
		m.resize()
		return tea.ClearScreen
	case app.ActionExit:
		return tea.Quit
	}
	switch {
	case result.Sessions != nil:
		m.panel = &commandPanel{sessions: result.Sessions, title: "Sessions"}
	case result.History != nil:
		if m.panel != nil && m.panel.sessions != nil {
			m.panel.history = result.History
			m.panel.scroll = 0
		} else {
			m.panel = &commandPanel{history: result.History, title: "History"}
		}
	case len(result.Help) > 0:
		var b strings.Builder
		for _, def := range result.Help {
			fmt.Fprintf(&b, "%s\n  %s\n", def.Usage, def.Description)
		}
		m.panel = &commandPanel{title: "Commands", text: b.String()}
	case result.Status != nil:
		status := result.Status
		state := "idle"
		if status.Running {
			state = "running · " + status.RunID
		}
		created := "unknown"
		if !status.CreatedAt.IsZero() {
			created = status.CreatedAt.Local().Format(time.RFC3339)
		}
		// 创建时间是元数据；耗时只来自已完成 run 的聚合，不使用会话年龄或打开时钟。
		text := fmt.Sprintf("Session: %s\nTitle: %s\nCreated: %s\nState: %s\nModel: %s/%s\nHistory: %d completed turns\nCompleted runs: %d\nCompleted run duration: %s\nUsage: %d calls (%d known) · %d tokens\nPersistence: %s",
			status.SessionID, status.Title, created, state, status.Model.Provider, status.Model.Model, status.Turns,
			status.CompletedRunCount, status.CompletedRunDuration,
			status.Stats.Calls, status.Stats.KnownCalls, status.Stats.TotalTokens, status.Persistence)
		if status.SaveStatus != "" {
			text += "\nSave status: " + status.SaveStatus
		}
		m.panel = &commandPanel{title: "Status", text: text}
	case len(result.Models) > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "Current: %s\n", m.modelLabel)
		for _, item := range result.Models {
			fmt.Fprintf(&b, "%s · %s/%s", item.ID, item.Provider, item.Model)
			if item.Reasoning != "" {
				fmt.Fprintf(&b, " · reasoning %s", item.Reasoning)
			}
			b.WriteByte('\n')
		}
		m.panel = &commandPanel{title: "Models (read only)", text: b.String()}
	}
	m.resize()
	return nil
}

// replaceSession 在应用层已经确认切换后重建全部显示状态；工具摘要与旧时钟
// 消息不复用，转录经 RunTranscriptLines 与 `-c` 共用转换。保留工作区采集
// 快照，但开屏永远不会随会话恢复重新出现。
func (m *Model) replaceSession(snapshot *app.SessionSnapshot) {
	m.sessionID = snapshot.SessionID
	m.options.Provider, m.options.ModelID = snapshot.Model.Provider, snapshot.Model.Model
	m.options.Reasoning = snapshot.Model.Reasoning
	m.options.ContextWindowTokens = snapshot.Model.ContextWindowTokens
	m.modelLabel = snapshot.Model.Provider + "/" + snapshot.Model.Model
	m.lines = nil
	m.splash = nil
	m.state, m.runID = stateIdle, ""
	m.phaseLabels = nil
	m.activeLine = -1
	m.toolLines = nil
	m.dirty = false
	m.readingFrozen = false
	m.firstAnswerRun = ""
	m.firstAnswerReported = false
	m.lastUsage = ""
	m.notice = ""
	m.panel = nil
	m.completions = nil
	m.completionDismissed = ""
	m.history, m.historyIdx, m.draft = nil, -1, ""
	m.input.Reset()
	m.status = statusState{sessionStarted: m.options.Clock(), workspace: m.status.workspace, stats: snapshot.Stats}
	if !snapshot.Stats.StartedAt.IsZero() {
		m.status.sessionStarted = snapshot.Stats.StartedAt
	}
	m.status.usage = snapshot.Stats.LastUsage
	if snapshot.Stats.Calls > 0 {
		m.status.contextLast = true
		if snapshot.Stats.LastUsage.OK {
			tokens := snapshot.Stats.LastUsage.PromptTokens
			m.status.contextTokens = &tokens
		}
	}
	m.initializeEmptyContext()
	for _, line := range RunTranscriptLines(snapshot.Runs, snapshot.Recovery) {
		entry := chatLine{text: sanitize(line.Text)}
		switch line.Kind {
		case TranscriptUser:
			entry.kind = lineUser
		case TranscriptAssistant:
			entry.kind = lineAssistant
			entry.reasoning = sanitize(line.Reasoning)
			entry.terminal = sanitize(line.Terminal)
		case TranscriptTool:
			entry.kind = lineTool
		default:
			entry.kind = lineSystem
		}
		m.lines = append(m.lines, entry)
	}
	// 恢复提示与回填候选只在恢复切换时出现；/new 只清空转录，不冒充恢复。
	// 提示是 system 展示行，不占状态栏；候选只进输入历史，用户编辑后按
	// Enter 才创建新 run。
	if snapshot.Resumed {
		for _, hint := range RecoveryHints(snapshot.Recovery, snapshot.Turns) {
			m.lines = append(m.lines, chatLine{kind: lineSystem, text: sanitize(hint)})
		}
	}
	if snapshot.Recovery != nil && snapshot.Recovery.ResendInput != "" {
		// 与 InitialResend 同一语义：历史存解码正文，取出时转换；超限
		// 丢弃候选时明确说明，不静默。
		if _, ok := escapeHistoryInput(snapshot.Recovery.ResendInput); ok {
			m.history = []string{snapshot.Recovery.ResendInput}
		} else {
			m.lines = append(m.lines, chatLine{kind: lineSystem, text: "Last task input exceeds the 4096-character limit; ↑ backfill is unavailable."})
		}
	}
	m.resize()
	m.syncViewport()
}

// refreshCompletions 只查询内置纯数据来源；未来来源可复用 app 的候选接口，
// 不让建议操作获得执行模型或工具的能力。
func (m *Model) refreshCompletions() {
	if m.panel != nil || m.input.Value() == m.completionDismissed {
		return
	}
	// Esc 的抑制只属于当时那份输入；编辑后重新键入相同前缀仍能打开建议。
	m.completionDismissed = ""
	previous := ""
	if m.completionIndex < len(m.completions) {
		previous = m.completions[m.completionIndex].Replacement
	}
	items, err := (app.BuiltinCompletionProvider{}).Complete(context.Background(), app.CompletionRequest{Text: m.input.Value(), Entrance: app.EntranceTUI})
	if err != nil {
		items = nil
	}
	changed := len(items) != len(m.completions)
	m.completions = items
	m.completionIndex = 0
	for i, candidate := range items {
		if candidate.Replacement == previous {
			m.completionIndex = i
			break
		}
	}
	if changed {
		m.resize()
	}
}

// suggestionRows 给候选最多六行空间，窄/矮窗口仍给输入和记录保留一行。
func (m *Model) suggestionRows() int {
	if m.panel != nil || len(m.completions) == 0 {
		return 0
	}
	return max(min(len(m.completions)+1, 6, m.height-6), 0)
}

func (m *Model) suggestionsView() string {
	rows := m.suggestionRows()
	if rows == 0 {
		return ""
	}
	lines := []string{styleSystem.Render(ansi.Truncate("Commands · ↑/↓ choose · Tab complete · Enter uses your text · Esc close", max(m.width, 1), ""))}
	start := max(m.completionIndex-(rows-2), 0)
	for i := start; i < min(start+rows-1, len(m.completions)); i++ {
		item := m.completions[i]
		text := "  " + item.Replacement + " · " + sanitize(item.Description)
		style := styleSystem
		if i == m.completionIndex {
			text = "> " + item.Replacement + " · " + sanitize(item.Description)
			style = styleStatus
		}
		lines = append(lines, style.Render(ansi.Truncate(text, max(m.width, 1), "")))
	}
	return strings.Join(lines, "\n")
}

// handlePanelKey 将导航限制在只读面板；Esc 始终返回原记录和草稿。
func (m *Model) handlePanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.panel
	km := m.keys
	switch {
	case key.Matches(msg, km.Cancel):
		m.presentationGeneration++
		m.panel = nil
		m.completionDismissed = m.input.Value()
		m.resize()
		return *m, nil
	case key.Matches(msg, km.NewSession):
		return *m, m.routeInput("/new", false)
	case key.Matches(msg, km.ToggleReasoning):
		p.details = !p.details
	case key.Matches(msg, km.HistoryUp):
		if p.sessions != nil && p.history == nil {
			previous := p.selected
			p.selected = max(p.selected-1, 0)
			if p.selected != previous {
				m.presentationGeneration++
			}
			p.history = nil
		} else {
			p.scroll = max(p.scroll-1, 0)
		}
	case key.Matches(msg, km.HistoryDown):
		if p.sessions != nil && p.history == nil {
			previous := p.selected
			p.selected = min(p.selected+1, max(len(p.sessions.Items)-1, 0))
			if p.selected != previous {
				m.presentationGeneration++
			}
			p.history = nil
		} else {
			p.scroll++
		}
	case key.Matches(msg, km.Complete):
		if p.sessions != nil && p.history != nil {
			m.presentationGeneration++
			p.history = nil
			p.scroll = 0
			return *m, nil
		}
		if p.sessions != nil && len(p.sessions.Items) > 0 {
			return *m, m.routeInput("/history --session "+p.sessions.Items[p.selected].ID, false)
		}
	case key.Matches(msg, km.Submit):
		if p.sessions != nil && len(p.sessions.Items) > 0 {
			return *m, m.routeInput("/resume "+p.sessions.Items[p.selected].ID, false)
		}
	case key.Matches(msg, km.ScrollUp, km.ScrollDown):
		delta := 1
		if key.Matches(msg, km.ScrollUp) {
			delta = -1
		}
		if p.history != nil {
			page := p.history.Page + delta
			if page >= 1 && (page-1)*p.history.PageSize < p.history.Total {
				return *m, m.routeInput(fmt.Sprintf("/history %d --session %s", page, p.history.SessionID), false)
			}
		} else if p.sessions != nil {
			page := p.sessions.Page + delta
			if page >= 1 && (page-1)*p.sessions.PageSize < p.sessions.Total {
				return *m, m.routeInput(fmt.Sprintf("/sessions %d", page), false)
			}
		} else {
			p.scroll = max(p.scroll+delta*max(m.viewport.Height()-2, 1), 0)
		}
	}
	return *m, nil
}

// panelView 每次按当前窗口宽度净化和换行；分页信息与选中 ID 保持可见，
// 保存过的标题和正文也不能信任其终端控制序列。
func (m *Model) panelView() string {
	p := m.panel
	width, height := max(m.width, 1), max(m.viewport.Height(), 1)
	if p.sessions != nil && p.history == nil {
		return m.sessionsPanelView(width, height)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · Esc close · ↑/↓ navigate\n", p.title)
	if p.history != nil {
		h := p.history
		fmt.Fprintf(&b, "History %s · page %d/%d · %d runs · PgUp/PgDn page · Ctrl+O details\n", h.SessionID, h.Page, max((h.Total+max(h.PageSize, 1)-1)/max(h.PageSize, 1), 1), h.Total)
		if p.sessions != nil {
			b.WriteString("Tab back to list · Enter resume selected session\n")
		}
		for _, run := range h.Runs {
			fmt.Fprintf(&b, "> %s\n● %s\n[%s]\n", sanitize(run.Input), sanitize(run.Reply), run.Status)
			if run.Reasoning != "" {
				if p.details {
					fmt.Fprintf(&b, "Thought:\n%s\n", sanitize(run.Reasoning))
				} else {
					b.WriteString("Thought · Ctrl+O to expand\n")
				}
			}
			if p.details {
				for _, tool := range run.ToolEvents {
					fmt.Fprintf(&b, "◇ %s · %s · %s\n", sanitize(tool.Name), sanitize(tool.Status), sanitize(tool.Summary))
				}
			} else if len(run.ToolEvents) > 0 {
				fmt.Fprintf(&b, "Tools: %d · Ctrl+O to expand\n", len(run.ToolEvents))
			}
		}
	}
	b.WriteString(sanitize(p.text))
	lines := wrapText(b.String(), width)
	start := min(p.scroll, max(len(lines)-height, 0))
	visible := lines[start:min(start+height, len(lines))]
	for len(visible) < height {
		visible = append(visible, "")
	}
	return strings.Join(visible, "\n")
}

// sessionsPanelView 固定选中项摘要在列表上方；每项只占一行，长中文标题
// 不会把后面的选择埋到窗口外。不可继续原因独立换行，不随横向裁剪消失。
func (m *Model) sessionsPanelView(width, height int) string {
	p, list := m.panel, m.panel.sessions
	pageSize := max(list.PageSize, 1)
	header := []string{
		fmt.Sprintf("Sessions · %d/%d · %d total", list.Page, max((list.Total+pageSize-1)/pageSize, 1), list.Total),
		"↑/↓ select · Tab preview · Enter resume · PgUp/PgDn page · Esc close",
	}
	if len(list.Items) > 0 {
		selected := list.Items[min(p.selected, len(list.Items)-1)]
		header = append(header, "Selected: "+sanitize(selected.Title))
		stamp := selected.LastRunAt
		if stamp.IsZero() {
			stamp = selected.CreatedAt
		}
		detail := fmt.Sprintf("%s/%s · %d turns / %d runs", selected.Provider, selected.Model, selected.TurnCount, selected.RunCount)
		if !stamp.IsZero() {
			detail += " · " + stamp.Local().Format("2006-01-02 15:04")
		}
		if height > 8 {
			header = append(header, detail)
		}
		if !selected.Resumable {
			header = append(header, "Cannot resume:")
			for _, line := range wrapText(sanitize(selected.Reason), width) {
				header = append(header, line)
				if len(header) >= max(height-1, 1) {
					break
				}
			}
		}
	}
	for i := range header {
		header[i] = ansi.Truncate(header[i], width, "")
	}
	if len(header) >= height {
		return strings.Join(header[:height], "\n")
	}
	rows := height - len(header)
	start := max(p.selected-rows+1, 0)
	start = min(start, max(len(list.Items)-rows, 0))
	for i := start; i < min(start+rows, len(list.Items)); i++ {
		item := list.Items[i]
		id := item.ID
		if len(id) > 8 {
			id = id[:8]
		}
		marker := "  "
		if i == p.selected {
			marker = "> "
		}
		text := marker + id + " · " + sanitize(item.Title)
		if width >= 60 {
			text += fmt.Sprintf(" · %d turns / %d runs", item.TurnCount, item.RunCount)
			if !item.LastRunAt.IsZero() {
				text += " · " + item.LastRunAt.Local().Format("01-02 15:04")
			}
			if !item.Resumable {
				text += " · unavailable"
			}
		}
		header = append(header, ansi.Truncate(text, width, ""))
	}
	for len(header) < height {
		header = append(header, "")
	}
	return strings.Join(header, "\n")
}
