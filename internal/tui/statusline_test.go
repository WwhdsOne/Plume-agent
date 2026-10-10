package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
)

// 守住：默认状态栏两行布局，空闲时 session 计时照走，ctx/cache 字段齐全。
func TestStatusLineDefaultTwoRowsAndSessionClock(t *testing.T) {
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	m := NewWithOptions("deepseek/deepseek-flash", Hooks{}, Options{Clock: func() time.Time { return now }})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	now = now.Add(75 * time.Second)
	text := ansi.Strip(m.statusBar())
	if strings.Count(text, "\n") != 1 || !strings.Contains(text, "session:1m15s") {
		t.Fatalf("default status must have two rows and idle session time: %q", text)
	}
	if !strings.Contains(text, "ctx:") || !strings.Contains(text, "cache:") {
		t.Fatalf("default status must show context and cache: %q", text)
	}
}

// 守住：默认配置下 context 显示进度条 + 当前/总计，cache 只显示百分比。
func TestStatusDefaultContextBarAndCachePercentage(t *testing.T) {
	cfg := config.DefaultStatusLine()
	cfg.Items = append([]config.StatusItemConfig(nil), cfg.Items[3:5]...)
	capacity, used, cached := int64(1000000), int64(200000), int64(50000)
	m := NewWithOptions("p/m", Hooks{}, Options{StatusLine: cfg, ContextWindowTokens: &capacity})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.status.contextTokens = &used
	session := app.NewSession()
	session.ObserveUsage("call", model.Usage{OK: true, PromptTokens: used, CachedPromptTokens: &cached})
	m.status.stats = session.Statistics()
	if got, want := ansi.Strip(m.statusBar()), "ctx: [██░░░░░░░░] 200k/1M │ cache: 25%"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

// 守住：宽终端放得下全部字段时状态栏并成一行，放不下自动换两行。
func TestStatusLineUsesOneRowWhenAllFieldsFit(t *testing.T) {
	m := New("deepseek/deepseek-flash", Hooks{})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 220, Height: 24})
	if m.statusRows() != 1 || strings.Contains(m.statusBar(), "\n") {
		t.Fatal("wide terminal must combine fields into one row")
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.statusRows() != 2 {
		t.Fatal("use second row when full field set does not fit")
	}
}

// 守住：git 字段长短变化引起状态栏行数切换时，总帧高与输入光标位置不被扰动。
func TestAdaptiveRowsKeepFrameAndInputCursorAligned(t *testing.T) {
	cfg := config.DefaultStatusLine()
	cfg.Items = []config.StatusItemConfig{{ID: "model", Enabled: true, Row: 1}, {ID: "git", Label: "git", Enabled: true, Row: 2}}
	m := NewWithOptions("p/model-name", Hooks{}, Options{StatusLine: cfg, Dir: "/workspace"})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, git := range []string{"main", strings.Repeat("b", 72), "main"} {
		m, _ = m.Update(WorkspaceEvent{app.WorkspaceStatus{Dir: "/workspace", Git: git}})
		if len(strings.Split(m.View(), "\n")) != 24 || m.inputCursor().Y != 24-m.statusRows()-2 {
			t.Fatal("status row transition moved frame height or input cursor")
		}
	}
}

// 守住：思考边界处状态栏收成一行、输入又逐行增高时，帧高与输入光标仍保持对齐。
func TestAdaptiveRowsAfterInputGrowAtThoughtBoundary(t *testing.T) {
	cfg := config.DefaultStatusLine()
	cfg.Items = []config.StatusItemConfig{{ID: "model", Enabled: true, Row: 1, Priority: 90}, {ID: "phase", Enabled: true, Row: 2, Priority: 100}, {ID: "run_elapsed", Enabled: true, Row: 2, Priority: 100}}
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	m := NewWithOptions("p/"+strings.Repeat("m", 70), Hooks{}, Options{StatusLine: cfg, Clock: func() time.Time { return now }, Choose: func(int) int { return 0 }})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for range 19 {
		m.appendLine(lineSystem, "history")
	}
	m.startRun("r")
	m.selectPhase(app.PhaseThinking)
	m.ensureAssistant().reasoning = "thought body"
	m.syncViewport()
	m.viewport.GotoTop()
	m.readingFrozen = true
	m.syncLayout()
	if m.statusRows() != 1 || !m.thoughtVisible() {
		t.Fatalf("fixture rows=%d thought=%v", m.statusRows(), m.thoughtVisible())
	}
	m.input.SetValue("first")
	m, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt}))
	for range 3 {
		cursorY := m.inputCursor().Y
		if len(strings.Split(m.View(), "\n")) != 24 || cursorY < m.viewport.Height()+1 || cursorY >= m.viewport.Height()+1+m.input.Height() {
			t.Fatalf("frame layout mismatch: rows=%d viewport=%d input=%d", m.statusRows(), m.viewport.Height(), m.input.Height())
		}
		m, _ = m.Update(streamTick(now))
	}
}

