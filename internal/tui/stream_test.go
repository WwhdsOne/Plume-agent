package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"plume-agent/internal/app"
)

func streamModel(t *testing.T) (*Model, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	m := NewWithOptions("test/model", Hooks{Submit: func(string) (string, error) { return "r1", nil }}, Options{Clock: func() time.Time { return now }, Choose: func(int) int { return 0 }})
	m.width, m.height = 40, 20
	m.resize()
	m.input.SetValue("hello")
	m.Update(specialKey(tea.KeyEnter))
	return &m, &now
}

func send(m *Model, e app.Event) { m.Update(AppEvent{Event: e}) }

func TestStreamPhasesAndTerminalAlignment(t *testing.T) {
	m, now := streamModel(t)
	if m.phase != app.PhasePreparing {
		t.Fatalf("submit phase = %s", m.phase)
	}
	start := m.startedAt
	*now = now.Add(time.Second)
	send(m, app.Event{RunID: "r1", Kind: app.EventRunStarted})
	if m.startedAt != start {
		t.Fatal("started event reset accepted time")
	}
	send(m, app.Event{RunID: "stale", Kind: app.EventTextDelta, TextDelta: "stale"})
	send(m, app.Event{RunID: "r1", Kind: app.EventReasoningDelta, ReasoningDelta: "one\ntwo\nthree\nfour"})
	if !strings.Contains(ansi.Strip(m.View()), "Thought") || strings.Contains(ansi.Strip(m.View()), "one\n") {
		t.Fatal("thought preview must keep latest three visible lines")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "answer"})
	if m.phase != app.PhaseResponding {
		t.Fatal("first answer must enter responding")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: "answer", Reasoning: "one\ntwo\nthree\nfour"})
	if m.state != stateIdle || len(m.lines) != 2 || m.lines[1].text != "answer" {
		t.Fatalf("terminal duplication: %+v", m.lines)
	}
	if strings.Contains(m.View(), "stale") {
		t.Fatal("stale event rendered")
	}
}

func TestReasoningToggleManualExpansionSurvivesAnswer(t *testing.T) {
	m, _ := streamModel(t)
	send(m, app.Event{RunID: "r1", Kind: app.EventReasoningDelta, ReasoningDelta: "one\ntwo\nthree\nfour"})
	m.Update(ctrlKey('o'))
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "reply"})
	if !strings.Contains(ansi.Strip(m.View()), "one") {
		t.Fatal("manual expansion lost on first answer")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: "reply"})
	m.Update(ctrlKey('o'))
	if strings.Contains(ansi.Strip(m.View()), "one") {
		t.Fatal("idle toggle should fold most recent thought")
	}
	m.Update(ctrlKey('o'))
	if !strings.Contains(ansi.Strip(m.View()), "one") {
		t.Fatal("idle toggle should expand most recent thought")
	}
}

func TestNoReasoningHasNoThoughtTitle(t *testing.T) {
	m, _ := streamModel(t)
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "reply"})
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: "reply"})
	if strings.Contains(m.View(), "Thought") {
		t.Fatal("empty reasoning rendered a thought title")
	}
}

func TestStreamCancellationKeepsPartialRawAndSanitizesCumulative(t *testing.T) {
	m, now := streamModel(t)
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "hello\x1b["})
	*now = now.Add(60 * time.Millisecond)
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "2Jworld"})
	send(m, app.Event{RunID: "r1", Kind: app.EventRunFailed, Reply: "hello\x1b[2Jworld", Err: context.Canceled})
	if m.lines[1].text != "hello\x1b[2Jworld" {
		t.Fatal("raw answer was mutated")
	}
	v := ansi.Strip(m.View())
	if strings.Contains(v, "2J") || strings.Count(v, "helloworld") != 1 || !strings.Contains(v, "cancelled") {
		t.Fatalf("partial failure view = %q", v)
	}
}

