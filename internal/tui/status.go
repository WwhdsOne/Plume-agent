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
	StatusMessages map[string][]string
	Clock          func() time.Time
	Choose         func(int) int
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
