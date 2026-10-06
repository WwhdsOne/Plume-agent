// Package provider 保存设置向导提供的静态模型供应商预设。它刻意不含任何 Eino 或
// HTTP 代码：把预设转成 Eino 组件的模型工厂在 G1b 落地；保持本包无依赖，配置校验
// 才能廉价地使用它。
//
// 预设清单在 docs/decisions/0001-scope.md §1 冻结：DeepSeek 与一个通用的 OpenAI 兼容
// 服务。百炼/Qwen 及原生协议供应商延后。
package provider

// Preset 是一个模型供应商选项。Protocol 是持久化进配置的 Eino 适配器 ID，
// 见 ModelConfig.Protocol；Component 是模型工厂从 G1b 起要用的 eino-ext 导入路径。
type Preset struct {
	ID             string
	DisplayName    string
	Protocol       string
	Component      string
	DefaultBaseURL string // 为空表示必须由用户提供
	// Models 是向导建议的模型 ID 列表。向导会把它们做成可选项，并在末尾附上"自定义…"；
	// 为空表示没有建议列表，直接让用户输入。
	Models []string
	// OptionalAPIKey 为真表示该预设允许用户显式声明"不需要 API Key"
	// （例如本机无鉴权服务）。默认要求 API Key。
	OptionalAPIKey bool
}

// Registry 是静态、有序的预设集合。注册新预设绝不能要求改动向导的分发逻辑。
type Registry struct {
	presets map[string]Preset
	order   []string
}

// NewRegistry 返回填入了首版预设的注册表。
func NewRegistry() *Registry {
	r := &Registry{presets: map[string]Preset{}}
	r.Register(Preset{
		ID:             "deepseek",
		DisplayName:    "DeepSeek",
		Protocol:       "deepseek",
		Component:      "github.com/cloudwego/eino-ext/components/model/deepseek",
		DefaultBaseURL: "https://api.deepseek.com",
		// 来源：https://api-docs.deepseek.com/ （2026-10-06 复核）。
		// 只是建议项，不是协议常量；带 reasoning 能力的模型名与旧名以官方文档为准。
		Models: []string{"deepseek-flash", "deepseek-v4-pro"},
	})
	r.Register(Preset{
		ID:             "custom-openai",
		DisplayName:    "自定义兼容服务",
		Protocol:       "openai-compatible",
		Component:      "github.com/cloudwego/eino-ext/components/model/openai",
		DefaultBaseURL: "",
		OptionalAPIKey: true,
	})
	return r
}

// Register 新增或替换一个预设。替换会保留原来的位置，让渲染注册表的调用方看到稳定的顺序。
func (r *Registry) Register(p Preset) {
	if _, exists := r.presets[p.ID]; !exists {
		r.order = append(r.order, p.ID)
	}
	r.presets[p.ID] = p
}

// All 按注册顺序返回全部预设。
func (r *Registry) All() []Preset {
	out := make([]Preset, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.presets[id])
	}
	return out
}

// Lookup 返回指定 ID 的预设。
func (r *Registry) Lookup(id string) (Preset, bool) {
	p, ok := r.presets[id]
	return p, ok
}

// ProtocolFor 返回预设对应的 Eino 适配器 ID，满足 config.ProviderCatalog。
func (r *Registry) ProtocolFor(id string) (string, bool) {
	p, ok := r.presets[id]
	if !ok {
		return "", false
	}
	return p.Protocol, true
}

// DefaultBaseURL 返回预设预填的 Base URL；须由用户提供时返回 ""。
// 它满足 config.ProviderCatalog。
func (r *Registry) DefaultBaseURL(id string) string {
	return r.presets[id].DefaultBaseURL
}
