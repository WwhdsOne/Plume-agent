package app

import (
	"context"
	"math"
	"testing"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
)

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