// 守住：容量未知时解释 capacity unknown 而非假 0%；空会话与 run 起止都从 0/容量起步。
func TestContextUnknownExplainsMissingSourceAndEmptySessionStartsAtZero(t *testing.T) {
	m := New("p/m", Hooks{})
	item := config.StatusItemConfig{ID: "context", Label: "ctx"}
	text, _ := m.statusValue(item, 10)
	if strings.Contains(text, "?") || text != "ctx: capacity unknown" {
		t.Fatal(text)
	}
	capacity := int64(100)
	m = NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity})
	text, _ = m.statusValue(item, 10)
	if got := ansi.Strip(text); got != "ctx: [░░░░░░░░░░] 0/100" {
		t.Fatal(got)
	}
	m.startRun("r")
	text, _ = m.statusValue(item, 10)
	if got := ansi.Strip(text); got != "ctx: [░░░░░░░░░░] 0/100" {
		t.Fatal(got)
	}
	m.endRun()
	m.sessionReset()
	text, _ = m.statusValue(item, 10)
	if got := ansi.Strip(text); got != "ctx: [░░░░░░░░░░] 0/100" {
		t.Fatal(got)
	}
}

// 守住：会话零值只用于展示，真实用量上报前不得被记成实际模型调用。
func TestStatusUsagePreservesInitialZeroUntilReported(t *testing.T) {
	for _, scope := range []string{"session", "last_call"} {
		t.Run(scope, func(t *testing.T) {
			s := app.NewSession()
			stats := s.Statistics()
			capacity := int64(100)
			m := NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity, InitialStats: &stats})
			m.options.StatusLine.CacheScope = scope
			check := func(ctx, cache string) {
				t.Helper()
				for _, tc := range []struct{ id, label, want string }{{"context", "ctx", ctx}, {"cache", "cache", cache}} {
					text, known := m.statusValue(config.StatusItemConfig{ID: tc.id, Label: tc.label}, 10)
					if !known || ansi.Strip(text) != tc.want {
						t.Fatalf("phase=%s %s=%q known=%v, want %q", m.phase, tc.id, ansi.Strip(text), known, tc.want)
					}
				}
			}
			m.startRun("r")
			check("ctx: [░░░░░░░░░░] 0/100", "cache: 0%")
			s.ObserveUsage("r/model-1", model.Usage{})
			for _, phase := range []app.Phase{app.PhaseWaiting, app.PhaseThinking, app.PhaseResponding} {
				m.applyEvent(app.Event{Kind: app.EventRunPhase, RunID: "r", Phase: phase, Stats: s.Statistics()})
				check("ctx: [░░░░░░░░░░] 0/100", "cache: 0%")
			}
			if m.status.stats.Calls != 1 || m.status.stats.KnownCalls != 0 || m.status.stats.CacheKnownCalls != 0 {
				t.Fatal("display zero was recorded as actual model usage")
			}
			cached := int64(8)
			usage := model.Usage{OK: true, PromptTokens: 20, CachedPromptTokens: &cached}
			s.ObserveUsage("r/model-1", usage)
			m.applyEvent(app.Event{Kind: app.EventUsageUpdate, RunID: "r", Usage: usage, Stats: s.Statistics()})
			check("ctx: [██░░░░░░░░] 20/100", "cache: 40%")
			m.applyEvent(app.Event{Kind: app.EventRunCompleted, RunID: "r", Reply: "done", Usage: usage, Stats: s.Statistics()})
			check("ctx: [██░░░░░░░░] 20/100", "cache: 40%")
		})
	}
}

