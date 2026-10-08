package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"plume-agent/internal/app"
)

func TestReviewTableSeparatorInsideCodeIsUnchanged(t *testing.T) {
	raw := "```text\n| a | b |\n| ---: | :---: |\n| x | y |\n```"
	got := ansi.Strip(renderMarkdown(raw, 80))
	if !strings.Contains(got, "| ---: | :---: |") {
		t.Fatalf("code source changed: %q", got)
	}
}

func TestReviewTableBoundaryRetainsEveryCell(t *testing.T) {
	raw := "| abcdefghijk | 123456 |\n| --- | --- |\n| xy | zz |"
	got := ansi.Strip(renderMarkdown(raw, 20))
	if !strings.Contains(strings.ReplaceAll(got, "\n", ""), "abcdefghijk") {
		t.Fatalf("临界宽度不得截断单元格：%q", got)
	}
}

func TestReviewOldRunTickCannotResubscribe(t *testing.T) {
	m, _ := streamModel(t)
	oldTick := nextStreamTick("r1")
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: "done"})
	m.hooks.Submit = func(string) (string, error) { return "r2", nil }
	m.input.SetValue("next")
	m.Update(specialKey(tea.KeyEnter))
	_, next := m.Update(oldTick())
	if next != nil {
		t.Fatal("上一轮计时消息不得为新一轮续订计时")
	}
}

func TestReviewFirstAnswerWaitsForVisibleBodyColumn(t *testing.T) {
	m, _ := streamModel(t)
	m.Update(tea.WindowSizeMsg{Width: bodyOffset(), Height: 10})
	calls := 0
	m.hooks.FirstAnswer = func(string, time.Duration) { calls++ }
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "answer"})
	tm := TeaModel{M: *m}
	frame := ansi.Strip(tm.View().Content)
	if strings.Contains(frame, "answer") || calls != 0 {
		t.Fatalf("正文列不可见时不应计量首答案：calls=%d frame=%q", calls, frame)
	}
	tm.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	tm.M.viewport.GotoBottom()
	frame = ansi.Strip(tm.View().Content)
	tm.View()
	if !strings.Contains(frame, "answer") || calls != 1 {
		t.Fatalf("正文可见后只计量一次：calls=%d frame=%q", calls, frame)
	}
}

func TestReviewFirstAnswerSkipsVisibleBlankMarkdownRow(t *testing.T) {
	m, _ := streamModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 3})
	m.appendLine(lineSystem, "older1")
	m.appendLine(lineSystem, "older2")
	m.viewport.GotoTop()
	calls := 0
	m.hooks.FirstAnswer = func(string, time.Duration) { calls++ }
	// 空链接产生一个空白派生行；下一行才是答案正文。
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "[]()\nanswer"})
	if m.lines[m.activeLine].cache != "\nanswer" {
		t.Fatalf("空链接的派生文本发生变化：%q", m.lines[m.activeLine].cache)
	}
	m.viewport.SetYOffset(m.answerStart)
	tm := TeaModel{M: *m}
	frame := ansi.Strip(tm.View().Content)
	if strings.Contains(frame, "answer") || calls != 0 {
		t.Fatalf("只看见角色标记时不应计量：calls=%d frame=%q", calls, frame)
	}
	tm.M.viewport.ScrollDown(1)
	frame = ansi.Strip(tm.View().Content)
	tm.View()
	if !strings.Contains(frame, "answer") || calls != 1 {
		t.Fatalf("正文进入可见帧后只计量一次：calls=%d frame=%q", calls, frame)
	}
}

