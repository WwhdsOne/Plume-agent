package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plume-agent/internal/channel"
	"plume-agent/internal/config"
	"plume-agent/internal/provider"
	"plume-agent/internal/telemetry"
)

// fakePrompter 是脚本化的 Prompter：答案预先给定，同时记录向导传进来的初始值，
// 用来断言"回填"行为。
type fakePrompter struct {
	provider    provider.Preset
	baseURL     string
	model       string
	secret      string
	keep        bool
	needsKey    bool
	chType      channel.Type
	skipChannel bool

	failAt string // 在这些步骤注入错误：provider/base_url/model/credential/channel

	gotProviderInitial string
	gotBaseURLCalled   bool
	gotBaseURLInitial  string
	gotBaseURLRequired bool
	gotModels          []string
	gotModelInitial    string
	gotSecretHasExist  bool
	notes              []string
}

func (f *fakePrompter) fail(step string) error {
	if f.failAt == step {
		return ErrCancelled
	}
	return nil
}

func (f *fakePrompter) SelectProvider(_ []provider.Preset, initial string) (provider.Preset, error) {
	f.gotProviderInitial = initial
	if err := f.fail("provider"); err != nil {
		return provider.Preset{}, err
	}
	return f.provider, nil
}

func (f *fakePrompter) InputBaseURL(initial string, required bool) (string, error) {
	f.gotBaseURLCalled = true
	f.gotBaseURLInitial, f.gotBaseURLRequired = initial, required
	if err := f.fail("base_url"); err != nil {
		return "", err
	}
	if strings.TrimSpace(f.baseURL) != "" {
		return f.baseURL, nil
	}
	return initial, nil
}

func (f *fakePrompter) SelectModel(models []string, initial string) (string, error) {
	f.gotModels, f.gotModelInitial = models, initial
	if err := f.fail("model"); err != nil {
		return "", err
	}
	// 空字符串表示"用户直接回车接受预选值"。
	if strings.TrimSpace(f.model) != "" {
		return f.model, nil
	}
	return initial, nil
}

func (f *fakePrompter) InputSecret(_ string, hasExisting bool) (string, bool, error) {
	f.gotSecretHasExist = hasExisting
	if err := f.fail("credential"); err != nil {
		return "", false, err
	}
	return f.secret, f.keep, nil
}

func (f *fakePrompter) ConfirmYesNo(_ string, _ bool) (bool, error) { return f.needsKey, nil }

func (f *fakePrompter) SelectChannelOrSkip(_ []channel.Type, _ string) (channel.Type, bool, error) {
	if err := f.fail("channel"); err != nil {
		return channel.Type{}, false, err
	}
	if f.skipChannel {
		return channel.Type{}, true, nil
	}
	return f.chType, false, nil
}

func (f *fakePrompter) Note(title, _ string) error {
	f.notes = append(f.notes, title)
	return nil
}

// harness 搭好一个隔离的 PLUME_HOME、注册表与 trace 缓冲。
type harness struct {
	fake  *fakePrompter
	trace bytes.Buffer
	wiz   *Wizard
}

func newHarness(t *testing.T, fake *fakePrompter) *harness {
	t.Helper()
	t.Setenv("PLUME_HOME", t.TempDir())
	h := &harness{fake: fake}
	rec := telemetry.NewSetupRecorder(&h.trace)
	h.wiz = New(provider.NewRegistry(), channel.NewRegistry(), fake, rec)
	return h
}

func (h *harness) traceText() string { return h.trace.String() }

func defaultFake() *fakePrompter {
	presets := provider.NewRegistry()
	deepseek, _ := presets.Lookup("deepseek")
	channels := channel.NewRegistry()
	weixin, _ := channels.Lookup("weixin")
	return &fakePrompter{
		provider: deepseek,
		model:    "deepseek-flash",
		secret:   "sk-test-SENTINEL-DO-NOT-LEAK",
		needsKey: true,
		chType:   weixin,
	}
}