func TestRoleAlignmentAndGraphemeWrapping(t *testing.T) {
	v := ansi.Strip(renderLines([]chatLine{{kind: lineUser, text: "第一行\n第二行"}, {kind: lineAssistant, text: "first\nsecond"}}, 40))
	for _, want := range []string{"  > 第一行", "    第二行", "  ● first", "    second"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in %q", want, v)
		}
	}
	text := "中🙂e\u0301👩‍💻文"
	parts := wrapText(text, 4)
	if strings.Join(parts, "") != text {
		t.Fatal("wrapping lost graphemes")
	}
	for _, p := range parts {
		if !utf8.ValidString(p) || ansi.StringWidth(p) > 4 {
			t.Fatalf("bad width/utf8 %q", p)
		}
	}
}

func TestMarkdownCompactReadableFallbacks(t *testing.T) {
	for _, raw := range []string{"**unfinished", "<div>HTML</div>", "$x^2$", "| a | very-long-cell-preserved |\n|---|---|\n| x | contents |"} {
		v := ansi.Strip(renderMarkdown(raw, 18))
		for _, token := range strings.Fields(raw) {
			if !strings.Contains(strings.ReplaceAll(v, "\n", ""), token) {
				t.Fatalf("fallback lost %q: %q", token, v)
			}
		}
	}
	openCode := ansi.Strip(renderMarkdown("```go\nfmt.Println(1)", 18))
	if !strings.Contains(openCode, "go") || !strings.Contains(openCode, "fmt.Println(1)") {
		t.Fatalf("未闭合代码块仍需保留语言和代码正文：%q", openCode)
	}
	v := ansi.Strip(renderMarkdown("# Heading\n\n**bold** and `code`\n\n[label](https://example.org)\n\n![alt](https://example.org/a.png)\n\n```go\nfmt.Println(1)\n```", 60))
	for _, token := range []string{"Heading", "bold", "code", "label", "https://example.org", "alt", "https://example.org/a.png", "go", "fmt.Println(1)"} {
		if !strings.Contains(v, token) {
			t.Fatalf("markdown lost %q: %q", token, v)
		}
	}
	if strings.Contains(v, "\x1b]8") || strings.HasPrefix(v, " ") {
		t.Fatalf("markdown spacing/OSC8 = %q", v)
	}
}

func TestRefreshThrottleAndCompletedCache(t *testing.T) {
	m, now := streamModel(t)
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "first"})
	if !strings.Contains(m.View(), "first") {
		t.Fatal("first answer not immediate")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: " second"})
	if strings.Contains(m.View(), "second") {
		t.Fatal("delta refresh ignored 50ms throttle")
	}
	*now = now.Add(50 * time.Millisecond)
	m.Update(streamTick(*now))
	if !strings.Contains(m.View(), "second") {
		t.Fatal("tick did not flush answer")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: "first second"})
	count := m.markdownRenders
	m.Update(streamTick(now.Add(time.Second)))
	m.View()
	if m.markdownRenders != count {
		t.Fatal("completed response rerendered on tick")
	}
}

func TestScrollFreezeAndResizePreservePosition(t *testing.T) {
	m, now := streamModel(t)
	for i := 0; i < 30; i++ {
		m.appendLine(lineSystem, "older line")
	}
	m.Update(specialKey(tea.KeyPgUp))
	offset := m.viewport.YOffset()
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: strings.Repeat("reply\n", 20)})
	*now = now.Add(60 * time.Millisecond)
	m.Update(streamTick(*now))
	if m.viewport.YOffset() != offset {
		t.Fatal("stream jumped scrolled viewport")
	}
	m.Update(tea.WindowSizeMsg{Width: 45, Height: 20})
	if m.viewport.YOffset() != offset {
		t.Fatal("resize jumped scrolled viewport")
	}
	for !m.viewport.AtBottom() {
		m.Update(specialKey(tea.KeyPgDown))
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: strings.Repeat("reply\n", 22)})
	if !m.viewport.AtBottom() {
		t.Fatal("bottom follow did not resume")
	}
}

