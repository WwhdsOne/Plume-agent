package tui

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
)

// TestInitialStatusRestoresLatestUsage 守住恢复快照与正常运行的共同口径：
// ctx 用末次实际输入，缓存按所选 scope；等待下一请求不会抹掉恢复快照。
func TestInitialStatusRestoresLatestUsage(t *testing.T) {
	two, eight := int64(2), int64(8)
	for _, tc := range []struct {
		name, context, sessionCache, lastCache string
		usages                                 []model.Usage
	}{
		// case: 无调用仍显示零，不能凭初始化制造 usage。
		{"empty", "ctx: [░░░░░░░░░░] 0/100", "cache: 0%", "cache: 0%", nil},
		// case: 两轮总输入 30，但 ctx 只能用末次 20；CC/Pi 分母不同。
		{"known latest", "ctx: [██░░░░░░░░] 20/100", "cache: 33.3%", "cache: 40%", []model.Usage{
			{OK: true, PromptTokens: 10, CachedPromptTokens: &two},
			{OK: true, PromptTokens: 20, CachedPromptTokens: &eight},
		}},
		// case: 最近调用没有 usage，不能用更早输入或累计量冒充它。
		{"unknown latest", "ctx: usage unknown", "cache: 20% (partial)", "cache: unknown", []model.Usage{
			{OK: true, PromptTokens: 10, CachedPromptTokens: &two}, {},
		}},
		// case: 供应商明确报告输入零与未报告不同；缓存未报告仍未知。
		{"known zero", "ctx: [░░░░░░░░░░] 0/100", "cache: unknown", "cache: unknown", []model.Usage{{OK: true}}},
	} {
		for _, scope := range []string{"session", "last_call"} {
			t.Run(tc.name+"/"+scope, func(t *testing.T) {
				session := app.NewSession()
				for i, usage := range tc.usages {
					session.ObserveUsage(fmt.Sprintf("stored/model-%d", i+1), usage)
				}
				stats := session.Statistics()
				capacity := int64(100)
				cfg := config.DefaultStatusLine()
				cfg.CacheScope = scope
				m := NewWithOptions("p/m", Hooks{}, Options{ContextWindowTokens: &capacity, StatusLine: cfg, InitialStats: &stats})
				// 调用方保留的初始快照后来发生修改，不能改变 TUI 的恢复快照。
				if stats.LastUsage.CachedPromptTokens != nil {
					*stats.LastUsage.CachedPromptTokens = 0
				}
				cache := tc.sessionCache
				if scope == "last_call" {
					cache = tc.lastCache
				}
				check := func(expectedCalls int) {
					t.Helper()
					for _, field := range []struct{ id, label, want string }{{"context", "ctx", tc.context}, {"cache", "cache", cache}} {
						text, _ := m.statusValue(config.StatusItemConfig{ID: field.id, Label: field.label}, 10)
						if got := ansi.Strip(text); got != field.want {
							t.Fatalf("restored %s = %q, want %q", field.id, got, field.want)
						}
					}
					if m.status.stats.Calls != expectedCalls {
						t.Fatal("initial rendering changed model call statistics")
					}
					if m.status.usageCurrent {
						t.Fatal("restored usage was marked as an in-progress call")
					}
				}
				check(len(tc.usages))
				m.startRun("next")
				session.ObserveUsage("next/model-1", model.Usage{})
				m.applyEvent(app.Event{Kind: app.EventRunPhase, RunID: "next", Phase: app.PhaseWaiting, Stats: session.Statistics()})
				// 调用起点确实增加统计；展示仍保留上一请求快照。
				check(len(tc.usages) + 1)
			})
		}
	}
}
