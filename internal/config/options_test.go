package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDisplayAndReasoningSettingsSurviveRoundTrip 守住往返不丢字段：TUI 自定义文案与显式
// reasoning_effort:"none"（关闭推理）在编码后必须原样保留。
func TestDisplayAndReasoningSettingsSurviveRoundTrip(t *testing.T) {
	raw := `{"schema_version":1,"models":[{"id":"main","provider":"deepseek","protocol":"deepseek","model":"deepseek-flash","base_url":"https://api.deepseek.com","reasoning_effort":"none"}],"tui":{"status_messages":{"waiting":["brewing"],"thinking":["Thought"]}}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved["tui"]; !ok {
		t.Fatal("TUI settings disappeared on save")
	}
	if !strings.Contains(string(encoded), `"reasoning_effort":"none"`) {
		t.Fatal("disabled reasoning disappeared on save")
	}
}

// TestInvalidOptionalSettingsAreRejected 守住展示文案边界：未知阶段名、含换行/控制序列的候选文案及非法 reasoning_effort 一律拒绝。
func TestInvalidOptionalSettingsAreRejected(t *testing.T) {
	for _, field := range []string{
		`"tui":{"status_messages":{"waitng":["hello"]}}`,
		`"tui":{"status_messages":{"waiting":["line\nbreak"]}}`,
		`"tui":{"status_messages":{"waiting":["\u001b[31m"]}}`,
	} {
		t.Run(field, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(`{"schema_version":1,`+field+`}`), &cfg); err != nil {
				return
			}
			if err := cfg.Validate(nil, nil); err == nil {
				t.Fatal("invalid display settings accepted")
			}
		})
	}
	for _, effort := range []string{`null`, `""`, `false`, `"ultra"`} {
		t.Run("effort_"+effort, func(t *testing.T) {
			var cfg Config
			raw := `{"schema_version":1,"models":[{"id":"main","provider":"deepseek","protocol":"deepseek","model":"deepseek-flash","base_url":"https://api.deepseek.com","reasoning_effort":` + effort + `}]}`
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				return
			}
			if err := cfg.Validate(nil, nil); err == nil {
				t.Fatal("invalid reasoning effort accepted")
			}
		})
	}
}
