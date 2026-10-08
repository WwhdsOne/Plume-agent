package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"plume-agent/internal/app"
)

type streamTick time.Time

type runStreamTick struct {
	runID string
	tick  streamTick
}

func nextStreamTick(runID string) tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
		return runStreamTick{runID: runID, tick: streamTick(t)}
	})
}

var thoughtSpinner = []string{"⠋", "⠙", "⠚", "⠓"}

func (m *Model) ensureAssistant() *chatLine {
	if m.activeLine < 0 {
		m.lines = append(m.lines, chatLine{kind: lineAssistant})
		m.activeLine = len(m.lines) - 1
	}
	return &m.lines[m.activeLine]
}

func (m *Model) flushStream() {
	m.dirty = false
	m.lastFlush = m.options.Clock()
	if m.activeLine >= 0 && sanitize(m.lines[m.activeLine].text) != "" && m.firstAnswerRun == "" {
		m.firstAnswerRun = m.runID
		m.firstAnswerAt = m.startedAt
	}
	m.syncViewport()
}

// reportFirstAnswer 是 View 帧钩子；滚动区外的答案不计入可见首帧。
// 它表示 renderer 构建了可见帧，不能证明物理终端已经刷新。
func (m *Model) reportFirstAnswer() {
	if m.width <= bodyOffset() || m.firstAnswerRun == "" || m.firstAnswerReported || m.answerStart < 0 {
		return
	}
	start := m.viewport.YOffset()
	end := start + m.viewport.Height()
	if m.answerEnd <= start || m.answerStart >= end {
		return
	}
	if m.activeLine < 0 {
		return
	}
	lines := strings.Split(m.lines[m.activeLine].cache, "\n")
	visible := false
	for row := max(start, m.answerStart); row < min(end, m.answerEnd); row++ {
		index := row - m.answerStart
		if index >= len(lines) {
			break
		}
		text := strings.TrimSpace(ansi.Strip(ansi.Truncate(lines[index], m.width-bodyOffset(), "")))
		if text != "" && ansi.StringWidth(text) > 0 {
			visible = true
			break
		}
	}
	if !visible {
		return
	}
	m.firstAnswerReported = true
	if m.hooks.FirstAnswer != nil {
		m.hooks.FirstAnswer(m.firstAnswerRun, m.options.Clock().Sub(m.firstAnswerAt))
	}
}

func thoughtPreview(raw string, width int) []string {
	text := sanitize(raw)
	// 最后三个自然段至少占三个可见行，之前的段无需分配折行结果。
	start := len(text)
	for i := 0; i < 3; i++ {
		index := strings.LastIndexByte(text[:start], '\n')
		if index < 0 {
			start = 0
			break
		}
		start = index
		if i == 2 {
			start++
		}
	}
	lines := wrapText(text[start:], width)
	if len(lines) > 3 {
		return lines[len(lines)-3:]
	}
	return lines
}

func (m *Model) thoughtVisible() bool {
	return m.thoughtStart >= 0 && m.thoughtStart >= m.viewport.YOffset() && m.thoughtStart < m.viewport.YOffset()+m.viewport.Height()
}

// renderHistory 只重算发生改变的答案；思考 tick 只改变标题和预览。
func (m *Model) renderHistory() string {
	var b strings.Builder
	row := 0
	m.thoughtStart, m.thoughtEnd = -1, -1
	m.answerStart, m.answerEnd = -1, -1
	bodyWidth := max(m.width-bodyOffset(), 1)
	for i := range m.lines {
		line := &m.lines[i]
		if line.kind == lineSplash {
			text := line.text
			if line.role != splashRawRole {
				text = splashStyle(line.role).Render(text)
			}
			b.WriteString(text + "\n")
			row++
			continue
		}
		if line.kind != lineAssistant {
			text := renderLines([]chatLine{*line}, m.width)
			b.WriteString(text)
			row += strings.Count(text, "\n")
			continue
		}
		active := i == m.activeLine && m.state == stateRunning
		reasoning := sanitize(line.reasoning)
		if reasoning != "" {
			title, icon := "Thought", "  "
			preview := active && m.phase == app.PhaseThinking && !line.manualFold
			if active && m.phase == app.PhaseThinking && (!line.manualFold || line.expanded) {
				title = fmt.Sprintf("%s… %s", m.phaseLabels[app.PhaseThinking], m.elapsed())
				icon = thoughtSpinner[m.spinnerFrame%len(thoughtSpinner)] + " "
				if m.width < 8 || ansi.StringWidth(thoughtSpinner[m.spinnerFrame%len(thoughtSpinner)]) != 1 {
					icon = ". "
				}
				m.thoughtStart = row
			}
			if !line.expanded && !preview {
				title = "Thought · Ctrl+O to expand"
				icon = "▸ "
			} else if !active || m.phase != app.PhaseThinking {
				icon = "▾ "
			}
			b.WriteString(styleSystem.Render(ansi.Truncate(icon+title, max(m.width, 1), "")) + "\n")
			row++
			var thoughtLines []string
			if line.expanded {
				thoughtLines = wrapText(reasoning, bodyWidth)
			} else if preview {
				thoughtLines = thoughtPreview(reasoning, bodyWidth)
			}
			for _, s := range thoughtLines {
				b.WriteString(rolePrefix("│", styleSystem) + styleSystem.Render(s) + "\n")
				row++
			}
			if active {
				m.thoughtEnd = row
			}
		}
		if line.text != "" {
			// 未到刷新窗口时保留旧答案派生值；raw 仍连续累计。
			if !m.dirty || line.cacheWidth != bodyWidth {
				if line.cacheRaw != line.text || line.cacheWidth != bodyWidth || line.cache == "" {
					if m.markdownEngine == nil || m.markdownEngine.width != bodyWidth {
						m.markdownEngine = newMarkdownEngine(bodyWidth)
					}
					line.cache = renderMarkdownUsing(line.text, bodyWidth, m.markdownEngine)
					line.cacheRaw, line.cacheWidth = line.text, bodyWidth
					m.markdownRenders++
				}
			}
			if line.cache != "" {
				if i == m.activeLine {
					m.answerStart = row
				}
				for j, s := range strings.Split(line.cache, "\n") {
					prefix := strings.Repeat(" ", bodyOffset())
					if j == 0 {
						prefix = rolePrefix("●", styleAssistant)
					}
					b.WriteString(prefix + s + "\n")
					row++
				}
				if i == m.activeLine {
					m.answerEnd = row
				}
			}
		}
		if line.terminal != "" {
			b.WriteString(styleError.Render(fmt.Sprintf("    [%s]", line.terminal)) + "\n")
			row++
		}
	}
	return b.String()
}