// TestWizardPreservesStatusLineAndSameModelCapacity 守住向导不重置状态栏自定义（含显式空 items），
// 且容量口径正确：同模型保留已有 context_window_tokens，换模型才写入预设容量。
func TestWizardPreservesStatusLineAndSameModelCapacity(t *testing.T) {
	for _, changedModel := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_model", true: "new_model"}[changedModel], func(t *testing.T) {
			fake := defaultFake()
			fake.skipChannel = true
			if changedModel {
				fake.model = "deepseek-v4-pro"
			}
			h := newHarness(t, fake)
			var existing config.Config
			raw := `{"schema_version":1,"default_model":"deepseek-default","models":[{"id":"deepseek-default","provider":"deepseek","protocol":"deepseek","base_url":"https://api.deepseek.com","model":"deepseek-flash","context_window_tokens":128000}],"tui":{"status_line":{"enabled":false,"items":[]}}}`
			if err := json.Unmarshal([]byte(raw), &existing); err != nil {
				t.Fatal(err)
			}
			res, err := h.wiz.Run(&existing)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(res.Config)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Models []map[string]any `json:"models"`
				TUI    struct {
					StatusLine map[string]any `json:"status_line"`
				} `json:"tui"`
			}
			if err := json.Unmarshal(encoded, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.TUI.StatusLine["enabled"] != false {
				t.Fatal("setup lost status line customization")
			}
			if items, ok := saved.TUI.StatusLine["items"].([]any); !ok || len(items) != 0 {
				t.Fatal("setup restored explicitly empty status items")
			}
			capacity, present := saved.Models[0]["context_window_tokens"]
			if !present || (!changedModel && capacity != float64(128000)) || (changedModel && capacity != float64(1000000)) {
				t.Fatalf("capacity = %v, changedModel = %v", capacity, changedModel)
			}
		})
	}
}

// TestWizardWritesContextWindowDefaults 守住容量默认值落盘：预设模型写入预设容量，
// 未验证的模型 ID 不伪造容量（保持缺省）。
func TestWizardWritesContextWindowDefaults(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "deepseek-v4-pro", "unknown"} {
		t.Run(model, func(t *testing.T) {
			fake := defaultFake()
			fake.model = model
			fake.skipChannel = true
			h := newHarness(t, fake)
			res, err := h.wiz.Run(nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(mustPath(t))
			if err != nil {
				t.Fatal(err)
			}
			var saved config.Config
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			for _, cfg := range []*config.Config{res.Config, &saved} {
				capacity := cfg.Models[0].ContextWindowTokens
				if model == "unknown" {
					if capacity != nil {
						t.Fatalf("unverified model capacity = %d", *capacity)
					}
				} else if capacity == nil || *capacity != 1000000 {
					t.Fatalf("preset capacity = %v, want 1000000", capacity)
				}
			}
		})
	}
}

