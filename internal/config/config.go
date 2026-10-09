// Package config 保存 plume 的非敏感用户配置：schema、校验规则、原子持久化，
// 以及配置所引用的凭据引用。密钥值从不放在这里——它们存放在 credentials 目录下，
// 通过引用名指向。
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// SchemaVersion 是本 build 唯一能识别的配置 schema 版本。读到未知版本直接报错，
// 而不是尽力解析，这样旧二进制永远不会误读新配置。
const SchemaVersion = 1

// Config 对应落盘的 config.json。
type Config struct {
	SchemaVersion int             `json:"schema_version"`
	DefaultModel  string          `json:"default_model,omitempty"`
	Models        []ModelConfig   `json:"models,omitempty"`
	Channels      []ChannelConfig `json:"channels,omitempty"`
	TUI           *TUIConfig      `json:"tui,omitempty"`
	rawJSON       []byte          // 保留解码来源的未知字段，跨目录 Save 时仍可合并。
}

// ModelConfig 描述一个模型端点。Provider 与 Protocol 分开存：Provider 是预设品牌，
// Protocol 是持久化的协议适配选择器（历史值 deepseek / openai-compatible，schema v1
// 原样保留），G1b.1 的注册工厂把它映射到自研适配器及内部 openai-chat-completions
// 协议族，见 docs/decisions/0003-model-runtime.md。
type ModelConfig struct {
	ID                  string  `json:"id"`
	Provider            string  `json:"provider"`
	Protocol            string  `json:"protocol"`
	BaseURL             string  `json:"base_url,omitempty"`
	Model               string  `json:"model"`
	APIKeyRef           string  `json:"api_key_ref,omitempty"`
	ReasoningEffort     *string `json:"reasoning_effort,omitempty"`
	ContextWindowTokens *int64  `json:"context_window_tokens"`
}

// ChannelConfig 描述一个消息渠道实例。渠道专有设置放在 Settings 里，通用配置层
// 因此无需知道它们（例如微信的 context_token 由微信适配器自管）。
type ChannelConfig struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Enabled       bool              `json:"enabled"`
	ModelRef      string            `json:"model_ref,omitempty"`
	CredentialRef string            `json:"credential_ref,omitempty"`
	Settings      map[string]string `json:"settings,omitempty"`
}

// ProviderCatalog 是校验所需的只读供应商查询。provider 注册表和测试替身都能满足它；
// 正是这层接口让本包不必依赖 G1b 模型工厂将引入的 HTTP/协议实现依赖。
type ProviderCatalog interface {
	// ProtocolFor 返回供应商预设 ID 对应的协议适配选择器。
	ProtocolFor(providerID string) (protocol string, ok bool)
	// DefaultBaseURL 返回预设预填的 Base URL；预设没有预填值时返回 ""，
	// 表示必须由用户提供。
	DefaultBaseURL(providerID string) string
}

// ChannelCatalog 是校验所需的只读渠道查询。
type ChannelCatalog interface {
	// Available 报告渠道类型是否已知，以及本 build 是否允许启用它。
	Available(channelType string) (known, enabled bool)
}

// ResolveBaseURL 返回最终生效的 Base URL：用户填了就用用户的，否则用预设预填的默认值。
// 设置向导会把解析结果持久化进 BaseURL，所以这里的回退主要是为了兼容手写配置和旧文件。
func (m ModelConfig) ResolveBaseURL(cat ProviderCatalog) string {
	if v := strings.TrimSpace(m.BaseURL); v != "" {
		return v
	}
	if cat == nil {
		return ""
	}
	return cat.DefaultBaseURL(m.Provider)
}

// credentialRefRe 把凭据引用限制为单个路径元素：必须以字母数字开头（因此排除了
// "."、".." 和任何隐藏文件），且不能包含路径分隔符。
var credentialRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidCredentialRef 报告 name 能否作为凭据引用。
func ValidCredentialRef(name string) bool {
	return credentialRefRe.MatchString(name)
}

