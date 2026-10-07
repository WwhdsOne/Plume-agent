package tui

import (
	"charm.land/bubbles/v2/key"
)

// KeyMap 是全部按键的集中绑定表（G1b.2.1 键位契约 docs/tui-keys.md §4）。
// Update/View 只引用这里的语义字段，键名硬编码不得散落在其他文件；
// 平台差异只允许出现在 newKeyMap 的帮助文本里。
type KeyMap struct {
	Submit           key.Binding // Enter 发送
	NewlineShift     key.Binding // Shift+Enter 换行（kitty 协议终端）
	NewlineAlt       key.Binding // Alt/Option+Enter 换行
	NewlineBackslash key.Binding // `\`+Enter 续行兜底（语义在 submitInput 里判定行尾 `\`）
	Cancel           key.Binding // Esc 中断当前 run
	Quit             key.Binding // Ctrl+D 空输入退出
	ClearScreen      key.Binding // Ctrl+L 清屏重绘
	HistoryUp        key.Binding // Up 上一条草稿历史
	HistoryDown      key.Binding // Down 下一条草稿历史
	ScrollUp         key.Binding // PgUp 滚动聊天记录
	ScrollDown       key.Binding // PgDn 滚动聊天记录
	NewSession       key.Binding // Ctrl+N 新会话
}

// newKeyMap 按 GOOS 构造键位表。darwin/linux 同表，差异仅是帮助文本里
// 修饰键的叫法（Option vs Alt）；windows 分支留 G-win 实测后补注记。
func newKeyMap(goos string) KeyMap {
	alt := "alt"
	if goos == "darwin" {
		alt = "option"
	}
	km := KeyMap{
		Submit:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		NewlineShift:     key.NewBinding(key.WithKeys("shift+enter"), key.WithHelp("shift+enter", "newline")),
		NewlineAlt:       key.NewBinding(key.WithKeys("alt+enter"), key.WithHelp(alt+"+enter", "newline")),
		NewlineBackslash: key.NewBinding(key.WithKeys("\\"), key.WithHelp("\\+enter", "newline")),
		Cancel:           key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel run")),
		Quit:             key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "quit")),
		ClearScreen:      key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "clear screen")),
		HistoryUp:        key.NewBinding(key.WithKeys("up"), key.WithHelp("up", "input history")),
		HistoryDown:      key.NewBinding(key.WithKeys("down"), key.WithHelp("down", "input history")),
		ScrollUp:         key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll log")),
		ScrollDown:       key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "scroll log")),
		NewSession:       key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("ctrl+n", "new session")),
	}
	if goos == "windows" {
		// TODO(G-win): Windows Console API 的按键行为待实测（Terminal.app/
		// ConHost 修饰键组合），键位表结构不变，只按实测调整注记。
		_ = km
	}
	return km
}
