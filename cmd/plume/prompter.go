package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"plume-agent/internal/channel"
	"plume-agent/internal/provider"
	"plume-agent/internal/setup"
)

// prompter 是 setup.Prompter 的终端实现。它只负责交互与取值，业务判断留在
// internal/setup，这样向导流程可以在没有 TTY 的测试里跑。
type prompter struct {
	in  *os.File
	out *os.File
}

func newPrompter(in, out *os.File) *prompter { return &prompter{in: in, out: out} }

func (p *prompter) form(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithInput(p.in).WithOutput(p.out)
}

func (p *prompter) SelectProvider(presets []provider.Preset, initial string) (provider.Preset, error) {
	options := make([]huh.Option[string], 0, len(presets))
	byID := make(map[string]provider.Preset, len(presets))
	for _, preset := range presets {
		options = append(options, huh.NewOption(preset.DisplayName, preset.ID))
		byID[preset.ID] = preset
	}

	choice := initial
	if _, ok := byID[choice]; !ok {
		choice = presets[0].ID
	}
	err := p.form(huh.NewGroup(
		huh.NewSelect[string]().
			Title("选择模型供应商").
			Description("↑/↓ 移动，Enter 确认，Ctrl+C 取消").
			Options(options...).
			Value(&choice),
	)).Run()
	if err != nil {
		return provider.Preset{}, mapAbort(err)
	}
	preset, ok := byID[choice]
	if !ok {
		return provider.Preset{}, fmt.Errorf("unknown provider %q", choice)
	}
	return preset, nil
}

func (p *prompter) InputBaseURL(initial string, required bool) (string, error) {
	value := initial
	field := huh.NewInput().
		Title("Base URL").
		Description("该供应商没有预填地址，必须填写").
		Value(&value)
	if required {
		field = field.Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("base URL 不能为空")
			}
			return nil
		})
	}

	if err := p.form(huh.NewGroup(field)).Run(); err != nil {
		return "", mapAbort(err)
	}
	return strings.TrimSpace(value), nil
}

// customModelOption 是模型选择列表末尾的"自定义…"哨兵值。
const customModelOption = "\x00custom"

// SelectModel 展示预设的模型列表；末尾的"自定义…"会再追问一次文本输入。
// 列表为空（例如自定义兼容服务）时直接进入文本输入。
func (p *prompter) SelectModel(models []string, initial string) (string, error) {
	if len(models) == 0 {
		return p.inputModelText(initial)
	}

	options := make([]huh.Option[string], 0, len(models)+1)
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}
	options = append(options, huh.NewOption("自定义…", customModelOption))

	choice := models[0]
	switch {
	case initial == "":
	case slices.Contains(models, initial):
		choice = initial
	default:
		// 已有配置用的是列表外的模型，默认落在"自定义…"上，让用户看到原值。
		choice = customModelOption
	}

	if err := p.form(huh.NewGroup(
		huh.NewSelect[string]().
			Title("选择模型").
			Description("↑/↓ 移动，Enter 确认；列表末尾可自定义").
			Options(options...).
			Value(&choice),
	)).Run(); err != nil {
		return "", mapAbort(err)
	}
	if choice == customModelOption {
		return p.inputModelText(initial)
	}
	return choice, nil
}

func (p *prompter) inputModelText(initial string) (string, error) {
	value := initial
	err := p.form(huh.NewGroup(
		huh.NewInput().
			Title("模型 ID").
			Description("不依赖模型列表接口，直接填官方文档里的 ID").
			Value(&value).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("模型 ID 不能为空")
				}
				return nil
			}),
	)).Run()
	if err != nil {
		return "", mapAbort(err)
	}
	return strings.TrimSpace(value), nil
}

func (p *prompter) InputSecret(label string, hasExisting bool) (string, bool, error) {
	value := ""
	field := huh.NewInput().
		Title(label).
		EchoMode(huh.EchoModePassword).
		Value(&value)
	if hasExisting {
		field = field.Description("已有密钥；直接回车保留原值")
	}
	if err := p.form(huh.NewGroup(field)).Run(); err != nil {
		return "", false, mapAbort(err)
	}
	if strings.TrimSpace(value) == "" && hasExisting {
		return "", true, nil
	}
	return value, false, nil
}

func (p *prompter) ConfirmYesNo(label string, initial bool) (bool, error) {
	value := initial
	err := p.form(huh.NewGroup(
		huh.NewConfirm().
			Title(label).
			Affirmative("是").
			Negative("否").
			Value(&value),
	)).Run()
	if err != nil {
		return false, mapAbort(err)
	}
	return value, nil
}

func (p *prompter) SelectChannelOrSkip(types []channel.Type, initial string) (channel.Type, bool, error) {
	options := make([]huh.Option[string], 0, len(types)+1)
	byID := make(map[string]channel.Type, len(types))
	for _, t := range types {
		label := t.DisplayName
		if !t.Enabled {
			label += "（待支持）"
		}
		options = append(options, huh.NewOption(label, t.ID))
		byID[t.ID] = t
	}
	const skipOption = "\x00skip"
	options = append(options, huh.NewOption("暂不接入渠道（TUI 聊天现在即可使用）", skipOption))

	choice := initial
	if t, ok := byID[choice]; !ok || !t.Enabled {
		choice = skipOption
	}
	err := p.form(huh.NewGroup(
		huh.NewSelect[string]().
			Title("选择消息渠道").
			Description("首版只启用一个渠道；可以先只配置模型").
			Options(options...).
			Validate(func(id string) error {
				if id == skipOption {
					return nil
				}
				t, ok := byID[id]
				if !ok {
					return fmt.Errorf("unknown channel %q", id)
				}
				if !t.Enabled {
					return fmt.Errorf("%s 尚未支持", t.DisplayName)
				}
				return nil
			}).
			Value(&choice),
	)).Run()
	if err != nil {
		return channel.Type{}, false, mapAbort(err)
	}
	if choice == skipOption {
		return channel.Type{}, true, nil
	}
	t, ok := byID[choice]
	if !ok {
		return channel.Type{}, false, fmt.Errorf("unknown channel %q", choice)
	}
	return t, false, nil
}

func (p *prompter) Note(title, body string) error {
	err := p.form(huh.NewGroup(
		huh.NewNote().
			Title(title).
			Description(body).
			Next(true).
			NextLabel("继续"),
	)).Run()
	return mapAbort(err)
}

// mapAbort 把 huh 的用户中断翻译成向导能识别的取消错误。
func mapAbort(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, huh.ErrUserAborted) {
		return setup.ErrCancelled
	}
	return err
}