// 守住：新一轮尚未上报时保留上一份快照；输入统计先到、缓存字段缺失时旧缓存值不丢。
func TestStatusUsagePreservesPreviousSnapshotUntilReported(t *testing.T) {
	for _, tc := range []struct{ scope, want string }{
		{"session", "cache: 70%"},
		{"last_call", "cache: 80%"},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			s := app.NewSession()
			capacity, firstCached, nextCached := int64(100), int64(8), int64(48)
			m := NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity})
			m.options.StatusLine.CacheScope = tc.scope
			first := model.Usage{OK: true, PromptTokens: 20, CachedPromptTokens: &firstCached}
			s.ObserveUsage("r1/model-1", first)
			m.startRun("r1")
			m.applyEvent(app.Event{Kind: app.EventRunCompleted, RunID: "r1", Usage: first, Stats: s.Statistics()})
			m.startRun("r2")
			s.ObserveUsage("r2/model-1", model.Usage{})
			m.applyEvent(app.Event{Kind: app.EventRunPhase, RunID: "r2", Phase: app.PhaseWaiting, Stats: s.Statistics()})
			item := config.StatusItemConfig{ID: "cache", Label: "cache"}
			if text, known := m.statusValue(item, 10); !known || ansi.Strip(text) != "cache: 40%" {
				t.Fatalf("pending request changed previous cache: %q known=%v", ansi.Strip(text), known)
			}
			if text, known := m.statusValue(config.StatusItemConfig{ID: "context", Label: "ctx"}, 10); !known || ansi.Strip(text) != "ctx: [██░░░░░░░░] 20/100" {
				t.Fatalf("pending request changed previous context: %q known=%v", ansi.Strip(text), known)
			}
			// 输入统计先到、缓存尚未报告时，缓存继续保留上一份快照。
			next := model.Usage{OK: true, PromptTokens: 60}
			s.ObserveUsage("r2/model-1", next)
			m.applyEvent(app.Event{Kind: app.EventUsageUpdate, RunID: "r2", Usage: next, Stats: s.Statistics()})
			if text, known := m.statusValue(item, 10); !known || ansi.Strip(text) != "cache: 40%" {
				t.Fatalf("missing cache update erased previous snapshot: %q known=%v", ansi.Strip(text), known)
			}
			if text, _ := m.statusValue(config.StatusItemConfig{ID: "context", Label: "ctx"}, 10); ansi.Strip(text) != "ctx: [██████░░░░] 60/100" {
				t.Fatalf("reported context was not updated: %q", ansi.Strip(text))
			}
			next.CachedPromptTokens = &nextCached
			s.ObserveUsage("r2/model-1", next)
			m.applyEvent(app.Event{Kind: app.EventUsageUpdate, RunID: "r2", Usage: next, Stats: s.Statistics()})
			if text, known := m.statusValue(item, 10); !known || ansi.Strip(text) != tc.want {
				t.Fatalf("reported cache=%q known=%v, want %q", ansi.Strip(text), known, tc.want)
			}
		})
	}
}

// 守住：终态事件缺输入统计时 context 转 unknown；仅 session 作用域保留历史 partial 缓存；新会话归零。
func TestStatusUsageTerminalWithoutStatistics(t *testing.T) {
	for _, scope := range []string{"session", "last_call"} {
		for _, priorCall := range []bool{false, true} {
			for _, outcome := range []string{"completed", "failed", "cancelled"} {
				t.Run(scope+"/"+map[bool]string{false: "first", true: "next"}[priorCall]+"/"+outcome, func(t *testing.T) {
					s := app.NewSession()
					capacity, cached := int64(100), int64(8)
					m := NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity})
					m.options.StatusLine.CacheScope = scope
					if priorCall {
						prior := model.Usage{OK: true, PromptTokens: 20, CachedPromptTokens: &cached}
						s.ObserveUsage("prior/model-1", prior)
						m.startRun("prior")
						m.applyEvent(app.Event{Kind: app.EventRunCompleted, RunID: "prior", Usage: prior, Stats: s.Statistics()})
					}
					m.startRun("r")
					s.ObserveUsage("r/model-1", model.Usage{})
					m.applyEvent(app.Event{Kind: app.EventRunPhase, RunID: "r", Phase: app.PhaseWaiting, Stats: s.Statistics()})
					event := app.Event{Kind: app.EventRunCompleted, RunID: "r", Stats: s.Statistics()}
					if outcome != "completed" {
						event.Kind, event.Err = app.EventRunFailed, errors.New("fixture failure")
						if outcome == "cancelled" {
							event.Err = context.Canceled
						}
					}
					m.applyEvent(event)
					if text, known := m.statusValue(config.StatusItemConfig{ID: "context", Label: "ctx"}, 10); known || ansi.Strip(text) != "ctx: usage unknown" {
						t.Fatalf("terminal missing usage retained context: %q known=%v", ansi.Strip(text), known)
					}
					want, wantKnown := "cache: unknown", false
					if scope == "session" && priorCall {
						want, wantKnown = "cache: 40% (partial)", true
					}
					if text, known := m.statusValue(config.StatusItemConfig{ID: "cache", Label: "cache"}, 10); known != wantKnown || ansi.Strip(text) != want {
						t.Fatalf("terminal cache=%q known=%v, want %q known=%v", ansi.Strip(text), known, want, wantKnown)
					}
					m.sessionReset()
					m.startRun("new")
					if text, known := m.statusValue(config.StatusItemConfig{ID: "context", Label: "ctx"}, 10); !known || ansi.Strip(text) != "ctx: [░░░░░░░░░░] 0/100" {
						t.Fatalf("new session leaked unknown context: %q", ansi.Strip(text))
					}
				})
			}
		}
	}
}