// Validate 检查必填项、取值形态与交叉引用。它会一次性返回发现的全部问题（而不是遇到
// 第一个就停），便于向导展示完整清单。catalog 为 nil 时跳过依赖它的检查，供只关心
// 结构合法性的测试使用。
func (c *Config) Validate(providers ProviderCatalog, channels ChannelCatalog) error {
	var errs []error
	errs = append(errs, c.validateDisplayOptions())

	if c.SchemaVersion != SchemaVersion {
		errs = append(errs, fmt.Errorf("schema_version is %d, want %d", c.SchemaVersion, SchemaVersion))
	}

	modelIDs := make(map[string]bool, len(c.Models))
	for i, m := range c.Models {
		if m.ReasoningEffort != nil && !validReasoningEffort(*m.ReasoningEffort) {
			errs = append(errs, fmt.Errorf("models[%d].reasoning_effort: expected none, low, medium, high, or max", i))
		}
		where := fmt.Sprintf("models[%d]", i)
		if m.ID != "" {
			where = fmt.Sprintf("model %q", m.ID)
		}

		switch {
		case m.ID == "":
			errs = append(errs, fmt.Errorf("%s: id is required", where))
		case modelIDs[m.ID]:
			errs = append(errs, fmt.Errorf("%s: duplicate model id", where))
		default:
			modelIDs[m.ID] = true
		}

		if m.Provider == "" {
			errs = append(errs, fmt.Errorf("%s: provider is required", where))
		}
		if m.Model == "" {
			errs = append(errs, fmt.Errorf("%s: model is required", where))
		}

		if providers != nil && m.Provider != "" {
			want, ok := providers.ProtocolFor(m.Provider)
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("%s: unknown provider %q", where, m.Provider))
			case m.Protocol == "":
				errs = append(errs, fmt.Errorf("%s: protocol is required for provider %q", where, m.Provider))
			case m.Protocol != want:
				errs = append(errs, fmt.Errorf("%s: protocol is %q but provider %q uses %q", where, m.Protocol, m.Provider, want))
			}
		}

		if base := m.ResolveBaseURL(providers); base == "" {
			errs = append(errs, fmt.Errorf("%s: base_url is required and provider %q has no default", where, m.Provider))
		} else if err := validateBaseURL(base); err != nil {
			errs = append(errs, fmt.Errorf("%s: base_url %q: %w", where, base, err))
		}

		if m.APIKeyRef != "" && !ValidCredentialRef(m.APIKeyRef) {
			errs = append(errs, fmt.Errorf("%s: api_key_ref %q is not a valid reference", where, m.APIKeyRef))
		}
	}

	if c.DefaultModel != "" && !modelIDs[c.DefaultModel] {
		errs = append(errs, fmt.Errorf("default_model %q does not match any model id", c.DefaultModel))
	}

	channelIDs := make(map[string]bool, len(c.Channels))
	enabled := 0
	for i, ch := range c.Channels {
		where := fmt.Sprintf("channels[%d]", i)
		if ch.ID != "" {
			where = fmt.Sprintf("channel %q", ch.ID)
		}

		switch {
		case ch.ID == "":
			errs = append(errs, fmt.Errorf("%s: id is required", where))
		case channelIDs[ch.ID]:
			errs = append(errs, fmt.Errorf("%s: duplicate channel id", where))
		default:
			channelIDs[ch.ID] = true
		}

		if ch.Type == "" {
			errs = append(errs, fmt.Errorf("%s: type is required", where))
		} else if channels != nil {
			known, available := channels.Available(ch.Type)
			switch {
			case !known:
				errs = append(errs, fmt.Errorf("%s: unknown channel type %q", where, ch.Type))
			case ch.Enabled && !available:
				errs = append(errs, fmt.Errorf("%s: channel type %q is not available yet", where, ch.Type))
			}
		}

		if ch.Enabled {
			enabled++
		}
		if ch.ModelRef != "" && !modelIDs[ch.ModelRef] {
			errs = append(errs, fmt.Errorf("%s: model_ref %q does not match any model id", where, ch.ModelRef))
		}
		if ch.CredentialRef != "" && !ValidCredentialRef(ch.CredentialRef) {
			errs = append(errs, fmt.Errorf("%s: credential_ref %q is not a valid reference", where, ch.CredentialRef))
		}
	}

	// docs/decisions/0001-scope.md：首版只保持一个活动渠道。
	if enabled > 1 {
		errs = append(errs, fmt.Errorf("at most one channel may be enabled, found %d", enabled))
	}

	return errors.Join(errs...)
}

// validateDisplayOptions 也用于持久化边界，不依赖供应商和渠道注册表。
func (c *Config) validateDisplayOptions() error {
	var errs []error
	for i, m := range c.Models {
		if m.ContextWindowTokens != nil && (*m.ContextWindowTokens < 1 || *m.ContextWindowTokens > 2147483647) {
			errs = append(errs, fmt.Errorf("models[%d].context_window_tokens: expected null or integer in 1..2147483647", i))
		}
	}
	if c.TUI != nil {
		errs = append(errs, c.TUI.Validate())
	}
	return errors.Join(errs...)
}

// validateBaseURL 强制 HTTPS，仅回环主机允许明文 HTTP（供本地无鉴权服务使用）。
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("http is only allowed for loopback hosts, use https for %q", u.Hostname())
		}
	default:
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("missing host")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
