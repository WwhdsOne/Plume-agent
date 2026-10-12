package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/telemetry"
)

// TestHistoryFailureTraceKeepsExecutionAndSaveSeparate 守住模型成功而落盘失败的两个结果维度。
func TestHistoryFailureTraceKeepsExecutionAndSaveSeparate(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "private response"}, FinishReason: model.FinishStop}})
	var log bytes.Buffer
	recorder := telemetry.NewModelRecorder(&log)
	svc := NewService(agent.New(fake, "m"), recorder)
	defer svc.Close()
	svc.EnablePersistence(&failStore{Store: store.NewMemory(), failCompleteRun: true}, "/ws", store.ModelRef{}, time.Second)
	if _, err := svc.Submit(context.Background(), "private input"); err != nil {
		t.Fatal(err)
	}
	drainEvents(t, svc)
	svc.waitIdle()
	raw := log.String()
	if !strings.Contains(raw, `"run_status":"completed"`) || !strings.Contains(raw, `"save_status":"failed"`) {
		t.Fatalf("missing independent terminal/save statuses: %s", raw)
	}
	if !strings.Contains(raw, `"status":"error"`) {
		t.Fatal("unsaved run was counted as an overall success")
	}
	if strings.Contains(raw, "private input") || strings.Contains(raw, "private response") {
		t.Fatal("trace leaked content")
	}
	logRunEnd(t, raw)
}

// TestHistoryEarlierStorageFailureRemainsInTrace 守住终态保存成功也不能掩盖更早的持久化故障。
func TestHistoryEarlierStorageFailureRemainsInTrace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage string
		store *failStore
	}{
		// case: 调用起点失败，取消模型发送后仍记录独立保存故障。
		{"call start", "call_start", &failStore{Store: store.NewMemory(), failStartCall: true}},
		// case: 用量快照失败，终态仍能保存，但不能归类为未知模型错误。
		{"usage snapshot", "usage", &failStore{Store: store.NewMemory(), failSaveUsage: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
				Message:      model.Message{Role: model.RoleAssistant, Content: "private answer"},
				FinishReason: model.FinishStop, Usage: model.Usage{OK: true, TotalTokens: 1},
			}})
			var log bytes.Buffer
			svc := NewService(agent.New(fake, "m"), telemetry.NewModelRecorder(&log))
			defer svc.Close()
			svc.EnablePersistence(tc.store, "/ws", store.ModelRef{}, time.Second)
			if _, err := svc.Submit(context.Background(), "private input"); err != nil {
				t.Fatal(err)
			}
			drainEvents(t, svc)
			svc.waitIdle()
			raw := log.String()
			for _, field := range []string{`"save_status":"saved"`, `"save_error_code":"history_storage"`, `"save_error_stage":"` + tc.stage + `"`, `"error_code":"history_storage"`} {
				if !strings.Contains(raw, field) {
					t.Fatalf("earlier storage failure was hidden; missing %s in %s", field, raw)
				}
			}
			if strings.Contains(raw, "private input") || strings.Contains(raw, "private answer") {
				t.Fatal("trace leaked content")
			}
			logRunEnd(t, raw)
		})
	}
}

// logRunEnd 只展示无正文的终态 trace，方便审核复现保存结果与故障阶段。
func logRunEnd(t *testing.T, raw string) {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, `"msg":"run_ended"`) {
			t.Log(line)
		}
	}
}

// TestHistoryCloseHonorsTotalDeadline 守住总退出期限，阻塞的流清理不能无限拖住调用方。
func TestHistoryCloseHonorsTotalDeadline(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "done"}, FinishReason: model.FinishStop}})
	svc := NewService(agent.New(reviewCloseGateClient{Client: fake, entered: entered, release: release}, "m"))
	svc.EnablePersistence(store.NewMemory(), "/ws", store.ModelRef{}, 30*time.Millisecond)
	if _, err := svc.Submit(context.Background(), "input"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	closed := make(chan struct{})
	go func() { svc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(200 * time.Millisecond):
		close(release)
		<-closed
		t.Fatal("Close ignored total shutdown deadline")
	}
	if _, err := svc.Submit(context.Background(), "another"); err == nil {
		close(release)
		t.Fatal("timed-out shutdown accepted a new run")
	}
	if err := svc.NewSession(); err == nil {
		close(release)
		t.Fatal("timed-out shutdown switched sessions")
	}
	close(release)
	svc.waitIdle()
}