// 守住：首次模型调用前就取消 run，context/cache 保持 0 而非转 unknown。
func TestStatusUsageCancellationBeforeFirstModelCallKeepsZero(t *testing.T) {
	for _, scope := range []string{"session", "last_call"} {
		t.Run(scope, func(t *testing.T) {
			s := app.NewSession()
			capacity := int64(100)
			m := NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity})
			m.options.StatusLine.CacheScope = scope
			m.startRun("r")
			m.applyEvent(app.Event{Kind: app.EventRunFailed, RunID: "r", Err: context.Canceled, Stats: s.Statistics()})
			for _, tc := range []struct{ id, label, want string }{{"context", "ctx", "ctx: [░░░░░░░░░░] 0/100"}, {"cache", "cache", "cache: 0%"}} {
				if text, known := m.statusValue(config.StatusItemConfig{ID: tc.id, Label: tc.label}, 10); !known || ansi.Strip(text) != tc.want {
					t.Fatalf("preparation cancellation changed %s: %q known=%v", tc.id, ansi.Strip(text), known)
				}
			}
		})
	}
}

// 守住：无容量时说明来源缺失；按实际报告值展示；超容量时进度条满格并显示 120%。
func TestContextProgressUnknownActualLastAndOverflow(t *testing.T) {
	cap := int64(100)
	m := New("p/m", Hooks{})
	m.width, m.height = 120, 24
	m.resize()
	item := config.StatusItemConfig{ID: "context", Label: "ctx"}
	unknown, _ := m.statusValue(item, 10)
	if !strings.Contains(unknown, "capacity unknown") || strings.Contains(unknown, "0%") {
		t.Fatal(unknown)
	}
	m.options.ContextWindowTokens = &cap
	m.startRun("r")
	m.applyEvent(app.Event{Kind: app.EventUsageUpdate, RunID: "r", Usage: model.Usage{OK: true, PromptTokens: 20}})
	text, _ := m.statusValue(item, 10)
	if got := ansi.Strip(text); got != "ctx: [██░░░░░░░░] 20/100" {
		t.Fatal(got)
	}
	m.applyEvent(app.Event{Kind: app.EventRunCompleted, RunID: "r", Reply: "done", Usage: model.Usage{OK: true, PromptTokens: 120}})
	text, _ = m.statusValue(item, 10)
	if got := ansi.Strip(text); got != "ctx: [██████████] 120% 120/100" {
		t.Fatal(got)
	}
}

// 守住：usage 格式的百分比与 token 数保留一位小数；label 里的 (last) 旧后缀不再展示。
func TestContextUsagePrecisionAndLegacyLabel(t *testing.T) {
	capacity := int64(1000000)
	m := NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity})
	m.options.StatusLine.ContextFormat = "usage"
	item := config.StatusItemConfig{ID: "context", Label: "ctx(last)"}
	for _, tc := range []struct {
		tokens int64
		want   string
	}{
		{0, "ctx: 0.0% 0/1M"},
		{123456, "ctx: 12.3% 123.5k/1M"},
		{1000000, "ctx: 100.0% 1M/1M"},
		{1200000, "ctx: 120.0% 1.2M/1M"},
	} {
		m.status.contextTokens, m.status.contextLast = &tc.tokens, true
		text, known := m.statusValue(item, 10)
		if !known || ansi.Strip(text) != tc.want {
			t.Fatalf("ctx=%q known=%v, want %q", ansi.Strip(text), known, tc.want)
		}
	}
}

// 守住：两种作用域、四种格式的缓存初始值都显示 0 而非 unknown。
func TestCacheInitialZeroForBothScopesAndFormats(t *testing.T) {
	for _, scope := range []string{"session", "last_call"} {
		for _, tc := range []struct{ format, want string }{
			{"bar", "cache [░░░░░░░░░░]0.0% 0/0"},
			{"ratio", "cache: 0%"}, {"tokens", "cache: 0"}, {"both", "cache: 0 (0%)"},
		} {
			t.Run(scope+"/"+tc.format, func(t *testing.T) {
				m := New("p/m", Hooks{})
				m.options.StatusLine.CacheScope, m.options.StatusLine.CacheFormat = scope, tc.format
				m.options.StatusLine.Unknown = "hide"
				text, known := m.statusValue(config.StatusItemConfig{ID: "cache", Label: "cache"}, 10)
				if !known || text != tc.want {
					t.Fatalf("initial cache=%q known=%v, want %q", text, known, tc.want)
				}
			})
		}
	}
}

