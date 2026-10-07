package config

import (
	"strings"
	"testing"

	"plume-agent/internal/channel"
	"plume-agent/internal/provider"
)

func catalogs() (*provider.Registry, *channel.Registry) {
	return provider.NewRegistry(), channel.NewRegistry()
}

func validConfig() *Config {
	return &Config{
		SchemaVersion: SchemaVersion,
		DefaultModel:  "deepseek-default",
		Models: []ModelConfig{{
			ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek",
			BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
			APIKeyRef: "deepseek-default",
		}},
		Channels: []ChannelConfig{{
			ID: "weixin-1", Type: "weixin", Enabled: true, ModelRef: "deepseek-default",
		}},
	}
}

func TestValidateAcceptsFirstReleaseConfig(t *testing.T) {
	providers, channels := catalogs()
	if err := validConfig().Validate(providers, channels); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateAcceptsEmptyConfig(t *testing.T) {
	providers, channels := catalogs()
	// 全新安装还没有任何模型和渠道。
	if err := (&Config{SchemaVersion: SchemaVersion}).Validate(providers, channels); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateFillsBaseURLFromPresetDefault(t *testing.T) {
	providers, channels := catalogs()
	cfg := validConfig()
	cfg.Models[0].BaseURL = "" // 由 DeepSeek 预设提供默认值。
	if err := cfg.Validate(providers, channels); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{
			name:    "unsupported schema version",
			mutate:  func(c *Config) { c.SchemaVersion = SchemaVersion + 1 },
			wantSub: "schema_version",
		},
		{
			name: "model without base url and no preset default",
			mutate: func(c *Config) {
				c.Models[0].Provider, c.Models[0].Protocol, c.Models[0].BaseURL = "custom-openai", "openai-compatible", ""
			},
			wantSub: "base_url is required",
		},
		{
			name:    "unknown provider",
			mutate:  func(c *Config) { c.Models[0].Provider = "not-a-provider" },
			wantSub: "unknown provider",
		},
		{
			name:    "protocol does not match provider",
			mutate:  func(c *Config) { c.Models[0].Protocol = "openai-compatible" },
			wantSub: "protocol is",
		},
		{
			name:    "missing model name",
			mutate:  func(c *Config) { c.Models[0].Model = "" },
			wantSub: "model is required",
		},
		{
			name: "duplicate model id",
			mutate: func(c *Config) {
				dup := c.Models[0]
				c.Models = append(c.Models, dup)
			},
			wantSub: "duplicate model id",
		},
		{
			name:    "dangling default_model",
			mutate:  func(c *Config) { c.DefaultModel = "nope" },
			wantSub: "default_model",
		},
		{
			name:    "invalid api key ref",
			mutate:  func(c *Config) { c.Models[0].APIKeyRef = "../escape" },
			wantSub: "api_key_ref",
		},
		{
			name:    "plain http to a remote host",
			mutate:  func(c *Config) { c.Models[0].BaseURL = "http://api.deepseek.com" },
			wantSub: "http is only allowed for loopback",
		},
		{
			name:    "non http scheme",
			mutate:  func(c *Config) { c.Models[0].BaseURL = "ftp://api.deepseek.com" },
			wantSub: "scheme must be http or https",
		},
		{
			name:    "unknown channel type",
			mutate:  func(c *Config) { c.Channels[0].Type = "carrier-pigeon" },
			wantSub: "unknown channel type",
		},
		{
			name:    "channel type is not available yet",
			mutate:  func(c *Config) { c.Channels[0].Type = "feishu" },
			wantSub: "not available yet",
		},
		{
			name:    "dangling channel model_ref",
			mutate:  func(c *Config) { c.Channels[0].ModelRef = "nope" },
			wantSub: "model_ref",
		},
		{
			name: "more than one enabled channel",
			mutate: func(c *Config) {
				second := c.Channels[0]
				second.ID = "weixin-2"
				c.Channels = append(c.Channels, second)
			},
			wantSub: "at most one channel may be enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, channels := catalogs()
			cfg := validConfig()
			tt.mutate(cfg)
			err := cfg.Validate(providers, channels)
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Validate() error = %q, want it to contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestValidateAllowsLoopbackHTTP(t *testing.T) {
	providers, channels := catalogs()
	cfg := validConfig()
	cfg.Models[0].BaseURL = "http://127.0.0.1:11434/v1"
	if err := cfg.Validate(providers, channels); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a loopback http URL", err)
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	providers, channels := catalogs()
	cfg := validConfig()
	cfg.Models[0].Provider = "unknown"
	cfg.Channels[0].Type = "unknown"
	cfg.DefaultModel = "dangling"

	err := cfg.Validate(providers, channels)
	if err == nil {
		t.Fatal("Validate() = nil, want errors")
	}
	for _, want := range []string{"unknown provider", "unknown channel type", "default_model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error %q does not mention %q", err, want)
		}
	}
}

// TestValidateAcceptsNewlyRegisteredEntries 是 G1a 的验收检查：新增供应商或渠道只需
// 一次 Register 调用——不需要改命令分发或校验逻辑。
func TestValidateAcceptsNewlyRegisteredEntries(t *testing.T) {
	providers, channels := catalogs()
	providers.Register(provider.Preset{
		ID: "test-provider", DisplayName: "Test", Protocol: "openai-compatible",
		DefaultBaseURL: "https://example.invalid/v1",
	})
	channels.Register(channel.Type{ID: "test-channel", DisplayName: "Test", Enabled: true})

	cfg := &Config{
		SchemaVersion: SchemaVersion,
		DefaultModel:  "test-model",
		Models: []ModelConfig{{
			ID: "test-model", Provider: "test-provider", Protocol: "openai-compatible",
			Model: "test-model-id",
		}},
		Channels: []ChannelConfig{{
			ID: "test-1", Type: "test-channel", Enabled: true, ModelRef: "test-model",
		}},
	}
	if err := cfg.Validate(providers, channels); err != nil {
		t.Fatalf("Validate() error = %v, want nil after registering new entries", err)
	}
}

func TestResolveBaseURLPrefersConfiguredValue(t *testing.T) {
	providers, _ := catalogs()
	m := ModelConfig{Provider: "deepseek", BaseURL: "https://proxy.internal/v1"}
	if got := m.ResolveBaseURL(providers); got != "https://proxy.internal/v1" {
		t.Fatalf("ResolveBaseURL() = %q, want the configured value", got)
	}
	m.BaseURL = ""
	if got := m.ResolveBaseURL(providers); got != "https://api.deepseek.com" {
		t.Fatalf("ResolveBaseURL() = %q, want the preset default", got)
	}
}
