package tools

import (
	"context"
	"time"
)

// Builtins 只装配算术与时间；固定时钟让离线证据可复现。
func Builtins(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	r, err := New(
		Definition{Name: "calculate", Description: "Perform one arithmetic operation on two finite numbers.", Parameters: `{"type":"object","properties":{"operation":{"type":"string","enum":["add","subtract","multiply","divide"]},"a":{"type":"number"},"b":{"type":"number"}},"required":["operation","a","b"],"additionalProperties":false}`, Execute: calculate},
		Definition{Name: "current_time", Description: "Read the current time in UTC, formatted as RFC3339.", Parameters: `{"type":"object","properties":{},"additionalProperties":false}`, Execute: func(ctx context.Context, raw string) Result {
			var args struct{}
			if decodeArguments(raw, &args) != nil {
				return Result{Code: "invalid_arguments"}
			}
			if ctx.Err() != nil {
				return Result{Code: "cancelled"}
			}
			return Result{OK: true, Value: now().UTC().Format(time.RFC3339)}
		}},
	)
	if err != nil {
		panic("invalid built-in tool definitions")
	}
	return r
}
