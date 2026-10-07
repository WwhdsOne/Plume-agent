package endpoint

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"plume-agent/internal/model"
)

// callCountServer 返回始终以 status 应答的服务器与请求计数器。
func callCountServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func newClient(t *testing.T, baseURL, apiKey string) openai.Client {
	t.Helper()
	opts, err := Options(baseURL, apiKey)
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	return openai.NewClient(opts...)
}

func TestValidateRejectsPlainHTTPForPublicHost(t *testing.T) {
	if _, err := Validate("http://api.deepseek.com"); err == nil {
		t.Fatal("plain http for public host should be rejected")
	}
}

func TestValidateAllowsLoopbackHTTP(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "http://localhost:9000", "http://[::1]:1"} {
		if _, err := Validate(raw); err != nil {
			t.Errorf("Validate(%q) error = %v, want allowed", raw, err)
		}
	}
}

func TestValidateRejectsUserinfoAndFragment(t *testing.T) {
	if _, err := Validate("https://user:pass@api.example.com"); err == nil {
		t.Error("userinfo must be rejected")
	}
	if _, err := Validate("https://api.example.com/v1#anchor"); err == nil {
		t.Error("fragment must be rejected")
	}
}

func TestValidatePreservesPathPrefix(t *testing.T) {
	got, err := Validate("https://api.example.com/v1/")
	if err != nil {
		t.Fatalf("Validate error = %v", err)
	}
	if got != "https://api.example.com/v1/" {
		t.Errorf("Validate = %q, want path prefix preserved", got)
	}
}

func TestOptionsDisableRetries(t *testing.T) {
	// SDK 默认重试 2 次；WithMaxRetries(0) 必须生效，500 也只发一次。
	srv, count := callCountServer(t, http.StatusInternalServerError)
	client := newClient(t, srv.URL, "test-key")
	_, _ = client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "m",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if got := count.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1 (auto-retry must stay disabled)", got)
	}
}

func TestOptionsDisableRedirects(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := newClient(t, srv.URL, "test-key")
	var httpResp *http.Response
	_, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "m",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	}, option.WithResponseInto(&httpResp))

	// 重定向必须不被跟随：只发一次请求，调用方拿到原始 302 响应。
	if got := hits.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if httpResp == nil || httpResp.StatusCode != http.StatusFound {
		t.Fatalf("redirect must surface as raw 302 response, got resp=%v err=%v", httpResp, err)
	}
}

func TestOversizedContentLengthRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "9437184") // 9 MiB > 8 MiB 上限
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1<<20)) // 只写一小段，客户端在预检处中止
	}))
	t.Cleanup(srv.Close)

	client := newClient(t, srv.URL, "test-key")
	_, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "m",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	var mErr *model.Error
	if !errors.As(err, &mErr) {
		t.Fatalf("error = %v, want *model.Error from limit transport", err)
	}
	if mErr.Code != model.ErrResponseTooLarge {
		t.Errorf("code = %q, want response_too_large", mErr.Code)
	}
}

func TestEmptyKeySendsNoAuthorizationEvenWithEnvLeak(t *testing.T) {
	// 显式空 Key 必须删除鉴权头：环境变量泄漏的凭据不得悄悄生效。
	t.Setenv("OPENAI_API_KEY", "sk-env-secret-should-not-leak")

	var sawAuth, sawHeader atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Add(1)
		}
		if r.Header.Get("X-Api-Key") != "" {
			sawHeader.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	client := newClient(t, srv.URL, "")
	_, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "m",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if sawAuth.Load() != 0 || sawHeader.Load() != 0 {
		t.Errorf("empty key must send no auth headers (auth=%d, api-key=%d)", sawAuth.Load(), sawHeader.Load())
	}
}

func TestAPIKeySentAsBearer(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	client := newClient(t, srv.URL, "test-key-123")
	_, _ = client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "m",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if auth, _ := got.Load().(string); auth != "Bearer test-key-123" {
		t.Errorf("Authorization = %q, want Bearer test-key-123", auth)
	}
}
