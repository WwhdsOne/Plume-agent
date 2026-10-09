package app

import (
	"math"
	"time"

	"plume-agent/internal/model"
)

// SessionStats 是只含数字的会话用量快照；未知调用不能算成已知零。
type SessionStats struct {
	StartedAt                                   time.Time
	Calls, KnownCalls                           int
	PromptTokens, CompletionTokens, TotalTokens int64
	CacheKnownCalls                             int
	CachePromptTokens, CachedPromptTokens       int64
	LastCallID                                  string
	LastUsage                                   model.Usage
	Overflow                                    bool
}

// ObserveUsage 同一个调用的更新替换旧快照，终态重复不会重复累计。
func (s *Session) ObserveUsage(callID string, usage model.Usage) {
	s.observeUsage(callID, usage, false)
}

func cloneUsage(usage model.Usage) model.Usage {
	if usage.CachedPromptTokens != nil {
		v := *usage.CachedPromptTokens
		usage.CachedPromptTokens = &v
	}
	return usage
}

func (s *Session) observeUsage(callID string, usage model.Usage, existingOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		s.usage = make(map[string]model.Usage)
	}
	if _, ok := s.usage[callID]; existingOnly && !ok {
		return
	}
	s.usage[callID] = cloneUsage(usage)
	s.lastCall = callID
}

func (s *Session) Statistics() SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := SessionStats{StartedAt: s.startedAt, Calls: len(s.usage), LastCallID: s.lastCall, LastUsage: s.usage[s.lastCall]}
	stats.LastUsage = cloneUsage(stats.LastUsage)
	add := func(dst *int64, n int64) {
		if n < 0 || n > math.MaxInt64-*dst {
			stats.Overflow = true
			*dst = math.MaxInt64
			return
		}
		*dst += n
	}
	for _, usage := range s.usage {
		if !usage.OK {
			continue
		}
		stats.KnownCalls++
		add(&stats.PromptTokens, usage.PromptTokens)
		add(&stats.CompletionTokens, usage.CompletionTokens)
		add(&stats.TotalTokens, usage.TotalTokens)
		// 缓存未知的调用不能作为零命中计入分母，命中量与输入量必须来自同一组调用。
		if cached := usage.CachedPromptTokens; cached != nil && *cached >= 0 && *cached <= usage.PromptTokens {
			stats.CacheKnownCalls++
			add(&stats.CachePromptTokens, usage.PromptTokens)
			add(&stats.CachedPromptTokens, *cached)
		}
	}
	return stats
}
