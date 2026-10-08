package main

import (
	"bytes"
	"strings"
	"testing"

	"plume-agent/internal/config"
)

func TestConfigShowStreamingDefaultsAndEffectiveReasoning(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	none := "none"
	for _, tc := range []struct {
		name, endpoint, want string
		effort               *string
	}{
		{"default-verified", "https://api.deepseek.com", "high (default; effective: high; capability: verified)", nil},
		{"disabled-verified", "https://api.deepseek.com", "none (configured; effective: none; capability: verified)", &none},
		{"default-proxy", "https://proxy.example.com", "high (default; effective: unknown; capability: provider_default)", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{SchemaVersion: 1, Models: []config.ModelConfig{{ID: "m", Provider: "deepseek", Protocol: "deepseek", Model: "deepseek-flash", BaseURL: tc.endpoint, ReasoningEffort: tc.effort}}}
			_, channels := newRegistries()
			var out bytes.Buffer
			printConfig(&out, cfg, channels)
			if !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), `thinking: ["Thought"] (default)`) {
				t.Fatalf("配置展示须区分偏好与生效值：%s", out.String())
			}
		})
	}
}
