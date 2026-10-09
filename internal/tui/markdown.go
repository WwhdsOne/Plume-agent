package tui

import (
	"os"
	"regexp"
	"slices"
	"strings"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"
)

type markdownEngine struct {
	width    int
	renderer *glamour.TermRenderer
}

func newMarkdownEngine(width int) *markdownEngine {
	formatter := "terminal"
	if strings.Contains(os.Getenv("TERM"), "256") {
		formatter = "terminal256"
	}
	if color := os.Getenv("COLORTERM"); color == "truecolor" || color == "24bit" {
		formatter = "terminal16m"
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		formatter = "noop"
	}
	renderer, _ := glamour.NewTermRenderer(glamour.WithStyles(markdownStyles()), glamour.WithWordWrap(max(width, 1)), glamour.WithTableWrap(false), glamour.WithChromaFormatter(formatter), glamour.WithPreservedNewLines())
	return &markdownEngine{width: width, renderer: renderer}
}

var (
	htmlSource     = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)|<!\[CDATA\[.*?(?:]]>|$)|<[/!?]?[A-Za-z][^>]*>`)
	tableSeparator = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$`)
)

// renderMarkdown 只生成派生文本；控制序列用累计原文清理，避免跨 delta
// 的 CSI/OSC 被拆开后变成正文。Glamour 输出只移除 OSC，保留自身 SGR。
func renderMarkdown(raw string, width int) string {
	return renderMarkdownUsing(raw, width, nil)
}

func renderMarkdownUsing(raw string, width int, engine *markdownEngine) string {
	text := sanitize(raw)
	if markdownFallback(text, width) {
		return strings.Join(wrapText(text, width), "\n")
	}
	text = leftAlignedTables(text)
	// Glamour 默认丢掉代码块语言；在开头加可见标签，代码本体仍由它渲染。
	text = labelCodeFences(text)
	if engine == nil || engine.width != width {
		engine = newMarkdownEngine(width)
	}
	if engine.renderer == nil {
		return strings.Join(wrapText(sanitize(raw), width), "\n")
	}
	rendered, err := engine.renderer.Render(text)
	if err != nil {
		return strings.Join(wrapText(sanitize(raw), width), "\n")
	}
	rendered = ansiOSC.ReplaceAllString(rendered, "")
	rendered = strings.Trim(rendered, "\n")
	var lines []string
	hasTable := slices.ContainsFunc(strings.Split(outsideFences(text), "\n"), tableSeparator.MatchString)
	for line := range strings.SplitSeq(rendered, "\n") {
		// Glamour 的 table cell 固定带一列外 margin，不能由 StyleConfig
		// 关闭；去掉行首这列，让表格从统一正文起点开始。
		if hasTable && strings.HasPrefix(line, " ") && strings.Contains(line, "│") {
			line = strings.TrimPrefix(line, " ")
		}
		lines = append(lines, wrapText(strings.TrimRight(line, " "), width)...)
	}
	return strings.Join(lines, "\n")
}

// 只给真正的开围栏插语言标签，外层代码中的短围栏是原样代码正文。
func labelCodeFences(text string) string {
	var b strings.Builder
	var fence codeFence
	for line := range strings.SplitAfterSeq(text, "\n") {
		_, label := fence.advance(line)
		if label != "" {
			b.WriteString(label + "\n\n")
		}
		b.WriteString(line)
	}
	return b.String()
}

// HTML/LaTeX 不加载、不执行；过宽表格保留源文，避免截断单元格。
// 开放中的围栏交给 Glamour 渲染，不能令此前已完成的 Markdown 回退。
func markdownFallback(text string, width int) bool {
	outside := outsideFences(text)
	if htmlSource.MatchString(outside) || strings.Contains(outside, "$") || strings.Contains(outside, `\(`) || strings.Contains(outside, `\[`) {
		return true
	}
	if wideTable(text, width) {
		return true
	}
	// 去除完整 fence 块，防止代码里的标点被当作半成品 Markdown。
	if strings.Count(outside, "**")%2 != 0 || strings.Count(outside, "`")%2 != 0 {
		return true
	}
	last := outside[strings.LastIndex(outside, "\n")+1:]
	if strings.Count(last, "[") > strings.Count(last, "]") || strings.Count(last, "](") > strings.Count(last, ")") {
		return true
	}
	return false
}

func leftAlignedTables(text string) string {
	lines := strings.Split(text, "\n")
	var fence codeFence
	for i, line := range lines {
		inCode, _ := fence.advance(line)
		if inCode || !tableSeparator.MatchString(line) {
			continue
		}
		cells := strings.Split(line, "|")
		for j, cell := range cells {
			if strings.TrimSpace(cell) != "" {
				cells[j] = " :--- "
			}
		}
		lines[i] = strings.Join(cells, "|")
	}
	return strings.Join(lines, "\n")
}

// 取每列所有行的最大宽度，避免长值分布在不同的行时被表格截断。
func wideTable(text string, width int) bool {
	lines := strings.Split(outsideFences(text), "\n")
	for i, line := range lines {
		if i == 0 || !tableSeparator.MatchString(line) {
			continue
		}
		var columns []int
		rows := []string{lines[i-1]}
		for j := i + 1; j < len(lines) && strings.Contains(lines[j], "|"); j++ {
			rows = append(rows, lines[j])
		}
		for _, row := range rows {
			cells := strings.Split(strings.Trim(strings.TrimSpace(row), "|"), "|")
			for j, cell := range cells {
				if j >= len(columns) {
					columns = append(columns, 0)
				}
				columns[j] = max(columns[j], ansi.StringWidth(strings.TrimSpace(cell)))
			}
		}
		// 除列间分隔符外预留 Glamour 每个单元格的左右 margin。
		total := max(len(columns)-1, 0)*3 + len(columns)*2
		for _, col := range columns {
			total += col
		}
		if total > width {
			return true
		}
	}
	return false
}

func outsideFences(text string) string {
	var b strings.Builder
	var fence codeFence
	for line := range strings.SplitSeq(text, "\n") {
		inCode, _ := fence.advance(line)
		if !inCode {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// 围栏状态用于显示副本的预处理；短围栏与不匹配的围栏不会关闭外层代码。
type codeFence struct {
	marker byte
	size   int
}

func (f *codeFence) advance(line string) (bool, string) {
	inside := f.size != 0
	trim := strings.TrimLeft(line, " ")
	if len(line)-len(trim) > 3 || len(trim) == 0 || (trim[0] != '`' && trim[0] != '~') {
		return inside, ""
	}
	n := 0
	for n < len(trim) && trim[n] == trim[0] {
		n++
	}
	if n < 3 {
		return inside, ""
	}
	info := strings.TrimSpace(trim[n:])
	if !inside {
		if trim[0] == '`' && strings.Contains(info, "`") {
			return false, ""
		}
		f.marker, f.size = trim[0], n
		return true, info
	}
	if trim[0] == f.marker && n >= f.size && info == "" {
		f.size = 0
	}
	return true, ""
}
