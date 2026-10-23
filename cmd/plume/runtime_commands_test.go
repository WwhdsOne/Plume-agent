package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// TestCommandRuntimeFactoryUsesStartupSnapshotWithoutProbe 守住恢复只读取启动配置和当前凭据，
// 不热加载损坏的新配置文件，也不为预览、恢复、新建发送付费探测请求。
func TestCommandRuntimeFactoryUsesStartupSnapshotWithoutProbe(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cfg := testResumeConfig(server.URL)
	cfg.Tools, cfg.Agent, cfg.Sessions = config.DefaultTools(), config.DefaultAgent(), config.DefaultSessions()
	cfg.Tools.Workspace = t.TempDir()
	cfg.Models[0].APIKeyRef = "model-key"
	if err := config.SetCredential("model-key", []byte("fixture-only")); err != nil {
		t.Fatal(err)
	}
	history := store.NewMemory()
	service := app.NewService(agent.New(model.NewFake(model.FakeScript{}), "initial"))
	t.Cleanup(func() { service.Close(); _ = service.CloseRuntimes(); _ = history.Close() })
	if err := configureServiceCommands(service, cfg, &cfg.Models[0], history, false, cfg.Tools.Workspace, nil); err != nil {
		t.Fatal(err)
	}
	// case: 启动后磁盘配置变坏，恢复仍按已授权快照装配。
	dir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("broken config"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := service.HandleInput(context.Background(), app.CommandRequest{Text: "/new named", Scope: app.LocalScope(), Entrance: app.EntranceTUI})
	if result.Code != "ok" || result.Switch == nil || result.Switch.Model.Model != cfg.Models[0].Model {
		t.Fatalf("new result = %+v", result)
	}
	if calls.Load() != 0 {
		t.Fatal("runtime assembly made a model request")
	}
}

// TestOfflineCommandAssemblyAvoidsRealConfiguration 守住离线公共指令和 /demo 共用内存后端，
// 实际配置、持久库和人格文件即使不可用也不影响离线新建及演示。
func TestOfflineCommandAssemblyAvoidsRealConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLUME_HOME", filepath.Join(dir, "unusable"))
	if err := os.WriteFile(filepath.Join(dir, "unusable"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	runtime, err := buildOfflineRuntime()
	if err != nil {
		t.Fatal(err)
	}
	history := store.NewMemory()
	service := app.NewService(runtime)
	t.Cleanup(func() { service.Close(); _ = service.CloseRuntimes(); _ = history.Close() })
	workspace, err := workspaceDir(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := configureServiceCommands(service, nil, nil, history, true, workspace, nil); err != nil {
		t.Fatal(err)
	}
	result := handleTUIInput(context.Background(), service, true, "/new offline")
	if result.Code != "ok" || result.Switch == nil || result.Switch.Model.Provider != "fake" {
		t.Fatalf("offline new = %+v", result)
	}
	result = handleTUIInput(context.Background(), service, true, "/demo calculate")
	if result.Code != "ok" || result.RunID == "" {
		t.Fatalf("offline demo = %+v", result)
	}
	for event := range service.Events() {
		if event.Kind == app.EventRunCompleted {
			if !strings.Contains(event.Reply, "42") {
				t.Fatalf("demo reply = %q", event.Reply)
			}
			break
		}
		if event.Kind == app.EventRunFailed {
			t.Fatal(event.Err)
		}
	}
	result = handleTUIInput(context.Background(), service, false, "/demo calculate")
	if result.Code != "unknown_command" {
		t.Fatalf("online demo must be unknown: %+v", result)
	}
}
