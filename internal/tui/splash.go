package tui

import (
	"runtime"
	"strings"

	"charm.land/lipgloss/v2"
)

// 开屏契约见 docs/tui-splash.md：方框上方 PLUME-AGENT 像素字标题，下方圆角
// 方框（完整边框，顶边不嵌字）：左栏羽毛盲文点阵（雾青三色），右栏版本、
// 键位提示与真实信息；是聊天记录区的初始内容（随记录滚动），只渲染一次，
// 不伪造信息。

// plumeWordmark 按参考图使用实心笔画与双线阴影：6 行 × 94 列。
var plumeWordmark = [...]string{
	"██████╗ ██╗     ██╗   ██╗███╗   ███╗███████╗       █████╗  ██████╗ ███████╗███╗   ██╗████████╗",
	"██╔══██╗██║     ██║   ██║████╗ ████║██╔════╝      ██╔══██╗██╔════╝ ██╔════╝████╗  ██║╚══██╔══╝",
	"██████╔╝██║     ██║   ██║██╔████╔██║█████╗  █████╗███████║██║  ███╗█████╗  ██╔██╗ ██║   ██║   ",
	"██╔═══╝ ██║     ██║   ██║██║╚██╔╝██║██╔══╝  ╚════╝██╔══██║██║   ██║██╔══╝  ██║╚██╗██║   ██║   ",
	"██║     ███████╗╚██████╔╝██║ ╚═╝ ██║███████╗      ██║  ██║╚██████╔╝███████╗██║ ╚████║   ██║   ",
	"╚═╝     ╚══════╝ ╚═════╝ ╚═╝     ╚═╝╚══════╝      ╚═╝  ╚═╝ ╚═════╝ ╚══════╝╚═╝  ╚═══╝   ╚═╝   ",
}

// 紧凑版保留相同轮廓，80–93 列窗口使用 6 行 × 71 列字形。
var plumeWordmarkCompact = [...]string{
	"████╗ █╗    █╗  █╗██╗  ██╗█████╗      ███╗  ████╗ █████╗██╗  █╗███████╗",
	"█╔══█╗█║    █║  █║███╗███║█╔═══╝     █╔══█╗█╔═══╝ █╔═══╝███╗ █║╚══█╔══╝",
	"████╔╝█║    █║  █║█╔███╔█║████╗ ████╗█████║█║ ███╗████╗ █╔██╗█║   █║   ",
	"█╔══╝ █║    █║  █║█║╚█╔╝█║█╔══╝ ╚═══╝█╔══█║█║   █║█╔══╝ █║╚███║   █║   ",
	"█║    █████╗╚███╔╝█║ ╚╝ █║█████╗     █║  █║╚████╔╝█████╗█║ ╚██║   █║   ",
	"╚╝    ╚════╝ ╚══╝ ╚╝    ╚╝╚════╝     ╚╝  ╚╝ ╚═══╝ ╚════╝╚╝  ╚═╝   ╚╝   ",
}

// featherBraille 从参考图等比例采样为 64×80 点位，以每字符 2×4 点
// 编码为 32 列×20 行盲文字符。留白用空格，运行时只绘制字符与主题色，
// 不加载图片；羽轴斜缝、分离碎羽和细茎均来自原图轮廓。
var featherBraille = [...]string{
	"                               ⣠",
	"                              ⣾⠏",
	"                           ⣠⡞ ⠁⡀",
	"                          ⣾⡿⠃⣠⡞ ",
	"                     ⢀⣠⣴⠂⢸⠟⠁ ⠁  ",
	"                 ⣀⣤⣴⣾⣿⣿⡟ ⢀⣠⣴⡟   ",
	"             ⢀⣤⣶⣿⣿⣿⣿⣿⣿⡿⠁⢠⡿⠟⠉    ",
	"          ⢀⡀⣰⣿⣿⣿⣿⣿⣿⣿⣿⠟⠁ ⢁⣠      ",
	"        ⢀⣴⣿⢠⣿⣿⣿⣿⣿⣿⣿⠟⢁⣠⣴⣾⣿⠃      ",
	"       ⣴⣿⣿⣧⣿⣿⣿⣿⡿⠟⣉⣤⣾⣿⣿⣿⡿⠃       ",
	"     ⢀⣾⣿⣿⣿⣿⣿⣿⠟⣉⣴⣾⣿⣿⣿⣿⣿⠟⠁        ",
	"    ⢀⣾⣿⣿⣿⣿⡿⢋⣤⣾⣿⣿⣿⠿⠛⢋⠁           ",
	"    ⣼⣿⣿⣿⠟⢋⣴⣿⣿⣿⣿⣷⣾⣿⡿⠋            ",
	"    ⣿⣿⡿⢃⣴⣿⣿⣿⣿⣿⣿⣿⠟⠉              ",
	"   ⢸⣿⠋⣴⣿⣿⣿⣿⠿⠟⠛⠉                 ",
	"   ⠈⣡⣾⣿⠿⠛⠉                      ",
	"   ⣴⣿⠟⠁                         ",
	" ⢀⣾⡟⠁                           ",
	"⢠⣿⡟                             ",
	"⠛⠉                              ",
}

