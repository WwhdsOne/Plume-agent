package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestSaveContextWindowDefaults 守住容量推断的保守边界：仅官方 DeepSeek 端点（含 /v1 与
// 尾斜杠变体）的已知模型预填 1000000，显式配置优先，其余一律保持 unknown 不猜。
func TestSaveContextWindowDefaults(t *testing.T) {
	cases := []struct {
		name, provider, protocol, baseURL, model string
		explicit, want                           int64
	}{
		{name: "flash_root", baseURL: "https://api.deepseek.com", model: "deepseek-flash", want: 1000000},
		{name: "flash_root_slash", baseURL: "https://api.deepseek.com/", model: "deepseek-flash", want: 1000000},
		{name: "flash_v1", baseURL: "https://api.deepseek.com/v1", model: "deepseek-flash", want: 1000000},
		{name: "flash_v1_slash", baseURL: "https://api.deepseek.com/v1/", model: "deepseek-flash", want: 1000000},
		{name: "pro_root", baseURL: "https://api.deepseek.com", model: "deepseek-v4-pro", want: 1000000},
		{name: "pro_root_slash", baseURL: "https://api.deepseek.com/", model: "deepseek-v4-pro", want: 1000000},
		{name: "pro_v1", baseURL: "https://api.deepseek.com/v1", model: "deepseek-v4-pro", want: 1000000},
		{name: "pro_v1_slash", baseURL: "https://api.deepseek.com/v1/", model: "deepseek-v4-pro", want: 1000000},
		{name: "manual_official", baseURL: "https://api.deepseek.com", model: "deepseek-flash", explicit: 128000, want: 128000},
		{name: "manual_proxy", baseURL: "https://proxy.example.com", model: "deepseek-flash", explicit: 64000, want: 64000},
		{name: "proxy", baseURL: "https://proxy.example.com", model: "deepseek-flash"},
		{name: "unknown_model", baseURL: "https://api.deepseek.com", model: "unknown"},
		{name: "custom_provider", provider: "custom-openai", protocol: "openai-compatible", baseURL: "https://api.deepseek.com", model: "deepseek-flash"},
		{name: "different_protocol", protocol: "openai-compatible", baseURL: "https://api.deepseek.com", model: "deepseek-flash"},
		{name: "http", baseURL: "http://api.deepseek.com", model: "deepseek-flash"},
		{name: "custom_path", baseURL: "https://api.deepseek.com/proxy", model: "deepseek-flash"},
		{name: "custom_query", baseURL: "https://api.deepseek.com?route=proxy", model: "deepseek-flash"},
		{name: "custom_port", baseURL: "https://api.deepseek.com:8443", model: "deepseek-flash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempDir(t)
			if tc.provider == "" {
				tc.provider = "deepseek"
			}
			if tc.protocol == "" {
				tc.protocol = "deepseek"
			}
			m := ModelConfig{ID: "main", Provider: tc.provider, Protocol: tc.protocol, BaseURL: tc.baseURL, Model: tc.model}
			if tc.explicit != 0 {
				m.ContextWindowTokens = &tc.explicit
			}
			cfg := &Config{SchemaVersion: SchemaVersion, Models: []ModelConfig{m}}
			if err := Save(cfg); err != nil {
				t.Fatal(err)
			}
			assertContextWindow(t, cfg.Models[0].ContextWindowTokens, tc.want)
			path, err := Path()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved Config
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			assertContextWindow(t, saved.Models[0].ContextWindowTokens, tc.want)
		})
	}
}

