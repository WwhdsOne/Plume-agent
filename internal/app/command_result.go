package app

import (
	"context"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/store"
)

// CommandRequest 只接受入口提供的可信身份；Text 内自称的身份不参与授权。
// RequestID 是渠道消息关联，本地入口可留空，由应用层生成独立关联。
type CommandRequest struct {
	Text      string
	RequestID string
	Scope     Scope
	Entrance  Entrance
}

// CommandAction 表达界面操作，应用层不直接清屏、退出或打开终端选择器。
type CommandAction string

// 可展示动作与业务命令分离，gateway 不消费 TUI 专属动作。
const (
	ActionNone          CommandAction = ""
	ActionClear         CommandAction = "clear"
	ActionExit          CommandAction = "exit"
	ActionSelectSession CommandAction = "select_session"
	ActionSwitchSession CommandAction = "switch_session"
)

// CommandModelInfo 是当前入口获准展示的模型信息，不包含端点和凭据引用。
type CommandModelInfo struct {
	ID                  string
	Provider            string
	Model               string
	Reasoning           string
	ContextWindowTokens *int64
}

// SessionSummary 是经过身份过滤后的目录条目；短 ID 仅用于展示。
type SessionSummary struct {
	ID, Title, Model, Provider string
	CreatedAt, LastRunAt       time.Time
	RunCount, TurnCount        int
	Resumable                  bool
	Reason                     string
}

// SessionList 给入口提供分页元数据，避免把整个目录一次发送给用户。
type SessionList struct {
	Items                 []SessionSummary
	Page, PageSize, Total int
}

// HistoryList 只包含指定页已提交的展示记录；远端结果不含 Thought 和工具详情。
type HistoryList struct {
	SessionID             string
	Runs                  []store.RunRecord
	Page, PageSize, Total int
}

// CommandStatusInfo 是当前会话只读快照，远端同样不包含本机目录或端点。
type CommandStatusInfo struct {
	CreatedAt            time.Time
	CompletedRunCount    int
	CompletedRunDuration time.Duration
	SessionID            string
	Title                string
	Running              bool
	RunID                string
	Model                CommandModelInfo
	Turns                int
	Stats                SessionStats
	Persistence          string
	SaveStatus           string
}

// RecoveryToolView 是恢复展示的工具条目；Unconfirmed 表示只有准备/运行
// 记录而无完成结果，不能猜成未执行或已成功。
type RecoveryToolView struct {
	Name, Status, Summary string
	Unconfirmed           bool
}

// SessionRecovery 是恢复会话的最后未完成任务展示信息：合并最后有效检查点
// 与终态记录后的正文/Thought/工具摘要，以及回填候选输入。它只服务展示与
// 输入候选，不进入模型历史；恢复成功本身不产生模型或工具调用。
type SessionRecovery struct {
	LastRunID   string
	LastStatus  store.RunStatus // 仅 failed/cancelled/interrupted 提供
	Reply       string
	Reasoning   string
	Tools       []RecoveryToolView
	ResendInput string // 最后未完成 run 的已接受输入；TUI 转义后作为 ↑ 候选
}

// SessionSnapshot 只在切换成功后交给 TUI；保存失败不会先清空原界面。
// Resumed 区分恢复（展示恢复提示与回填候选）与新建（清空转录即可）。
type SessionSnapshot struct {
	SessionID string
	Model     CommandModelInfo
	Stats     SessionStats
	Turns     int
	Runs      []store.RunRecord
	Recovery  *SessionRecovery
	Resumed   bool
}

// CommandResult 是共享处理器的结构化回执；Code 为稳定英文状态码。
// 指令回执不进入模型上下文，Replayed 表示复用既有收据而没有再次执行操作。
type CommandResult struct {
	Command           CommandName
	Code              string
	Message           string
	RunID             string
	SessionID         string
	Action            CommandAction
	Sessions          *SessionList
	History           *HistoryList
	Status            *CommandStatusInfo
	Models            []CommandModelInfo
	Help              []CommandDefinition
	Switch            *SessionSnapshot
	Replayed          bool
	AllowedOperations []CommandName
}

// CommandOptions 是入口在启动时注入的管理能力，不读取或热加载全局配置。
// BuildRuntime 每次返回新的工具状态；ValidateModel 只校验兼容引用，不联网。
type CommandOptions struct {
	History       store.Store
	Scope         Scope
	Workspace     string
	ModelRef      store.ModelRef
	PageSize      int
	LoadLimits    store.LoadLimits
	Persistent    bool
	Models        []CommandModelInfo
	BuildRuntime  func(context.Context, store.ModelRef) (*agent.Runtime, error)
	ValidateModel func(store.ModelRef) error
	DescribeModel func(store.ModelRef) CommandModelInfo
}