// 守住：provider 项默认标签取品牌；cache bar 按 session 累计与 last_call 单次两种口径取数。
func TestProviderDefaultLabelAndCacheBarScopes(t *testing.T) {
	s := app.NewSession()
	first, latest := int64(8), int64(6)
	s.ObserveUsage("first", model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &first})
	s.ObserveUsage("latest", model.Usage{OK: true, PromptTokens: 30, CachedPromptTokens: &latest})
	m := New("deepseek/deepseek-flash", Hooks{})
	m.options.StatusLine.CacheFormat = "bar"
	text, known := m.statusValue(m.options.StatusLine.Items[0], 10)
	if !known || text != "Provider: deepseek" {
		t.Fatalf("provider = %q known=%v", text, known)
	}
	m.status.stats, m.status.usage = s.Statistics(), s.Statistics().LastUsage
	item := config.StatusItemConfig{ID: "cache", Label: "cache"}
	for _, tc := range []struct{ scope, want string }{
		{"session", "cache [███░░░░░░░]35.0% 14/40"},
		{"last_call", "cache [██░░░░░░░░]20.0% 6/30"},
	} {
		m.options.StatusLine.CacheScope = tc.scope
		text, known := m.statusValue(item, 10)
		if !known || ansi.Strip(text) != tc.want {
			t.Fatalf("scope=%s cache=%q known=%v, want %q", tc.scope, ansi.Strip(text), known, tc.want)
		}
	}
}

// 守住：缓存比例分母只用带缓存字段的输入 token，部分已知时标注 (partial)；自定义条宽/样式生效。
func TestCacheBarUsesInputDenominatorAndPartialSubset(t *testing.T) {
	s := app.NewSession()
	cached := int64(8)
	s.ObserveUsage("known", model.Usage{OK: true, PromptTokens: 20, CachedPromptTokens: &cached})
	s.ObserveUsage("unknown", model.Usage{OK: true, PromptTokens: 80})
	capacity := int64(1000000)
	m := NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity})
	m.options.StatusLine.CacheFormat = "bar"
	m.status.stats = s.Statistics()
	text, known := m.statusValue(config.StatusItemConfig{ID: "cache", Label: "cache"}, 10)
	if !known || ansi.Strip(text) != "cache [████░░░░░░]40.0% 8/20 (partial)" {
		t.Fatalf("cache used wrong denominator: %q", ansi.Strip(text))
	}
	m.options.StatusLine.CacheBar = config.CacheBarConfig{Width: 5, Style: "ascii"}
	text, known = m.statusValue(config.StatusItemConfig{ID: "cache", Label: "缓存"}, 5)
	if !known || ansi.Strip(text) != "缓存 [##---]40.0% 8/20 (partial)" {
		t.Fatalf("custom cache bar ignored: %q", ansi.Strip(text))
	}
}

// 守住：cache bar 宽度独立于 context bar；空间不足先收缩条宽再裁剪整行。
func TestCacheBarWidthIndependentAndShrinksBeforeClipping(t *testing.T) {
	cfg := config.DefaultStatusLine()
	cfg.CacheFormat = "bar"
	cfg.ContextBar.Width, cfg.CacheBar.Width = 5, 20
	cfg.Items = []config.StatusItemConfig{{ID: "cache", Label: "cache", Enabled: true, Row: 1, Priority: 40}}
	m := NewWithOptions("p/m", Hooks{}, Options{StatusLine: cfg})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	text := ansi.Strip(m.statusBar())
	if text != "cache ["+strings.Repeat("░", 20)+"]0.0% 0/0" {
		t.Fatalf("cache width coupled to context width: %q", text)
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 25, Height: 16})
	text = ansi.Strip(m.statusBar())
	if text != "cache ["+strings.Repeat("░", 9)+"]0.0% 0/0" || ansi.StringWidth(text) > 25 {
		t.Fatalf("cache bar clipped before shrinking: %q", text)
	}
}

// 守住：context bar 的样式/宽度/show_percent 配置照常生效，不附加 last 后缀。
func TestContextBarRemainsConfigurableWithoutLastSuffix(t *testing.T) {
	cfg := config.DefaultStatusLine()
	cfg.ContextFormat = "bar"
	cfg.ContextBar.Style, cfg.ContextBar.Width = "ascii", 5
	capacity, used := int64(100), int64(20)
	m := NewWithOptions("p/m", Hooks{}, Options{StatusLine: cfg, ContextWindowTokens: &capacity})
	m.status.contextTokens, m.status.contextLast = &used, true
	text, known := m.statusValue(config.StatusItemConfig{ID: "context", Label: "容量"}, 5)
	if !known || ansi.Strip(text) != "容量: [#----] 20/100" {
		t.Fatalf("configured bar changed: %q", ansi.Strip(text))
	}
	m.options.StatusLine.ContextBar.ShowPercent = true
	text, known = m.statusValue(config.StatusItemConfig{ID: "context", Label: "容量"}, 5)
	if !known || ansi.Strip(text) != "容量: [#----] 20% 20/100" {
		t.Fatalf("optional percentage changed: %q", ansi.Strip(text))
	}
}

