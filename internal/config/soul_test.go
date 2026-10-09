package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSoulDefaultsPersist(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	cfg := &Config{SchemaVersion: SchemaVersion, Agent: &AgentConfig{Budget: DefaultAgent().Budget}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Agent struct {
			Soul map[string]json.RawMessage `json:"soul"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Agent.Soul) != 3 || string(raw.Agent.Soul["enabled"]) != "true" || string(raw.Agent.Soul["path"]) != `"soul.md"` || string(raw.Agent.Soul["max_bytes"]) != "65536" {
		t.Fatalf("soul defaults not persisted: %s", data)
	}
	if !reflect.DeepEqual(cfg.Agent.Soul, DefaultSoul()) {
		t.Fatalf("soul defaults not applied to saved config: %#v", cfg.Agent.Soul)
	}
}

func TestSoulMigrationPreservesOverridesAndUnknownFields(t *testing.T) {
	for _, soul := range []string{
		``, `null`, `{}`, `{"enabled":null,"path":null,"max_bytes":null,"extension":{"keep":true}}`,
		`{"enabled":false,"path":"personality/agent.md","max_bytes":8192,"extension":{"keep":true}}`,
		`{"enabled":false,"extension":{"keep":true}}`,
	} {
		t.Run(soul, func(t *testing.T) {
			t.Setenv("PLUME_HOME", t.TempDir())
			path, _ := Path()
			soulField := ""
			if soul != "" {
				soulField = `"soul":` + soul + `,`
			}
			initial := []byte(`{"schema_version":1,"agent":{` + soulField + `"future_agent":7},"future_root":true}`)
			if err := os.WriteFile(path, initial, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			want := DefaultSoul()
			if strings.Contains(soul, `"enabled":false`) {
				want.Enabled = false
			}
			if strings.Contains(soul, "personality/agent.md") {
				want.Path, want.MaxBytes = "personality/agent.md", 8192
			}
			if !reflect.DeepEqual(cfg.Agent.Soul, want) {
				t.Fatalf("migration result %#v, want %#v", cfg.Agent.Soul, want)
			}
			first, _ := os.ReadFile(path)
			if !bytes.Contains(first, []byte(`"future_agent": 7`)) || !bytes.Contains(first, []byte(`"future_root": true`)) {
				t.Fatalf("migration lost unknown fields: %s", first)
			}
			if strings.Contains(soul, "extension") && !bytes.Contains(first, []byte(`"keep": true`)) {
				t.Fatal("migration lost soul extension")
			}
			if _, err := Load(); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(path)
			if !bytes.Equal(first, second) {
				t.Fatal("soul migration is not idempotent")
			}
			if err := Save(cfg); err != nil {
				t.Fatal(err)
			}
			saved, _ := os.ReadFile(path)
			if strings.Contains(soul, "extension") && !bytes.Contains(saved, []byte(`"keep": true`)) {
				t.Fatal("save lost soul extension")
			}
		})
	}
}

func TestSoulConfigRejectsInvalidValues(t *testing.T) {
	for _, soul := range []string{
		`[]`, `false`, `"soul.md"`, `{"enabled":0}`, `{"enabled":"false"}`,
		`{"path":""}`, `{"path":" \t"}`, `{"path":false}`, `{"path":"a\u0000b"}`,
		`{"path":"a\nb"}`, `{"path":"a\rb"}`, `{"path":"a\tb"}`, `{"path":"a\u001bb"}`,
		`{"path":"a\u0085b"}`, `{"max_bytes":0}`, `{"max_bytes":-1}`,
		`{"max_bytes":8388609}`, `{"max_bytes":1.5}`, `{"max_bytes":"65536"}`,
	} {
		t.Run(soul, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(`{"schema_version":1,"agent":{"soul":`+soul+`}}`), &cfg); err == nil {
				t.Fatal("invalid soul config accepted")
			}
		})
	}
	for _, size := range []int{1, 8 << 20} {
		cfg := DefaultSoul()
		cfg.MaxBytes = size
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid size %d rejected: %v", size, err)
		}
	}
}

func TestSoulResolvePathUsesConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PLUME_HOME", home)
	t.Setenv("PLUME_SOUL_TEST_PATH", "personal/soul.md")
	for _, test := range []struct {
		name string
		cfg  *SoulConfig
		want string
	}{
		{"default", nil, filepath.Join(home, "soul.md")},
		{"relative", &SoulConfig{Enabled: true, Path: "personal/soul.md", MaxBytes: 1}, filepath.Join(home, "personal", "soul.md")},
		{"absolute", &SoulConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "soul.md"), MaxBytes: 1}, ""},
		{"variable", &SoulConfig{Enabled: true, Path: "$PLUME_SOUL_TEST_PATH", MaxBytes: 1}, filepath.Join(home, "personal", "soul.md")},
		{"disabled", &SoulConfig{Enabled: false, Path: "\x00", MaxBytes: 0}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "absolute" {
				test.want = test.cfg.Path
			}
			got, err := test.cfg.ResolvePath()
			if err != nil || got != test.want {
				t.Fatalf("resolved %q, want %q; error=%v", got, test.want, err)
			}
		})
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"~/personal/soul.md", "~/$PLUME_SOUL_TEST_PATH"} {
		got, err := (&SoulConfig{Enabled: true, Path: path, MaxBytes: 1}).ResolvePath()
		if err != nil || got != filepath.Join(userHome, "personal", "soul.md") {
			t.Fatalf("home expansion %q: %v", got, err)
		}
	}
	t.Setenv("PLUME_SOUL_TEST_PATH", "")
	if _, err := (&SoulConfig{Enabled: true, Path: "$PLUME_SOUL_TEST_PATH", MaxBytes: 1}).ResolvePath(); err == nil {
		t.Fatal("empty expanded path accepted")
	}
	for _, path := range []string{"personal/\x1b[31msoul.md", "personal/\tsoul.md", "personal/\u0085soul.md"} {
		t.Setenv("PLUME_SOUL_TEST_PATH", path)
		if _, err := (&SoulConfig{Enabled: true, Path: "$PLUME_SOUL_TEST_PATH", MaxBytes: 1}).ResolvePath(); err == nil {
			t.Fatalf("expanded control character path accepted: %q", path)
		}
	}
}
