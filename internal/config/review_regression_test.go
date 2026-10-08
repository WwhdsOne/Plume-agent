package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewCandidateTypesHaveExactPath(t *testing.T) {
	for _, value := range []string{"null", "42", "false", "{}", "[]"} {
		t.Run(value, func(t *testing.T) {
			var cfg Config
			raw := `{"schema_version":1,"tui":{"status_messages":{"waiting":["hello",` + value + `]}}}`
			err := json.Unmarshal([]byte(raw), &cfg)
			if err == nil || !strings.Contains(err.Error(), "tui.status_messages.waiting[1]") {
				t.Fatalf("invalid candidate must report exact field path: %v", err)
			}
		})
	}
}

func TestReviewNullPhaseStillFallsBack(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"schema_version":1,"tui":{"status_messages":{"waiting":null}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.TUI.MessageOverrides()["waiting"]) != 0 {
		t.Fatal("null phase produced a custom candidate")
	}
}