// 守住：同行字段按优先级排列；状态栏禁用时关键通知仍显示且帧高不变。
func TestStatusItemsOrderDisabledAndCriticalNotice(t *testing.T) {
	cfg := config.DefaultStatusLine()
	cfg.MaxRows = 1
	cfg.Items = []config.StatusItemConfig{{ID: "model", Enabled: true, Row: 1, Priority: 90}, {ID: "provider", Enabled: true, Row: 1, Priority: 50}}
	m := NewWithOptions("p/m", Hooks{}, Options{StatusLine: cfg})
	m.width, m.height = 80, 24
	m.resize()
	if got := ansi.Strip(m.statusBar()); got != "m │ p" {
		t.Fatal(got)
	}
	cfg.Enabled = false
	m.resize()
	m.setNotice("critical notice")
	if m.statusRows() != 0 || !strings.Contains(ansi.Strip(m.View()), "critical notice") {
		t.Fatal("disabled status hid notice")
	}
	if len(strings.Split(m.View(), "\n")) != 24 {
		t.Fatal("disabled status layout has wrong height")
	}
}

// 守住：同优先级字段挤不下时保留配置靠前者、隐藏靠后者。
func TestEqualPriorityHidesLaterField(t *testing.T) {
	now := time.Now()
	cfg := config.DefaultStatusLine()
	cfg.MaxRows = 1
	cfg.Items = []config.StatusItemConfig{{ID: "phase", Enabled: true, Row: 1, Priority: 100}, {ID: "run_elapsed", Enabled: true, Row: 1, Priority: 100}}
	m := NewWithOptions("p/m", Hooks{}, Options{StatusLine: cfg, Clock: func() time.Time { return now }, Choose: func(int) int { return 0 }})
	m.width, m.height = 16, 24
	m.resize()
	m.startRun("r")
	now = now.Add(time.Second)
	text := ansi.Strip(m.statusBar())
	if !strings.Contains(text, "getting ready") || strings.Contains(text, "1s") {
		t.Fatalf("equal priority must drop later field: %q", text)
	}
}

// 守住：状态栏禁用且阅读历史（视口冻结）时，关键通知仍必须可见。
func TestDisabledStatusShowsCriticalNoticeWhileReadingHistory(t *testing.T) {
	cfg := config.DefaultStatusLine()
	cfg.Enabled = false
	m := NewWithOptions("p/m", Hooks{}, Options{StatusLine: cfg})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for range 80 {
		m.appendLine(lineSystem, "history line")
	}
	m.viewport.GotoTop()
	m.readingFrozen = true
	m.setNotice("cannot start another run")
	if !strings.Contains(ansi.Strip(m.View()), "cannot start another run") {
		t.Fatal("critical notice is hidden outside frozen viewport")
	}
}

// 守住：状态栏隐藏（禁用或空 items）时，提交失败/运行失败/取消会解除阅读冻结并显示错误行。
func TestHiddenStatusShowsSubmissionAndRunFailureWhileReadingHistory(t *testing.T) {
	for _, emptyItems := range []bool{false, true} {
		for _, failure := range []string{"submit", "cancel", "failure"} {
			t.Run(failure+"/"+map[bool]string{false: "disabled", true: "empty"}[emptyItems], func(t *testing.T) {
				cfg := config.DefaultStatusLine()
				if emptyItems {
					cfg.Items = []config.StatusItemConfig{}
				} else {
					cfg.Enabled = false
				}
				m := NewWithOptions("p/m", Hooks{Submit: func(string) (string, error) { return "", errors.New("backend unavailable") }}, Options{StatusLine: cfg})
				m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
				for range 80 {
					m.appendLine(lineSystem, "history line")
				}
				m.viewport.GotoTop()
				m.readingFrozen = true
				if failure == "submit" {
					m.input.SetValue("new request")
					m.submitInput()
				} else {
					m.startRun("r")
					err := errors.New("backend failed")
					if failure == "cancel" {
						err = context.Canceled
					}
					m.applyEvent(app.Event{Kind: app.EventRunFailed, RunID: "r", Err: err})
				}
				if m.readingFrozen || !m.viewport.AtBottom() || len(m.lines) == 0 || m.lines[len(m.lines)-1].kind != lineError {
					t.Fatal("critical error is hidden outside frozen viewport")
				}
				if !strings.Contains(ansi.Strip(m.View()), m.lines[len(m.lines)-1].text) {
					t.Fatal("critical error was not rendered in the visible viewport")
				}
			})
		}
	}
}