// TestWizardHappyPath 端到端走通完整向导：模型与渠道正确落盘、凭据只写进 credentials/，
// 且配置文件与 setup trace 都不泄漏密钥值。
func TestWizardHappyPath(t *testing.T) {
	fake := defaultFake()
	h := newHarness(t, fake)

	res, err := h.wiz.Run(nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.ModelID != "deepseek-default" {
		t.Errorf("ModelID = %q", res.ModelID)
	}
	if res.ChannelID != "weixin-1" {
		t.Errorf("ChannelID = %q", res.ChannelID)
	}

	// deepseek 有预填默认地址，向导应当直接跳过 Base URL 这一步。
	if fake.gotBaseURLCalled {
		t.Error("有预填默认地址时不应询问 Base URL")
	}
	// 模型列表来自预设，供选择而不是让用户凭空填。
	if len(fake.gotModels) == 0 || fake.gotModels[0] != "deepseek-flash" {
		t.Errorf("SelectModel models = %v, want the preset list", fake.gotModels)
	}

	saved, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if saved.DefaultModel != "deepseek-default" {
		t.Errorf("default_model = %q", saved.DefaultModel)
	}
	if got := saved.Models[0].BaseURL; got != "https://api.deepseek.com" {
		t.Errorf("saved base_url = %q, want the resolved URL", got)
	}
	if saved.Models[0].APIKeyRef != "deepseek-default" {
		t.Errorf("api_key_ref = %q", saved.Models[0].APIKeyRef)
	}
	if len(saved.Channels) != 1 || !saved.Channels[0].Enabled || saved.Channels[0].Type != "weixin" {
		t.Errorf("channels = %+v", saved.Channels)
	}

	// 凭据写进 credentials/，且配置里不含密钥值。
	rawSecret, err := config.GetCredential("deepseek-default")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if string(rawSecret) != fake.secret {
		t.Errorf("stored credential = %q", rawSecret)
	}
	rawConfig, err := os.ReadFile(mustPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawConfig, []byte(fake.secret)) {
		t.Fatal("config.json leaked the credential value")
	}
	if strings.Contains(h.traceText(), fake.secret) {
		t.Fatal("setup trace leaked the credential value")
	}
}

// TestWizardCancelledDuringModelWritesNothing 守住模型配置完成前的取消是全有或全无：
// 任一步取消都不写配置、不写凭据，并在 trace 记录取消事件。
func TestWizardCancelledDuringModelWritesNothing(t *testing.T) {
	cases := []struct {
		step     string
		provider string
	}{
		{"provider", "deepseek"},
		// 只有没有预填地址的预设才会走 Base URL 这一步。
		{"base_url", "custom-openai"},
		{"model", "deepseek"},
		{"credential", "deepseek"},
	}
	for _, tc := range cases {
		t.Run(tc.step, func(t *testing.T) {
			fake := defaultFake()
			if tc.provider != "deepseek" {
				presets := provider.NewRegistry()
				preset, _ := presets.Lookup(tc.provider)
				fake.provider = preset
				fake.model = "custom-model"
			}
			fake.failAt = tc.step
			h := newHarness(t, fake)

			res, err := h.wiz.Run(nil)
			if !errors.Is(err, ErrCancelled) {
				t.Fatalf("Run() error = %v, want ErrCancelled", err)
			}
			if res != nil {
				t.Errorf("Result = %+v, want nil", res)
			}
			if _, err := config.Load(); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("config was written during a cancelled model step (err = %v)", err)
			}
			if exists, _ := config.CredentialExists("deepseek-default"); exists {
				t.Error("credential was written during a cancelled model step")
			}
			if !strings.Contains(h.traceText(), telemetry.EventSetupCancelled) {
				t.Errorf("trace does not record the cancel:\n%s", h.traceText())
			}
		})
	}
}

// TestWizardCancelledAtChannelKeepsModelConfig 守住渠道阶段取消只撤销渠道：
// 返回 ErrChannelSkipped，已保存的模型配置不回滚。
func TestWizardCancelledAtChannelKeepsModelConfig(t *testing.T) {
	fake := defaultFake()
	fake.failAt = "channel"
	h := newHarness(t, fake)

	res, err := h.wiz.Run(nil)
	if !errors.Is(err, ErrChannelSkipped) {
		t.Fatalf("Run() error = %v, want ErrChannelSkipped", err)
	}
	if res == nil || res.ModelID == "" {
		t.Fatalf("Result = %+v, want the saved model", res)
	}
	if res.ChannelID != "" {
		t.Errorf("ChannelID = %q, want empty", res.ChannelID)
	}

	saved, err := config.Load()
	if err != nil {
		t.Fatalf("阶段计划要求渠道取消不撤销模型配置，Load() error = %v", err)
	}
	if len(saved.Models) != 1 || len(saved.Channels) != 0 {
		t.Errorf("saved = %+v, want the model and no channel", saved)
	}
}

