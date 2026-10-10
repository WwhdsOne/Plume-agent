package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestReviewCandidateTypesHaveExactPath 守住错误可定位：候选文案元素类型非法时，错误必须指向 tui.status_messages.waiting[1] 这样的精确路径。
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

// TestReviewNullPhaseStillFallsBack 守住 null 回退：阶段显式 null 视为未自定义，MessageOverrides 不产出该阶段候选。
func TestReviewNullPhaseStillFallsBack(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"schema_version":1,"tui":{"status_messages":{"waiting":null}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.TUI.MessageOverrides()["waiting"]) != 0 {
		t.Fatal("null phase produced a custom candidate")
	}
}
