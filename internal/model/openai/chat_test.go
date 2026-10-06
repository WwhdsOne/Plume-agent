package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"herald-agent/internal/model"
)

const completionBody = `{
  "id": "cmpl-123",
  "object": "chat.completion",
  "model": "test-model",
  "choices": [
    {
      "index": 0,
      "finish_reason": "stop",
      "message": {"role": "assistant", "content": "hello from fake"}
    }
  ],
  "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}`

// newTestAdapter 构造指向 httptest 服务器的适配器。
func newTestAdapter(t *testing.T, baseURL string) *Adapter {
	t.Helper()
	a, err := NewAdapter("custom", baseURL, "test-key-123")
	if err != nil {
		t.Fatalf("NewAdapter error = %v", err)
	}
	return a
}

// captureServer 返回记录最近一次请求（方法、路径、头、body）的服务器，
// 并以 JSON respBody 应答。Content-Type 必须是 application/json——
// SDK 拒绝解析其他类型（这也是"非 JSON 响应拒绝"契约的依据）。
func captureServer(t *testing.T, respStatus int, respBody string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-capture-42")
		w.WriteHeader(respStatus)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

type capturedRequest struct {
	mu     sync.Mutex
	method string
	path   string
	auth   string
	body   map[string]any
}

func (c *capturedRequest) record(t *testing.T, r *http.Request) {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.method, c.path, c.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c.body); err != nil {
			t.Errorf("request body is not JSON: %v (%s)", err, raw)
		}
	}
}

func (c *capturedRequest) snapshot() (string, string, string, map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.method, c.path, c.auth, c.body
}

func simpleRequest(modelID string) model.ChatRequest {
	return model.ChatRequest{
		Model:    modelID,
		Messages: []model.Message{{Role: model.RoleUser, Content: "Say OK"}},
	}
}

func TestGenerateAssertsURLAuthModelAndBody(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, completionBody)
	adapter := newTestAdapter(t, srv.URL)

	resp, err := adapter.Generate(context.Background(), simpleRequest("test-model"))
	if err != nil {
		t.Fatalf("Generate error = %v", err)
	}

	method, path, auth, body := cap.snapshot()
	if method != http.MethodPost || path != "/chat/completions" {
		t.Errorf("request = %s %s, want POST /chat/completions", method, path)
	}
	if auth != "Bearer test-key-123" {
		t.Errorf("Authorization = %q, want Bearer test-key-123", auth)
	}
	if got, _ := body["model"].(string); got != "test-model" {
		t.Errorf("body model = %v, want test-model", body["model"])
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("body messages = %v, want 1 entry", messages)
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "Say OK" {
		t.Errorf("messages[0] = %v, want user/Say OK", first)
	}

	if resp.Message.Content != "hello from fake" {
		t.Errorf("content = %q, want hello from fake", resp.Message.Content)
	}
	if resp.FinishReason != model.FinishStop {
		t.Errorf("finish reason = %q, want stop", resp.FinishReason)
	}
	if resp.Provider != "custom" || resp.Protocol != model.ProtocolOpenAIChatCompletions {
		t.Errorf("provider/protocol = %q/%q, want custom/openai-chat-completions", resp.Provider, resp.Protocol)
	}
	if !resp.Usage.OK || resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v, want known 10/5/15", resp.Usage)
	}
	if resp.ID != "req-capture-42" {
		t.Errorf("request id = %q, want req-capture-42 from response header", resp.ID)
	}
}

func TestBaseURLVariantsKeepPathPrefix(t *testing.T) {
	// 用户配置的路径前缀必须保留，不猜测补 /v1（0003 §4）。
	cases := []struct {
		suffix string
		want   string
	}{
		{"", "/chat/completions"},                           // 根地址
		{"/v1", "/v1/chat/completions"},                     // 常见前缀
		{"/", "/chat/completions"},                          // 尾斜线不双写
		{"/proxy/openai", "/proxy/openai/chat/completions"}, // 代理前缀
	}
	for _, tc := range cases {
		t.Run("/"+tc.suffix, func(t *testing.T) {
			srv, cap := captureServer(t, http.StatusOK, completionBody)
			adapter := newTestAdapter(t, srv.URL+tc.suffix)
			if _, err := adapter.Generate(context.Background(), simpleRequest("m")); err != nil {
				t.Fatalf("Generate error = %v", err)
			}
			if _, path, _, _ := cap.snapshot(); path != tc.want {
				t.Errorf("path = %q, want %q", path, tc.want)
			}
		})
	}
}

