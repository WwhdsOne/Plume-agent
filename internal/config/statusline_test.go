package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func statusLineJSON(t *testing.T, cfg *Config) map[string]any {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	tui, ok := root["tui"].(map[string]any)
	if !ok {
		t.Fatal("missing tui defaults")
	}
	line, ok := tui["status_line"].(map[string]any)
	if !ok {
		t.Fatal("missing status_line defaults")
	}
	return line
}

func TestSaveWritesCompleteStatusLineDefaults(t *testing.T) {
	withTempDir(t)
	cfg := &Config{SchemaVersion: SchemaVersion, Models: []ModelConfig{{ID: "main", Provider: "custom-openai", Protocol: "openai-compatible", BaseURL: "https://example.com", Model: "custom"}}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	line := statusLineJSON(t, cfg)
	for key, want := range map[string]any{
		"enabled": true, "max_rows": float64(2), "separator": " │ ", "unknown": "show",
		"clock_refresh_ms": float64(1000), "environment_refresh_ms": float64(5000), "git_timeout_ms": float64(500),
		"token_format": "compact", "time_format": "compact", "context_format": "usage", "cache_format": "bar", "cache_scope": "session",
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	bar := line["context_bar"].(map[string]any)
	wantBar := map[string]any{"width": float64(10), "show_percent": true, "style": "unicode", "warning_percent": float64(80), "critical_percent": float64(95)}
	if !reflect.DeepEqual(bar, wantBar) {
		t.Errorf("context_bar = %v, want %v", bar, wantBar)
	}
	if got := line["cache_bar"]; !reflect.DeepEqual(got, map[string]any{"width": float64(10), "style": "unicode"}) {
		t.Errorf("cache_bar = %v, want width 10 unicode", got)
	}
	items := line["items"].([]any)
	wantIDs := []string{"provider", "model", "reasoning", "context", "cache", "git", "uv_env", "session_elapsed", "phase", "run_elapsed", "last_usage", "session_usage", "run_id", "cwd"}
	if len(items) != len(wantIDs) {
		t.Fatalf("items = %d, want 14", len(items))
	}
	for i, id := range wantIDs {
		item := items[i].(map[string]any)
		if item["id"] != id || len(item) != 5 || item["enabled"] != (i < 10 && id != "context") {
			t.Errorf("item[%d] = %v", i, item)
		}
		if id == "cache" && item["label"] != "cache" {
			t.Errorf("cache label = %v, want cache", item["label"])
		}
		if id == "provider" && item["label"] != "Provider" {
			t.Errorf("provider label = %v, want Provider", item["label"])
		}
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("PLUME_HOME"), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"context_window_tokens": null`) {
		t.Fatal("missing explicit unknown model capacity")
	}
	var saved Config
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if got := statusLineJSON(t, &saved); got["cache_format"] != "bar" || got["cache_scope"] != "session" || got["context_format"] != "usage" {
		t.Fatalf("cache defaults not persisted: %v", got)
	}
}

func TestCacheScopeLoadPreservesSelectionAndFillsMissing(t *testing.T) {
	for _, scope := range []string{"", "session", "last_call"} {
		t.Run(scope, func(t *testing.T) {
			dir := withTempDir(t)
			line := map[string]any{"cache_format": "both", "context_format": "bar", "cache_bar": map[string]any{"width": 7, "style": "ascii"}, "items": []any{map[string]any{"id": "cache", "label": "缓存"}}}
			if scope != "" {
				line["cache_scope"] = scope
			}
			raw, err := json.Marshal(map[string]any{"schema_version": SchemaVersion, "tui": map[string]any{"status_line": line}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			want := scope
			if want == "" {
				want = "session"
			}
			got := statusLineJSON(t, cfg)
			if got["cache_scope"] != want || got["cache_format"] != "both" || got["context_format"] != "bar" || got["items"].([]any)[0].(map[string]any)["label"] != "缓存" {
				t.Fatalf("cache settings not preserved: %v", got)
			}
			if got["cache_bar"].(map[string]any)["width"] != float64(7) || got["cache_bar"].(map[string]any)["style"] != "ascii" {
				t.Fatal("custom cache bar overwritten")
			}
			migrated, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(migrated), `"cache_scope": "`+want+`"`) {
				t.Fatal("cache scope not written to config")
			}
			if err := Save(cfg); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != string(second) {
				t.Fatal("complete cache settings rewritten")
			}
			cfg.TUI.StatusLine.CacheScope = "last_call"
			if want == "last_call" {
				cfg.TUI.StatusLine.CacheScope = "session"
			}
			if err := Save(cfg); err != nil {
				t.Fatal(err)
			}
			reloaded, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.TUI.StatusLine.CacheScope != cfg.TUI.StatusLine.CacheScope {
				t.Fatal("changed cache scope not persisted")
			}
		})
	}
}

func TestLoadStatusLinePartialDefaultsPreservesSelectionAndUnknownFields(t *testing.T) {
	dir := withTempDir(t)
	path := filepath.Join(dir, "config.json")
	initial := `{"schema_version":1,"custom_root":true,"models":[{"id":"main","provider":"custom-openai","protocol":"openai-compatible","base_url":"https://example.com","model":"custom","custom_model":42}],"tui":{"custom_tui":true,"status_line":{"enabled":false,"separator":"","custom_line":true,"context_bar":{"show_percent":false,"custom_bar":true},"items":[{"id":"model","label":"","priority":0,"enabled":false,"custom_item":true},{"id":"git"}]}}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	line := statusLineJSON(t, cfg)
	if line["enabled"] != false || line["separator"] != "" || line["max_rows"] != float64(2) || line["context_bar"].(map[string]any)["show_percent"] != false {
		t.Fatalf("partial defaults overwrite explicit zero: %v", line)
	}
	items := line["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("selected fields were added back: %v", items)
	}
	if item := items[0].(map[string]any); item["priority"] != float64(0) || item["enabled"] != false || item["label"] != "" || item["row"] != float64(1) {
		t.Fatalf("explicit item values overwritten: %v", item)
	}
	if item := items[1].(map[string]any); item["row"] != float64(2) || item["priority"] != float64(50) || item["label"] != "git" || item["enabled"] != true {
		t.Fatalf("ID defaults missing: %v", item)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"custom_root", "custom_model", "custom_tui", "custom_line", "custom_bar", "custom_item", "context_window_tokens"} {
		if !strings.Contains(string(first), `"`+key+`"`) {
			t.Errorf("lost %s", key)
		}
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("complete status line rewritten on second Load")
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	for _, key := range []string{"custom_root", "custom_model", "custom_tui", "custom_line", "custom_bar", "custom_item"} {
		if !strings.Contains(string(saved), `"`+key+`"`) {
			t.Errorf("Save lost %s", key)
		}
	}
}

func TestStatusLineEmptyItemsRemainEmpty(t *testing.T) {
	withTempDir(t)
	var cfg Config
	if err := json.Unmarshal([]byte(`{"schema_version":1,"tui":{"status_line":{"items":[]}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := Save(&cfg); err != nil {
		t.Fatal(err)
	}
	if got := statusLineJSON(t, &cfg)["items"].([]any); len(got) != 0 {
		t.Fatalf("empty items restored defaults: %v", got)
	}
}

func TestInvalidStatusLineSettingsRejectedWithSafePaths(t *testing.T) {
	cases := []struct{ fragment, path string }{
		{`null`, "tui.status_line"},
		{`[]`, "tui.status_line"},
		{`{"enabled":null}`, "tui.status_line.enabled"},
		{`{"max_rows":0}`, "tui.status_line.max_rows"},
		{`{"max_rows":3}`, "tui.status_line.max_rows"},
		{`{"max_rows":"SENTINEL"}`, "tui.status_line.max_rows"},
		{`{"unknown":"SENTINEL"}`, "tui.status_line.unknown"},
		{`{"clock_refresh_ms":249}`, "tui.status_line.clock_refresh_ms"},
		{`{"clock_refresh_ms":5001}`, "tui.status_line.clock_refresh_ms"},
		{`{"environment_refresh_ms":999}`, "tui.status_line.environment_refresh_ms"},
		{`{"environment_refresh_ms":60001}`, "tui.status_line.environment_refresh_ms"},
		{`{"git_timeout_ms":99}`, "tui.status_line.git_timeout_ms"},
		{`{"git_timeout_ms":2001}`, "tui.status_line.git_timeout_ms"},
		{`{"token_format":"SENTINEL"}`, "tui.status_line.token_format"},
		{`{"time_format":"SENTINEL"}`, "tui.status_line.time_format"},
		{`{"cache_format":"SENTINEL"}`, "tui.status_line.cache_format"},
		{`{"cache_scope":"SENTINEL"}`, "tui.status_line.cache_scope"},
		{`{"cache_scope":null}`, "tui.status_line.cache_scope"},
		{`{"context_format":"SENTINEL"}`, "tui.status_line.context_format"},
		{`{"context_format":null}`, "tui.status_line.context_format"},
		{`{"cache_bar":null}`, "tui.status_line.cache_bar"},
		{`{"cache_bar":{"width":4}}`, "tui.status_line.cache_bar.width"},
		{`{"cache_bar":{"width":31}}`, "tui.status_line.cache_bar.width"},
		{`{"cache_bar":{"style":"SENTINEL"}}`, "tui.status_line.cache_bar.style"},
		{`{"separator":"123456789"}`, "tui.status_line.separator"},
		{`{"separator":"SENTINEL\n"}`, "tui.status_line.separator"},
		{`{"separator":"界界界界界"}`, "tui.status_line.separator"},
		{`{"context_bar":null}`, "tui.status_line.context_bar"},
		{`{"context_bar":{"width":4}}`, "tui.status_line.context_bar.width"},
		{`{"context_bar":{"width":31}}`, "tui.status_line.context_bar.width"},
		{`{"context_bar":{"show_percent":null}}`, "tui.status_line.context_bar.show_percent"},
		{`{"context_bar":{"style":"SENTINEL"}}`, "tui.status_line.context_bar.style"},
		{`{"context_bar":{"warning_percent":0}}`, "tui.status_line.context_bar.warning_percent"},
		{`{"context_bar":{"warning_percent":95}}`, "tui.status_line.context_bar.warning_percent"},
		{`{"context_bar":{"critical_percent":101}}`, "tui.status_line.context_bar.critical_percent"},
		{`{"items":null}`, "tui.status_line.items"},
		{`{"items":[{"id":"SENTINEL"}]}`, "tui.status_line.items[0].id"},
		{`{"items":[{"id":"model"},{"id":"model"}]}`, "tui.status_line.items[1].id"},
		{`{"items":[{"id":"model","row":0}]}`, "tui.status_line.items[0].row"},
		{`{"items":[{"id":"model","row":3}]}`, "tui.status_line.items[0].row"},
		{`{"items":[{"id":"model","priority":-1}]}`, "tui.status_line.items[0].priority"},
		{`{"items":[{"id":"model","priority":101}]}`, "tui.status_line.items[0].priority"},
		{`{"items":[{"id":"model","label":"界界界界界界界界界界界"}]}`, "tui.status_line.items[0].label"},
		{`{"items":[{"id":"model","label":"SENTINEL\u001b"}]}`, "tui.status_line.items[0].label"},
	}
	for _, tc := range cases {
		t.Run(tc.fragment, func(t *testing.T) {
			var cfg Config
			err := json.Unmarshal([]byte(`{"schema_version":1,"tui":{"status_line":`+tc.fragment+`}}`), &cfg)
			if err == nil {
				err = cfg.Validate(nil, nil)
			}
			if err == nil {
				t.Fatal("invalid status line accepted")
			}
			if !strings.Contains(err.Error(), tc.path) || strings.Contains(err.Error(), "SENTINEL") {
				t.Fatalf("unsafe or imprecise error: %v", err)
			}
		})
	}
}

func TestModelContextWindowTokensValidation(t *testing.T) {
	for _, capacity := range []string{`null`, `1`, `2147483647`, `0`, `-1`, `2147483648`, `1.5`, `"SENTINEL"`, `true`} {
		t.Run(capacity, func(t *testing.T) {
			var cfg Config
			err := json.Unmarshal([]byte(`{"schema_version":1,"models":[{"id":"main","provider":"custom-openai","protocol":"openai-compatible","base_url":"https://example.com","model":"custom","context_window_tokens":`+capacity+`}]}`), &cfg)
			if err == nil {
				err = cfg.Validate(nil, nil)
			}
			valid := capacity == "null" || capacity == "1" || capacity == "2147483647"
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid && (err == nil || !strings.Contains(err.Error(), "models[0].context_window_tokens") || strings.Contains(err.Error(), "SENTINEL")) {
				t.Fatalf("invalid capacity accepted or error unsafe: %v", err)
			}
		})
	}
}

func TestLoadInvalidStatusLineDoesNotRewriteConfig(t *testing.T) {
	dir := withTempDir(t)
	path := filepath.Join(dir, "config.json")
	initial := `{"schema_version":1,"tui":{"status_line":{"max_rows":0}}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("invalid status line loaded")
	}
	after, _ := os.ReadFile(path)
	if string(after) != initial {
		t.Fatal("invalid config changed on Load")
	}
}

func TestSaveRejectsNilStatusItemsWithoutChangingExistingFile(t *testing.T) {
	dir := withTempDir(t)
	path := filepath.Join(dir, "config.json")
	initial := `{"schema_version":1,"custom_root":true}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	line := DefaultStatusLine()
	line.Items = nil
	cfg := &Config{SchemaVersion: SchemaVersion, TUI: &TUIConfig{StatusLine: line}}
	if err := Save(cfg); err == nil || !strings.Contains(err.Error(), "tui.status_line.items") {
		t.Fatalf("invalid programmatic items accepted: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != initial {
		t.Fatal("failed validation changed existing config")
	}
}

func TestStatusLineRejectsUnicodeLineSeparators(t *testing.T) {
	for _, value := range []string{"\u2028", "\u2029"} {
		line := DefaultStatusLine()
		line.Separator = value
		cfg := &Config{SchemaVersion: SchemaVersion, TUI: &TUIConfig{StatusLine: line}}
		if err := cfg.Validate(nil, nil); err == nil || !strings.Contains(err.Error(), "tui.status_line.separator") {
			t.Fatalf("Unicode newline accepted: %v", err)
		}
	}
}

func TestSaveRetainsDecodedExtensionsWithoutExistingFile(t *testing.T) {
	withTempDir(t)
	var cfg Config
	initial := `{"schema_version":1,"custom_root":true,"tui":{"status_line":{"custom_line":true,"items":[{"id":"model","custom_item":true}]}}}`
	if err := json.Unmarshal([]byte(initial), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := Save(&cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("PLUME_HOME"), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"custom_root", "custom_line", "custom_item"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("lost decoded extension %s", key)
		}
	}
}
