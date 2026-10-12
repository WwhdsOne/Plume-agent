package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
)

// cleanupCloser 在测试里注入关闭边界，避免启动真实终端或阻塞文件系统。
type cleanupCloser struct{ close func() error }

func (c cleanupCloser) Close() error { return c.close() }

// stalledClient 模拟取消后仍未退出的底层调用；只有测试释放后才返回。
type stalledClient struct {
	model.Client
	started chan struct{}
	release chan struct{}
}

func (c *stalledClient) Stream(context.Context, model.ChatRequest) (model.EventStream, error) {
	close(c.started)
	<-c.release
	return nil, io.EOF
}

// TestChatShutdownRetainsResourcesUntilExecutionStops 守住退出期限到达时
// 不能关闭仍被 run 使用的工具根、trace 或数据库并提前释放执行锁。
func TestChatShutdownRetainsResourcesUntilExecutionStops(t *testing.T) {
	client := &stalledClient{started: make(chan struct{}), release: make(chan struct{})}
	service := app.NewService(agent.New(client, "m"))
	if _, err := service.Submit(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	closed := make(chan struct{}, 3)
	closer := cleanupCloser{close: func() error { closed <- struct{}{}; return nil }}
	resources := chatResources{timeout: 30 * time.Millisecond, service: service, runtime: closer, history: closer, recorder: closer}
	started := time.Now()
	err := resources.close()
	close(client.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if stopErr := service.CloseContext(ctx); stopErr != nil {
		t.Fatal(stopErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "not saved/interrupted") {
		t.Fatalf("shutdown error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("shutdown ignored total deadline")
	}
	select {
	case <-closed:
		t.Fatal("resources released while run was unresolved")
	default:
	}
}

// TestChatShutdownResourceCloseSharesDeadline 守住无 context 的 Close 也受
// 总期限限制，到期后不再开始其他释放动作，测试结束后释放阻塞调用。
func TestChatShutdownResourceCloseSharesDeadline(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	started := make(chan struct{})
	blocking := cleanupCloser{close: func() error { close(started); <-release; close(finished); return nil }}
	closedHistory := false
	resources := chatResources{timeout: 30 * time.Millisecond, runtime: blocking, history: cleanupCloser{close: func() error { closedHistory = true; return nil }}}
	err := resources.close()
	close(release)
	<-finished
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v", err)
	}
	if closedHistory {
		t.Fatal("started next close after total deadline")
	}
	select {
	case <-started:
	default:
		t.Fatal("resource close never started")
	}
}

// TestChatShutdownClosesInOwnershipOrder 守住执行已停止后先释放工具、
// 历史，最后刷 trace；工作区采集在同一个流程内结束。
func TestChatShutdownClosesInOwnershipOrder(t *testing.T) {
	var order []string
	closer := func(name string) cleanupCloser {
		return cleanupCloser{close: func() error { order = append(order, name); return nil }}
	}
	workspace := make(chan app.WorkspaceStatus)
	resources := chatResources{timeout: time.Second, runtime: closer("tools"), history: closer("history"), recorder: closer("trace"), workspaceEvents: workspace,
		cancelWorkspace: func() { order = append(order, "cancel-workspace"); close(workspace) }}
	if err := resources.close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"cancel-workspace", "tools", "history", "trace"}; !slices.Equal(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
}

// TestBuildRuntimeBindsAPIModelNameNotConfigID 守住 G1b.2 踩过的坑：
// agent 必须绑定 API 模型名（Model.Model），而不是配置 ID（Model.ID）。
// 混用会被供应商以 400 invalid_request_error 拒绝。
func TestBuildRuntimeBindsAPIModelNameNotConfigID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  "deepseek-default", // 配置 ID
		Models: []config.ModelConfig{{
			ID:       "deepseek-default",
			Provider: "deepseek",
			Protocol: "deepseek",
			BaseURL:  srv.URL,          // 回环 httptest，Build 只装配不请求
			Model:    "deepseek-flash", // API 模型名
		}},
	}

	runtime, label, err := buildRuntime(cfg, "")
	if err != nil {
		t.Fatalf("buildRuntime error = %v", err)
	}
	if runtime.ModelID() != "deepseek-flash" {
		t.Errorf("runtime model = %q, want API model name deepseek-flash (not the config id)", runtime.ModelID())
	}
	if label != "deepseek/deepseek-flash" {
		t.Errorf("label = %q, want deepseek/deepseek-flash", label)
	}
}

func TestBuildRuntimeRejectsUnknownModelFlag(t *testing.T) {
	cfg := &config.Config{SchemaVersion: config.SchemaVersion, DefaultModel: "a"}
	if _, _, err := buildRuntime(cfg, "nope"); err == nil {
		t.Fatal("unknown --model must be rejected")
	}
}