func TestReviewThoughtCollapseKeepsReaderFrozenUntilManualBottom(t *testing.T) {
	m, now := streamModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m.appendLine(lineSystem, "older")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventReasoningDelta, ReasoningDelta: strings.Repeat("reason\n", 20)})
	m.Update(ctrlKey('o'))
	m.Update(specialKey(tea.KeyPgUp))
	if m.viewport.AtBottom() {
		t.Fatal("复现必须先主动上滚")
	}
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "answer"})
	m.Update(ctrlKey('o'))
	offset := m.viewport.YOffset()
	if !m.viewport.AtBottom() {
		t.Fatal("复现必须因折叠内容收缩而夹紧到新底部")
	}
	*now = now.Add(60 * time.Millisecond)
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: strings.Repeat("\nnew", 20)})
	if m.viewport.YOffset() != offset || m.viewport.AtBottom() {
		t.Fatalf("折叠不应恢复跟随：offset=%d want=%d bottom=%v", m.viewport.YOffset(), offset, m.viewport.AtBottom())
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 9})
	if m.viewport.YOffset() != offset {
		t.Fatal("resize 不应解除阅读冻结")
	}
	for !m.viewport.AtBottom() {
		m.Update(specialKey(tea.KeyPgDown))
	}
	*now = now.Add(60 * time.Millisecond)
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: "\nlatest"})
	if !m.viewport.AtBottom() {
		t.Fatal("主动滚回底部后应恢复跟随")
	}
}

func TestReviewNestedCodeFencePreservesCodeBody(t *testing.T) {
	raw := "````markdown\n```go\nx\n```\n````"
	got := ansi.Strip(renderMarkdown(raw, 60))
	if !strings.Contains(got, "markdown") || !strings.Contains(got, "```go\nx\n```") || strings.Count(got, "go") != 1 {
		t.Fatalf("内层围栏属于代码正文，不能额外插入语言标签：%q", got)
	}
}

func TestReviewOpenCodeFenceDoesNotDisableEarlierMarkdown(t *testing.T) {
	raw := "下面是 **Python、Java、C++** 三个语言的快速排序实现。\n\n### 1. Python\n```python\ndef quick_sort(arr, low, high):\n    if low < high:\n        return arr"
	got := ansi.Strip(renderMarkdown(raw, 80))
	if strings.Contains(got, "**Python") || strings.Contains(got, "### 1. Python") || strings.Contains(got, "```python") {
		t.Fatalf("代码围栏尚未结束时，不应让前文退回原始 Markdown：%q", got)
	}
	for _, want := range []string{"Python、Java、C++", "1. Python", "quick_sort(arr, low, high)", "return arr"} {
		if !strings.Contains(got, want) {
			t.Fatalf("增量渲染丢失 %q：%q", want, got)
		}
	}
}

func TestReviewCodeContentDoesNotTriggerDocumentFallback(t *testing.T) {
	raw := "## Python\n```python\nprice = '$5'\nprint('<div>')"
	got := ansi.Strip(renderMarkdown(raw, 80))
	if strings.Contains(got, "## Python") || strings.Contains(got, "```python") {
		t.Fatalf("代码中的数学/HTML 字符不得关闭整篇 Markdown 渲染：%q", got)
	}
	if !strings.Contains(got, "price = '$5'") || !strings.Contains(got, "print('<div>')") {
		t.Fatalf("代码源文发生丢失：%q", got)
	}
}

func TestReviewUnclosedFenceRendersInChatAfterRunCompletes(t *testing.T) {
	m, _ := streamModel(t)
	raw := "下面是 **Python、Java、C++** 三个语言。\n\n### 1. Python\n```python\ndef quick_sort(arr):\n    return arr"
	send(m, app.Event{RunID: "r1", Kind: app.EventTextDelta, TextDelta: raw})
	send(m, app.Event{RunID: "r1", Kind: app.EventRunCompleted, Reply: raw})
	got := ansi.Strip(m.View())
	if strings.Contains(got, "**Python") || strings.Contains(got, "### 1. Python") || strings.Contains(got, "```python") {
		t.Fatalf("完成回复后，代码围栏未闭合不应令聊天区整体显示原始 Markdown：%q", got)
	}
	if !strings.Contains(got, "quick_sort") {
		t.Fatalf("代码正文丢失：%q", got)
	}
}

func TestReviewHTMLCommentsAndCDATAPreserveReadableSource(t *testing.T) {
	for _, raw := range []string{
		"<!-- keep source -->\nanswer",
		"<!-- incomplete comment",
		"<![CDATA[<tag>source</tag>]]>\nanswer",
	} {
		t.Run(raw, func(t *testing.T) {
			if got := ansi.Strip(renderMarkdown(raw, 60)); got != raw {
				t.Fatalf("HTML 源文不能被丢弃：got=%q want=%q", got, raw)
			}
		})
	}
}
