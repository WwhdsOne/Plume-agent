package tui

import (
	"strings"
	"testing"
)

// TestTranscriptRestoresSanitizedThought 守住持久 Thought 的恢复与终端控制序列净化。
func TestTranscriptRestoresSanitizedThought(t *testing.T) {
	m := NewWithOptions("fake/model", Hooks{}, Options{InitialTranscript: []TranscriptLine{{Kind: TranscriptAssistant, Text: "answer", Reasoning: "stored thought\x1b]11;?", Terminal: "failed"}}})
	if len(m.lines) != 1 || !strings.Contains(m.lines[0].reasoning, "stored thought") {
		t.Fatal("resumed Thought was lost")
	}
	if strings.ContainsRune(m.lines[0].reasoning, '\x1b') {
		t.Fatal("terminal query survived Thought sanitization")
	}
}
