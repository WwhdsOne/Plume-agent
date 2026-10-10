package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"plume-agent/internal/model"
)

// mapCreds 是脚本化凭据源。
type mapCreds map[string]string

func (m mapCreds) Key(ref string) (string, error) {
	if key, ok := m[ref]; ok {
		return key, nil
	}
	return "", errors.New("credential not found")
}

// schema v1 的两个历史协议选择器（deepseek/openai-compatible）都必须能装配出可用 Client。
func TestBuildMapsBothPresetProtocols(t *testing.T) {
	registry := NewRegistry()
	factory := NewModelFactory(registry, mapCreds{"ds-key": "sk-test"})

	cases := []ModelSpec{
		{Provider: "deepseek", Protocol: ProtocolDeepSeek, BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKeyRef: "ds-key"},
		{Provider: "custom-openai", Protocol: ProtocolOpenAICompatible, BaseURL: "http://127.0.0.1:9000", Model: "m"},
	}
	for i, spec := range cases {
		client, err := factory.Build(spec)
		if err != nil {
			t.Fatalf("case %d: Build(%+v) error = %v", i, spec, err)
		}
		if client == nil {
			t.Fatalf("case %d: Build returned nil client", i)
		}
	}
}

// 非法 ModelSpec（缺字段、未知供应商、协议不匹配、凭据缺失、不安全 URL 等）一律以 invalid_config 拒绝。
func TestBuildRejectsInvalidSpecs(t *testing.T) {
	registry := NewRegistry()
	factory := NewModelFactory(registry, mapCreds{})

	cases := map[string]ModelSpec{
		"missing provider":    {Protocol: ProtocolOpenAICompatible, BaseURL: "http://127.0.0.1:1", Model: "m"},
		"missing model":       {Provider: "deepseek", Protocol: ProtocolDeepSeek, BaseURL: "https://api.deepseek.com"},
		"missing protocol":    {Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "m"},
		"unknown provider":    {Provider: "qwen", Protocol: "native", BaseURL: "https://x.example.com", Model: "m"},
		"protocol mismatch":   {Provider: "deepseek", Protocol: ProtocolOpenAICompatible, BaseURL: "https://api.deepseek.com", Model: "m"},
		"missing base url":    {Provider: "deepseek", Protocol: ProtocolDeepSeek, Model: "m"},
		"missing credentials": {Provider: "deepseek", Protocol: ProtocolDeepSeek, BaseURL: "https://api.deepseek.com", Model: "m", APIKeyRef: "nope"},
		"nil credentials src": {Provider: "deepseek", Protocol: ProtocolDeepSeek, BaseURL: "https://api.deepseek.com", Model: "m", APIKeyRef: "any"},
		"unknown protocol":    {Provider: "custom-openai", Protocol: "native-anthropic", BaseURL: "http://127.0.0.1:1", Model: "m"},
		"unsafe base url":     {Provider: "deepseek", Protocol: ProtocolDeepSeek, BaseURL: "https://user:pass@api.deepseek.com", Model: "m"},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := factory.Build(spec)
			var mErr *model.Error
			if !errors.As(err, &mErr) {
				t.Fatalf("Build error = %v (%T), want *model.Error", err, err)
			}
			if mErr.Code != model.ErrInvalidConfig {
				t.Fatalf("code = %q, want invalid_config", mErr.Code)
			}
		})
	}
}

// 守住 custom-openai 可选凭据契约：无 Key 引用甚至 nil 凭据源也必须能构造 Client。
func TestBuildWithoutCredentialsSucceedsForOptionalKey(t *testing.T) {
	// custom-openai 允许无 Key（本机服务）；nil 凭据源也能构造。
	registry := NewRegistry()
	factory := NewModelFactory(registry, nil)
	spec := ModelSpec{Provider: "custom-openai", Protocol: ProtocolOpenAICompatible, BaseURL: "http://127.0.0.1:9000", Model: "m"}
	if _, err := factory.Build(spec); err != nil {
		t.Fatalf("Build error = %v", err)
	}
}

// Probe 是显式单次探测：恰好发出一次携带配置 model id 的请求，响应原样透传。
func TestProbeRunsExactlyOneRequest(t *testing.T) {
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"model":"probe-model"`) {
			t.Errorf("probe body = %s, want configured model id", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"probe-1","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"pong"}}]}`)
	}))
	t.Cleanup(srv.Close)

	registry := NewRegistry()
	factory := NewModelFactory(registry, nil)
	spec := ModelSpec{Provider: "custom-openai", Protocol: ProtocolOpenAICompatible, BaseURL: srv.URL, Model: "probe-model"}

	resp, err := factory.Probe(context.Background(), spec)
	if err != nil {
		t.Fatalf("Probe error = %v", err)
	}
	if resp.Message.Content != "pong" {
		t.Errorf("Probe response = %q, want pong", resp.Message.Content)
	}
	if got := count.Load(); got != 1 {
		t.Errorf("probe request count = %d, want exactly 1", got)
	}
}
