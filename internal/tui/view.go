package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// 渲染样式。克制的单屏配色：前缀标来源，正文不加花哨装饰。
var (
	styleUser      = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))  // 青
	styleAssistant = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // 绿
	styleSystem    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // 暗
	styleError     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // 红
	styleStatus    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleNotice    = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // 黄
)

// View 渲染单屏三段：聊天记录（可滚动）、输入区、状态栏。
func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(m.input.View())
	b.WriteString("\n")
	b.WriteString(m.statusBar())
	return b.String()
}

// renderLines 把聊天记录渲染为纯文本行（viewport 内容）。
func renderLines(lines []chatLine, width int) string {
	var b strings.Builder
	for _, line := range lines {
		prefix, style := lineStyle(line.kind)
		for i, chunk := range wrapText(line.text, width) {
			if i == 0 {
				fmt.Fprintf(&b, "%s %s\n", style.Render(prefix), chunk)
				continue
			}
			fmt.Fprintf(&b, "%s %s\n", strings.Repeat(" ", len(prefix)+1), chunk)
		}
	}
	return b.String()
}

func lineStyle(kind lineKind) (prefix string, style lipgloss.Style) {
	switch kind {
	case lineUser:
		return "You > ", styleUser
	case lineAssistant:
		return "Herald > ", styleAssistant
	case lineError:
		return "Error > ", styleError
	default:
		return "· ", styleSystem
	}
}

// wrapText 按显示宽度折行（超宽文本不撑破单屏布局）。
func wrapText(text string, width int) []string {
	if width <= 0 {
		return strings.Split(text, "\n")
	}
	var out []string
	for paragraph := range strings.SplitSeq(text, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		for len(paragraph) > width {
			out = append(out, paragraph[:width])
			paragraph = paragraph[width:]
		}
		out = append(out, paragraph)
	}
	return out
}

// statusBar 渲染模型、运行状态、run ID、耗时与已知 usage。
func (m *Model) statusBar() string {
	var b strings.Builder
	b.WriteString(styleStatus.Render(m.modelLabel))
	b.WriteString(styleStatus.Render(" │ "))
	if m.state == stateRunning {
		b.WriteString(styleNotice.Render("running"))
		if m.runID != "" {
			b.WriteString(styleStatus.Render(" " + m.runID))
		}
		b.WriteString(styleStatus.Render(fmt.Sprintf(" %s", m.elapsed())))
	} else {
		b.WriteString(styleStatus.Render("idle"))
	}
	if m.lastUsage != "" {
		b.WriteString(styleStatus.Render(" │ " + m.lastUsage))
	}
	if m.notice != "" {
		b.WriteString("  ")
		b.WriteString(styleNotice.Render(m.notice))
	}
	return b.String()
}