func TestTemperatureOptional(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, completionBody)
	adapter := newTestAdapter(t, srv.URL)

	temp := 0.5
	req := simpleRequest("m")
	req.Temperature = &temp
	if _, err := adapter.Generate(context.Background(), req); err != nil {
		t.Fatalf("Generate error = %v", err)
	}
	if _, _, _, body := cap.snapshot(); body["temperature"] != 0.5 {
		t.Errorf("temperature = %v, want 0.5", body["temperature"])
	}

	srv2, cap2 := captureServer(t, http.StatusOK, completionBody)
	adapter2 := newTestAdapter(t, srv2.URL)
	if _, err := adapter2.Generate(context.Background(), simpleRequest("m")); err != nil {
		t.Fatalf("Generate error = %v", err)
	}
	_, _, _, body2 := cap2.snapshot()
	if _, present := body2["temperature"]; present {
		t.Errorf("temperature should be omitted when nil, got %v", body2["temperature"])
	}
}

func TestToolCallHistoryEncoding(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, completionBody)
	adapter := newTestAdapter(t, srv.URL)

	req := model.ChatRequest{
		Model: "m",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "What time is it?"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "call-1", Name: "clock", Arguments: `{"tz":"UTC"}`}}},
			{Role: model.RoleTool, ToolCallID: "call-1", Content: "12:00"},
		},
	}
	if _, err := adapter.Generate(context.Background(), req); err != nil {
		t.Fatalf("Generate error = %v", err)
	}

	_, _, _, body := cap.snapshot()
	messages, _ := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %v, want 3", messages)
	}
	assistant, _ := messages[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant tool_calls = %v, want 1", assistant["tool_calls"])
	}
	call, _ := calls[0].(map[string]any)
	fn, _ := call["function"].(map[string]any)
	if call["id"] != "call-1" || fn["name"] != "clock" || fn["arguments"] != `{"tz":"UTC"}` {
		t.Errorf("tool call = %v, want call-1/clock/args", call)
	}
	toolMsg, _ := messages[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call-1" {
		t.Errorf("tool message = %v, want role=tool with call id", toolMsg)
	}
}

func TestUsageMissingMeansUnknown(t *testing.T) {
	noUsage := `{"id":"c","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"x"}}]}`
	srv, _ := captureServer(t, http.StatusOK, noUsage)
	adapter := newTestAdapter(t, srv.URL)

	resp, err := adapter.Generate(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatalf("Generate error = %v", err)
	}
	if resp.Usage.OK {
		t.Errorf("usage should be unknown when server omits it, got %+v", resp.Usage)
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("unknown usage must not fabricate token counts: %+v", resp.Usage)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	cases := map[string]model.FinishReason{
		"stop":           model.FinishStop,
		"length":         model.FinishLength,
		"tool_calls":     model.FinishToolCalls,
		"content_filter": model.FinishContentFilter,
		"exotic_reason":  model.FinishUnknown,
		"":               model.FinishUnknown,
	}
	for raw, want := range cases {
		body := fmt.Sprintf(`{"id":"c","choices":[{"finish_reason":%q,"message":{"role":"assistant","content":"x"}}]}`, raw)
		srv, _ := captureServer(t, http.StatusOK, body)
		adapter := newTestAdapter(t, srv.URL)
		resp, err := adapter.Generate(context.Background(), simpleRequest("m"))
		if err != nil {
			t.Fatalf("Generate(%q) error = %v", raw, err)
		}
		if resp.FinishReason != want {
			t.Errorf("finish reason %q mapped to %q, want %q", raw, resp.FinishReason, want)
		}
	}
}

func TestMultiChoiceRejected(t *testing.T) {
	twoChoices := `{"id":"c","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"a"}},{"finish_reason":"stop","message":{"role":"assistant","content":"b"}}]}`
	srv, _ := captureServer(t, http.StatusOK, twoChoices)
	adapter := newTestAdapter(t, srv.URL)

	_, err := adapter.Generate(context.Background(), simpleRequest("m"))
	assertCode(t, err, model.ErrInvalidResponse)
}

func TestEmptyChoicesRejected(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{"id":"c","choices":[]}`)
	adapter := newTestAdapter(t, srv.URL)

	_, err := adapter.Generate(context.Background(), simpleRequest("m"))
	assertCode(t, err, model.ErrInvalidResponse)
}

func TestHTTPStatusClassification(t *testing.T) {
	cases := map[int]model.ErrCode{
		http.StatusUnauthorized:        model.ErrAuthentication,
		http.StatusForbidden:           model.ErrAuthentication,
		http.StatusTooManyRequests:     model.ErrRateLimited,
		http.StatusBadRequest:          model.ErrInvalidResponse,
		http.StatusInternalServerError: model.ErrUpstream,
		http.StatusBadGateway:          model.ErrUpstream,
	}
	for status, want := range cases {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv, _ := captureServer(t, status, `{"error":{"message":"boom","type":"server_error"}}`)
			adapter := newTestAdapter(t, srv.URL)
			_, err := adapter.Generate(context.Background(), simpleRequest("m"))
			var mErr *model.Error
			if !errors.As(err, &mErr) {
				t.Fatalf("error = %v, want *model.Error", err)
			}
			if mErr.Code != want {
				t.Errorf("status %d classified as %q, want %q", status, mErr.Code, want)
			}
			if mErr.StatusCode != status {
				t.Errorf("status code not preserved: %d", mErr.StatusCode)
			}
			if mErr.RequestID != "req-capture-42" {
				t.Errorf("request id = %q, want captured header", mErr.RequestID)
			}
			if mErr.Summary == "" || strings.Contains(mErr.Summary, "test-key") {
				t.Errorf("summary must be present and never contain the key: %q", mErr.Summary)
			}
		})
	}
}

func TestNonJSONAndMalformedResponsesRejected(t *testing.T) {
	cases := map[string]string{
		"html body": "<html>under construction</html>",
		"malformed": `{"id":"c","choices":[{"finish_reason":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := captureServer(t, http.StatusOK, body)
			adapter := newTestAdapter(t, srv.URL)
			_, err := adapter.Generate(context.Background(), simpleRequest("m"))
			assertCode(t, err, model.ErrInvalidResponse)
		})
	}
}

func TestTimeoutClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = io.WriteString(w, completionBody)
	}))
	t.Cleanup(srv.Close)
	adapter := newTestAdapter(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := adapter.Generate(ctx, simpleRequest("m"))
	assertCode(t, err, model.ErrTimeout)
}