func TestStatusCandidatesChosenOnceAndClockNotReset(t *testing.T) {
	now := time.Now()
	calls := 0
	m := NewWithOptions("model", Hooks{}, Options{Clock: func() time.Time { return now }, Choose: func(n int) int { calls++; return n - 1 }, StatusMessages: map[string][]string{"preparing": {"p1", "p2"}, "thinking": {"t1", "t2"}}})
	m.width, m.height = 40, 20
	m.resize()
	m.startRun("r1")
	if !strings.Contains(m.statusBar(), "p2") {
		t.Fatal("custom preparation label ignored")
	}
	now = now.Add(time.Second)
	send(&m, app.Event{RunID: "r1", Kind: app.EventReasoningDelta, ReasoningDelta: "reason"})
	count := calls
	m.Update(streamTick(now))
	m.resize()
	if calls != count || m.elapsed() != time.Second {
		t.Fatal("stage candidate reselected or time reset")
	}
	if strings.Contains(ansi.Strip(m.statusBar()), "t2") {
		t.Fatal("footer duplicated visible thought title")
	}
	resolved := ResolvedStatusMessages(map[string][]string{"waiting": {}, "thinking": {" custom "}})
	if len(resolved["waiting"]) == 0 || resolved["thinking"][0] != "custom" {
		t.Fatalf("resolved=%v", resolved)
	}
}

func TestFirstAnswerCallbackOnlyOnVisibleTeaViewOnce(t *testing.T) {
	m, now := streamModel(t)
	calls := 0
	var latency time.Duration
	m.hooks.FirstAnswer = func(runID string, d time.Duration) {
		if runID != "r1" {
			t.Error(runID)
		}
		calls++
		latency = d
	}
	*now = now.Add(200 * time.Millisecond)
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "visible"})
	if calls != 0 {
		t.Fatal("first answer counted before View frame")
	}
	tm := TeaModel{M: *m}
	tm.View()
	tm.View()
	if calls != 1 || latency != 200*time.Millisecond {
		t.Fatalf("calls=%d latency=%s", calls, latency)
	}
}

func TestThoughtHeaderCarriesElapsedAndStopsSpinner(t *testing.T) {
	m, now := streamModel(t)
	*now = now.Add(time.Second)
	send(m, app.Event{RunID: "r1", Kind: app.EventReasoningDelta, ReasoningDelta: "reason"})
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "⠋ Thought… 1s") {
		t.Fatalf("thought header = %q", v)
	}
	*now = now.Add(100 * time.Millisecond)
	m.Update(streamTick(*now))
	if !strings.Contains(ansi.Strip(m.View()), "⠙ Thought") {
		t.Fatal("spinner did not advance")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "reply"})
	v = ansi.Strip(m.View())
	if !strings.Contains(v, "▸ Thought") || strings.Contains(v, "⠙ Thought") {
		t.Fatalf("fold did not stop spinner: %q", v)
	}
}

func TestSanitizeIncompleteAndExtendedSequences(t *testing.T) {
	for _, raw := range []string{"ok\x1b[38:2:1:2:3m", "ok\x1b[123;", "ok\x1bPsecret\x1b\\", "ok\u009b2J"} {
		if got := sanitize(raw); got != "ok" {
			t.Fatalf("sanitize(%q) = %q", raw, got)
		}
	}
}

func TestFooterPreservesRunIDAndNarrowElapsed(t *testing.T) {
	m, now := streamModel(t)
	*now = now.Add(1234 * time.Millisecond)
	if !strings.Contains(ansi.Strip(m.statusBar()), "r1") {
		t.Fatal("footer lost run ID")
	}
	m.width = 18
	v := ansi.Strip(m.statusBar())
	if !strings.Contains(v, "1.234s") || ansi.StringWidth(v) > 18 {
		t.Fatalf("narrow footer = %q", v)
	}
}

func TestEmptyRunIDAndLateReasoningDoNotChangeRun(t *testing.T) {
	m, _ := streamModel(t)
	send(m, app.Event{Kind: app.EventRunFailed, Err: context.Canceled})
	if m.state != stateRunning {
		t.Fatal("untagged event ended active run")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "answer"})
	send(m, app.Event{RunID: "r1", Kind: app.EventReasoningDelta, ReasoningDelta: "late reason"})
	if m.phase != app.PhaseResponding || !strings.Contains(m.lines[1].reasoning, "late reason") {
		t.Fatal("late reasoning lost or changed phase")
	}
}