// 守住：缓存比例只按输入 token 计算（不含 completion），session/last_call 两口径与缺省值正确。
func TestCacheScopeUsesWeightedInputTotals(t *testing.T) {
	s := app.NewSession()
	first, latest := int64(8), int64(6)
	s.ObserveUsage("first", model.Usage{OK: true, PromptTokens: 10, CompletionTokens: 100, TotalTokens: 110, CachedPromptTokens: &first})
	s.ObserveUsage("latest", model.Usage{OK: true, PromptTokens: 30, CompletionTokens: 300, TotalTokens: 330, CachedPromptTokens: &latest})
	for _, tc := range []struct{ settings, want string }{
		{`{"cache_format":"ratio"}`, "cache: 35%"},
		{`{"cache_scope":"session","cache_format":"ratio"}`, "cache: 35%"},
		{`{"cache_scope":"last_call","cache_format":"ratio"}`, "cache: 20%"},
	} {
		t.Run(tc.settings, func(t *testing.T) {
			var cfg config.StatusLineConfig
			if err := json.Unmarshal([]byte(tc.settings), &cfg); err != nil {
				t.Fatal(err)
			}
			m := NewWithOptions("p/m", Hooks{}, Options{StatusLine: &cfg})
			m.status.stats = s.Statistics()
			m.status.usage = m.status.stats.LastUsage
			item := config.StatusItemConfig{ID: "cache", Label: "cache"}
			for _, current := range []bool{false, true} {
				m.status.usageCurrent = current
				text, known := m.statusValue(item, 10)
				if !known || text != tc.want {
					t.Fatalf("current=%v cache=%q known=%v, want %q", current, text, known, tc.want)
				}
			}
		})
	}
}

// 守住：session 缓存部分已知时标注 (partial)；Ctrl+L 清屏不清统计，Ctrl+N 新会话归零。
func TestSessionCachePartialAndReset(t *testing.T) {
	s := app.NewSession()
	cached := int64(8)
	s.ObserveUsage("known", model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &cached})
	s.ObserveUsage("unknown-cache", model.Usage{OK: true, PromptTokens: 30})
	m := New("p/m", Hooks{})
	m.options.StatusLine.CacheFormat = "ratio"
	m.status.stats = s.Statistics()
	item := config.StatusItemConfig{ID: "cache", Label: "cache"}
	text, known := m.statusValue(item, 10)
	if !known || text != "cache: 80% (partial)" {
		t.Fatalf("missing cache treated as zero: %q known=%v", text, known)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if text, known := m.statusValue(item, 10); !known || text != "cache: 80% (partial)" {
		t.Fatal("clear screen erased cache statistics")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if text, known := m.statusValue(item, 10); !known || text != "cache: 0%" {
		t.Fatalf("new session leaked cache statistics: %q known=%v", text, known)
	}
}

// 守住：两种作用域下缓存零值/字段缺失/无分母的 known 语义与各格式展示；unknown 可配置隐藏。
func TestCacheScopesZeroUnknownAndFormats(t *testing.T) {
	zero, hit := int64(0), int64(8)
	for _, scope := range []string{"session", "last_call"} {
		for _, tc := range []struct {
			name, format, want string
			usage              model.Usage
			known              bool
		}{
			{"zero", "ratio", "cache: 0%", model.Usage{OK: true, PromptTokens: 20, CachedPromptTokens: &zero}, true},
			{"missing", "ratio", "cache: unknown", model.Usage{OK: true, PromptTokens: 20}, false},
			// case: 输入为 0 时比例无分母，即使缓存字段已知也按 unknown 处理
			{"zero-input", "ratio", "cache: unknown", model.Usage{OK: true, CachedPromptTokens: &zero}, false},
			{"tokens", "tokens", "cache: 8", model.Usage{OK: true, PromptTokens: 20, CachedPromptTokens: &hit}, true},
			{"both", "both", "cache: 8 (40%)", model.Usage{OK: true, PromptTokens: 20, CachedPromptTokens: &hit}, true},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				s := app.NewSession()
				s.ObserveUsage("call", tc.usage)
				m := New("p/m", Hooks{})
				m.options.StatusLine.CacheScope, m.options.StatusLine.CacheFormat = scope, tc.format
				m.status.stats, m.status.usage = s.Statistics(), tc.usage
				item := config.StatusItemConfig{ID: "cache", Label: "cache"}
				text, known := m.statusValue(item, 10)
				if text != tc.want || known != tc.known {
					t.Fatalf("cache=%q known=%v, want %q known=%v", text, known, tc.want, tc.known)
				}
				if !tc.known {
					m.options.StatusLine.Unknown = "hide"
					if text, known := m.statusValue(item, 10); text != "" || known {
						t.Fatal("unknown cache was not hidden")
					}
				}
			})
		}
	}
}

