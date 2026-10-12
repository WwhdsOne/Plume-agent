package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/tui"
)

// TestContinueRestoresContextBeforeAnyRequest 守住 -c 的完整恢复装配：重新打开
// SQLite 后，首次 View 就展示持久用量，不依赖再发请求或伪造一次 usage 事件。
func TestContinueRestoresContextBeforeAnyRequest(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	history, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testResumeConfig("https://api.example/v1")
	capacity := int64(1000000)
	cfg.Models[0].ContextWindowTokens = &capacity
	seedHistory(t, history, modelRefOf(&cfg.Models[0]))
	if err := history.Close(); err != nil {
		t.Fatal(err)
	}
	history, err = store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	resume, err := findResumableSession(ctx, history, cfg, "", "/ws")
	if err != nil || resume == nil {
		t.Fatalf("resume = %+v, err = %v", resume, err)
	}
	fake := model.NewFake(model.FakeScript{})
	service := app.NewService(agent.New(fake, "m"))
	t.Cleanup(service.Close)
	if err := adoptResumedSession(ctx, service, history, resume); err != nil {
		t.Fatal(err)
	}
	stats := service.Session().Statistics()
	if !stats.LastUsage.OK || stats.LastUsage.PromptTokens != 3 {
		t.Fatalf("persisted usage was not restored: %+v", stats)
	}
	chat := tui.NewWithOptions("deepseek/deepseek-flash", tui.Hooks{}, tui.Options{
		InitialStats: &stats, InitialTranscript: resume.transcript,
		ContextWindowTokens: resume.selected.ContextWindowTokens,
	})
	chat, _ = chat.Update(tea.WindowSizeMsg{Width: 180, Height: 30})
	view := ansi.Strip(chat.View())
	if strings.Contains(view, "ctx: usage unknown") || !strings.Contains(view, "] 3/1M") {
		t.Fatalf("-c lost restored context in initial View: %q", view)
	}
	if len(fake.Calls()) != 0 {
		t.Fatal("restoring the status bar sent a model request")
	}
}