// 布局常量：splashBoxMinWidth 同时容纳 32 列羽毛与最长 35 列提示，
// 低于此值竖排降级。开屏总高 = 标题 6 + 方框(羽毛 20+2) = 28 行，
// 记录区不足时靠滚动，标题与羽毛原始行不折行。
const (
	splashBoxMinWidth = 80
	splashBoxMaxWidth = 88 // 方框宽度上限
	featherWidth      = 32
	featherHeight     = len(featherBraille)
	featherPadding    = 2
)

// Splash 是开屏展示的真实信息，由 cmd 装配注入。
type Splash struct {
	Version string // 如 "v0.1.0 (734aa8b)"；空则省略版本段
	Label   string // 模型/供应商标签（如 DeepSeek/deepseek-flash 或 fake/offline）
	Dir     string // 工作目录（真实信息；空则省略）
}

// splashRole 区分开屏行的处理方式（chatLine.role 仅在 kind==lineSplash 时使用）。
type splashRole int

const (
	splashRawRole  splashRole = iota // 已染色整行，渲染层原样直出
	splashInfoRole                   // 纯文本信息行（浅色，窄端降级用）
	splashHintRole                   // 纯文本提示行（暗灰，窄端降级用）
)

// 开屏行样式（方框/像素字行以已染色整行输出，不走单色渲染）。
var (
	styleSplashBorder = lipgloss.NewStyle().Foreground(themePrimary)
	styleSplashWord   = lipgloss.NewStyle().Foreground(themePrimary)
	styleSplashShadow = lipgloss.NewStyle().Foreground(themeDark)
	styleSplashHead   = lipgloss.NewStyle().Foreground(themeLight)
	styleSplashLabel  = lipgloss.NewStyle().Foreground(themeLight)
	styleSplashHint   = lipgloss.NewStyle().Foreground(colorMuted)
)

// 羽毛点阵的三色渐变映射（右上深 → 左下浅）。
var featherStyle = map[byte]lipgloss.Style{
	'd': lipgloss.NewStyle().Foreground(themeDark),
	'm': lipgloss.NewStyle().Foreground(themePrimary),
	'l': lipgloss.NewStyle().Foreground(themeLight),
}

// buildSplashLines 按当前宽度生成开屏行：宽端像素字 + 方框，窄端竖排降级。
func buildSplashLines(width int, s Splash) []chatLine {
	if width >= splashBoxMinWidth {
		return buildBoxedSplash(width, s)
	}
	return buildPlainSplash(width, s)
}

// buildBoxedSplash 渲染像素字标题 + 圆角方框（完整边框）：左栏羽毛，
// 右栏版本、提示与信息。
func buildBoxedSplash(width int, s Splash) []chatLine {
	boxW := min(width-2, splashBoxMaxWidth)
	inner := boxW - 2
	// 左栏两侧各留白，右栏两侧各一格，分隔线占一格。
	rightW := inner - featherWidth - 2*featherPadding - 3

	var lines []chatLine
	// 沿用左对齐；按可见列宽选择完整或紧凑字形，避免截断标题。
	wordmark := plumeWordmark
	if lipgloss.Width(wordmark[0]) > width {
		wordmark = plumeWordmarkCompact
	}
	for _, row := range wordmark {
		lines = append(lines, chatLine{kind: lineSplash, role: splashRawRole,
			text: renderWordmarkRow(row)})
	}

	// 右栏单元格按可见列宽补齐，提示内的快捷键与说明分别染色。
	type rightCell struct {
		text  string
		style lipgloss.Style
	}
	var right [featherHeight]rightCell
	right[1] = rightCell{strings.TrimSpace("Plume-agent " + splashText(s.Version)), styleSplashLabel}
	right[3] = rightCell{"Tips for getting started", styleSplashHead}
	tip := func(row int, shortcut, action string) {
		right[row] = rightCell{
			styleSplashLabel.Render(padRightCells(shortcut, 16)) + styleSplashHint.Render(action),
			lipgloss.NewStyle(),
		}
	}
	altEnter := newKeyMap(runtime.GOOS).NewlineAlt.Help().Key
	altEnter = strings.ToUpper(altEnter[:1]) + strings.ReplaceAll(altEnter[1:], "enter", "Enter")
	tip(5, "Enter / Esc", "Send / cancel")
	tip(6, "Shift+Enter", "Newline")
	tip(7, altEnter, "Newline")
	tip(8, "\\+Enter", "Newline fallback")
	tip(9, "Up / Down", "Input history")
	tip(10, "PgUp / PgDn", "Scroll chat")
	tip(11, "Ctrl+L / Ctrl+N", "Clear / new session")
	tip(12, "Ctrl+C twice", "Quit on empty input")
	tip(13, "Ctrl+D", "Quit on empty input")
	right[15] = rightCell{strings.Repeat("─", min(24, rightW)), styleSplashHint}
	right[17] = rightCell{splashText(s.Label), styleSplashLabel}
	right[18] = rightCell{splashText(s.Dir), styleSplashHint}

	// 完整顶边框（不嵌文字）。
	lines = append(lines, chatLine{kind: lineSplash, role: splashRawRole,
		text: styleSplashBorder.Render("╭" + strings.Repeat("─", inner) + "╮")})
	for i := range featherHeight {
		left := strings.Repeat(" ", featherPadding) + renderFeatherRow(i) + strings.Repeat(" ", featherPadding)
		// 空单元格也必须补满右栏宽度，否则右边框会参差内缩。
		r := strings.Repeat(" ", rightW)
		if right[i].text != "" {
			r = right[i].style.Render(padRightCells(right[i].text, rightW))
		}
		// LOGO 与右栏之间一根竖线分隔（贯穿框内全高，主色，同边框）。
		row := styleSplashBorder.Render("│") + left +
			styleSplashBorder.Render("│") + " " + r + " " +
			styleSplashBorder.Render("│")
		lines = append(lines, chatLine{kind: lineSplash, role: splashRawRole, text: row})
	}
	lines = append(lines, chatLine{kind: lineSplash, role: splashRawRole,
		text: styleSplashBorder.Render("╰" + strings.Repeat("─", inner) + "╯")})
	return lines
}

