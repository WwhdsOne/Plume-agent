package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	modelopenai "plume-agent/internal/model/openai"
	"plume-agent/internal/store"
	"plume-agent/internal/telemetry"
)

// 响应头之前取消真实 SDK 请求，仍须保存 cancelled、零成功 turns，trace
// 保持执行取消与保存成功两个独立结论，不能退化为普通模型失败。
func TestPersistenceSDKCancellationBeforeResponseHeaders(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		started, release := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(started)
			// SDK 未收到响应头时返回取消错误；独立 release 避免依赖服务端
			// 是否及时感知客户端断连，并保证测试清理不会卡在 server.Close。
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(release) })
		client, err := modelopenai.NewAdapter("custom-openai", server.URL, "")
		if err != nil {
			t.Fatal(err)
		}
		var trace bytes.Buffer
		recorder := telemetry.NewModelRecorder(&trace)
		service := NewService(agent.New(client, "test-model"), recorder)
		t.Cleanup(service.Close)
		service.EnablePersistence(backend, "/same-workspace", store.ModelRef{ConfigID: "test", Model: "test-model"}, time.Second)
		if _, err := service.Submit(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("SDK request did not reach fixture")
		}
		service.Cancel()
		last := terminal(t, drainEvents(t, service))
		if !errors.Is(last.Err, context.Canceled) {
			t.Errorf("cancellation identity lost: %v", last.Err)
		}
		var classified *model.Error
		if !errors.As(last.Err, &classified) || classified.Code != model.ErrCancelled {
			t.Errorf("SDK cancellation classification lost: %v", last.Err)
		}
		runs, err := backend.ListRuns(context.Background(), service.SessionID())
		if err != nil || len(runs) != 1 || runs[0].Status != store.RunCancelled {
			t.Errorf("runs = %+v, err = %v; want cancelled", runs, err)
		}
		messages, nextSeq, err := backend.LoadTurns(context.Background(), service.SessionID())
		if err != nil || len(messages) != 0 || nextSeq != 1 || service.Session().Turns() != 0 {
			t.Errorf("cancelled run committed turns: messages=%d nextSeq=%d err=%v", len(messages), nextSeq, err)
		}
		found := false
		for _, line := range bytes.Split(trace.Bytes(), []byte{'\n'}) {
			var event struct {
				Message    string `json:"msg"`
				RunStatus  string `json:"run_status"`
				SaveStatus string `json:"save_status"`
				ErrorCode  string `json:"error_code"`
			}
			if len(line) == 0 {
				continue
			}
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatal(err)
			}
			if event.Message == "run_ended" {
				found = true
				if event.RunStatus != "cancelled" || event.SaveStatus != "saved" || event.ErrorCode != "cancelled" {
					t.Errorf("trace lost cancellation/save status: %+v", event)
				}
			}
		}
		if !found {
			t.Error("missing run_ended trace")
		}
	})
}