// TestWizardPrefillsFromExistingConfig 守住重进向导的回填契约：供应商/模型用已有值预选、
// 已有凭据可保留不重输，且 TUI 展示与 reasoning_effort 设置不被丢弃。
func TestWizardPrefillsFromExistingConfig(t *testing.T) {
	fake := defaultFake()
	h := newHarness(t, fake)

	existing := &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  "deepseek-default",
		TUI:           &config.TUIConfig{StatusMessages: map[string][]string{"waiting": {"brewing"}}},
		Models: []config.ModelConfig{{
			ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek",
			BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro",
			APIKeyRef: "deepseek-default",
		}},
	}
	effort := "none"
	existing.Models[0].ReasoningEffort = &effort
	// 先放一份已有凭据，模拟"重进向导不该要求重新输入 Key"。
	if err := config.SetCredential("deepseek-default", []byte("sk-existing")); err != nil {
		t.Fatal(err)
	}
	fake.secret, fake.keep = "", true // 用户直接回车 = 保留原 Key
	fake.baseURL, fake.model = "", ""

	res, err := h.wiz.Run(existing)
	if err == nil && (res.Config.TUI == nil || res.Config.Models[0].ReasoningEffort == nil || *res.Config.Models[0].ReasoningEffort != "none") {
		t.Fatal("setup discarded display or reasoning settings")
	}
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if fake.gotProviderInitial != "deepseek" {
		t.Errorf("provider initial = %q, want the existing provider", fake.gotProviderInitial)
	}
	if fake.gotBaseURLCalled {
		t.Error("有默认地址时不应追问 Base URL")
	}
	if fake.gotModelInitial != "deepseek-v4-pro" {
		t.Errorf("model initial = %q, want the existing model", fake.gotModelInitial)
	}
	if !fake.gotSecretHasExist {
		t.Error("hasExisting should be true when a credential is already stored")
	}
	if res.CredentialRef != "" {
		t.Errorf("CredentialRef = %q, want empty (nothing new was written)", res.CredentialRef)
	}
	// 原凭据必须原封不动。
	got, err := config.GetCredential("deepseek-default")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sk-existing" {
		t.Errorf("credential = %q, want the untouched existing value", got)
	}
}

// TestWizardCustomOpenAICanDeclineAPIKey 守住 custom-openai 可不配 Key（如本地推理服务）：
// 用户拒绝输入时 api_key_ref 留空也能完成保存。
func TestWizardCustomOpenAICanDeclineAPIKey(t *testing.T) {
	fake := defaultFake()
	presets := provider.NewRegistry()
	custom, _ := presets.Lookup("custom-openai")
	fake.provider = custom
	fake.baseURL = "http://127.0.0.1:11434/v1"
	fake.model = "llama3"
	fake.needsKey = false
	h := newHarness(t, fake)

	res, err := h.wiz.Run(nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Models[0].APIKeyRef != "" {
		t.Errorf("api_key_ref = %q, want empty when the user declines a key", saved.Models[0].APIKeyRef)
	}
	if saved.Models[0].BaseURL != "http://127.0.0.1:11434/v1" {
		t.Errorf("base_url = %q", saved.Models[0].BaseURL)
	}
	if res.CredentialRef != "" {
		t.Errorf("CredentialRef = %q, want empty", res.CredentialRef)
	}
}

// TestWizardCustomOpenAIRequiresBaseURL 守住 custom-openai 必填 Base URL：
// 地址为空时校验拒绝，且不落盘任何配置。
func TestWizardCustomOpenAIRequiresBaseURL(t *testing.T) {
	fake := defaultFake()
	presets := provider.NewRegistry()
	custom, _ := presets.Lookup("custom-openai")
	fake.provider = custom
	fake.model = "llama3"
	fake.needsKey = false
	h := newHarness(t, fake)

	// fake 在 baseURL 为空时回填 initial，也就是空串 → 校验应当拒绝。
	if _, err := h.wiz.Run(nil); err == nil {
		t.Fatal("Run() = nil, want a validation error for a missing base_url")
	}
	if _, err := config.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Error("an invalid configuration must not be written")
	}
}

