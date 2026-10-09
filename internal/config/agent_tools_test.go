package config

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWorkspaceDefaultsPersistAndMigrate(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	cfg := &Config{SchemaVersion: SchemaVersion}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	data, _ := os.ReadFile(path)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw["agent"]) == 0 || len(raw["tools"]) == 0 {
		t.Fatalf("missing persisted defaults: %s", data)
	}
	var agent struct {
		Budget map[string]int `json:"budget"`
	}
	_ = json.Unmarshal(raw["agent"], &agent)
	if len(agent.Budget) != 9 || agent.Budget["model_calls"] != 0 || agent.Budget["tool_timeout_seconds"] != 180 {
		t.Fatalf("budget: %v", agent.Budget)
	}
	// 旧配置补齐、未知嵌套字段及显式零/空数组保持不变，第二次读取不重写。
	initial := []byte(`{"schema_version":1,"agent":{"budget":{"model_calls":42,"run_timeout_seconds":0,"custom":true}},"tools":{"enabled":[],"workspace":".","custom":"kept"},"unknown_root":7}`)
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if !bytes.Contains(first, []byte(`"model_calls": 42`)) || !bytes.Contains(first, []byte(`"enabled": []`)) || !bytes.Contains(first, []byte(`"custom": "kept"`)) || !bytes.Contains(first, []byte(`"unknown_root": 7`)) {
		t.Fatalf("lost overrides: %s", first)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("migration is not idempotent")
	}
	if err := Save(loaded); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if !bytes.Contains(saved, []byte(`"custom": true`)) {
		t.Fatal("save lost budget extension")
	}
}

func TestWorkspaceConfigRejectsInvalidValues(t *testing.T) {
	for _, fragment := range []string{
		`"agent":null`, `"agent":{"budget":null}`, `"agent":{"budget":{"model_calls":-1}}`,
		`"agent":{"budget":{"tool_calls":null}}`, `"agent":{"budget":{"model_timeout_seconds":0}}`,
		`"agent":{"budget":{"result_bytes":32}}`, `"agent":{"budget":{"request_bytes":1.5}}`,
		`"tools":null`, `"tools":{"enabled":null}`, `"tools":{"enabled":["unknown"]}`,
		`"tools":{"enabled":["read","read"]}`, `"tools":{"max_output_bytes":0}`, `"tools":{"shell":""}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			var cfg Config
			err := json.Unmarshal([]byte(`{"schema_version":1,`+fragment+`}`), &cfg)
			if err == nil {
				t.Fatal("invalid tool config accepted")
			}
		})
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"schema_version":1,"agent":{"budget":{"model_calls":0,"tool_calls":0,"run_timeout_seconds":0}},"tools":{"enabled":[]}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validateDisplayOptions(); err != nil && !strings.Contains(err.Error(), "tui") {
		t.Fatal(err)
	}
}
