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
