package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestInputSeparatorWrapsInput 用户要求：输入区上下各一条与终端同宽的
// 主题色横线，醒目框出输入框；整体布局为记录区、横线、输入区、横线、状态栏。
func TestInputSeparatorWrapsInput(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.input.SetValue("hello")
	m.resize()

	lines := strings.Split(m.View(), "\n")
	wantTotal := m.viewport.Height() + 1 + m.input.Height() + 1 + 1
	if len(lines) != wantTotal {
		t.Fatalf("view lines = %d, want %d (viewport + sep + input + sep + status)", len(lines), wantTotal)
	}
	sep := inputSeparator(m.width)
	if got := lines[m.viewport.Height()]; got != sep {
		t.Errorf("line above input = %q, want separator", got)
	}
	if got := lines[m.viewport.Height()+1+m.input.Height()]; got != sep {
		t.Errorf("line below input = %q, want separator", got)
	}
	if w := ansi.StringWidth(sep); w != m.width {
		t.Errorf("separator width = %d, want %d (terminal width)", w, m.width)
	}
}

// TestInputCursorFollowsCaret IME 候选窗跟随终端光标：View 声明的光标必须
// 定位在输入框插入点（记录区下方一行、提示符之后的实际列），而不是帧尾。
func TestInputCursorFollowsCaret(t *testing.T) {
	m := newTestModel(t, Hooks{})
	m.input.SetValue("ab")
	m.resize()

	c := m.inputCursor()
	if c == nil {
		t.Fatal("inputCursor = nil, want cursor at caret (virtual cursor disabled)")
	}
	if want := m.viewport.Height() + 1; c.Position.Y != want {
		t.Errorf("cursor Y = %d, want %d (viewport height + separator)", c.Position.Y, want)
	}
	// 提示符（"┃ "）占 2 列 + 默认行号列占 3 列 + 两个半宽字符
	if want := 2 + 3 + 2; c.Position.X != want {
		t.Errorf("cursor X = %d, want %d (prompt + line number + typed columns)", c.Position.X, want)
	}

	// 中文双宽字符按显示列宽计算，候选窗跟随列位置而非字符数
	m.input.SetValue("你好")
	m.resize()
	c = m.inputCursor()
	if want := 2 + 3 + 4; c.Position.X != want {
		t.Errorf("cursor X with CJK = %d, want %d (prompt + line number + 2 double-width runes)", c.Position.X, want)
	}
}
