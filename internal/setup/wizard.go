// Package setup 实现首次设置向导的流程。它只依赖 Prompter 接口，不依赖任何终端库，
// 因此可以在没有 TTY 的测试里跑完整流程（见 wizard_test.go 的脚本化 Prompter）。
package setup

import (
	"errors"
	"fmt"
	"strings"

	"plume-agent/internal/channel"
	"plume-agent/internal/config"
	"plume-agent/internal/provider"
	"plume-agent/internal/telemetry"
)

// ErrCancelled 表示用户主动取消（Ctrl+C 或选择退出）。
var ErrCancelled = errors.New("setup cancelled by user")

// ErrChannelSkipped 表示模型配置已保存，但渠道步骤被取消。
// 按阶段计划，这是允许的中间状态：可以只配好模型先做本地调试。
var ErrChannelSkipped = errors.New("channel setup skipped, model configuration was kept")

// Result 是一次设置的结果。
type Result struct {
	Config        *config.Config
	ModelID       string
	ChannelID     string // 渠道被跳过时为空
	CredentialRef string // 本次新写入的凭据引用；沿用旧凭据时为空
}

// Prompter 抽象向导所需的交互。真实实现基于 huh，见 cmd/plume/prompter.go。
type Prompter interface {
	// SelectProvider 让用户从预设中选一个供应商。
	SelectProvider(presets []provider.Preset, initial string) (provider.Preset, error)
	// InputBaseURL 让用户输入 Base URL。只对没有预填默认值的预设调用。
	InputBaseURL(initial string, required bool) (string, error)
	// SelectModel 让用户在预设的模型列表里选一个；列表末尾应提供"自定义…"，
	// 选中后由实现自行追问文本输入并返回最终模型 ID。列表为空时直接文本输入。
	SelectModel(models []string, initial string) (string, error)
	// InputSecret 隐藏输入一个密钥。hasExisting 为真表示已有可用凭据，
	// 此时返回 keepExisting=true 表示沿用旧值、不改动。
	InputSecret(label string, hasExisting bool) (secret string, keepExisting bool, err error)
	// ConfirmYesNo 询问一个是非问题。
	ConfirmYesNo(label string, initial bool) (bool, error)
	// SelectChannelOrSkip 让用户选择渠道；返回 skip=true 表示用户显式
	// 选择"暂不接入渠道"（区别于 Esc 取消）。列表包含禁用中的占位项。
	SelectChannelOrSkip(types []channel.Type, initial string) (channel.Type, bool, error)
	// Note 展示一段只读信息。
	Note(title, body string) error
}

// Wizard 串起向导流程。
type Wizard struct {
	providers *provider.Registry
	channels  *channel.Registry
	prompter  Prompter
	trace     *telemetry.SetupRecorder
}

// New 构造一个向导。
func New(providers *provider.Registry, channels *channel.Registry, prompter Prompter, trace *telemetry.SetupRecorder) *Wizard {
	return &Wizard{providers: providers, channels: channels, prompter: prompter, trace: trace}
}

// Run 执行向导。分两阶段落盘：先模型（含凭据），后渠道。
//
// 返回约定：
//   - 模型阶段失败或被取消：Result 为 nil，**磁盘上什么都没有写**。
//   - 渠道阶段被取消：Result 非 nil（Config 里只有模型），错误为 ErrChannelSkipped。
//     阶段计划要求"渠道配置失败不撤销已保存的模型配置"。
func (w *Wizard) Run(existing *config.Config) (*Result, error) {
	w.trace.Start()

	res, err := w.runModel(existing)
	if err != nil {
		if errors.Is(err, ErrCancelled) {
			w.trace.Finish(telemetry.EventSetupCancelled, nil)
		} else {
			w.trace.Finish(telemetry.EventSetupFailed, err)
		}
		return nil, err
	}

	if err := w.runChannel(res); err != nil {
		if errors.Is(err, ErrCancelled) {
			w.trace.Finish(telemetry.EventSetupCancelled, nil)
			return res, ErrChannelSkipped
		}
		w.trace.Finish(telemetry.EventSetupFailed, err)
		return res, err
	}

	w.trace.Finish(telemetry.EventSetupCompleted, nil)
	return res, nil
}