func TestCompletedHistoricalCacheUnaffectedByNextRun(t *testing.T) {
	m, _ := streamModel(t)
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: "**old answer**"})
	old := m.lines[1].cache
	count := m.markdownRenders
	m.startRun("r2")
	send(m, app.Event{RunID: "r2", Kind: app.EventTextDelta, TextDelta: "new"})
	send(m, app.Event{RunID: "r2", Kind: app.EventReasoningDelta, ReasoningDelta: "late"})
	if m.lines[1].cache != old || m.markdownRenders != count+1 {
		t.Fatal("history markdown was parsed again")
	}
}

func TestThoughtPreviewUsesVisibleWrappedRows(t *testing.T) {
	raw := "🙂🙂🙂🙂🙂🙂🙂🙂"
	got := thoughtPreview(raw, 4)
	if len(got) != 3 || strings.Join(got, "") != "🙂🙂🙂🙂🙂🙂" {
		t.Fatalf("preview=%q", got)
	}
}

func TestMarkdownTableAlwaysLeftAlignedAndKeepsWideCells(t *testing.T) {
	raw := "| name | values |\n| :---: | ---: |\n| x | y |\n| longname | z |"
	v := ansi.Strip(renderMarkdown(raw, 50))
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "x") && strings.Contains(line, "y") && strings.HasPrefix(line, " ") {
			t.Fatalf("center/right table cell = %q", line)
		}
	}
	raw = "| a | b |\n| :---: | ---: |\n| abcdefghijklmno | x |\n| x | abcdefghijklmno |"
	v = ansi.Strip(renderMarkdown(raw, 24))
	if strings.Count(strings.ReplaceAll(v, "\n", ""), "abcdefghijklmno") != 2 {
		t.Fatalf("wide table lost cells: %q", v)
	}
}

func TestRunningToggleDoesNotChangePreviousThought(t *testing.T) {
	m, _ := streamModel(t)
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: "done", Reasoning: "prior"})
	m.startRun("r2")
	m.Update(ctrlKey('o'))
	if m.lines[1].expanded {
		t.Fatal("new run without reasoning toggled previous reply")
	}
}

func TestAcceptedAtCorrectsClockWithoutResettingPhaseOrChoice(t *testing.T) {
	m, now := streamModel(t)
	send(m, app.Event{RunID: "r1", Kind: app.EventReasoningDelta, ReasoningDelta: "thought"})
	labels := m.phaseLabels[app.PhasePreparing]
	accepted := now.Add(-100 * time.Millisecond)
	send(m, app.Event{RunID: "r1", Kind: app.EventRunStarted, AcceptedAt: accepted})
	if m.startedAt != accepted || m.phase != app.PhaseThinking || m.lines[1].reasoning != "thought" || m.phaseLabels[app.PhasePreparing] != labels {
		t.Fatal("accepted time correction reset run state")
	}
}

func BenchmarkMarkdown(b *testing.B) {
	for _, size := range []int{1024, 10240, 102400} {
		b.Run(fmt.Sprintf("%dKiB", size/1024), func(b *testing.B) {
			unit := "**bold** `code` text\n\n"
			raw := strings.Repeat(unit, size/len(unit)) + strings.Repeat("x", size%len(unit))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				renderMarkdown(raw, 76)
			}
		})
	}
}
func BenchmarkThoughtVisibleWrap(b *testing.B) {
	for _, size := range []int{1024, 10240, 102400} {
		b.Run(fmt.Sprintf("%dKiB", size/1024), func(b *testing.B) {
			unit := "思考🙂e\u0301文本\n"
			raw := strings.Repeat(unit, size/len(unit)) + strings.Repeat("x", size%len(unit))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				thoughtPreview(raw, 76)
			}
		})
	}
}
