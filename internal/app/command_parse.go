package app

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// InputKind 区分可提交模型的正文和只交给管理执行层的指令。
type InputKind string

// 解析失败不返回 InputMessage，防止错误指令变成付费模型输入。
const (
	InputMessage InputKind = "message"
	InputCommand InputKind = "command"
)

// Command 保存已归一化的参数；Page 默认 1，空 SessionID 表示当前会话或选择器。
type Command struct {
	Name        CommandName
	Title       string
	SessionID   string
	Page        int
	HelpCommand string
}

// ParsedInput 只在 InputMessage 时携带 Text，指令正文不会混入模型上下文。
type ParsedInput struct {
	Kind    InputKind
	Text    string
	Command Command
}

// CommandError 保留稳定机器错误码，入口负责渲染具体提示。
type CommandError struct {
	Code    string
	Message string
}

func (e *CommandError) Error() string { return e.Message }

// DefaultSessionTitle 是尚未接收普通输入或手工命名的会话标题。
const DefaultSessionTitle = "New conversation"

// ParseInput 仅解析本次顶层输入，不执行 Shell、环境展开或任何副作用。
func ParseInput(text string) (ParsedInput, error) {
	input := strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(input, "/") {
		return ParsedInput{Kind: InputMessage, Text: text}, nil
	}
	if strings.HasPrefix(input, "//") {
		// 转义早于单行/指令校验且直接返回，禁止再次把转义后的文本送回解析器。
		prefixLen := len(text) - len(input)
		return ParsedInput{Kind: InputMessage, Text: text[:prefixLen] + input[1:]}, nil
	}
	parsed := ParsedInput{Kind: InputCommand, Command: Command{Page: 1}}
	if !utf8.ValidString(input) {
		return parsed, invalidCommandArguments("command must contain valid UTF-8")
	}
	for _, character := range input {
		if isCommandLineBreak(character) || (unicode.IsControl(character) && character != '\t') {
			return parsed, invalidCommandArguments("command must be a single line without control characters")
		}
	}
	name, rawArgs := input[1:], ""
	if separator := strings.IndexAny(name, " \t"); separator >= 0 {
		rawArgs = strings.TrimLeft(name[separator+1:], " \t")
		name = name[:separator]
	}
	args := strings.Trim(rawArgs, " \t")
	if name == "" {
		if args != "" {
			return parsed, invalidCommandArguments("use /help [command]")
		}
		name = string(CommandHelp)
	}
	parsed.Command.Name = CommandName(name)
	definition, exists := LookupCommand(name)
	if !exists {
		return parsed, &CommandError{Code: "unknown_command", Message: "unknown command; use /help"}
	}
	parsed.Command.Name = definition.Name
	var err error
	switch definition.Name {
	case CommandHelp:
		if args != "" {
			fields := commandArgumentFields(args)
			if len(fields) != 1 {
				err = invalidCommandArguments("use /help [command]")
				break
			}
			target, ok := LookupCommand(strings.TrimPrefix(fields[0], "/"))
			if !ok {
				err = invalidCommandArguments("unknown help target; use /help")
				break
			}
			parsed.Command.HelpCommand = string(target.Name)
		}
	case CommandNew, CommandRename:
		// 参数开头的空格/Tab 是分隔符，标题末尾 Tab 却仍是禁止的控制字符。
		title := strings.TrimRight(rawArgs, " ")
		if title == "" && definition.Name == CommandNew {
			break
		}
		err = ValidateTitle(title)
		if err == nil {
			parsed.Command.Title = title
		}
	case CommandSessions:
		if args != "" {
			parsed.Command.Page, err = parseCommandPage(args)
		}
	case CommandResume:
		if args != "" {
			err = validateCommandSessionID(args)
			if err == nil {
				parsed.Command.SessionID = strings.ToLower(args)
			}
		}
	case CommandHistory:
		err = parseHistoryArguments(args, &parsed.Command)
	default:
		if args != "" {
			err = invalidCommandArguments("command does not accept arguments")
		}
	}
	return parsed, err
}

// ValidateTitle 校验手工标题的 Unicode 字符数与终端控制边界。
func ValidateTitle(title string) error {
	if !utf8.ValidString(title) {
		return invalidCommandArguments("title must contain valid UTF-8")
	}
	count := utf8.RuneCountInString(title)
	if count < 1 || count > 120 || strings.TrimSpace(title) == "" {
		return invalidCommandArguments("title must contain 1 to 120 Unicode characters")
	}
	for _, character := range title {
		if unicode.IsControl(character) || isCommandLineBreak(character) {
			return invalidCommandArguments("title must not contain control characters or line breaks")
		}
	}
	return nil
}

// AutomaticTitle 取首个非空行的前 60 个 Unicode 字符，不发起模型请求。
func AutomaticTitle(text string) string {
	for _, line := range strings.FieldsFunc(text, isCommandLineBreak) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		characters := []rune(line)
		if len(characters) > 60 {
			characters = characters[:60]
		}
		return string(characters)
	}
	return DefaultSessionTitle
}

// commandArgumentFields 只把 ASCII 空格与 Tab 作为参数分隔符，不执行 Shell 引号或展开。
func commandArgumentFields(args string) []string {
	return strings.FieldsFunc(args, func(character rune) bool { return character == ' ' || character == '\t' })
}

func invalidCommandArguments(message string) *CommandError {
	return &CommandError{Code: "invalid_arguments", Message: message}
}

// isCommandLineBreak 包含 Unicode 行/段分隔符，单行契约不能只拦截 ASCII 换行。
func isCommandLineBreak(character rune) bool {
	return character == '\n' || character == '\r' || character == '\u2028' || character == '\u2029'
}

func parseCommandPage(text string) (int, error) {
	for _, character := range text {
		if character < '0' || character > '9' {
			return 0, invalidCommandArguments("page must be a positive integer")
		}
	}
	page, err := strconv.Atoi(text)
	if err != nil || page < 1 {
		return 0, invalidCommandArguments("page must be a positive integer")
	}
	return page, nil
}

// validateCommandSessionID 只做语法校验；唯一性必须由执行层在可见集合内解析。
func validateCommandSessionID(id string) error {
	if len(id) < 8 {
		return invalidCommandArguments("session ID must contain at least 8 hexadecimal characters")
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return invalidCommandArguments("session ID must contain at least 8 hexadecimal characters")
		}
	}
	return nil
}

func parseHistoryArguments(args string, command *Command) error {
	fields := commandArgumentFields(args)
	pageSet, sessionSet := false, false
	for i := 0; i < len(fields); i++ {
		if fields[i] == "--session" {
			if sessionSet || i+1 >= len(fields) {
				return invalidCommandArguments("use /history [page] [--session <id>]")
			}
			i++
			if err := validateCommandSessionID(fields[i]); err != nil {
				return err
			}
			command.SessionID = strings.ToLower(fields[i])
			sessionSet = true
			continue
		}
		if pageSet {
			return invalidCommandArguments("use /history [page] [--session <id>]")
		}
		page, err := parseCommandPage(fields[i])
		if err != nil {
			return err
		}
		command.Page, pageSet = page, true
	}
	return nil
}