// TestWizardRejectsUnavailableChannel 守住未实现渠道必须显式报错，
// 不默默接受一个当前不可用的渠道配置。
func TestWizardRejectsUnavailableChannel(t *testing.T) {
	fake := defaultFake()
	channels := channel.NewRegistry()
	feishu, _ := channels.Lookup("feishu")
	fake.chType = feishu
	h := newHarness(t, fake)

	if _, err := h.wiz.Run(nil); err == nil || !strings.Contains(err.Error(), "not available yet") {
		t.Fatalf("Run() error = %v, want a not-available error", err)
	}
}

// TestWizardSaveFailureRemovesNewCredential 守住"配置保存失败不留孤儿凭据"：
// 写盘失败时本次新写的凭据必须被清理。
func TestWizardSaveFailureRemovesNewCredential(t *testing.T) {
	fake := defaultFake()
	h := newHarness(t, fake)

	// 让 config.json 成为一个非空目录，rename 必然失败。
	path := mustPath(t)
	if err := os.MkdirAll(filepath.Join(path, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := h.wiz.Run(nil); err == nil {
		t.Fatal("Run() = nil, want a save error")
	}
	if exists, _ := config.CredentialExists("deepseek-default"); exists {
		t.Error("配置写入失败后不应留下孤儿凭据")
	}
}

// TestWizardWeixinNoteIsShown 守住选择微信渠道时展示且仅展示一条"微信"说明
// （登录属后续阶段，向导只记录待登录状态）。
func TestWizardWeixinNoteIsShown(t *testing.T) {
	fake := defaultFake()
	h := newHarness(t, fake)
	if _, err := h.wiz.Run(nil); err != nil {
		t.Fatal(err)
	}
	if len(fake.notes) != 1 || fake.notes[0] != "微信" {
		t.Errorf("notes = %v, want a single 微信 note", fake.notes)
	}
}

// TestWizardSwitchingProviderDoesNotReusePreviousKey 守住凭据按供应商隔离：
// 换供应商后不把上一家的 Key 当作已有凭据，新引用按新供应商命名，旧凭据原样保留。
func TestWizardSwitchingProviderDoesNotReusePreviousKey(t *testing.T) {
	fake := defaultFake()
	presets := provider.NewRegistry()
	custom, _ := presets.Lookup("custom-openai")
	fake.provider = custom
	fake.baseURL = "https://proxy.example.com/v1"
	fake.model = "gpt-4o-mini"
	fake.needsKey = true
	fake.secret = "sk-test-NEW-PROVIDER"
	h := newHarness(t, fake)

	// 已有 DeepSeek 的凭据；切换供应商后绝不能用它。
	if err := config.SetCredential("deepseek-default", []byte("sk-test-OLD")); err != nil {
		t.Fatal(err)
	}
	existing := &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  "deepseek-default",
		Models: []config.ModelConfig{{
			ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek",
			BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
			APIKeyRef: "deepseek-default",
		}},
	}

	res, err := h.wiz.Run(existing)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if fake.gotSecretHasExist {
		t.Error("换供应商后不应把上一家的凭据当作已有可用凭据")
	}
	if res.CredentialRef != "custom-openai-default" {
		t.Errorf("CredentialRef = %q, want the new provider's ref", res.CredentialRef)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Models[0].APIKeyRef != "custom-openai-default" {
		t.Errorf("api_key_ref = %q, want a ref namespaced to the new provider", saved.Models[0].APIKeyRef)
	}
	// 旧凭据必须原封不动，且不能在新模型目录下被复用。
	old, err := config.GetCredential("deepseek-default")
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != "sk-test-OLD" {
		t.Errorf("previous provider credential = %q, want it untouched", old)
	}
}

// TestWizardKeepsExistingChannelAndRepointsModelRef 守住重跑向导保留已有渠道，
// 并把渠道 model_ref 重新指向本次保存的模型。
func TestWizardKeepsExistingChannelAndRepointsModelRef(t *testing.T) {
	fake := defaultFake()
	h := newHarness(t, fake)

	existing := &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  "deepseek-default",
		Models: []config.ModelConfig{{
			ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek",
			BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
			APIKeyRef: "deepseek-default",
		}},
		Channels: []config.ChannelConfig{{
			ID: "weixin-1", Type: "weixin", Enabled: true, ModelRef: "deepseek-default",
		}},
	}

	res, err := h.wiz.Run(existing)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Channels) != 1 {
		t.Fatalf("channels = %+v, want the existing one preserved", saved.Channels)
	}
	if saved.Channels[0].ModelRef != res.ModelID {
		t.Errorf("channel model_ref = %q, want it re-pointed to %q", saved.Channels[0].ModelRef, res.ModelID)
	}
}

