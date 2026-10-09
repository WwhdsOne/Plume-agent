package tui

import (
	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/model"
	"strings"
	"testing"
)

func TestG3ToolStateUsesOneSafeLeftAlignedLine(t *testing.T) {
	m := New("fake", Hooks{})
	m.startRun("r")
	m, _ = m.Update(AppEvent{Event: app.Event{Kind: app.EventToolUpdate, RunID: "r", Tool: agent.ToolEvent{CallID: "c", Name: "calculate", Status: "running"}}})
	m, _ = m.Update(AppEvent{Event: app.Event{Kind: app.EventToolUpdate, RunID: "r", Tool: agent.ToolEvent{CallID: "c", Name: "calculate", Status: "completed", Summary: "42\x1b]52;c;evil\a"}}})
	if len(m.lines) != 1 {
		t.Fatalf("tool updates create %d lines", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "calculate") || !strings.Contains(m.lines[0].text, "42") || strings.Contains(m.lines[0].text, "\x1b") {
		t.Fatalf("unsafe or missing summary: %q", m.lines[0].text)
	}
	before := m.lines[0].text
	m, _ = m.Update(AppEvent{Event: app.Event{Kind: app.EventToolUpdate, RunID: "old", Tool: agent.ToolEvent{CallID: "c", Name: "unknown", Status: "failed"}}})
	if m.lines[0].text != before {
		t.Fatal("old tool update changed current run")
	}
}

func TestG3CacheSnapshotSurvivesNextModelCall(t *testing.T) {
	m := New("fake", Hooks{})
	m.startRun("r")
	cached := int64(4)
	session := app.NewSession()
	session.ObserveUsage("c1", model.Usage{OK: true, PromptTokens: 10, CachedPromptTokens: &cached})
	old := session.Statistics()
	m.updateStatus(app.Event{Kind: app.EventUsageUpdate, Stats: old, Usage: old.LastUsage})
	before, _ := m.cacheValue(10)
	next := old
	next.Calls = 2
	next.LastUsage = model.Usage{}
	m.updateStatus(app.Event{Kind: app.EventRunPhase, Phase: app.PhaseWaiting, Stats: next})
	after, _ := m.cacheValue(10)
	if before != after {
		t.Fatalf("cache changed before next call usage: %q -> %q", before, after)
	}
}

func TestG3ToolThenFinalAnswerKeepsChronologicalLines(t *testing.T) {
	m := New("fake", Hooks{})
	m.startRun("r")
	m.applyEvent(app.Event{Kind: app.EventReasoningDelta, RunID: "r", ReasoningDelta: "first thought"})
	m.applyEvent(app.Event{Kind: app.EventToolUpdate, RunID: "r", Tool: agent.ToolEvent{CallID: "c", Name: "calculate", Status: "completed", Summary: "42"}})
	m.applyEvent(app.Event{Kind: app.EventRunPhase, RunID: "r", Phase: app.PhaseWaiting, ModelCall: 2})
	m.applyEvent(app.Event{Kind: app.EventTextDelta, RunID: "r", TextDelta: "final answer"})
	if len(m.lines) != 3 || m.lines[0].reasoning != "first thought" || m.lines[1].kind != lineTool || m.lines[2].text != "final answer" {
		t.Fatalf("tool/answer out of order: %+v", m.lines)
	}
}