// runModel 收集模型配置，写凭据并原子保存配置。
func (w *Wizard) runModel(existing *config.Config) (*Result, error) {
	prev := previousDefaultModel(existing)

	// 1) 供应商
	var preset provider.Preset
	{
		done := w.trace.Step("select_provider")
		initial := ""
		if prev != nil {
			initial = prev.Provider
		}
		presets := w.providers.All()
		if len(presets) == 0 {
			err := errors.New("no provider preset is available")
			done(err)
			return nil, err
		}
		var err error
		preset, err = w.prompter.SelectProvider(presets, initial)
		done(err)
		if err != nil {
			return nil, err
		}
		w.trace.Event("provider_selected", telemetry.Field("provider", preset.ID), telemetry.Field("protocol", preset.Protocol))
	}

	// 2) Base URL —— 有预填默认值就跳过整步。
	//
	// 例外：已有配置里该供应商存的地址与预设默认值不同（例如用户手改成了代理），
	// 此时保留已有值。否则重进向导会静默把请求目的地改回默认，那是不能接受的。
	baseURL := preset.DefaultBaseURL
	if prev != nil && prev.Provider == preset.ID && prev.BaseURL != "" {
		baseURL = prev.BaseURL
	}
	{
		done := w.trace.Step("resolve_base_url")
		source := ""
		switch {
		case preset.DefaultBaseURL != "" && baseURL == preset.DefaultBaseURL:
			source = "preset_default"
		case baseURL != "":
			// 已有配置里的地址不是默认值，保留它，不询问也不覆盖。
			source = "existing_config"
		default:
			var err error
			baseURL, err = w.prompter.InputBaseURL("", true)
			if err != nil {
				done(err)
				return nil, err
			}
			source = "user_input"
		}
		done(nil)
		w.trace.Event("base_url_resolved",
			telemetry.Field("provider", preset.ID),
			telemetry.Field("source", source))
	}

	// 3) 模型 ID —— 从预设列表里选，或选"自定义…"自行输入
	var modelName string
	{
		done := w.trace.Step("select_model")
		initial := ""
		if prev != nil && prev.Provider == preset.ID {
			initial = prev.Model
		}
		var err error
		modelName, err = w.prompter.SelectModel(preset.Models, initial)
		done(err)
		if err != nil {
			return nil, err
		}
	}

	// 4) API Key —— 按模型配置隔离，不复用别家供应商的 Key
	modelID := modelConfigID(preset.ID)
	keyRef := modelID
	if prev != nil && prev.Provider == preset.ID && prev.APIKeyRef != "" {
		keyRef = prev.APIKeyRef
	}

	var secret []byte
	needsKey := true
	{
		done := w.trace.Step("input_credential")

		hasExisting := false
		if ok, err := config.CredentialExists(keyRef); err == nil && ok {
			hasExisting = true
		}

		if preset.OptionalAPIKey {
			// 自定义兼容服务可能是本机无鉴权服务，允许显式声明不需要 Key。
			var err error
			needsKey, err = w.prompter.ConfirmYesNo("该服务需要 API Key 吗？", true)
			if err != nil {
				done(err)
				return nil, err
			}
		}

		if needsKey {
			value, keepExisting, err := w.prompter.InputSecret("API Key", hasExisting)
			if err != nil {
				done(err)
				return nil, err
			}
			if !keepExisting && strings.TrimSpace(value) != "" {
				secret = []byte(strings.TrimSpace(value))
			}
		} else {
			keyRef = ""
		}
		done(nil)
	}

	// 5) 组装并本地校验
	model := config.ModelConfig{
		ID:        modelID,
		Provider:  preset.ID,
		Protocol:  preset.Protocol,
		BaseURL:   baseURL,
		Model:     modelName,
		APIKeyRef: keyRef,
	}
	// 复用同一供应商配置时保留用户的显式偏好；向导不增加思考强度问题。
	if prev != nil && prev.Provider == preset.ID && prev.ReasoningEffort != nil {
		effort := *prev.ReasoningEffort
		model.ReasoningEffort = &effort
	}
	// 展示容量只适用于相同端点与模型，不在换模型时继承旧容量。
	if prev != nil && prev.Provider == preset.ID && prev.Protocol == preset.Protocol && prev.BaseURL == baseURL && prev.Model == modelName && prev.ContextWindowTokens != nil {
		tokens := *prev.ContextWindowTokens
		model.ContextWindowTokens = &tokens
	}
	cfg := &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  model.ID,
		Models:        []config.ModelConfig{model},
	}
	if existing != nil && existing.TUI != nil {
		cfg.TUI = &config.TUIConfig{StatusMessages: make(map[string][]string)}
		for phase, messages := range existing.TUI.StatusMessages {
			cfg.TUI.StatusMessages[phase] = append([]string(nil), messages...)
		}
		if existing.TUI.StatusLine != nil {
			line := *existing.TUI.StatusLine
			line.Items = append([]config.StatusItemConfig{}, line.Items...)
			cfg.TUI.StatusLine = &line
		}
	}
	// 保留已有渠道，但把它们重新指向新的默认模型。
	for _, ch := range existingChannels(existing) {
		ch.ModelRef = model.ID
		cfg.Channels = append(cfg.Channels, ch)
	}

	{
		done := w.trace.Step("validate")
		err := cfg.Validate(w.providers, w.channels)
		if err == nil {
			_, err = provider.ResolveReasoning(provider.ModelSpec{Provider: model.Provider, Protocol: model.Protocol, BaseURL: model.BaseURL, Model: model.Model, ReasoningEffort: model.ReasoningEffort})
		}
		done(err)
		if err != nil {
			return nil, err
		}
	}

	// 6) 先写凭据，再写配置。凭据在前：配置指向缺失的凭据比多一个孤儿文件更糟。
	newCredential := len(secret) > 0
	if newCredential {
		done := w.trace.Step("store_credential")
		err := config.SetCredential(keyRef, secret)
		done(err)
		w.trace.CredentialStored(keyRef, err)
		if err != nil {
			return nil, err
		}
	}

	{
		done := w.trace.Step("save_config")
		err := config.Save(cfg)
		done(err)
		path, _ := config.Path()
		w.trace.Saved(path, len(cfg.Models), len(cfg.Channels), err)
		if err != nil {
			// 配置没写成功就不要留下孤儿凭据。
			if newCredential {
				_ = config.DeleteCredential(keyRef)
			}
			return nil, err
		}
	}

	credentialRef := ""
	if newCredential {
		credentialRef = keyRef
	}
	return &Result{
		Config:        cfg,
		ModelID:       model.ID,
		CredentialRef: credentialRef,
	}, nil
}

