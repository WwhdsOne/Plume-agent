package provider

import (
	"fmt"

	"plume-agent/internal/model"
	"plume-agent/internal/model/deepseek"
	"plume-agent/internal/model/openai"
)

// 持久化协议适配选择器的历史值（schema v1 原样保留，0003 §7）。
const (
	ProtocolDeepSeek         = "deepseek"
	ProtocolOpenAICompatible = "openai-compatible"
)

// ModelSpec 是工厂装配所需的模型配置子集。刻意不引用 config.ModelConfig：
// 本包保持零依赖（config 的测试以本包做校验替身），由调用方从
// config.ModelConfig 拆字段传入。
type ModelSpec struct {
	Provider        string
	Protocol        string
	BaseURL         string
	Model           string
	APIKeyRef       string
	ReasoningEffort *string
}

// Credentials 是工厂获取密钥的最小接口。真实实现读 credentials 目录；
// 测试用脚本化替身。密钥只在构造适配器时使用，不进入工厂状态，
// 也不出现在任何 trace 或错误信息中。
type Credentials interface {
	// Key 返回凭据引用对应的密钥值。
	Key(ref string) (string, error)
}

// ModelFactory 把已保存的模型配置装配成 model.Client。Adapter +
// Factory 是 0003 §2 的设计手法：新增协议适配器注册进来即可，
// 调用方与 Agent/TUI 不感知；未知 ID 明确失败，禁止悄悄回退。
type ModelFactory struct {
	registry *Registry
	creds    Credentials
}

// NewModelFactory 构造工厂。creds 允许为 nil（仅当所有配置都不引用
// 凭据时才能 Build 成功，例如本机无鉴权服务）。
func NewModelFactory(registry *Registry, creds Credentials) *ModelFactory {
	return &ModelFactory{registry: registry, creds: creds}
}

// Build 校验配置并按 0003 §7 的映射表构造适配器：
//
//	deepseek / deepseek → internal/model/deepseek（组合兼容实现）
//	任意 provider / openai-compatible → internal/model/openai（兼容实现）
func (f *ModelFactory) Build(spec ModelSpec) (model.Client, error) {
	if spec.Provider == "" {
		return nil, invalidConfig("provider is required")
	}
	if spec.Model == "" {
		return nil, invalidConfig("model is required")
	}
	if spec.Protocol == "" {
		return nil, invalidConfig("protocol is required")
	}
	want, ok := f.registry.ProtocolFor(spec.Provider)
	if !ok {
		return nil, invalidConfig(fmt.Sprintf("unknown provider %q", spec.Provider))
	}
	if want != spec.Protocol {
		return nil, invalidConfig(fmt.Sprintf("protocol %q does not match provider %q (want %q)", spec.Protocol, spec.Provider, want))
	}
	if spec.BaseURL == "" {
		return nil, invalidConfig("base_url is required")
	}
	policy, err := ResolveReasoning(spec)
	if err != nil {
		return nil, err
	}

	apiKey := ""
	if spec.APIKeyRef != "" {
		if f.creds == nil {
			return nil, invalidConfig("credentials source is not configured")
		}
		key, err := f.creds.Key(spec.APIKeyRef)
		if err != nil {
			return nil, invalidConfig(fmt.Sprintf("resolve api key ref %q: %v", spec.APIKeyRef, err))
		}
		apiKey = key
	}

	// 切换供应商天然不复用 Key：每个配置独立构造客户端，Key 在构造时绑定。
	var client model.Client
	switch spec.Protocol {
	case ProtocolDeepSeek:
		client, err = deepseek.NewAdapter(spec.BaseURL, apiKey)
	case ProtocolOpenAICompatible:
		client, err = openai.NewAdapter(spec.Provider, spec.BaseURL, apiKey)
	default:
		return nil, invalidConfig(fmt.Sprintf("unknown protocol %q", spec.Protocol))
	}
	if err != nil {
		return nil, err
	}
	return reasoningClient{Client: client, effort: policy.Effective}, nil
}

func invalidConfig(summary string) error {
	return model.NewError(model.ErrInvalidConfig).WithSummary(summary)
}
