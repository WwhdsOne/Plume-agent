package app

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/tools"
)

// BenchmarkSessionCommandsSQLite 测量真实 SQLite 收据/绑定写入和恢复读取，
// 每次装配真实工作区句柄。结果只代表本地管理链路，不推断模型或平台延迟。
func BenchmarkSessionCommandsSQLite(b *testing.B) {
	for _, operation := range []string{"new", "resume"} {
		b.Run(operation, func(b *testing.B) {
			ctx := context.Background()
			workspace := b.TempDir()
			backend, err := store.OpenSQLite(filepath.Join(b.TempDir(), "history.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = backend.Close() })
			build := func(context.Context, store.ModelRef) (*agent.Runtime, error) {
				options := tools.DefaultWorkspaceOptions()
				options.Root = workspace
				registry, err := tools.NewWorkspace(options)
				if err != nil {
					return nil, err
				}
				runtime := agent.New(model.NewFake(), "bench")
				runtime.SetTools(registry)
				return runtime, nil
			}
			runtime, err := build(ctx, store.ModelRef{})
			if err != nil {
				b.Fatal(err)
			}
			service := NewService(runtime)
			b.Cleanup(func() { service.Close(); _ = service.CloseRuntimes() })
			if err := service.ConfigureCommands(CommandOptions{History: backend, Scope: LocalScope(), Workspace: workspace, BuildRuntime: build}); err != nil {
				b.Fatal(err)
			}
			var ids []string
			if operation == "resume" {
				for i := 0; i < 2; i++ {
					result := service.HandleInput(ctx, CommandRequest{Text: fmt.Sprintf("/new seed-%d", i), Entrance: EntranceTUI})
					if result.Code != "ok" {
						b.Fatal(result)
					}
					ids = append(ids, result.SessionID)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				text := fmt.Sprintf("/new benchmark-%d", i)
				if operation == "resume" {
					text = "/resume " + ids[i%2]
				}
				result := service.HandleInput(ctx, CommandRequest{Text: text, RequestID: fmt.Sprintf("bench-%d", i), Entrance: EntranceTUI})
				if result.Code != "ok" || result.Switch == nil {
					b.Fatalf("benchmark did not switch: %+v", result)
				}
			}
			b.StopTimer()
		})
	}
}
