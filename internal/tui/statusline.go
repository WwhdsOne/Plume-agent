package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type statusState struct {
	sessionStarted time.Time
	stats          app.SessionStats
	usage          model.Usage
	usageCurrent   bool
	cacheBeforeRun *cacheSnapshot
	contextTokens  *int64
	contextLast    bool
	workspace      app.WorkspaceStatus
	lastRunID      string
	runDuration    time.Duration
}

// cacheSnapshot 保留等待新缓存统计时的展示值，不改变实际调用与用量累计。
type cacheSnapshot struct {
	stats app.SessionStats
	usage model.Usage
}

// WorkspaceEvent 来自装配层的异步本地采集，View 不访问文件或进程。
type WorkspaceEvent struct{ Status app.WorkspaceStatus }
type statusClockTick struct{ sessionStarted time.Time }

func (m *Model) nextStatusTick() tea.Cmd {
	if m.statusRows() == 0 {
		return nil
	}
	needed := false
	for _, item := range m.options.StatusLine.Items {
		if item.Enabled && item.ID == "session_elapsed" {
			needed = true
		}
	}
	if !needed {
		return nil
	}
	generation := m.status.sessionStarted
	return tea.Tick(time.Duration(m.options.StatusLine.ClockRefreshMS)*time.Millisecond, func(time.Time) tea.Msg {
		return statusClockTick{generation}
	})
}

func (m *Model) statusRows() int {
	if m.layoutReady {
		return m.lastStatusRows
	}
	return m.desiredStatusRows()
}

func (m *Model) desiredStatusRows() int {
	cfg := m.options.StatusLine
	if cfg == nil || !cfg.Enabled {
		return 0
	}
	exist := false
	for _, item := range cfg.Items {
		if item.Enabled {
			exist = true
			break
		}
	}
	if !exist {
		return 0
	}
	if cfg.MaxRows == 1 || m.width < 60 || m.height < 18 {
		return 1
	}
	// 先尝试合并所有已选字段；条体可缩至三格，不为合并而隐藏字段。
	maxBarWidth := max(cfg.ContextBar.Width, cfg.CacheBar.Width)
	for shrink := 0; shrink <= maxBarWidth-3; shrink++ {
		var values []string
		for _, item := range cfg.Items {
			if item.Enabled {
				if text, _ := m.statusValue(item, max(m.statusBarWidth(item)-shrink, 3)); text != "" {
					values = append(values, text)
				}
			}
		}
		if ansi.StringWidth(strings.Join(values, sanitize(cfg.Separator))) <= max(m.width, 1) {
			return 1
		}
	}
	return 2
}

func (m *Model) statusBarWidth(item config.StatusItemConfig) int {
	if item.ID == "cache" {
		return m.options.StatusLine.CacheBar.Width
	}
	return m.options.StatusLine.ContextBar.Width
}

func formatToken(n int64, style string) string {
	if style == "full" || n < 1000 {
		return fmt.Sprint(n)
	}
	div, unit := 1000.0, "k"
	if n >= 1000000 {
		div, unit = 1000000, "M"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/div), ".0") + unit
}

