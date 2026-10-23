package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 守住错误字符串契约：Error() 输出稳定的单行键值对且字段顺序固定，
// 不得混入换行，供 trace 与测试断言依赖。
func TestErrorFormatStableSingleLine(t *testing.T) {
	err := NewError(ErrRateLimited).
		WithProvider("deepseek", ProtocolOpenAIChatCompletions).
		WithStatus(429).
		WithRequestID("req-123").
		WithSummary("too many requests")

	got := err.Error()
	want := `model error: rate_limited provider=deepseek protocol=openai-chat-completions status=429 request_id=req-123 summary="too many requests"`
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("error string must stay single-line, got %q", got)
	}
}

// 守住 Truncate 的字符语义：按 rune 截断不破坏 UTF-8，未超长原样返回，
// 截断以 … 收尾。
func TestTruncateByRunes(t *testing.T) {
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("Truncate short string = %q, want unchanged", got)
	}
	got := Truncate(strings.Repeat("a", 20), 5)
	if len([]rune(got)) != 6 || !strings.HasSuffix(got, "…") {
		t.Errorf("Truncate = %q, want 5 runes plus ellipsis", got)
	}
	multibyte := Truncate("中文测试字符串", 2)
	if multibyte != "中文…" {
		t.Errorf("Truncate multibyte = %q, want 中文…", multibyte)
	}
}

// 守住 context 错误分类：DeadlineExceeded 归 timeout，Canceled（含包装）
// 归 cancelled，其余错误不强行归类。
func TestClassifyContext(t *testing.T) {
	if code := ClassifyContext(context.DeadlineExceeded); code != ErrTimeout {
		t.Errorf("DeadlineExceeded classified as %q, want timeout", code)
	}
	if code := ClassifyContext(context.Canceled); code != ErrCancelled {
		t.Errorf("Canceled classified as %q, want cancelled", code)
	}
	if code := ClassifyContext(fmt.Errorf("wrapped: %w", context.Canceled)); code != ErrCancelled {
		t.Errorf("wrapped Canceled classified as %q, want cancelled", code)
	}
	if code := ClassifyContext(errors.New("plain")); code != "" {
		t.Errorf("plain error classified as %q, want empty", code)
	}
}

// 统一错误保留标准取消/期限身份；包装后仍可判断，其他分类不能冒充取消。
func TestErrorPreservesContextIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   ErrCode
		target error
		want   bool
	}{
		// case: 取消与期限分别匹配对应的标准错误。
		{"cancelled", ErrCancelled, context.Canceled, true},
		{"timeout", ErrTimeout, context.DeadlineExceeded, true},
		// case: 超时不能被误判为用户取消，取消也不是期限耗尽。
		{"timeout_not_cancelled", ErrTimeout, context.Canceled, false},
		{"cancelled_not_timeout", ErrCancelled, context.DeadlineExceeded, false},
		// case: 其他模型分类及任意普通错误都不能凭错误文本匹配。
		{"transport_not_cancelled", ErrTransport, context.Canceled, false},
		{"upstream_not_timeout", ErrUpstream, context.DeadlineExceeded, false},
		{"unrelated_target", ErrCancelled, errors.New("context canceled"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classified := NewError(tc.code).WithSummary("redacted failure")
			for _, err := range []error{classified, fmt.Errorf("model call: %w", classified)} {
				// case: 直接错误和调用方附加上下文的包装都遵守相同身份。
				if got := errors.Is(err, tc.target); got != tc.want {
					t.Errorf("errors.Is(%v, %v) = %v, want %v", err, tc.target, got, tc.want)
				}
			}
		})
	}
}
