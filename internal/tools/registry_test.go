package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestBuiltinsStrictArguments 守住内置工具的严格参数校验：缺失/多余/重复键/
// null/未知操作一律 invalid_arguments，除零与溢出报各自错误码；声明列表是不可
// 变快照，且 builtins 不含 shell。
func TestBuiltinsStrictArguments(t *testing.T) {
	r := Builtins(func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.FixedZone("test", 3600)) })
	for _, tc := range []struct{ name, args, want string }{
		{"calculate", `{"operation":"multiply","a":6,"b":7}`, `{"ok":true,"result":42}`},
		{"current_time", `{}`, `{"ok":true,"result":"2026-10-09T11:00:00Z"}`},
		{"calculate", `{"operation":"divide","a":1,"b":0}`, `division_by_zero`},
		{"calculate", `{"operation":"multiply","a":1e308,"b":1e308}`, `overflow`},
		{"calculate", `{"operation":"add","a":1}`, `invalid_arguments`},
		{"calculate", `{"operation":"add","a":1,"b":2,"extra":0}`, `invalid_arguments`},
		{"calculate", `{"operation":"add","a":1,"a":2,"b":2}`, `invalid_arguments`},
		{"calculate", `{"operation":"add","a":null,"b":2}`, `invalid_arguments`},
		{"calculate", `{"operation":"shell","a":1,"b":2}`, `invalid_arguments`},
		{"current_time", `{"timezone":"UTC"}`, `invalid_arguments`},
		{"current_time", `null`, `invalid_arguments`},
		{"current_time", `{} {}`, `invalid_arguments`},
		{"shell", `{}`, `unknown_tool`},
	} {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			result := r.Execute(context.Background(), tc.name, tc.args)
			if !strings.Contains(result.JSON(), tc.want) {
				t.Fatalf("got %s, want %s", result.JSON(), tc.want)
			}
		})
	}
	decl := r.Declarations()
	if len(decl) != 2 || decl[0].Name != "calculate" || decl[1].Name != "current_time" {
		t.Fatalf("declarations: %+v", decl)
	}
	decl[0].Name = "changed"
	if r.Declarations()[0].Name != "calculate" {
		t.Fatal("registry declarations mutated")
	}
}

// TestRegistryDuplicateAndCancel 守住注册表的两条底线：重名工具定义必须拒绝
// 注册，已取消的上下文让执行直接返回 cancelled。
func TestRegistryDuplicateAndCancel(t *testing.T) {
	d := Definition{Name: "x", Parameters: `{"type":"object"}`, Execute: func(context.Context, string) Result { return Result{OK: true} }}
	_, err := New(d, d)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal("duplicate allowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := Builtins(time.Now).Execute(ctx, "current_time", `{}`); result.Code != "cancelled" {
		t.Fatalf("cancel: %+v", result)
	}
}