// 标题主体与双线阴影分别着色，空白保留，不增加背景或终端列宽。
func renderWordmarkRow(row string) string {
	var b strings.Builder
	for _, glyph := range row {
		switch glyph {
		case ' ':
			b.WriteRune(glyph)
		case '█':
			b.WriteString(styleSplashWord.Render(string(glyph)))
		default:
			b.WriteString(styleSplashShadow.Render(string(glyph)))
		}
	}
	return b.String()
}

// buildPlainSplash 是窄端降级：羽毛 + 折行信息/提示，极窄端仅保留小标题与文本。
func buildPlainSplash(width int, s Splash) []chatLine {
	lines := make([]chatLine, 0, featherHeight+4)
	if width >= featherWidth {
		for i := range featherHeight {
			lines = append(lines, chatLine{kind: lineSplash, role: splashRawRole,
				text: renderFeatherRow(i)})
		}
	}
	appendText := func(text string, role splashRole) {
		for row := range strings.SplitSeq(lipgloss.NewStyle().Width(max(width, 1)).Render(text), "\n") {
			lines = append(lines, chatLine{kind: lineSplash, role: role, text: row})
		}
	}
	if width < featherWidth {
		appendText("PLUME", splashInfoRole)
	}
	info := splashText(s.Label)
	if s.Version != "" {
		info = strings.TrimSpace("plume-agent "+splashText(s.Version)) + " · " + splashText(s.Label)
	}
	if info != "" {
		appendText(info, splashInfoRole)
	}
	appendText("Enter send · Shift+Enter or \\+Enter newline · Esc cancel · double Ctrl+C quit · Ctrl+N new session", splashHintRole)
	return lines
}

// renderFeatherRow 按点阵单元格的对角位置着色；同一盲文字符的点共用
// 前景色，不填背景，点间留白保持透明。每行仍占 32 个终端列。
func renderFeatherRow(i int) string {
	var b strings.Builder
	for col, dot := range []rune(featherBraille[i]) {
		if dot == ' ' {
			b.WriteByte(' ')
			continue
		}
		// 字符高约为宽的两倍，因此行位置按两倍计入渐变轴。
		position := col + 2*(featherHeight-1-i)
		span := featherWidth + 2*featherHeight
		style := featherStyle['m']
		switch {
		case position >= 2*span/3:
			style = featherStyle['d']
		case position < span/3:
			style = featherStyle['l']
		}
		b.WriteString(style.Render(string(dot)))
	}
	return b.String()
}

// padRightCells 按终端列宽裁剪并补齐，中文路径和 ANSI 颜色不影响边框对齐。
func padRightCells(s string, w int) string {
	s = truncateCells(s, w)
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// truncateCells 按终端列宽截断并追加省略号。
func truncateCells(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	if w < 1 {
		return ""
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// splashText 将外部信息限制为一行纯文本，不让目录名或配置标签破坏边框。
func splashText(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(sanitize(s))
}

// splashStyle 按角色返回纯文本开屏行样式（窄端降级路径使用）。
func splashStyle(role splashRole) lipgloss.Style {
	switch role {
	case splashInfoRole:
		return styleSplashLabel
	default:
		return styleSplashHint
	}
}
