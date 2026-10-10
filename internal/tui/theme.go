package tui

import (
	glamouransi "charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

// 主题色 token（G1b.2.2 契约 docs/specs/tui/splash.md §1，雾青三色，用户拍板）。
// 全部界面颜色只能引用这里的 token 与语义色，其他文件不得出现裸色值。
const (
	themePrimaryValue = "#5BC8C8"
	themeLightValue   = "#7DD3D8"
	themeDarkValue    = "#3A9EA3"
)

var (
	// themePrimary 主色：开屏标题 art、Plume 前缀。
	themePrimary = lipgloss.Color(themePrimaryValue) // hsl(180, 45%, 57%)
	// themeLight 浅色态：开屏版本/模型信息行等次要信息。
	themeLight = lipgloss.Color(themeLightValue) // hsl(184, 47%, 67%)
	// themeDark 深色态：You 前缀、状态栏 running 强调。
	themeDark = lipgloss.Color(themeDarkValue) // hsl(183, 47%, 43%)
)

// markdownStyles 的正文沿用终端默认前景色，块不添加外边距。
func markdownStyles() glamouransi.StyleConfig {
	zero := uint(0)
	bold := true
	block := glamouransi.StyleBlock{Margin: &zero, Indent: &zero}
	heading := block
	heading.Bold = &bold
	heading.Color = new(themePrimaryValue)
	quote := block
	quote.IndentToken = new("│ ")
	indent := uint(2)
	quote.Indent = &indent
	return glamouransi.StyleConfig{
		Document: block, Paragraph: block, BlockQuote: quote,
		Heading: heading, H1: heading, H2: heading, H3: heading, H4: heading, H5: heading, H6: heading,
		Strong: glamouransi.StylePrimitive{Bold: &bold}, Emph: glamouransi.StylePrimitive{Italic: &bold},
		Item: glamouransi.StylePrimitive{BlockPrefix: "• "}, Enumeration: glamouransi.StylePrimitive{BlockPrefix: ". "},
		Code: glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: "`", Suffix: "`"}, Margin: &zero, Indent: &zero},
		CodeBlock: glamouransi.StyleCodeBlock{StyleBlock: block, Chroma: &glamouransi.Chroma{
			Keyword:       glamouransi.StylePrimitive{Color: new(themePrimaryValue)},
			NameFunction:  glamouransi.StylePrimitive{Color: new(themePrimaryValue)},
			LiteralString: glamouransi.StylePrimitive{Color: new(themeLightValue)},
			Comment:       glamouransi.StylePrimitive{Color: new(themeDarkValue)},
		}},
		Table: glamouransi.StyleTable{StyleBlock: block},
	}
}

// 语义色（不占用主题色）：错误红、提示黄、中性暗灰。
var (
	colorError  = lipgloss.Color("9")
	colorNotice = lipgloss.Color("11")
	colorMuted  = lipgloss.Color("8")
)
