// Package tui 是聊天终端界面（Bubble Tea v2）。它只做输入、状态与渲染：
// 不发送模型 HTTP、不执行工具、不解析日志——一切运行状态来自 app.Service
// 的事件（阶段计划 §2/§4）。
package tui

import (
	"runtime"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"plume-agent/internal/app"
)

// maxInputLines 是输入区一次可见的最大行数（G1b.2.1 键位契约 §3.3：
// 随换行/折行长高，最多 4 行，超出输入框内部滚动）。由 textarea 的
// DynamicHeight + MinHeight/MaxHeight 原生实现。
const (
	minInputLines = 1
	maxInputLines = 4
)

// ctrlCDoublePress 是 Ctrl+C 空闲双击退出的判定窗口（契约 §5.2）。
const ctrlCDoublePress = time.Second

// lineKind 是聊天记录一行的类别。
type lineKind int

const (
	lineUser lineKind = iota
	lineAssistant
	lineSystem // 状态/提示（会话重置、取消确认等）
	lineError  // 失败与被拒绝的提交
	lineSplash // 开屏行（docs/tui-splash.md；不折行，role 决定配色）
)

type chatLine struct {
	kind       lineKind
	text       string
	role       splashRole // 仅 kind==lineSplash 时使用
	reasoning  string     // 原始累积思考，显示时净化
	expanded   bool
	manualFold bool   // 手动选择优先于首答案自动折叠
	terminal   string // cancelled/incomplete
	cache      string
	cacheRaw   string
	cacheWidth int
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
	// FirstAnswer 在 TeaModel.View 首次包含可见答案时记录帧耗时。
	FirstAnswer func(runID string, latency time.Duration)
}

// Model 是聊天界面的全部状态（经 TeaModel 适配 tea.Model）。
type Model struct {
	width, height int

	lines         []chatLine
	viewport      viewport.Model
	readingFrozen bool

	input textarea.Model

	state      busyState
	runID      string
	startedAt  time.Time
	lastUsage  string // 状态栏展示的已知 usage（unknown 不显示为 0）
	modelLabel string // 状态栏展示的模型/供应商标识

	notice string // 需要用户看到的一次性提示（如 busy 拒绝）
	hooks  Hooks
	keys   KeyMap

	// 输入草稿历史是 UI 层状态（契约 §5.3），不进 app 会话历史：
	// history 存本会话已提交输入，historyIdx 为 -1 表示停在编辑中的草稿，
	// draft 保存进入历史导航前的编辑内容。
	history    []string
	historyIdx int
	draft      string

	lastCtrlC       time.Time // Ctrl+C 双击判定窗口
	lastInputHeight int       // 上次同步给 viewport 的输入区高度
	lastStatusRows  int       // 上次同步给 viewport 的状态栏高度
	layoutReady     bool      // View 使用实际预留行数，避免时间/Thought 临界点重新改变栏高

	// splash 是开屏数据（cmd 注入）。首次 resize 按当前宽度渲染进 lines
	// 头部后置空——开屏只出现一次，窗口变化不重排，新会话不复活。
	splash                   *Splash
	options                  Options
	phase                    app.Phase
	phaseLabels              map[app.Phase]string
	activeLine               int
	lastFlush                time.Time
	dirty                    bool
	spinnerFrame             int
	lastSpinner              time.Time
	markdownRenders          int
	markdownEngine           *markdownEngine
	thoughtStart, thoughtEnd int
	answerStart, answerEnd   int
	firstAnswerRun           string
	firstAnswerAt            time.Time
	firstAnswerReported      bool
	status                   statusState
}

// AppEvent 包装一条 app.Service 事件供 Update 消费。cmd 层的桥接
// goroutine 通过 tea.Program.Send 投递它。
type AppEvent struct {
	Event app.Event
}

// New 构造聊天界面。modelLabel 展示在状态栏（如 deepseek/deepseek-flash
// 或 fake/offline）。
func New(modelLabel string, hooks Hooks) Model {
	return NewWithOptions(modelLabel, hooks, Options{})
}

// NewWithOptions 注入阶段文案、时钟和抽选器；旧 New 的调用保持兼容。
func NewWithOptions(modelLabel string, hooks Hooks, options Options) Model {
	options = normalizeOptions(options)
	input := textarea.New()
	input.Placeholder = "Say something… (Enter to send, Shift+Enter or \\ for newline)"
	input.CharLimit = 4096
	// 动态高度：随换行/折行 1→4 行，超出内部滚动（契约 §3.3）。
	input.DynamicHeight = true
	input.MinHeight = minInputLines
	input.MaxHeight = maxInputLines
	input.SetWidth(60)
	input.SetHeight(minInputLines)
	// 用真实终端光标而非字符反色模拟的虚拟光标：输入法候选窗跟随终端
	// 光标位置（TeaModel.View 会把它定位到插入点），否则候选窗会出现在
	// 帧渲染结束的位置（输入框右侧）。
	input.SetVirtualCursor(false)
	input.Focus()

	m := Model{
		state:           stateIdle,
		modelLabel:      modelLabel,
		input:           input,
		hooks:           hooks,
		keys:            newKeyMap(runtime.GOOS),
		historyIdx:      -1,
		lastInputHeight: minInputLines,
		options:         options,
		activeLine:      -1,
		thoughtStart:    -1,
		answerStart:     -1,
	}
	m.status.sessionStarted = options.Clock()
	if options.InitialStats != nil {
		m.status.stats = *options.InitialStats
		m.status.sessionStarted = options.InitialStats.StartedAt
	}
	if options.ModelID == "" {
		if p, id, found := strings.Cut(modelLabel, "/"); found {
			m.options.Provider, m.options.ModelID = p, id
		} else {
			m.options.ModelID = modelLabel
		}
	}
	m.initializeEmptyContext()
	return m
}

