package deepseek

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"herald-agent/internal/model"
)

func TestDeepseekAdapterComposesCompatibleImplementation(t *testing.T) {
	// DeepSeek 适配器组合兼容实现；本测试锁定 provider 标识与基本转发，
	// 供应商差异在后续单元验证后落在本包。
	const body = `{"id":"cmpl-9","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"deepseek ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	adapter, err := NewAdapter(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("NewAdapter error = %v", err)
	}
	resp, err := adapter.Generate(context.Background(), model.ChatRequest{
		Model:    "deepseek-flash",
		Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate error = %v", err)
	}
	if resp.Provider != "deepseek" {
		t.Errorf("provider = %q, want deepseek", resp.Provider)
	}
	if resp.Protocol != model.ProtocolOpenAIChatCompletions {
		t.Errorf("protocol = %q, want openai-chat-completions", resp.Protocol)
	}
	if resp.Message.Content != "deepseek ok" {
		t.Errorf("content = %q", resp.Message.Content)
	}
}

func TestDeepseekStreamUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	adapter, err := NewAdapter(srv.URL, "")
	if err != nil {
		t.Fatalf("NewAdapter error = %v", err)
	}
	if _, err := adapter.Stream(context.Background(), model.ChatRequest{}); err == nil {
		t.Fatal("Stream should return unsupported until G1b.3")
	}
}
