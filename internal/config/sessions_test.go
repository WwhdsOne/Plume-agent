package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveInvalidSessionsKeepsExistingConfig 守住写入边界：非法会话配置
// 必须在替换文件前拒绝，原配置的字节和可加载性均保持不变。
func TestSaveInvalidSessionsKeepsExistingConfig(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*SessionsConfig)
	}{
		// case: 分页不能超过已公布的上界。
		{"page_too_large", func(c *SessionsConfig) { c.PageSize = 101 }},
		// case: 显式零页数不能被 omitempty 偷偷改成默认。
		{"page_zero", func(c *SessionsConfig) { c.PageSize = 0 }},
		// case: 收尾期限上下界都在 Save 时校验。
		{"shutdown_too_small", func(c *SessionsConfig) { c.ShutdownTimeoutMS = 999 }},
		{"shutdown_too_large", func(c *SessionsConfig) { c.ShutdownTimeoutMS = 60001 }},
		// case: 禁止空路径和目录路径，不能落盘后才由 Load 拒绝。
		{"empty_path", func(c *SessionsConfig) { c.History.Path = "" }},
		{"directory_path", func(c *SessionsConfig) { c.History.Path = "sessions/" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PLUME_HOME", t.TempDir())
			cfg := &Config{SchemaVersion: SchemaVersion, Sessions: DefaultSessions()}
			if err := Save(cfg); err != nil {
				t.Fatal(err)
			}
			path := mustConfigPath(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			test.change(cfg.Sessions)
			if err := Save(cfg); err == nil {
				t.Fatal("invalid sessions accepted by Save")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("invalid Save changed existing configuration")
			}
			if _, err := Load(); err != nil {
				t.Fatalf("existing configuration became invalid: %v", err)
			}
		})
	}
}

// TestSessionsDefaultsRoundTrip 守住 sessions 段补齐：缺失/null 补默认并
// 实际落盘，显式 false、用户路径与有效数值保留，未知字段不丢。
func TestSessionsDefaultsRoundTrip(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	if err := Save(&Config{SchemaVersion: SchemaVersion}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(mustConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, ok := persisted["sessions"]; !ok {
		t.Fatalf("sessions section not persisted: %s", raw)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Sessions; got == nil || got.PageSize != 20 || got.ShutdownTimeoutMS != 5000 || got.History == nil || got.History.Path != "sessions/history.db" || got.History.Enabled == nil || !*got.History.Enabled {
		t.Fatalf("defaults not filled: %+v", got)
	}
	path, err := loaded.HistoryDatabasePath()
	if err != nil || !strings.HasSuffix(filepath.ToSlash(path), "/sessions/history.db") {
		t.Fatalf("history path = %q err=%v", path, err)
	}
}

// TestSessionsUserValuesPreserved 守住用户值：显式关闭、自定义路径与有效
// 数值在 Save/Load 往返后原样保留，不被默认值覆盖。
func TestSessionsUserValuesPreserved(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	enabled := false
	cfg := &Config{SchemaVersion: SchemaVersion, Sessions: &SessionsConfig{
		History:           &HistoryConfig{Enabled: &enabled, Path: "custom/hist.db"},
		PageSize:          50,
		ShutdownTimeoutMS: 10000,
	}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Sessions
	if got == nil || got.PageSize != 50 || got.ShutdownTimeoutMS != 10000 || got.History.Path != "custom/hist.db" || got.History.Enabled == nil || *got.History.Enabled {
		t.Fatalf("user values clobbered: %+v", got)
	}
	if loaded.HistoryEnabled() {
		t.Fatal("explicitly disabled history became enabled")
	}
}

// TestSessionsValidationBounds 守住范围校验：page_size 与收尾期限越界、
// 空路径/目录路径报配置错误，不静默改默认。
func TestSessionsValidationBounds(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{"page_too_small", `{"page_size": 0}`},
		{"page_too_large", `{"page_size": 101}`},
		{"shutdown_too_small", `{"shutdown_timeout_ms": 999}`},
		{"shutdown_too_large", `{"shutdown_timeout_ms": 60001}`},
		{"empty_path", `{"history": {"path": ""}}`},
		{"directory_path", `{"history": {"path": "sessions/"}}`},
		{"dot_path", `{"history": {"path": "."}}`},
		{"control_path", `{"history": {"path": "a\u0000b"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var section SessionsConfig
			if err := json.Unmarshal([]byte(test.raw), &section); err == nil {
				t.Fatalf("expected validation error for %s", test.raw)
			}
		})
	}
}

// TestSessionsNullHistoryKeepsDefault 守住 null 语义：history 为 null 时
// 保留默认开启，不把 null 当显式 false。
func TestSessionsNullHistoryKeepsDefault(t *testing.T) {
	var section SessionsConfig
	if err := json.Unmarshal([]byte(`{"history": null, "page_size": 20, "shutdown_timeout_ms": 5000}`), &section); err != nil {
		t.Fatal(err)
	}
	if section.History == nil || section.History.Enabled == nil || !*section.History.Enabled || section.History.Path != "sessions/history.db" {
		t.Fatalf("null history lost defaults: %+v", section.History)
	}
}

// TestSessionsNullDefaultsKeepUnknownFields 守住校验补强后的兼容语义：
// 旧配置 null 字段仍补默认，sessions/history 的未来扩展经过 Load/Save 不丢失。
func TestSessionsNullDefaultsKeepUnknownFields(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	path := mustConfigPath(t)
	raw := []byte(`{"schema_version":1,"sessions":{"page_size":null,"shutdown_timeout_ms":null,"future":{"mode":"keep","nullable":null},"history":{"enabled":null,"path":null,"future":42}}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sessions.PageSize != 20 || cfg.Sessions.ShutdownTimeoutMS != 5000 || cfg.Sessions.History.Path != "sessions/history.db" || !cfg.HistoryEnabled() {
		t.Fatalf("null defaults lost: %+v", cfg.Sessions)
	}
	// Load 本身必须补入文件；不能依赖后续 Save 才让磁盘与内存一致。
	loadedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var afterLoad struct {
		Sessions struct {
			PageSize          int `json:"page_size"`
			ShutdownTimeoutMS int `json:"shutdown_timeout_ms"`
			History           struct {
				Enabled bool   `json:"enabled"`
				Path    string `json:"path"`
			} `json:"history"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(loadedBytes, &afterLoad); err != nil {
		t.Fatal(err)
	}
	if got := afterLoad.Sessions; got.PageSize != 20 || got.ShutdownTimeoutMS != 5000 || !got.History.Enabled || got.History.Path != "sessions/history.db" {
		t.Fatalf("defaults not persisted by Load: %s", loadedBytes)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	var section map[string]json.RawMessage
	var history map[string]json.RawMessage
	if err := json.Unmarshal(persisted, &root); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(root["sessions"], &section); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(section["history"], &history); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(section["future"], []byte(`"keep"`)) || !bytes.Contains(section["future"], []byte(`"nullable": null`)) || string(history["future"]) != "42" {
		t.Fatalf("unknown session fields lost: %s", persisted)
	}
}

func mustConfigPath(t *testing.T) string {
	t.Helper()
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	return path
}
