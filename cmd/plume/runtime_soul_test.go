package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plume-agent/internal/config"
	"plume-agent/internal/setup"
)

func TestSetupSummaryShowsSoulInitialization(t *testing.T) {
	for _, state := range []string{"created", "preserved", "disabled"} {
		t.Run(state, func(t *testing.T) {
			res := &setup.Result{Config: &config.Config{Models: []config.ModelConfig{{Provider: "fixture", Model: "m"}}}, ModelID: "fixture"}
			want := "Soul:     disabled"
			if state != "disabled" {
				res.SoulPath = "/fixture/soul.md"
				res.SoulCreated = state == "created"
				want = "Soul:     /fixture/soul.md (" + state + ")"
			}
			var out bytes.Buffer
			printSetupSummary(&out, res, "")
			if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "Run setup again: plume setup") {
				t.Fatalf("missing soul initialization or next steps: %s", out.String())
			}
		})
	}
}

func TestBuildRuntimeSendsConfiguredSoulThroughSDK(t *testing.T) {
	for _, state := range []string{"default", "custom", "disabled"} {
		t.Run(state, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("PLUME_HOME", home)
			options := config.DefaultAgent()
			if state == "custom" {
				options.Soul.Path = "custom.md"
			}
			options.Soul.Enabled = state != "disabled"
			private := "soul-sentinel-请用简洁中文"
			if state == "disabled" {
				private = string([]byte{0xff})
			}
			if err := os.WriteFile(filepath.Join(home, options.Soul.Path), []byte(private), 0o600); err != nil {
				t.Fatal(err)
			}
			requests := make(chan []map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				requests <- req.Messages
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
			}))
			defer srv.Close()
			cfg := &config.Config{DefaultModel: "fixture", Agent: options, Tools: config.DefaultTools(), Models: []config.ModelConfig{{ID: "fixture", Provider: "deepseek", Protocol: "deepseek", BaseURL: srv.URL, Model: "fixture"}}}
			cfg.Tools.Workspace = t.TempDir()
			if err := os.WriteFile(filepath.Join(cfg.Tools.Workspace, "soul.md"), []byte("wrong-workspace-personality"), 0o600); err != nil {
				t.Fatal(err)
			}
			runtime, _, err := buildRuntime(cfg, "")
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			if _, err := runtime.Run(context.Background(), nil, "task"); err != nil {
				t.Fatal(err)
			}
			messages := <-requests
			if len(messages) != 2 || messages[0]["role"] != "system" {
				t.Fatal("expected one system and current user")
			}
			text := messages[0]["content"].(string)
			if strings.Contains(text, "wrong-workspace-personality") || (state != "disabled" && strings.Count(text, private) != 1) || (state == "disabled" && strings.Contains(text, "Personality (soul.md):")) {
				t.Fatal("configured personality not respected by SDK request")
			}
		})
	}
}
