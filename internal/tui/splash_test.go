package tui

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TestThemeColorsMatchContract 锁定雾青三色的精确值（docs/specs/tui/splash.md §1，
// 用户拍板），防止实现时顺手改色。
func TestThemeColorsMatchContract(t *testing.T) {
	type colorAssert struct {
		name string
		got  any
		want any
	}
	asserts := []colorAssert{
		{"themePrimary", themePrimary, lipgloss.Color("#5BC8C8")},
		{"themeLight", themeLight, lipgloss.Color("#7DD3D8")},
		{"themeDark", themeDark, lipgloss.Color("#3A9EA3")},
		{"colorError", colorError, lipgloss.Color("9")},
		{"colorNotice", colorNotice, lipgloss.Color("11")},
		{"colorMuted", colorMuted, lipgloss.Color("8")},
	}
	for _, a := range asserts {
		if a.got != a.want {
			t.Errorf("%s = %v, want %v", a.name, a.got, a.want)
		}
	}
}

// 启动时颜色能力、环境快照或时钟可能先于窗口尺寸到达，不能提前消耗开屏。
func TestSplashWaitsForValidWindowSize(t *testing.T) {
	cases := []struct {
		name string
		msg  func(*Model) tea.Msg
	}{
		{"color-profile", func(*Model) tea.Msg { return tea.ColorProfileMsg{} }},
		{"workspace", func(*Model) tea.Msg { return WorkspaceEvent{} }},
		{"clock", func(m *Model) tea.Msg { return statusClockTick{m.status.sessionStarted} }},
		{"zero-width", func(*Model) tea.Msg { return tea.WindowSizeMsg{Width: 0, Height: 34} }},
		{"zero-height", func(*Model) tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 0} }},
		{"negative-size", func(*Model) tea.Msg { return tea.WindowSizeMsg{Width: -1, Height: -1} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New("test/model", Hooks{})
			m.SetSplash(Splash{Version: "v0.1.0 (test)", Label: "test/model"})
			m, _ = m.Update(tc.msg(&m))
			if m.splash == nil || len(m.lines) != 0 || m.layoutReady {
				t.Fatalf("startup message consumed splash before valid dimensions: pending=%v lines=%d layout=%v", m.splash != nil, len(m.lines), m.layoutReady)
			}
			m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
			if m.splash != nil {
				t.Fatal("valid dimensions must render splash")
			}
			frame := stripANSI(m.View())
			for _, want := range []string{"╭", "╮", "╰", "╯", featherBraille[0], "Plume-agent", "Tips for getting started"} {
				if !strings.Contains(frame, want) {
					t.Errorf("startup frame missing %q", want)
				}
			}
			if strings.Count(frame, strings.Repeat("─", 100)) != 2 {
				t.Error("startup frame must retain both input separators")
			}
		})
	}
}

// TestSplashRendersBoxedSplash：宽终端渲染像素字标题 + 圆角方框（完整边框）。
func TestSplashRendersBoxedSplash(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.SetSplash(Splash{Version: "v0.1.0 (abc1234)", Label: "test/model", Dir: "/tmp/work"})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated

	if m.splash != nil {
		t.Fatal("splash must be consumed after first resize")
	}
	// 立体字标题 + 方框（顶边 + 羽毛栏 + 底边）
	wantLines := len(plumeWordmark) + featherHeight + 2
	if len(m.lines) != wantLines {
		t.Fatalf("splash lines = %d, want %d", len(m.lines), wantLines)
	}
	joined := ""
	for i, l := range m.lines {
		if l.kind != lineSplash || l.role != splashRawRole {
			t.Fatalf("lines[%d] = %+v, want raw splash line", i, l)
		}
		joined += l.text + "\n"
	}
	// 方框上方是六行立体标题，沿用当前左对齐。
	for i := range len(plumeWordmark) {
		row := stripANSI(m.lines[i].text)
		if i < len(plumeWordmark)-1 && !strings.Contains(row, "█") || strings.Contains(row, ".") {
			t.Errorf("wordmark row %d = %q, want pixel blocks and blank negative space", i, row)
		}
		if strings.HasPrefix(row, " ") {
			t.Errorf("wordmark row %d must be left aligned", i)
		}
	}
	// 顶边框完整且不嵌任何文字（宽 80 → 方框 78，内宽 76）
	top := stripANSI(m.lines[len(plumeWordmark)].text)
	if top != "╭"+strings.Repeat("─", 76)+"╮" {
		t.Errorf("top border = %q, want plain ╭───╮ without embedded text", top)
	}
	// LOGO 与右栏之间有分隔竖线：每根羽毛行的去色文本含 3 根 │（左边框+分隔线+右边框）
	for i := len(plumeWordmark) + 1; i < len(m.lines)-1; i++ {
		if n := strings.Count(stripANSI(m.lines[i].text), "│"); n != 3 {
			t.Errorf("row %d has %d vertical bars, want 3 (border+divider+border)", i, n)
		}
	}
	for _, want := range []string{
		"Plume-agent v0.1.0 (abc1234)",
		"Tips for getting started",
		"Shift+Enter",
		"\\+Enter",
		"Newline fallback",
		"Input history",
		"Ctrl+L / Ctrl+N",
		"Quit on empty input",
		"test/model",
		"/tmp/work",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("boxed splash missing %q", want)
		}
	}
	// 渐变羽毛：三色 ANSI 真彩序列都必须出现
	for _, rgb := range []string{"38;2;58;158;163", "38;2;91;200;200", "38;2;125;211;216"} {
		if !strings.Contains(joined, "\x1b["+rgb+"m") {
			t.Errorf("gradient feather missing color %s", rgb)
		}
	}
	last := stripANSI(m.lines[len(m.lines)-1].text)
	if !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
		t.Errorf("bottom border = %q, want rounded box close", last)
	}
}

