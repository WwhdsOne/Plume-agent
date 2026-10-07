// Package tui 是聊天终端界面（Bubble Tea）。它只做输入、状态与渲染：
// 不发送模型 HTTP、不执行工具、不解析日志——一切运行状态来自 app.Service
// 的事件（阶段计划 §2/§4）。
package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"plume-agent/internal/app"
)

// lineKind 是聊天记录一行的类别。
type lineKind int

const (
	lineUser lineKind = iota
	lineAssistant
	lineSystem // 状态/提示（会话重置、取消确认等）
	lineError  // 失败与被拒绝的提交
)

type chatLine struct {
	kind lineKind
	text string
}

// busyState 是状态栏的运行状态。
type busyState string

const (
	stateIdle    busyState = "idle"
	stateRunning busyState = "running"
)

// Hooks 把 TUI 连接到 app 层，由 cmd 装配注入。Update 是纯状态转移，
// 副作用只经这三个函数离开界面层。
type Hooks struct {
	// Submit 提交一条用户输入（app.Service.Submit）；返回 run ID 或拒绝错误。
	Submit func(input string) (runID string, err error)
	// Cancel 打断当前 run（app.Service.Cancel）。
	Cancel func()
	// ResetSession 清空会话上下文（app.Session.Reset）。
	ResetSession func()
}

// Model 是聊天界面的全部状态（实现 tea.Model）。
type Model struct {
	width, height int

	lines    []chatLine
	viewport viewport.Model

	input textarea.Model

	state      busyState
	runID      string
	startedAt  time.Time
	lastUsage  string // 状态栏展示的已知 usage（unknown 不显示为 0）
	modelLabel string // 状态栏展示的模型/供应商标识

	notice string // 需要用户看到的一次性提示（如 busy 拒绝）
	hooks  Hooks
}

// AppEvent 包装一条 app.Service 事件供 Update 消费。cmd 层的桥接
// goroutine 通过 tea.Program.Send 投递它。
type AppEvent struct {
	Event app.Event
}

// New 构造聊天界面。modelLabel 展示在状态栏（如 deepseek/deepseek-flash
// 或 fake/offline）。
func New(modelLabel string, hooks Hooks) Model {
	input := textarea.New()
	input.Placeholder = "Say something… (Enter to send, Ctrl+J newline)"
	input.CharLimit = 4096
	input.SetWidth(60)
	input.SetHeight(3)
	input.Focus()

	return Model{
		state:      stateIdle,
		modelLabel: modelLabel,
		input:      input,
		hooks:      hooks,
	}
}

// Init 实现 tea.Model。
func (m *Model) Init() tea.Cmd { return nil }

// AddSystemLine 追加一条系统提示行（装配层启动时展示欢迎/键位说明）。
func (m *Model) AddSystemLine(text string) { m.appendLine(lineSystem, text) }

// TeaModel 把 Model 适配为 tea.Model：Bubble Tea 的接口要求 Update 返回
// tea.Model，而 Model 的值语义 Update 返回自身（便于测试直接调用）。
type TeaModel struct{ M Model }

// Init 实现 tea.Model。
func (t *TeaModel) Init() tea.Cmd { return t.M.Init() }

// Update 转发到 Model.Update 并保持值语义。
func (t *TeaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := t.M.Update(msg)
	t.M = m
	return t, cmd
}

// View 转发到 Model.View。
func (t *TeaModel) View() string { return t.M.View() }

// appendLine 追加一行聊天记录并滚动到底部。
func (m *Model) appendLine(kind lineKind, text string) {
	m.lines = append(m.lines, chatLine{kind: kind, text: text})
	m.syncViewport()
}

// syncViewport 用当前行内容刷新滚动区。
func (m *Model) syncViewport() {
	m.viewport.SetContent(renderLines(m.lines, m.width))
	m.viewport.GotoBottom()
}

// startRun 更新状态栏并记录起点。
func (m *Model) startRun(runID string) {
	m.state = stateRunning
	m.runID = runID
	m.startedAt = time.Now()
	m.notice = ""
}

// endRun 回到空闲态。
func (m *Model) endRun() {
	m.state = stateIdle
	m.runID = ""
}
