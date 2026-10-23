package app

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestParseInputSeparatesCommandsAndMessages 守住顶层解析、原文保留与单次转义边界。
func TestParseInputSeparatesCommandsAndMessages(t *testing.T) {
	tests := []struct {
		name, text string
		kind       InputKind
		message    string
		command    Command
	}{
		// case: 普通正文中的斜杠不触发指令，也不丢失两端空白。
		{"ordinary", "  请看 /tmp/a.txt\n第二行  ", InputMessage, "  请看 /tmp/a.txt\n第二行  ", Command{}},
		// case: 引用和代码围栏是普通正文。
		{"quote", "> /new", InputMessage, "> /new", Command{}},
		{"fence", "```\n/new\n```", InputMessage, "```\n/new\n```", Command{}},
		// case: 仅 ASCII 空格和 Tab 参与指令起始判定。
		{"unicode leading space", "　/new", InputMessage, "　/new", Command{}},
		{"leading blanks", " \t/status\t ", InputCommand, "", Command{Name: CommandStatus, Page: 1}},
		// case: 转义仅去掉第一个斜杠，允许多行且不再次解析。
		{"escaped", "//tmp/a.txt", InputMessage, "/tmp/a.txt", Command{}},
		{"escaped multiline", " \t///new\n正文", InputMessage, " \t//new\n正文", Command{}},
		// case: 裸斜杠与 help、别名均归一为正式命令名。
		{"bare slash", "/", InputCommand, "", Command{Name: CommandHelp, Page: 1}},
		{"help", "/help /stop", InputCommand, "", Command{Name: CommandHelp, HelpCommand: "cancel", Page: 1}},
		{"stop alias", "/stop", InputCommand, "", Command{Name: CommandCancel, Page: 1}},
		{"quit alias", "/quit", InputCommand, "", Command{Name: CommandExit, Page: 1}},
		// case: 标题直接消费普通文本，不展开 Shell、环境变量或引号。
		{"new default", "/new", InputCommand, "", Command{Name: CommandNew, Page: 1}},
		{"new literal title", "/new  '$HOME' $(touch /tmp/x)  ", InputCommand, "", Command{Name: CommandNew, Title: "'$HOME' $(touch /tmp/x)", Page: 1}},
		{"rename unicode", "/rename  中文🐦 标题  ", InputCommand, "", Command{Name: CommandRename, Title: "中文🐦 标题", Page: 1}},
		// case: 页码从 1 起；history 的可选参数可单独或组合出现。
		{"sessions default", "/sessions", InputCommand, "", Command{Name: CommandSessions, Page: 1}},
		{"sessions page", "/sessions 2", InputCommand, "", Command{Name: CommandSessions, Page: 2}},
		{"resume selector", "/resume", InputCommand, "", Command{Name: CommandResume, Page: 1}},
		{"resume ID", "/resume abcdef01", InputCommand, "", Command{Name: CommandResume, SessionID: "abcdef01", Page: 1}},
		{"history default", "/history", InputCommand, "", Command{Name: CommandHistory, Page: 1}},
		{"history page", "/history 3", InputCommand, "", Command{Name: CommandHistory, Page: 3}},
		{"history target", "/history --session abcdef0123", InputCommand, "", Command{Name: CommandHistory, Page: 1, SessionID: "abcdef0123"}},
		{"history target and page", "/history 2 --session abcdef01", InputCommand, "", Command{Name: CommandHistory, Page: 2, SessionID: "abcdef01"}},
		{"history page after target", "/history --session abcdef01 2", InputCommand, "", Command{Name: CommandHistory, Page: 2, SessionID: "abcdef01"}},
		// case: 首批无参数命令保留各自名称。
		{"model", "/model", InputCommand, "", Command{Name: CommandModel, Page: 1}},
		{"clear", "/clear", InputCommand, "", Command{Name: CommandClear, Page: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInput(tt.text)
			if err != nil {
				t.Fatalf("ParseInput() error = %v", err)
			}
			if got.Kind != tt.kind || got.Text != tt.message || got.Command != tt.command {
				t.Fatalf("ParseInput() = %#v, want kind=%s text=%q command=%#v", got, tt.kind, tt.message, tt.command)
			}
		})
	}
}