// runChannel 收集渠道选择并保存。"暂不接入渠道"是显式选择：只保存模型，
// 不写渠道、不伪造渠道账号，也不把 TUI 伪装成渠道。
func (w *Wizard) runChannel(res *Result) error {
	done := w.trace.Step("select_channel")
	initial := ""
	if len(res.Config.Channels) > 0 {
		initial = res.Config.Channels[0].Type
	}
	chType, skip, err := w.prompter.SelectChannelOrSkip(w.channels.All(), initial)
	if err != nil {
		done(err)
		return err
	}
	if skip {
		done(nil)
		w.trace.Event("channel_skipped_explicitly", telemetry.Field("reason", "user chose to skip channels"))
		return w.prompter.Note("暂不接入渠道", "已保存模型配置。你可以随时重新运行 plume setup 配置渠道；TUI 聊天现在即可使用。")
	}
	if !chType.Enabled {
		err := fmt.Errorf("channel %q is not available yet", chType.ID)
		done(err)
		return err
	}
	w.trace.Event("channel_selected", telemetry.Field("channel_type", chType.ID))

	ch := config.ChannelConfig{
		ID:       chType.ID + "-1",
		Type:     chType.ID,
		Enabled:  true,
		ModelRef: res.ModelID,
	}
	// 首版只保持一个活动渠道。
	res.Config.Channels = []config.ChannelConfig{ch}

	done = w.trace.Step("save_channel")
	err = res.Config.Validate(w.providers, w.channels)
	if err == nil {
		err = config.Save(res.Config)
	}
	done(err)
	path, _ := config.Path()
	w.trace.Saved(path, len(res.Config.Models), len(res.Config.Channels), err)
	if err != nil {
		return err
	}
	res.ChannelID = ch.ID

	// 首版：微信只记录渠道配置，真实扫码登录在 G2a.1 完成。
	if chType.ID == "weixin" {
		return w.prompter.Note("微信", "已记录微信渠道配置。真实扫码登录将在 G2a.1 实现，当前状态为「待登录」。")
	}
	return nil
}

// previousDefaultModel 返回已有配置里的默认模型，用于回填。
func previousDefaultModel(existing *config.Config) *config.ModelConfig {
	if existing == nil || existing.DefaultModel == "" {
		return nil
	}
	for i := range existing.Models {
		if existing.Models[i].ID == existing.DefaultModel {
			return &existing.Models[i]
		}
	}
	return nil
}

func existingChannels(existing *config.Config) []config.ChannelConfig {
	if existing == nil {
		return nil
	}
	out := make([]config.ChannelConfig, len(existing.Channels))
	copy(out, existing.Channels)
	return out
}

func modelConfigID(providerID string) string {
	return providerID + "-default"
}
