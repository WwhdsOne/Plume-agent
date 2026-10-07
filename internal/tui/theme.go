package tui

import "charm.land/lipgloss/v2"

// 主题色 token（G1b.2.2 契约 docs/tui-splash.md §1，雾青三色，用户拍板）。
// 全部界面颜色只能引用这里的 token 与语义色，其他文件不得出现裸色值。
var (
	// themePrimary 主色：开屏标题 art、Plume 前缀。
	themePrimary = lipgloss.Color("#5BC8C8") // hsl(180, 45%, 57%)
	// themeLight 浅色态：开屏版本/模型信息行等次要信息。
	themeLight = lipgloss.Color("#7DD3D8") // hsl(184, 47%, 67%)
	// themeDark 深色态：You 前缀、状态栏 running 强调。
	themeDark = lipgloss.Color("#3A9EA3") // hsl(183, 47%, 43%)
)

// 语义色（不占用主题色）：错误红、提示黄、中性暗灰。
var (
	colorError  = lipgloss.Color("9")
	colorNotice = lipgloss.Color("11")
	colorMuted  = lipgloss.Color("8")
)