// TestParseInputRejectsInvalidCommands 守住失败只返回结构化错误，不把指令误发为普通正文。
func TestParseInputRejectsInvalidCommands(t *testing.T) {
	tests := []struct {
		name, text, code string
	}{
		// case: 未知、大小写错误和非 ASCII 名称均不能成为已支持指令。
		{"unknown", "/not-a-command", "unknown_command"},
		{"uppercase", "/HELP", "unknown_command"},
		{"unicode command", "/帮助", "unknown_command"},
		{"model future", "/think", "unknown_command"},
		// case: 单斜杠命令禁止换行和控制字符，避免吞掉后续正文。
		{"multiline", "/new title\nordinary", "invalid_arguments"},
		{"CR", "/status\r", "invalid_arguments"},
		{"unicode line separator", "/new title\u2028next", "invalid_arguments"},
		{"NUL", "/status\x00", "invalid_arguments"},
		// case: 无参数命令与 help 不接受额外参数。
		{"bare slash args", "/ resume", "invalid_arguments"},
		{"status args", "/status arg", "invalid_arguments"},
		{"model switch", "/model other", "invalid_arguments"},
		{"help extra", "/help new extra", "invalid_arguments"},
		{"help missing target", "/help unknown", "invalid_arguments"},
		// case: 必填标题、长度和内部控制字符校验不能被命令参数绕过。
		{"rename empty", "/rename", "invalid_arguments"},
		{"rename whitespace", "/rename 　 ", "invalid_arguments"},
		{"title tab", "/new x\ty", "invalid_arguments"},
		{"title trailing tab", "/new x\t", "invalid_arguments"},
		{"rename trailing tab", "/rename x\t ", "invalid_arguments"},
		{"title too long", "/rename " + strings.Repeat("界", 121), "invalid_arguments"},
		{"title invalid UTF-8", "/new " + string([]byte{0xff}), "invalid_arguments"},
		// case: 页码必须是正整数，不接受符号、Shell 文本、重复值或溢出。
		{"zero page", "/sessions 0", "invalid_arguments"},
		{"negative page", "/sessions -1", "invalid_arguments"},
		{"signed page", "/sessions +1", "invalid_arguments"},
		{"overflow page", "/sessions 9999999999999999999999", "invalid_arguments"},
		{"repeated page", "/history 1 2", "invalid_arguments"},
		{"duplicate session", "/history --session abcdef01 --session abcdef02", "invalid_arguments"},
		{"unknown flag", "/history --bad 1", "invalid_arguments"},
		{"missing session", "/history --session", "invalid_arguments"},
		{"shell page", "/sessions $PAGE", "invalid_arguments"},
		// case: ID 只接受至少八位十六进制，不解释路径或额外参数。
		{"short ID", "/resume abcdef0", "invalid_arguments"},
		{"nonhex ID", "/resume abcdef0z", "invalid_arguments"},
		{"path ID", "/resume /tmp/file", "invalid_arguments"},
		{"resume extra", "/resume abcdef01 extra", "invalid_arguments"},
		{"history invalid ID", "/history --session 123", "invalid_arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInput(tt.text)
			var parseErr *CommandError
			if !errors.As(err, &parseErr) || parseErr.Code != tt.code {
				t.Fatalf("ParseInput() = %#v, error=%v, want code=%s", got, err, tt.code)
			}
			if got.Kind == InputMessage || got.Text != "" {
				t.Fatalf("invalid command became message: %#v", got)
			}
		})
	}
}

// TestCommandTitlesRespectUnicodeBounds 守住手工标题按字符计数、自动标题不调用模型且保留 UTF-8。
func TestCommandTitlesRespectUnicodeBounds(t *testing.T) {
	if err := ValidateTitle(strings.Repeat("🐦", 120)); err != nil {
		t.Fatalf("120 Unicode characters: %v", err)
	}
	for _, title := range []string{"", " \t", strings.Repeat("🐦", 121), "x\ny", "x\ry", "x\ty", "x\x1by", "x\u2028y", string([]byte{0xff})} {
		// case: 空白、超限及任意控制/换行标记都不能作为手工标题。
		if err := ValidateTitle(title); err == nil {
			t.Errorf("ValidateTitle(%q) accepted invalid title", title)
		}
	}
	title := AutomaticTitle("\n\t \n  " + strings.Repeat("界🐦", 40) + "  \nignored")
	if utf8.RuneCountInString(title) != 60 || !utf8.ValidString(title) || title != strings.Repeat("界🐦", 30) {
		t.Fatalf("AutomaticTitle() = %q", title)
	}
	if got := AutomaticTitle("\n\t \r\n"); got != DefaultSessionTitle {
		t.Fatalf("empty automatic title = %q", got)
	}
}
