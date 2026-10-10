package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 渲染样式。主题三色 token 见 theme.go（雾青，G1b.2.2）；错误红/提示黄/
// 中性灰是语义色不占用主题色。克制的单屏配色：前缀标来源，正文不加花哨装饰。
var (
	styleUser            = lipgloss.NewStyle().Foreground(themeDark)    // 深色态
	styleAssistant       = lipgloss.NewStyle().Foreground(themePrimary) // 主色
	styleSystem          = lipgloss.NewStyle().Foreground(colorMuted)
	styleError           = lipgloss.NewStyle().Foreground(colorError)
	styleStatus          = lipgloss.NewStyle().Foreground(themeLight)
	styleStatusLabel     = lipgloss.NewStyle().Foreground(themePrimary).Bold(true)
	styleStatusSeparator = lipgloss.NewStyle().Foreground(themeDark)
	styleNotice          = lipgloss.NewStyle().Foreground(colorNotice)
	// styleSeparator 渲染输入区上下分隔线（用户要求醒目标出输入框，
	// 用主题主色；语义色不占用主题色，横线属于装饰性主题元素）。
	styleSeparator = lipgloss.NewStyle().Foreground(themePrimary)
)

// inputSeparator 渲染一行与终端同宽的主题色横线，醒目框出输入区。
func inputSeparator(width int) string {
	return styleSeparator.Render(strings.Repeat("─", max(width, 1)))
}

// View 渲染单屏三段：聊天记录（可滚动）、输入区（上下各一条主题色横线）、
// 状态栏。
func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(inputSeparator(m.width))
	b.WriteString("\n")
	b.WriteString(m.input.View())
	b.WriteString("\n")
	b.WriteString(inputSeparator(m.width))
	if m.statusRows() > 0 {
		b.WriteString("\n")
		b.WriteString(m.statusBar())
	}
	return b.String()
}

// renderLines 把聊天记录渲染为纯文本行（viewport 内容）。
// 开屏行不折行：已染色整行（方框/羽毛）原样直出，纯文本行按角色配色；
// 其余行按显示宽度折行。
func renderLines(lines []chatLine, width int) string {
	var b strings.Builder
	for _, line := range lines {
		if line.kind == lineSplash {
			if line.role == splashRawRole {
				b.WriteString(line.text)
			} else {
				b.WriteString(splashStyle(line.role).Render(line.text))
			}
			b.WriteString("\n")
			continue
		}
		prefix, style := lineStyle(line.kind)
		text := sanitize(line.text)
		if line.kind == lineAssistant {
			text = renderMarkdown(line.text, max(width-bodyOffset(), 1))
		}
		for i, chunk := range wrapText(text, max(width-bodyOffset(), 1)) {
			if line.kind == lineTool {
				chunk = styleAssistant.Render(chunk)
			}
			if i == 0 {
				fmt.Fprintf(&b, "%s%s\n", rolePrefix(prefix, style), chunk)
				continue
			}
			fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", bodyOffset()), chunk)
		}
	}
	return b.String()
}

// roleMarkerWidth 共用显示槽宽，不能按 UTF-8 字节或某个角色单独计算。
func roleMarkerWidth() int {
	return max(ansi.StringWidth(">"), ansi.StringWidth("●"), ansi.StringWidth("│"))
}
func bodyOffset() int { return 2 + roleMarkerWidth() + 1 }
func rolePrefix(marker string, style lipgloss.Style) string {
	return "  " + style.Render(marker) + strings.Repeat(" ", max(roleMarkerWidth()-ansi.StringWidth(marker), 0)+1)
}

func lineStyle(kind lineKind) (prefix string, style lipgloss.Style) {
	switch kind {
	case lineUser:
		return ">", styleUser
	case lineAssistant:
		return "●", styleAssistant
	case lineError:
		return "!", styleError
	case lineTool:
		return "◇", styleAssistant
	default:
		return "·", styleSystem
	}
}

// wrapText 按显示宽度折行（超宽文本不撑破单屏布局）。
func wrapText(text string, width int) []string {
	if width <= 0 {
		return strings.Split(text, "\n")
	}
	return strings.Split(ansi.Hardwrap(text, width, true), "\n")
}
