package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// configuredHistoryService 构造绑定持久历史与命令配置的服务，供退出摘要测试。
func configuredHistoryService(t *testing.T, fake model.Client) (*app.Service, *store.MemoryStore) {
	t.Helper()
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	svc := app.NewService(agent.New(fake, "m"))
	if err := svc.ConfigureCommands(app.CommandOptions{History: history, Scope: app.LocalScope(), Workspace: "/ws",
		ModelRef: store.ModelRef{ConfigID: "c"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, history
}

// TestPrintExitSummaryOutput 守住退出摘要：在线持久历史且有已登记 run 时
// 输出会话 ID 与恢复方法；结论绑定本轮 run，超时如实 unknown；空会话、
// offline/禁用历史不打印成功承诺。
func TestPrintExitSummaryOutput(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message: model.Message{Role: model.RoleAssistant, Content: "ok"}, FinishReason: model.FinishStop,
	}})
	svc, _ := configuredHistoryService(t, fake)
	ctx := context.Background()

	// case: 空会话不打印。
	cfg := &config.Config{SchemaVersion: config.SchemaVersion}
	snapshot := svc.CaptureExitState()
	var out bytes.Buffer
	printExitSummary(&out, svc, snapshot, true, cfg)
	if out.Len() != 0 {
		t.Fatalf("empty session summary = %q", out.String())
	}

	// case: 成功 run 后正常收尾打印 saved 与恢复方法。
	if _, err := svc.Submit(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	terminal := false
	deadline := time.After(2 * time.Second)
	for !terminal {
		select {
		case event := <-svc.Events():
			if event.Kind == app.EventRunCompleted || event.Kind == app.EventRunFailed {
				terminal = true
			}
		case <-deadline:
			t.Fatal("terminal event missing")
		}
	}
	snapshot = svc.CaptureExitState()
	out.Reset()
	printExitSummary(&out, svc, snapshot, true, cfg)
	text := out.String()
	if !strings.Contains(text, "Session: "+snapshot.SessionID) || !strings.Contains(text, "History: saved") ||
		!strings.Contains(text, "plume -c") || !strings.Contains(text, "/resume "+snapshot.SessionID) {
		t.Fatalf("summary = %q", text)
	}

	// case: 收尾超时打印 unknown，不打印 saved。
	out.Reset()
	printExitSummary(&out, svc, snapshot, false, cfg)
	if !strings.Contains(out.String(), "History: unknown") {
		t.Fatalf("timeout summary = %q", out.String())
	}

	// case: 禁用历史不打印。
	off := false
	disabled := &config.Config{SchemaVersion: config.SchemaVersion}
	disabled.Sessions = &config.SessionsConfig{History: &config.HistoryConfig{Enabled: &off}}
	out.Reset()
	printExitSummary(&out, svc, snapshot, true, disabled)
	if out.Len() != 0 {
		t.Fatalf("disabled history summary = %q", out.String())
	}
}