// TestWizardPreservesExistingNonDefaultBaseURL 守住"跳过 Base URL"不能变成"静默重置"：
// 已有配置里存的是代理地址时，重进向导必须原样保留。
func TestWizardPreservesExistingNonDefaultBaseURL(t *testing.T) {
	fake := defaultFake()
	h := newHarness(t, fake)

	if err := config.SetCredential("deepseek-default", []byte("sk-existing")); err != nil {
		t.Fatal(err)
	}
	fake.secret, fake.keep = "", true

	existing := &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  "deepseek-default",
		Models: []config.ModelConfig{{
			ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek",
			BaseURL: "https://proxy.internal/v1", Model: "deepseek-flash",
			APIKeyRef: "deepseek-default",
		}},
	}
	if _, err := h.wiz.Run(existing); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if fake.gotBaseURLCalled {
		t.Error("有默认地址时不应追问 Base URL")
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Models[0].BaseURL != "https://proxy.internal/v1" {
		t.Errorf("base_url = %q, want the existing custom URL preserved", saved.Models[0].BaseURL)
	}
}

func mustPath(t *testing.T) string {
	t.Helper()
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestWizardSkipsChannelExplicitly 守住显式跳过渠道的完整契约：模型照常保存、渠道不写入、
// 不计为错误，trace 记 channel_skipped_explicitly 以便与取消区分。
func TestWizardSkipsChannelExplicitly(t *testing.T) {
	// "暂不接入渠道"是显式选择：模型保留、不写渠道、不算取消、不算失败。
	fake := defaultFake()
	fake.skipChannel = true
	h := newHarness(t, fake)

	res, err := h.wiz.Run(nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (explicit skip is not an error)", err)
	}
	if res == nil || res.Config == nil {
		t.Fatal("result must carry the saved model configuration")
	}
	if len(res.Config.Models) != 1 || res.Config.DefaultModel == "" {
		t.Errorf("models = %+v default = %q, want the model kept", res.Config.Models, res.Config.DefaultModel)
	}
	if len(res.Config.Channels) != 0 {
		t.Errorf("channels = %+v, want none", res.Config.Channels)
	}
	if res.ChannelID != "" {
		t.Errorf("channel id = %q, want empty", res.ChannelID)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if len(saved.Channels) != 0 || len(saved.Models) != 1 {
		t.Errorf("saved config = %+v, want model only", saved)
	}
	// trace 记录显式跳过，便于与"取消"区分。
	if !strings.Contains(h.trace.String(), "channel_skipped_explicitly") {
		t.Errorf("trace = %s, want channel_skipped_explicitly event", h.trace.String())
	}
	if len(fake.notes) == 0 {
		t.Error("explicit skip should show an explanatory note")
	}
}
