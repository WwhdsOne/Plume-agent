package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrCode 是统一错误分类（0003 §6）。错误字符串保持英文，便于 grep
// 与测试断言；给用户的解释在展示层完成。
type ErrCode string

// ErrBudgetExceeded 表示调用数或字节预算不足，不当作供应商错误。
const ErrBudgetExceeded ErrCode = "budget_exceeded"

const (
	ErrInvalidConfig    ErrCode = "invalid_config"
	ErrUnsupported      ErrCode = "unsupported"
	ErrAuthentication   ErrCode = "authentication"
	ErrRateLimited      ErrCode = "rate_limited"
	ErrUpstream         ErrCode = "upstream"
	ErrTransport        ErrCode = "transport"
	ErrTimeout          ErrCode = "timeout"
	ErrCancelled        ErrCode = "cancelled"
	ErrInvalidResponse  ErrCode = "invalid_response"
	ErrResponseTooLarge ErrCode = "response_too_large"
	ErrStreamInterrupt  ErrCode = "stream_interrupted"
)

// MaxErrorBodyBytes 是读取错误正文的上限（0003 §4）。
const MaxErrorBodyBytes = 16 << 10

// Error 是模型调用的统一错误：携带分类、provider/内部协议、HTTP 状态
// （若有）、供应商请求 ID（若有）与已脱敏的摘要。
type Error struct {
	Code       ErrCode
	Provider   string
	Protocol   string
	StatusCode int
	RequestID  string
	// Summary 必须是已脱敏、已裁剪的摘要；不得包含 Key、Authorization
	// 或完整响应体。
	Summary string
}

// Error 实现 error 接口。格式稳定为单行键值对，便于 trace 与测试断言。
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("model error: ")
	b.WriteString(string(e.Code))
	if e.Provider != "" {
		fmt.Fprintf(&b, " provider=%s", e.Provider)
	}
	if e.Protocol != "" {
		fmt.Fprintf(&b, " protocol=%s", e.Protocol)
	}
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " status=%d", e.StatusCode)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " request_id=%s", e.RequestID)
	}
	if e.Summary != "" {
		fmt.Fprintf(&b, " summary=%q", e.Summary)
	}
	return b.String()
}

// NewError 构造一个分类错误，随后可用 With* 方法补齐上下文。
func NewError(code ErrCode) *Error { return &Error{Code: code} }

// WithProvider 补记供应商品牌与内部协议族。
func (e *Error) WithProvider(provider, protocol string) *Error {
	e.Provider, e.Protocol = provider, protocol
	return e
}

// WithStatus 补记 HTTP 状态码。
func (e *Error) WithStatus(status int) *Error {
	e.StatusCode = status
	return e
}

// WithRequestID 补记供应商请求 ID。
func (e *Error) WithRequestID(id string) *Error {
	e.RequestID = id
	return e
}

// WithSummary 补记脱敏摘要（自动裁剪到 maxSummaryRunes）。
func (e *Error) WithSummary(summary string) *Error {
	e.Summary = Truncate(summary, maxSummaryRunes)
	return e
}

// maxSummaryRunes 限制摘要长度，避免把大段响应体带进错误与 trace。
const maxSummaryRunes = 512

// Truncate 按字符数截断（不截断 UTF-8 序列），超出部分以 … 标记。
func Truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// ClassifyContext 把 context 相关错误映射为 timeout/cancelled 分类；
// 不是 context 错误时返回空串。
func ClassifyContext(err error) ErrCode {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrTimeout
	case errors.Is(err, context.Canceled):
		return ErrCancelled
	default:
		return ""
	}
}
