package tui

import (
	"math/rand/v2"
	"strings"
	"time"

	"plume-agent/internal/app"
	"plume-agent/internal/config"
)

// Options 是 TUI 的展示策略；Clock 与 Choose 只用于可重复的时钟和抽选。
type Options struct {
	StatusMessages                             map[string][]string
	StatusLine                                 *config.StatusLineConfig
	Provider, ModelID, Reasoning, Dir, HomeDir string
	ContextWindowTokens                        *int64
	InitialStats                               *app.SessionStats
	// InitialSessionID 绑定启动会话身份；之后只接受该会话与当前 run 的事件。
	InitialSessionID string
	// InitialTranscript 是恢复会话时预置进聊天记录的展示行（`-c` 路径）；
	// 输入草稿与 Up/Down 草稿历史不从磁盘恢复。
	InitialTranscript []TranscriptLine
	// InitialResend 是恢复会话的最后未完成输入候选：转义后只放入本进程
	// 输入历史（↑ 取回），不自动填入输入框，也不发送。
	InitialResend string
	Clock         func() time.Time
	Choose        func(int) int
}

// TranscriptKind 是恢复转录行的展示类别。
type TranscriptKind int

// 转录行类别：user/assistant 对应聊天记录，system 用于恢复提示，
// tool 是脱敏工具摘要行。
const (
	TranscriptUser TranscriptKind = iota
	TranscriptAssistant
	TranscriptSystem
	TranscriptTool
)

// TranscriptLine 是持久历史的一行展示文本；Terminal 携带取消/中断等
// 终态标记（仅 assistant 行使用），渲染沿用现有净化路径。
type TranscriptLine struct {
	Kind      TranscriptKind
	Text      string
	Reasoning string // 仅 assistant 展示思考；恢复时净化，不作为模型 system 来源。
	Terminal  string
}

// DefaultStatusMessages 每次返回独立默认集合，调用方可安全修改。
func DefaultStatusMessages() map[string][]string {
	return config.DefaultStatusMessages()
}

// ResolvedStatusMessages 清理空白并补齐缺失/null/空候选。
func ResolvedStatusMessages(overrides map[string][]string) map[string][]string {
	resolved := DefaultStatusMessages()
	for stage := range resolved {
		var candidates []string
		for _, candidate := range overrides[stage] {
			if s := strings.TrimSpace(sanitize(candidate)); s != "" {
				candidates = append(candidates, s)
			}
		}
		if len(candidates) > 0 {
			resolved[stage] = candidates
		}
	}
	return resolved
}

func normalizeOptions(options Options) Options {
	if options.StatusLine == nil {
		options.StatusLine = config.DefaultStatusLine()
	}
	options.StatusMessages = ResolvedStatusMessages(options.StatusMessages)
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Choose == nil {
		options.Choose = rand.IntN
	}
	return options
}

func (m *Model) selectPhase(phase app.Phase) {
	if m.phaseLabels == nil {
		m.phaseLabels = make(map[app.Phase]string)
	}
	if _, ok := m.phaseLabels[phase]; !ok {
		candidates := m.options.StatusMessages[string(phase)]
		if len(candidates) == 0 {
			candidates = []string{string(phase)}
		}
		index := m.options.Choose(len(candidates))
		if index < 0 || index >= len(candidates) {
			index = 0
		}
		m.phaseLabels[phase] = candidates[index]
	}
	m.phase = phase
}
