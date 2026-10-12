package tui

import (
	"strings"
	"testing"

	"plume-agent/internal/app"
)

// TestNewSessionFailurePreservesTranscriptAndDraft 守住切换失败时原会话展示、统计和草稿不被清掉。
func TestNewSessionFailurePreservesTranscriptAndDraft(t *testing.T) {
	m := newTestModel(t, Hooks{ResetSession: func() error { return app.ErrHistoryUncertain }})
	m.appendLine(lineUser, "old conversation")
	m.input.SetValue("draft")
	m.history = []string{"previous input"}
	started := m.status.sessionStarted
	m.sessionReset()
	if len(m.lines) != 1 || m.lines[0].text != "old conversation" || m.input.Value() != "draft" || len(m.history) != 1 || !m.status.sessionStarted.Equal(started) {
		t.Fatal("failed session change erased original state")
	}
	if !strings.Contains(m.notice, "commit state unknown") {
		t.Fatalf("missing failure notice: %s", m.notice)
	}
}