// TestLoadContextWindowDefaults 守住读取侧迁移：缺失/null 的容量按同一推断规则补默认并落盘，
// 迁移保留未知字段与状态栏选中项，第二次 Load 不重写文件、不改 mtime（幂等）。
func TestLoadContextWindowDefaults(t *testing.T) {
	cases := []struct {
		name, baseURL, model, capacity string
		want                           int64
	}{
		{name: "flash_missing", model: "deepseek-flash", want: 1000000},
		{name: "flash_null", model: "deepseek-flash", capacity: "null", want: 1000000},
		{name: "pro_missing", model: "deepseek-v4-pro", want: 1000000},
		{name: "pro_null", model: "deepseek-v4-pro", capacity: "null", want: 1000000},
		{name: "flash_manual", model: "deepseek-flash", capacity: "128000", want: 128000},
		{name: "pro_manual", model: "deepseek-v4-pro", capacity: "128000", want: 128000},
		{name: "proxy_missing", baseURL: "https://proxy.example.com", model: "deepseek-flash"},
		{name: "proxy_null", baseURL: "https://proxy.example.com", model: "deepseek-flash", capacity: "null"},
		{name: "unknown_missing", model: "unknown"},
		{name: "unknown_null", model: "unknown", capacity: "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := withTempDir(t)
			path := filepath.Join(dir, "config.json")
			if tc.baseURL == "" {
				tc.baseURL = "https://api.deepseek.com"
			}
			initial := contextWindowFixture(t, tc.baseURL, tc.model, tc.capacity)
			if err := os.WriteFile(path, initial, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			assertContextWindow(t, cfg.Models[0].ContextWindowTokens, tc.want)
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Models     []map[string]json.RawMessage `json:"models"`
				CustomRoot json.RawMessage              `json:"custom_root"`
				TUI        struct {
					StatusLine struct {
						Items []struct {
							ID string `json:"id"`
						} `json:"items"`
					} `json:"status_line"`
				} `json:"tui"`
			}
			if err := json.Unmarshal(first, &saved); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(saved.CustomRoot, []byte("true")) || !bytes.Equal(saved.Models[0]["custom_model"], []byte("42")) {
				t.Fatal("migration lost unknown config fields")
			}
			var persisted *int64
			if err := json.Unmarshal(saved.Models[0]["context_window_tokens"], &persisted); err != nil {
				t.Fatal(err)
			}
			assertContextWindow(t, persisted, tc.want)
			if len(saved.TUI.StatusLine.Items) != 2 || saved.TUI.StatusLine.Items[0].ID != "git" || saved.TUI.StatusLine.Items[1].ID != "model" {
				t.Fatal("migration changed selected item order")
			}
			if runtime.GOOS != "windows" {
				if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
					t.Fatalf("config permission = %v, err = %v", fi, err)
				}
			}
			stamp := time.Unix(1234567890, 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("second Load changed complete config: %v", err)
			}
			if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(stamp) {
				t.Fatalf("second Load rewrote complete config: %v, %v", fi, err)
			}
		})
	}
}

// TestLoadContextWindowMigrationFailureKeepsFile 守住迁移失败语义：容量迁移写回失败时 Load 报错，既有文件保持原样。
func TestLoadContextWindowMigrationFailureKeepsFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires enforced POSIX directory permissions")
	}
	dir := withTempDir(t)
	path := filepath.Join(dir, "config.json")
	initial := contextWindowFixture(t, "https://api.deepseek.com", "deepseek-flash", "null")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded despite failing capacity migration")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(initial, after) {
		t.Fatalf("failed capacity migration changed existing file: %v", err)
	}
}

// 完整显示配置使 null 用例只触发容量迁移，不依赖其他缺失默认项改写文件。
func contextWindowFixture(t *testing.T, baseURL, model, capacity string) []byte {
	t.Helper()
	high := "high"
	cfg := &Config{
		SchemaVersion: SchemaVersion,
		Models:        []ModelConfig{{ID: "main", Provider: "deepseek", Protocol: "deepseek", BaseURL: baseURL, Model: model, ReasoningEffort: &high}},
		TUI:           &TUIConfig{StatusMessages: DefaultStatusMessages(), StatusLine: DefaultStatusLine()},
	}
	cfg.TUI.StatusLine.Items = []StatusItemConfig{
		{ID: "git", Label: "git", Enabled: true, Row: 2, Priority: 50},
		{ID: "model", Label: "model", Enabled: true, Row: 1, Priority: 100},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(root["models"], &models); err != nil {
		t.Fatal(err)
	}
	delete(models[0], "context_window_tokens")
	if capacity != "" {
		models[0]["context_window_tokens"] = json.RawMessage(capacity)
	}
	models[0]["custom_model"] = json.RawMessage("42")
	root["custom_root"] = json.RawMessage("true")
	root["models"], err = json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertContextWindow(t *testing.T, got *int64, want int64) {
	t.Helper()
	if want == 0 {
		if got != nil {
			t.Fatalf("context window = %d, want unknown", *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("context window = unknown, want %d", want)
	}
	if *got != want {
		t.Fatalf("context window = %d, want %d", *got, want)
	}
}