// 守住：last_call 下已知零缓存在 tokens/both 格式如实显示 0；有调用但 usage 未知时转 unknown。
func TestCacheZeroUnknownAndRatio(t *testing.T) {
	m := New("p/m", Hooks{})
	m.options.StatusLine.CacheScope = "last_call"
	m.options.StatusLine.CacheFormat = "tokens"
	m.width = 120
	m.height = 24
	item := config.StatusItemConfig{ID: "cache", Label: "cache(last)"}
	zero := int64(0)
	m.status.usage = model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &zero}
	text, known := m.statusValue(item, 10)
	if !known || text != "cache(last): 0" {
		t.Fatal(text)
	}
	m.options.StatusLine.CacheFormat = "both"
	text, _ = m.statusValue(item, 10)
	if text != "cache(last): 0 (0%)" {
		t.Fatal(text)
	}
	m.status.usage = model.Usage{}
	m.status.stats.Calls = 1
	text, known = m.statusValue(item, 10)
	if known || !strings.Contains(text, "unknown") {
		t.Fatal(text)
	}
}

// 守住：both 格式在比例无分母时仍保留已知的缓存数量，不退成隐藏。
func TestCacheBothKeepsKnownQuantityWithoutRatio(t *testing.T) {
	for _, u := range []model.Usage{
		{OK: true, PromptTokens: 0}, // case: 成功调用但输入为 0，比例无分母
		{OK: false},                 // case: 失败调用拿不到比例，数量仍算已知
	} {
		m := New("p/m", Hooks{})
		zero := int64(0)
		u.CachedPromptTokens = &zero
		m.status.usage = u
		m.options.StatusLine.CacheFormat = "both"
		m.options.StatusLine.CacheScope = "last_call"
		m.options.StatusLine.Unknown = "hide"
		text, known := m.statusValue(config.StatusItemConfig{ID: "cache", Label: "cache(last)"}, 10)
		if !known || text != "cache(last): 0" {
			t.Fatalf("known zero was hidden when ratio unavailable: %q", text)
		}
	}
}

// 守住：last_call 口径以真实最后一次模型调用为准——准备阶段取消保留上次值，新调用未报缓存则转 unknown。
func TestTerminalCacheUsesActualLastModelCall(t *testing.T) {
	for _, newCall := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel-before-call", true: "call-with-unknown-usage"}[newCall], func(t *testing.T) {
			m := New("p/m", Hooks{})
			m.options.StatusLine.CacheScope = "last_call"
			m.options.StatusLine.CacheFormat = "ratio"
			cached := int64(6)
			prior := model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &cached}
			stats := app.SessionStats{StartedAt: time.Now(), Calls: 1, KnownCalls: 1, LastCallID: "r1/model-1", LastUsage: prior}
			m.startRun("r1")
			m.applyEvent(app.Event{Kind: app.EventRunCompleted, RunID: "r1", Usage: prior, Stats: stats})
			m.startRun("r2")
			if newCall {
				stats.Calls++
				stats.LastCallID, stats.LastUsage = "r2/model-1", model.Usage{}
			}
			m.applyEvent(app.Event{Kind: app.EventRunFailed, RunID: "r2", Err: context.Canceled, Stats: stats})
			text, known := m.statusValue(config.StatusItemConfig{ID: "cache", Label: "cache"}, 10)
			if !newCall && (!known || text != "cache: 60%") {
				t.Fatalf("preparation cancellation erased actual last call: %q", text)
			}
			if newCall && (known || !strings.Contains(text, "unknown")) {
				t.Fatalf("new unknown call kept previous cache: %q", text)
			}
		})
	}
}

// 守住：会话重置后旧时钟 tick 不得重启计时；run_elapsed 冻结在 run 完成时刻。
func TestStatusClockResetRejectsOldTickAndFreezesRunTime(t *testing.T) {
	now := time.Now()
	m := NewWithOptions("p/m", Hooks{}, Options{Clock: func() time.Time { return now }})
	m.width, m.height = 120, 24
	m.resize()
	old := m.status.sessionStarted
	m.startRun("r")
	now = now.Add(2 * time.Second)
	m.applyEvent(app.Event{Kind: app.EventRunCompleted, RunID: "r", Reply: "done"})
	now = now.Add(3 * time.Second)
	text, _ := m.statusValue(config.StatusItemConfig{ID: "run_elapsed"}, 10)
	if text != "2s" {
		t.Fatal(text)
	}
	m.sessionReset()
	_, cmd := m.Update(statusClockTick{old})
	if cmd != nil {
		t.Fatal("old clock restarted after session reset")
	}
	text, _ = m.statusValue(config.StatusItemConfig{ID: "session_elapsed"}, 10)
	if text != "0s" {
		t.Fatal(text)
	}
}

// 守住：各窗口尺寸下状态栏帧高精确等于窗口高度，行宽不溢出。
func TestStatusLineHeightFitsWindow(t *testing.T) {
	for _, size := range [][2]int{{120, 24}, {80, 24}, {40, 16}, {20, 8}} {
		m := New("fake/offline", Hooks{})
		m, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(m.View(), "\n")
		if len(lines) != size[1] {
			t.Fatalf("%v frame height=%d", size, len(lines))
		}
		for _, row := range lines {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("%v overflowing row %q", size, row)
			}
		}
	}
}
