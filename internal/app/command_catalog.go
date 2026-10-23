package app

// Entrance 标识已完成输入适配的入口能力，不代表身份或权限。
type Entrance string

// 首批入口仅声明 TUI 与可信 gateway 的公共语义。
const (
	EntranceTUI     Entrance = "tui"
	EntranceGateway Entrance = "gateway"
)

// CommandName 是执行层使用的正式指令名，别名只在解析阶段存在。
type CommandName string

// 首批指令名对应已授权的管理与本地操作。
const (
	CommandHelp     CommandName = "help"
	CommandNew      CommandName = "new"
	CommandSessions CommandName = "sessions"
	CommandResume   CommandName = "resume"
	CommandRename   CommandName = "rename"
	CommandHistory  CommandName = "history"
	CommandStatus   CommandName = "status"
	CommandModel    CommandName = "model"
	CommandCancel   CommandName = "cancel"
	CommandClear    CommandName = "clear"
	CommandExit     CommandName = "exit"
)

// CommandDefinition 是帮助和补全共享的纯数据定义；入口限制仍由执行层检查。
type CommandDefinition struct {
	Name        CommandName
	Aliases     []string
	Usage       string
	Description string
	Entrances   []Entrance
	ReadOnly    bool
}

// Supports 报告指定入口是否声明支持这条指令。
func (d CommandDefinition) Supports(entrance Entrance) bool {
	for _, supported := range d.Entrances {
		if supported == entrance {
			return true
		}
	}
	return false
}

var commandDefinitions = []CommandDefinition{
	{Name: CommandHelp, Usage: "/help [command]", Description: "Show supported commands and usage", Entrances: []Entrance{EntranceTUI, EntranceGateway}, ReadOnly: true},
	{Name: CommandNew, Usage: "/new [title]", Description: "Create and switch to a new conversation", Entrances: []Entrance{EntranceTUI, EntranceGateway}},
	{Name: CommandSessions, Usage: "/sessions [page]", Description: "List conversations in the current scope and workspace", Entrances: []Entrance{EntranceTUI, EntranceGateway}, ReadOnly: true},
	{Name: CommandResume, Usage: "/resume [id]", Description: "Select or resume a saved conversation", Entrances: []Entrance{EntranceTUI, EntranceGateway}},
	{Name: CommandRename, Usage: "/rename <title>", Description: "Rename the current conversation", Entrances: []Entrance{EntranceTUI, EntranceGateway}},
	{Name: CommandHistory, Usage: "/history [page] [--session <id>]", Description: "View saved runs without switching conversation", Entrances: []Entrance{EntranceTUI, EntranceGateway}, ReadOnly: true},
	{Name: CommandStatus, Usage: "/status", Description: "Show current conversation and run status", Entrances: []Entrance{EntranceTUI, EntranceGateway}, ReadOnly: true},
	{Name: CommandModel, Usage: "/model", Description: "View the current model and visible model references", Entrances: []Entrance{EntranceTUI, EntranceGateway}, ReadOnly: true},
	{Name: CommandCancel, Aliases: []string{"stop"}, Usage: "/cancel (alias: /stop)", Description: "Request cancellation of the current run", Entrances: []Entrance{EntranceTUI, EntranceGateway}},
	{Name: CommandClear, Usage: "/clear", Description: "Redraw the TUI while preserving conversation history", Entrances: []Entrance{EntranceTUI}},
	{Name: CommandExit, Aliases: []string{"quit"}, Usage: "/exit (alias: /quit)", Description: "Exit the TUI and close its resources", Entrances: []Entrance{EntranceTUI}},
}

// Commands 返回指定入口的定义快照，不暴露可变的共享目录。
func Commands(entrance Entrance) []CommandDefinition {
	var result []CommandDefinition
	for _, definition := range commandDefinitions {
		if definition.Supports(entrance) {
			result = append(result, cloneCommandDefinition(definition))
		}
	}
	return result
}

// LookupCommand 严格反查正式名或别名，不做大小写折叠。
func LookupCommand(name string) (CommandDefinition, bool) {
	for _, definition := range commandDefinitions {
		if string(definition.Name) == name {
			return cloneCommandDefinition(definition), true
		}
		for _, alias := range definition.Aliases {
			if alias == name {
				return cloneCommandDefinition(definition), true
			}
		}
	}
	return CommandDefinition{}, false
}

// cloneCommandDefinition 防止帮助或来源提供器修改切片后影响公共解析目录。
func cloneCommandDefinition(definition CommandDefinition) CommandDefinition {
	definition.Aliases = append([]string(nil), definition.Aliases...)
	definition.Entrances = append([]Entrance(nil), definition.Entrances...)
	return definition
}