func TestWordmarkShadow(t *testing.T) {
	for _, width := range []int{80, 93, 94, 100, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			lines := buildSplashLines(width, Splash{})
			if len(plumeWordmark) != 6 {
				t.Fatalf("wordmark height = %d, want six shadow-font rows", len(plumeWordmark))
			}
			var title strings.Builder
			wantWidth := 94
			if width < 94 {
				wantWidth = 71
			}
			for i := 0; i < 6; i++ {
				row := stripANSI(lines[i].text)
				if got := lipgloss.Width(row); got != wantWidth || got > width {
					t.Errorf("row %d width = %d, want %d within %d", i, got, wantWidth, width)
				}
				if !strings.ContainsAny(row, "╔╗╚╝═║") || strings.HasPrefix(row, " ") {
					t.Errorf("row %d lacks left aligned shadow outline: %q", i, row)
				}
				title.WriteString(lines[i].text)
			}
			for _, rgb := range []string{"38;2;91;200;200", "38;2;58;158;163"} {
				if !strings.Contains(title.String(), "\x1b["+rgb+"m") {
					t.Errorf("wordmark missing face/shadow color %s", rgb)
				}
			}
		})
	}
}

// TestSplashNarrowFallback：窄终端降级为羽毛竖排（无方框），信息/提示保留。
func TestSplashNarrowFallback(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.SetSplash(Splash{Version: "dev (none)", Label: "fake/offline"})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated

	joined := ""
	for _, l := range m.lines {
		joined += l.text + "\n"
		if strings.Contains(l.text, "╭") || strings.Contains(l.text, "│") {
			t.Error("narrow splash must not render the box border")
		}
	}
	if !strings.ContainsFunc(joined, func(r rune) bool { return r > '\u2800' && r <= '\u28ff' }) {
		t.Error("narrow splash must keep the braille feather logo")
	}
	if !strings.Contains(joined, "plume-agent dev (none) · fake/offline") {
		t.Errorf("splash missing version+label info line")
	}
	if !strings.Contains(joined, "Enter send") {
		t.Error("narrow splash missing key hint line")
	}
}

// TestSplashOnlyOnce：开屏只出现一次；新会话不复活，后续 resize 不重排。
func TestSplashOnlyOnce(t *testing.T) {
	var resets atomic.Int64
	m := newTestModel(t, Hooks{ResetSession: func() { resets.Add(1) }})
	m.SetSplash(Splash{Version: "v0.1.0 (abc1234)", Label: "test/model"})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated
	first := len(m.lines)
	if first != len(plumeWordmark)+featherHeight+2 {
		t.Fatalf("splash lines = %d, want %d", first, len(plumeWordmark)+featherHeight+2)
	}

	// 再次 resize（窗口变化）不得重复 prepend
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = updated
	if len(m.lines) != first {
		t.Errorf("lines after second resize = %d, want %d (no re-render)", len(m.lines), first)
	}

	// Ctrl+N 清空记录后只剩新会话提示，开屏不复活
	updated, _ = m.Update(ctrlKey('n'))
	m = updated
	if len(m.lines) != 1 || m.lines[0].kind != lineSystem {
		t.Errorf("lines after reset = %+v, want only new-session notice", m.lines)
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = updated
	if len(m.lines) != 1 {
		t.Errorf("lines = %d, splash must not revive after session reset", len(m.lines))
	}
}

// TestSplashFitsTerminal 检查真实信息包含长文本、中文路径与控制字符时，
// 宽端边框仍对齐，窄端提示也不溢出屏幕；不依赖羽毛网格的具体形状。
func TestSplashFitsTerminal(t *testing.T) {
	s := Splash{
		Version: "v0.1.0 (" + strings.Repeat("abcdef", 12) + ")",
		Label:   "DeepSeek/" + strings.Repeat("模型", 30) + "\nlabel",
		Dir:     "/Users/学习/" + strings.Repeat("项目🪶", 30) + "\t\x1b[31mwork\x1b[0m",
	}
	for _, width := range []int{20, 32, 40, 79, 80, 88, 100} {
		lines := buildSplashLines(width, s)
		for i, line := range lines {
			got := lipgloss.Width(line.text)
			if got > width || strings.ContainsAny(line.text, "\n\t") {
				t.Errorf("width %d: row %d spans %d columns or contains a control character: %q", width, i, got, line.text)
			}
			if width >= splashBoxMinWidth && i >= len(plumeWordmark) {
				if want := min(width-2, splashBoxMaxWidth); got != want {
					t.Errorf("width %d: box row %d spans %d columns, want %d", width, i, got, want)
				}
			}
		}
	}
}

// TestSplashLinesAreNotWrapped：开屏行不折行（方框/羽毛折行即毁），
// 普通行仍按宽度折行。
func TestSplashLinesAreNotWrapped(t *testing.T) {
	long := strings.Repeat("x", 60)
	lines := []chatLine{
		{kind: lineSplash, role: splashRawRole, text: long},
		{kind: lineUser, text: long},
	}
	out := renderLines(lines, 20)
	if !strings.Contains(out, long) {
		t.Error("splash line must not be re-wrapped")
	}
	if strings.Count(out, long) != 1 {
		t.Error("splash line must appear verbatim exactly once")
	}
	if !strings.Contains(stripANSI(out), "  > ") {
		t.Error("user line must keep its prefix")
	}
	if strings.Count(out, "\n") < 3 {
		t.Error("user line should wrap into multiple rows at width 20")
	}
}

// stripANSI 去掉 ANSI 转义序列，便于断言方框边框字符。
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			for ; i < len(s); i++ {
				c := s[i]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					i++
					break
				}
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
