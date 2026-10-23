//go:build ignore

// Package main 仅演示 G4a.3.1 账本，不属于 plume CLI；显式文件方式运行：
// go run scripts/demo_g4a31_memory.go --output <trace.jsonl>。
// 临时 PLUME_HOME 与私密库结束即删除；日志只接固定操作名、状态与错误码。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"plume-agent/internal/store"
)

func main() {
	output := flag.String("output", "", "private JSONL evidence path; default stdout")
	flag.Parse()
	if err := runMemoryDemo(*output); err != nil {
		// 不回显底层错误，连演示失败也不能意外泄漏正文或凭据。
		fmt.Fprintln(os.Stderr, "memory demo failed")
		os.Exit(1)
	}
}

func runMemoryDemo(output string) error {
	var sink io.Writer = os.Stdout
	if output != "" {
		if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
			return err
		}
		// 拒绝覆盖已有证据，方便核对每次实际执行结果。
		file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		sink = file
	}
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(sink), zap.InfoLevel))
	defer func() { _ = logger.Sync() }()
	// 在当前仓库下创建临时目录，完全不解析用户真实配置或凭据路径。
	dir, err := os.MkdirTemp(".", ".g4a31-memory-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.Setenv("PLUME_HOME", dir); err != nil {
		return err
	}
	ctx := context.Background()
	path := filepath.Join(dir, "sessions", "history.db")
	s, err := store.OpenSQLite(path)
	if err != nil {
		return err
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	steps := 0
	step := func(name, code string, check func() bool) error {
		steps++
		logger.Info("memory_operation_started", zap.Int("step", steps), zap.String("operation", name))
		if !check() {
			logger.Info("memory_operation_failed", zap.Int("step", steps), zap.String("operation", name), zap.String("error_code", "demo_assertion_failed"))
			return errors.New("memory demo assertion failed")
		}
		status := "completed"
		if code != "" {
			status = "rejected"
		}
		logger.Info("memory_operation_ended", zap.Int("step", steps), zap.String("operation", name), zap.String("status", status), zap.String("error_code", code))
		return nil
	}
	scope := store.MemoryScope{ScopeKey: "local", Kind: store.MemoryWorkspace, Workspace: dir}
	write := store.MemoryWrite{Scope: scope, Key: "demo-preference", Content: "MEMORY-PRIVATE-SENTINEL 合成偏好", Source: store.MemorySourceUser}
	var first, updated store.MemoryRecord
	checks := []struct {
		name, code string
		check      func() bool
	}{
		{"create", "", func() bool {
			first, err = s.PutMemory(ctx, write)
			return err == nil && first.Revision == 1
		}},
		{"idempotent_replay", "", func() bool {
			replay, e := s.PutMemory(ctx, write)
			return e == nil && reflect.DeepEqual(first, replay)
		}},
		{"update", "", func() bool {
			write.Content, write.ExpectedRevision = "MEMORY-PRIVATE-SENTINEL 更新后的合成偏好", 1
			updated, err = s.PutMemory(ctx, write)
			return err == nil && updated.Revision == 2
		}},
		{"stale_update", "memory_conflict", func() bool {
			stale := write
			stale.Content = "MEMORY-PRIVATE-SENTINEL 陈旧值"
			_, e := s.PutMemory(ctx, stale)
			return errors.Is(e, store.ErrMemoryConflict)
		}},
		{"oversized_write", "memory_limit", func() bool {
			large := write
			large.Content = strings.Repeat("私", 3000)
			_, e := s.PutMemory(ctx, large)
			return errors.Is(e, store.ErrMemoryLimit)
		}},
		{"bounded_read", "memory_limit", func() bool {
			got, e := s.ListMemories(ctx, scope, store.MemoryLimits{MaxBytes: 1})
			return got == nil && errors.Is(e, store.ErrMemoryLimit)
		}},
		{"owner_isolation", "", func() bool {
			other := scope
			other.ScopeKey = "other"
			got, e := s.ListMemories(ctx, other, store.MemoryLimits{})
			return e == nil && len(got) == 0
		}},
		{"session_isolation", "", func() bool {
			for _, id := range []string{"a", "b"} {
				record := store.SessionRecord{ID: id, Workspace: dir, CreatedAt: time.Now()}
				_, _, e := s.ApplyCommand(ctx, store.CommandMutation{Receipt: store.Receipt{Workspace: dir,
					RequestID: "register-" + id, Fingerprint: "fixed", Command: "rename", Payload: []byte(`{"ok":true}`)}, CreateSession: &record})
				if e != nil {
					return false
				}
			}
			private := write
			private.Scope.Kind, private.Scope.SessionID, private.ExpectedRevision = store.MemorySession, "a", 0
			if _, e := s.PutMemory(ctx, private); e != nil {
				return false
			}
			private.Scope.SessionID = "b"
			got, e := s.ListMemories(ctx, private.Scope, store.MemoryLimits{})
			return e == nil && len(got) == 0
		}},
		{"reopen_and_verify", "", func() bool {
			if e := s.Close(); e != nil {
				return false
			}
			s, err = store.OpenSQLite(path)
			if err != nil {
				return false
			}
			got, e := s.ListMemories(ctx, scope, store.MemoryLimits{})
			return e == nil && len(got) == 1 && reflect.DeepEqual(got[0], updated)
		}},
	}
	for _, check := range checks {
		if err := step(check.name, check.code, check.check); err != nil {
			return err
		}
	}
	logger.Info("memory_demo_completed", zap.Int("steps", steps), zap.Int("model_calls", 0), zap.Int("tool_calls", 0))
	return nil
}