func formatStatusTime(d time.Duration, style string) string {
	d = max(d, 0)
	if style == "clock" {
		seconds := int64(d / time.Second)
		return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return d.Truncate(time.Second).String()
}

// percent 保留一位精度，展示舍入不冒充 token 估算。
func percent(value float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0") + "%" }

func contextBar(tokens *int64, capacity *int64, cfg config.ContextBarConfig, width int, tokenFormat string) (string, bool) {
	width = max(width, 3)
	if capacity == nil || *capacity <= 0 {
		return "capacity unknown", false
	}
	if tokens == nil {
		return "usage unknown", false
	}
	ratio := float64(*tokens) / float64(*capacity)
	bar := tokenBar(ratio, width, cfg.Style)
	if cfg.ShowPercent || ratio > 1 {
		bar += " " + percent(ratio*100)
	}
	bar += " " + formatToken(*tokens, tokenFormat) + "/" + formatToken(*capacity, tokenFormat)
	return renderContextValue(bar, ratio, cfg), true
}

func tokenBar(ratio float64, width int, style string) string {
	width = max(width, 3)
	filled := int(math.Floor(min(max(ratio, 0), 1) * float64(width)))
	fill, empty := "█", "░"
	if style == "ascii" {
		fill, empty = "#", "-"
	}
	return "[" + strings.Repeat(fill, filled) + strings.Repeat(empty, width-filled) + "]"
}

func renderContextValue(text string, ratio float64, cfg config.ContextBarConfig) string {
	switch {
	case ratio*100 >= float64(cfg.CriticalPercent):
		return styleError.Render(text)
	case ratio*100 >= float64(cfg.WarningPercent):
		return styleNotice.Render(text)
	default:
		return styleStatus.Render(text)
	}
}

func (m *Model) contextValue(barWidth int) (string, bool) {
	cfg := m.options.StatusLine
	tokens, capacity := m.status.contextTokens, m.options.ContextWindowTokens
	if cfg.ContextFormat == "bar" {
		return contextBar(tokens, capacity, cfg.ContextBar, barWidth, cfg.TokenFormat)
	}
	if capacity == nil || *capacity <= 0 {
		return "capacity unknown", false
	}
	if tokens == nil {
		return "usage unknown", false
	}
	ratio := float64(*tokens) / float64(*capacity)
	text := fmt.Sprintf("%.1f%% %s/%s", ratio*100, formatToken(*tokens, cfg.TokenFormat), formatToken(*capacity, cfg.TokenFormat))
	return renderContextValue(text, ratio, cfg.ContextBar), true
}

func sourceLabel(label, source string) string {
	label = strings.TrimSuffix(label, "(last)")
	if source == "last" {
		return label + "(last)"
	}
	return label
}

// cacheValue 用同一口径的命中量与输入量计算，缺失缓存字段不当成零命中。
func (m *Model) cacheValue(barWidth int) (string, bool) {
	cfg, u := m.options.StatusLine, m.status.usage
	s := m.status.stats
	if previous := m.status.cacheBeforeRun; previous != nil {
		s, u = previous.stats, previous.usage
	}
	var cached, prompt int64
	known, ratioKnown, partial := false, false, false
	// 尚无调用的新会话采用 Go 数值零值；不把它写成供应商已报告的 usage。
	if s.Calls == 0 && !u.OK && u.CachedPromptTokens == nil {
		known, ratioKnown = true, true
	} else if cfg.CacheScope == "session" {
		known = s.CacheKnownCalls > 0 && !s.Overflow
		cached, prompt = s.CachedPromptTokens, s.CachePromptTokens
		ratioKnown = known && prompt > 0 && cached >= 0 && cached <= prompt
		partial = s.CacheKnownCalls < s.Calls
	} else {
		known = u.CachedPromptTokens != nil
		if known {
			cached, prompt = *u.CachedPromptTokens, u.PromptTokens
			ratioKnown = u.OK && prompt > 0 && cached >= 0 && cached <= prompt
		}
	}
	if !known {
		return "", false
	}
	text := formatToken(cached, cfg.TokenFormat)
	if cfg.CacheFormat != "tokens" {
		if ratioKnown {
			ratio := 0.0
			if prompt > 0 {
				ratio = float64(cached) / float64(prompt) * 100
			}
			p := percent(ratio)
			if cfg.CacheFormat == "bar" {
				bar := tokenBar(ratio/100, min(barWidth, cfg.CacheBar.Width), cfg.CacheBar.Style)
				text = fmt.Sprintf("%s%.1f%% %s/%s", bar, ratio, formatToken(cached, cfg.TokenFormat), formatToken(prompt, cfg.TokenFormat))
			} else if cfg.CacheFormat == "ratio" {
				text = p
			} else {
				text += " (" + p + ")"
			}
		} else if cfg.CacheFormat == "ratio" || cfg.CacheFormat == "bar" {
			return "", false
		}
	}
	if partial {
		text += " (partial)"
	}
	return text, true
}

func (m *Model) statusValue(item config.StatusItemConfig, barWidth int) (text string, known bool) {
	cfg, u := m.options.StatusLine, m.status.usage
	label := sanitize(item.Label)
	known = true
	switch item.ID {
	case "provider":
		text = m.options.Provider
		known = text != ""
	case "model":
		text = m.options.ModelID
		known = text != ""
	case "reasoning":
		text = m.options.Reasoning
		if text == "none" {
			text = "off"
		}
		if text == "" {
			text = "unverified"
			known = false
		}
	case "context":
		text, known = m.contextValue(barWidth)
		label = sourceLabel(label, "")
	case "cache":
		text, known = m.cacheValue(barWidth)
	case "git":
		text = m.status.workspace.Git
		known = text != "" && text != "unknown" && text != "—"
		if m.status.workspace.GitStale {
			text += " (stale)"
		}
	case "uv_env":
		text = m.status.workspace.UVEnv
		known = text != "" && text != "unknown" && text != "—"
		if m.status.workspace.UVStale {
			text += " (stale)"
		}
	case "session_elapsed":
		text = formatStatusTime(m.options.Clock().Sub(m.status.sessionStarted), cfg.TimeFormat)
	case "phase":
		if m.state == stateRunning {
			if m.phase == app.PhaseThinking && m.thoughtVisible() {
				return "", true
			}
			text = thoughtSpinner[m.spinnerFrame%len(thoughtSpinner)] + " " + m.phaseLabels[m.phase]
		} else {
			text = "idle"
		}
	case "run_elapsed":
		if m.state == stateRunning {
			if m.phase == app.PhaseThinking && m.thoughtVisible() {
				return "", true
			}
			if cfg.TimeFormat == "clock" {
				text = formatStatusTime(m.elapsed(), cfg.TimeFormat)
			} else {
				text = m.elapsed().String()
			}
		} else if m.status.lastRunID != "" {
			if cfg.TimeFormat == "clock" {
				text = formatStatusTime(m.status.runDuration, cfg.TimeFormat)
			} else {
				text = m.status.runDuration.Round(time.Millisecond).String()
			}
		} else {
			return "", true
		}
	case "last_usage":
		known = u.OK
		if !m.status.usageCurrent {
			label = sourceLabel(label, "last")
		} else {
			label = sourceLabel(label, "")
		}
		if known {
			text = formatToken(u.PromptTokens, cfg.TokenFormat) + "/" + formatToken(u.CompletionTokens, cfg.TokenFormat) + "/" + formatToken(u.TotalTokens, cfg.TokenFormat)
		}
	case "session_usage":
		s := m.status.stats
		known = s.Calls == 0 || s.KnownCalls > 0
		if known {
			text = formatToken(s.TotalTokens, cfg.TokenFormat)
			if s.Calls > s.KnownCalls || s.Overflow {
				text = "≥" + text + " (partial)"
			}
		}
	case "run_id":
		text = m.runID
		if text == "" {
			text = m.status.lastRunID
		}
		if text == "" {
			return "", true
		}
	case "cwd":
		text = m.options.Dir
		if home := strings.TrimRight(m.options.HomeDir, "/\\"); home != "" && (text == home || strings.HasPrefix(text, home+"/") || strings.HasPrefix(text, home+"\\")) {
			text = "~" + strings.TrimPrefix(text, home)
		}
		known = text != ""
	default:
		return "", true
	}
	if !known && cfg.Unknown == "hide" {
		return "", false
	}
	if text == "" {
		text = "unknown"
	}
	// 所有动态字段均可能来自本地文件名或协议，不允许它们控制终端。
	if item.ID != "context" {
		text = sanitize(text)
	}
	if label != "" {
		separator := ":"
		if item.ID == "cache" || item.ID == "context" || item.ID == "provider" {
			separator = ": "
		}
		if item.ID == "cache" && cfg.CacheFormat == "bar" {
			separator = " "
		}
		text = label + separator + text
	}
	return text, known
}

type statusPart struct {
	item  config.StatusItemConfig
	text  string
	order int
	known bool
}

func renderStatusPart(part statusPart) string {
	valueStyle := styleStatus
	if !part.known {
		valueStyle = styleNotice
	}
	if part.item.ID == "model" {
		valueStyle = valueStyle.Bold(true)
	}
	// 标签可能含冒号；匹配实际来源后缀，不能按第一个冒号拆开用户标签。
	label := sanitize(part.item.Label)
	if part.item.ID == "cache" && label != "" {
		if value, found := strings.CutPrefix(part.text, label+" "); found {
			return styleStatusLabel.Render(label) + valueStyle.Render(" "+value)
		}
	}
	for _, candidate := range []string{sourceLabel(label, "last"), strings.TrimSuffix(label, "(last)")} {
		if value, found := strings.CutPrefix(part.text, candidate+":"); candidate != "" && found {
			return styleStatusLabel.Render(candidate+":") + valueStyle.Render(value)
		}
	}
	return valueStyle.Render(part.text)
}

func (m *Model) renderStatusRow(items []statusPart, width int) string {
	cfg := m.options.StatusLine
	maxBarWidth := max(cfg.ContextBar.Width, cfg.CacheBar.Width)
	barWidth := maxBarWidth
	join := func() string {
		parts := make([]string, 0, len(items))
		for _, part := range items {
			if part.text != "" {
				parts = append(parts, renderStatusPart(part))
			}
		}
		return strings.Join(parts, styleStatusSeparator.Render(sanitize(cfg.Separator)))
	}
	for ansi.StringWidth(join()) > width && barWidth > 3 {
		barWidth--
		for i := range items {
			if items[i].item.ID == "context" || items[i].item.ID == "cache" {
				width := max(m.statusBarWidth(items[i].item)-(maxBarWidth-barWidth), 3)
				items[i].text, items[i].known = m.statusValue(items[i].item, width)
			}
		}
	}
	for ansi.StringWidth(join()) > width && len(items) > 1 {
		remove := 0
		for i := 1; i < len(items); i++ {
			if items[i].item.Priority < items[remove].item.Priority || (items[i].item.Priority == items[remove].item.Priority && items[i].order > items[remove].order) {
				remove = i
			}
		}
		items = append(items[:remove], items[remove+1:]...)
	}
	return ansi.Truncate(join(), width, "…")
}

func (m *Model) statusBar() string {
	rows := m.statusRows()
	if rows == 0 {
		return ""
	}
	parts := make([][]statusPart, rows)
	for order, item := range m.options.StatusLine.Items {
		if !item.Enabled {
			continue
		}
		text, known := m.statusValue(item, m.statusBarWidth(item))
		if text == "" {
			continue
		}
		row := min(max(item.Row-1, 0), rows-1)
		parts[row] = append(parts[row], statusPart{item, text, order, known})
	}
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = m.renderStatusRow(parts[i], max(m.width, 1))
	}
	if m.notice != "" {
		lines[rows-1] = styleNotice.Render(ansi.Truncate(sanitize(m.notice), max(m.width, 1), "…"))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) initializeEmptyContext() {
	if m.options.ContextWindowTokens != nil && m.status.stats.Calls == 0 {
		zero := int64(0)
		m.status.contextTokens = &zero
		m.status.contextLast = false
	}
}

func (m *Model) updateStatus(event app.Event) {
	if event.Kind == app.EventRunPhase && event.Phase == app.PhaseWaiting && event.Stats.Calls > m.status.stats.Calls && m.status.cacheBeforeRun == nil {
		// 工具后的下一次模型调用仍属于同一 run；收到新缓存统计前保留上一步展示。
		m.status.cacheBeforeRun = &cacheSnapshot{stats: m.status.stats, usage: m.status.usage}
	}
	if !event.Stats.StartedAt.IsZero() {
		m.status.stats = event.Stats
	}
	if event.Kind == app.EventUsageUpdate || event.Kind == app.EventRunCompleted || event.Kind == app.EventRunFailed {
		usage := event.Usage
		if !event.Stats.StartedAt.IsZero() {
			// 准备阶段取消可能没有新模型调用，以会话快照保留实际最近调用口径。
			usage = event.Stats.LastUsage
		}
		m.status.usage = usage
		m.status.usageCurrent = event.Kind == app.EventUsageUpdate
		// 尚未报告缓存时继续展示请求前快照；终态按实际统计决定 unknown/partial。
		if usage.CachedPromptTokens != nil || event.Kind != app.EventUsageUpdate {
			m.status.cacheBeforeRun = nil
		}
		if event.Kind != app.EventUsageUpdate && !event.Stats.StartedAt.IsZero() && event.Stats.Calls == 0 {
			m.initializeEmptyContext()
		}
		if usage.OK {
			tokens := usage.PromptTokens
			m.status.contextTokens = &tokens
			m.status.contextLast = event.Kind != app.EventUsageUpdate
		} else if event.Kind != app.EventUsageUpdate && (event.Stats.StartedAt.IsZero() || event.Stats.Calls > 0) {
			// 有新调用但终态仍无输入统计，不能把上一轮或初始化的零值当成本次数据。
			m.status.contextTokens = nil
			m.status.contextLast = true
		}
	}
}

func (m *Model) setNotice(text string) {
	m.notice = text
	if m.statusRows() == 0 {
		m.appendCriticalLine(lineSystem, text)
	}
}

func (m *Model) appendCriticalLine(kind lineKind, text string) {
	m.appendLine(kind, text)
	if m.statusRows() == 0 {
		// 无固定提示行时让关键错误立即可见，避免冻结阅读将它藏在视口外。
		m.readingFrozen = false
		m.viewport.GotoBottom()
	}
}