// Init 实现 tea.Model。
func (m *Model) Init() tea.Cmd { return m.nextStatusTick() }

// AddSystemLine 追加一条系统提示行（装配层启动时展示欢迎/键位说明）。
func (m *Model) AddSystemLine(text string) { m.appendLine(lineSystem, text) }

// SetSplash 注入开屏数据；首次 resize（拿到真实窗口宽度）时渲染进记录区头部。
func (m *Model) SetSplash(s Splash) { m.splash = &s }

// TeaModel 把 Model 适配为 tea.Model：Bubble Tea 的接口要求 Update 返回
// tea.Model，而 Model 的值语义 Update 返回自身（便于测试直接调用）。
// v2 的 View 返回 tea.View，alt screen 与鼠标模式也在这里声明。
type TeaModel struct{ M Model }

// Init 实现 tea.Model。
func (t *TeaModel) Init() tea.Cmd { return t.M.Init() }

// Update 转发到 Model.Update 并保持值语义。
func (t *TeaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := t.M.Update(msg)
	t.M = m
	return t, cmd
}

// View 转发到 Model.View 并声明 alt screen 与鼠标滚轮滚动。
// 鼠标若与终端文本选择冲突，可在终端按住 Shift 选择（契约 §3.2）。
func (t *TeaModel) View() tea.View {
	v := tea.NewView(t.M.View())
	t.M.reportFirstAnswer()
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// 声明终端光标位置：渲染器会把硬件光标移到插入点，输入法候选窗
	// （跟随终端光标）由此出现在正在输入的文字旁，而不是帧的右下角。
	v.Cursor = t.M.inputCursor()
	return v
}

// inputCursor 返回输入框插入点在整帧中的终端光标位置；未聚焦时为 nil。
// textarea.Cursor 给出的是相对输入框自身的坐标，这里平移到输入区在帧内
// 的实际行（记录区高度 + 上分隔线一行），并把 X 从字符数修正为显示列宽
// （中文等双宽字符下两者不同）。
func (m *Model) inputCursor() *tea.Cursor {
	c := m.input.Cursor()
	if c == nil {
		return nil
	}
	info := m.input.LineInfo()
	c.Position.X += max(info.ColumnOffset-info.CharOffset, 0)
	c.Position.Y += m.viewport.Height() + 1
	return c
}

// appendLine 追加一行聊天记录并滚动到底部。
func (m *Model) appendLine(kind lineKind, text string) {
	m.lines = append(m.lines, chatLine{kind: kind, text: text})
	m.syncViewport()
}

// syncViewport 用当前行内容刷新滚动区。
func (m *Model) syncViewport() {
	if !m.viewport.AtBottom() {
		m.readingFrozen = true
	}
	follow := !m.readingFrozen
	offset := m.viewport.YOffset()
	m.viewport.SetContent(m.renderHistory())
	if follow {
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(offset)
	}
}

// syncLayout 在输入区高度变化（换行/折行/重置）时重排 viewport。
// textarea 的 DynamicHeight 自身维护高度，这里只做联动。
func (m *Model) syncLayout() {
	if m.input.Height() != m.lastInputHeight || m.desiredStatusRows() != m.lastStatusRows {
		m.resize()
	}
}

// startRun 更新状态栏并记录起点。
func (m *Model) startRun(runID string) {
	if m.state == stateRunning && m.runID == runID {
		return
	}
	m.state = stateRunning
	m.status.cacheBeforeRun = &cacheSnapshot{stats: m.status.stats, usage: m.status.usage}
	m.status.usageCurrent = false
	m.runID = runID
	m.startedAt = m.options.Clock()
	m.notice = ""
	m.phaseLabels = make(map[app.Phase]string)
	m.phase = app.PhasePreparing
	m.selectPhase(m.phase)
	m.activeLine = -1
	m.spinnerFrame = 0
	m.lastSpinner = m.startedAt
	m.lastFlush = time.Time{}
	m.firstAnswerRun = ""
	m.firstAnswerReported = false
}

// endRun 回到空闲态。
func (m *Model) endRun() {
	m.status.lastRunID = m.runID
	m.status.runDuration = max(m.options.Clock().Sub(m.startedAt), 0)
	m.state = stateIdle
	m.runID = ""
}