func TestCancelClassified(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		// client 侧取消后 SDK 不保证立即关闭底层连接，服务端可能感知不到
		// 断开；release 通道保证测试结束时 handler 一定退出。
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// cleanup 按 LIFO 执行：release close 后注册、先执行，handler 退出后
	// srv.Close 才能等到连接结束。
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	adapter := newTestAdapter(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := adapter.Generate(ctx, simpleRequest("m"))
		errCh <- err
	}()
	<-started
	cancel()
	assertCode(t, <-errCh, model.ErrCancelled)
}

func TestToolDeclarationsRejectedWithoutHTTP(t *testing.T) {
	srv, cap := captureServer(t, http.StatusOK, completionBody)
	adapter := newTestAdapter(t, srv.URL)

	req := simpleRequest("m")
	req.Tools = []model.ToolDeclaration{{Name: "clock"}}
	_, err := adapter.Generate(context.Background(), req)
	assertCode(t, err, model.ErrUnsupported)
	if cap.body != nil {
		t.Errorf("no HTTP request should be sent for unsupported tools")
	}
}

func TestInvalidRequestsRejectedWithoutHTTP(t *testing.T) {
	cases := map[string]model.ChatRequest{
		"empty model":          {Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}},
		"empty messages":       {Model: "m"},
		"bad role":             {Model: "m", Messages: []model.Message{{Role: model.Role("robot"), Content: "x"}}},
		"tool without call id": {Model: "m", Messages: []model.Message{{Role: model.RoleTool, Content: "x"}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := captureServer(t, http.StatusOK, completionBody)
			adapter := newTestAdapter(t, srv.URL)
			_, err := adapter.Generate(context.Background(), req)
			assertCode(t, err, model.ErrInvalidConfig)
		})
	}
}

func TestStreamUnsupported(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, completionBody)
	adapter := newTestAdapter(t, srv.URL)

	_, err := adapter.Stream(context.Background(), simpleRequest("m"))
	assertCode(t, err, model.ErrUnsupported)
}

func assertCode(t *testing.T, err error, want model.ErrCode) {
	t.Helper()
	var mErr *model.Error
	if !errors.As(err, &mErr) {
		t.Fatalf("error = %v (%T), want *model.Error", err, err)
	}
	if mErr.Code != want {
		t.Fatalf("code = %q, want %q", mErr.Code, want)
	}
}
