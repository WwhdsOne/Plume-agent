package app

import (
	"context"
	"math"
	"testing"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
)

// TestSessionUsageSnapshotReplacesInsteadOfDoubleCounting 守住用量去重累计：同一调用的快照
// 按最后一份替换、不重复计数，未知用量只计入调用数；Reset 后归零。
func TestSessionUsageSnapshotReplacesInsteadOfDoubleCounting(t *testing.T) {
	s := NewSession()
	s.ObserveUsage("call-1", model.Usage{})
	s.ObserveUsage("call-1", model.Usage{OK: true, PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
	s.ObserveUsage("call-1", model.Usage{OK: true, PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14})
	s.ObserveUsage("call-1", model.Usage{OK: true, PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14})
	s.ObserveUsage("call-2", model.Usage{})
	got := s.Statistics()
	if got.Calls != 2 || got.KnownCalls != 1 || got.TotalTokens != 14 {
		t.Fatalf("duplicate usage or unknown counted incorrectly: %+v", got)
	}
	s.Reset()
	if got := s.Statistics(); got.Calls != 0 || got.TotalTokens != 0 {
		t.Fatalf("session reset leaked usage: %+v", got)
	}
}

// TestCancelledBeforeModelCallDoesNotCount 守住统计口径：模型调用前就取消的 run 不计入调用统计。
func TestCancelledBeforeModelCallDoesNotCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewService(agent.New(model.NewLoopFake(model.FakeScript{Response: &model.ChatResponse{}}), "fake"))
	defer s.Close()
	if _, err := s.Submit(ctx, "input"); err != nil {
		t.Fatal(err)
	}
	s.waitIdle()
	if got := s.Session().Statistics(); got.Calls != 0 {
		t.Fatalf("preparation cancellation counted model call: %+v", got)
	}
}

// TestUsageSnapshotsDoNotAliasCallerPointers 守住快照隔离：指针字段被克隆，会话与调用方、
// Statistics 返回值之间互不共享可变状态。
func TestUsageSnapshotsDoNotAliasCallerPointers(t *testing.T) {
	s := NewSession()
	cached := int64(2)
	s.ObserveUsage("call", model.Usage{OK: true, PromptTokens: 4, CachedPromptTokens: &cached})
	cached = 4
	first := s.Statistics()
	if *first.LastUsage.CachedPromptTokens != 2 {
		t.Fatal("incoming usage aliased caller")
	}
	*first.LastUsage.CachedPromptTokens = 3
	if *s.Statistics().LastUsage.CachedPromptTokens != 2 {
		t.Fatal("snapshot exposed session cache pointer")
	}
}

// TestSessionCacheSnapshotsReplaceAndExcludeUnknownInputs 守住缓存口径：缓存 token 按快照
// 替换累计，缓存未知或非法（超过输入 token）的调用不计入缓存分子与分母；Reset 清零。
func TestSessionCacheSnapshotsReplaceAndExcludeUnknownInputs(t *testing.T) {
	s := NewSession()
	old, updated, zero, invalid := int64(2), int64(8), int64(0), int64(11)
	s.ObserveUsage("known", model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &old})
	s.ObserveUsage("known", model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &updated})
	s.ObserveUsage("known", model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &updated})
	s.ObserveUsage("zero", model.Usage{OK: true, PromptTokens: 30, CachedPromptTokens: &zero})
	s.ObserveUsage("unknown-cache", model.Usage{OK: true, PromptTokens: 20})
	s.ObserveUsage("unknown-usage", model.Usage{CachedPromptTokens: &updated})
	s.ObserveUsage("invalid", model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &invalid})
	got := s.Statistics()
	if got.Calls != 5 || got.CacheKnownCalls != 2 || got.CachedPromptTokens != 8 || got.CachePromptTokens != 40 || got.PromptTokens != 70 {
		t.Fatalf("cache totals mismatched or duplicated: %+v", got)
	}
	s.Reset()
	if got := s.Statistics(); got.CacheKnownCalls != 0 || got.CachedPromptTokens != 0 || got.CachePromptTokens != 0 {
		t.Fatalf("cache totals survived reset: %+v", got)
	}
}

// TestSessionCacheOverflowDoesNotWrap 守住溢出行为：累计溢出时钳到 MaxInt64 并置 Overflow
// 标记，不回绕。
func TestSessionCacheOverflowDoesNotWrap(t *testing.T) {
	s := NewSession()
	large, one := int64(math.MaxInt64), int64(1)
	s.ObserveUsage("large", model.Usage{OK: true, PromptTokens: large, CachedPromptTokens: &large})
	s.ObserveUsage("one", model.Usage{OK: true, PromptTokens: one, CachedPromptTokens: &one})
	got := s.Statistics()
	if !got.Overflow || got.CachedPromptTokens != math.MaxInt64 || got.CachePromptTokens != math.MaxInt64 {
		t.Fatalf("cache totals overflowed: %+v", got)
	}
}
