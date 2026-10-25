package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"plume-agent/internal/store"
)

// executeCommandLocked 只准备结果与变更；状态切换和取消副作用由收据提交后
// 的 commitCommandLocked 执行。调用方持有同一准入锁，保证固定取消目标。
func (s *Service) executeCommandLocked(ctx context.Context, command Command, entrance Entrance) commandExecution {
	result := CommandResult{Command: command.Name, Code: "ok", SessionID: s.sessionID}
	fail := func(code, message string) commandExecution {
		return commandExecution{result: commandFailure(command.Name, code, message)}
	}
	if command.Name == CommandNew || command.Name == CommandResume || command.Name == CommandRename {
		if s.historyUncertain {
			return fail("history_uncertain", "reopen history before switching sessions")
		}
		if s.busy {
			return fail("busy", "a run is in progress; use /cancel first")
		}
	}
	switch command.Name {
	case CommandHelp:
		if command.HelpCommand != "" {
			definition, exists := LookupCommand(command.HelpCommand)
			if !exists || !definition.Supports(entrance) {
				return fail("unknown_command", "use /help to list supported commands")
			}
			result.Help = []CommandDefinition{definition}
		} else {
			result.Help = Commands(entrance)
		}
	case CommandSessions:
		list, err := s.visibleSessionsLocked(ctx, command.Page)
		if err != nil {
			return fail(commandErrorCode(err), err.Error())
		}
		result.Sessions = list
	case CommandResume:
		if command.SessionID == "" {
			list, err := s.visibleSessionsLocked(ctx, command.Page)
			if err != nil {
				return fail(commandErrorCode(err), err.Error())
			}
			result.Sessions = list
			if entrance == EntranceTUI {
				result.Action = ActionSelectSession
			} else {
				result.Message = "Use /resume <id> to continue a visible session."
			}
			break
		}
		record, empty, err := s.resolveSessionLocked(ctx, command.SessionID)
		if err != nil {
			return fail(commandErrorCode(err), "session is unavailable in this scope")
		}
		if record.ID == s.sessionID {
			result.Message = "Session is already current."
			break
		}
		prepared, err := s.prepareSessionLocked(ctx, record, empty)
		if err != nil {
			code := commandErrorCode(err)
			if strings.Contains(err.Error(), "resume_incompatible") {
				code = "resume_incompatible"
			}
			return fail(code, "cannot resume session: "+err.Error())
		}
		result.SessionID, result.Message = record.ID, "Session resumed."
		if entrance == EntranceTUI {
			result.Action = ActionSwitchSession
			result.Switch = &SessionSnapshot{SessionID: record.ID, Model: s.displayModel(record.ModelRef),
				Stats: prepared.session.Statistics(), Turns: prepared.session.Turns(), Runs: prepared.runs, Recovery: prepared.recovery, Resumed: true}
		}
		return commandExecution{result: result, record: record, prepared: prepared, mutation: &store.CommandMutation{BindSessionID: record.ID}}
	case CommandNew:
		title, manual := DefaultSessionTitle, false
		if command.Title != "" {
			title, manual = command.Title, true
		}
		record := store.SessionRecord{ID: newSessionID(), ScopeKey: s.commands.key, Workspace: s.workspace,
			Title: title, TitleManual: manual, ModelRef: s.commands.initialRef, CreatedAt: time.Now()}
		prepared, err := s.prepareSessionLocked(ctx, record, true)
		if err != nil {
			return fail("operation_failed", "cannot create session: "+err.Error())
		}
		mutation := &store.CommandMutation{BindSessionID: record.ID}
		if manual {
			mutation.CreateSession = &record
		}
		result.SessionID, result.Message = record.ID, "New session started."
		if entrance == EntranceTUI {
			result.Action = ActionSwitchSession
			result.Switch = &SessionSnapshot{SessionID: record.ID, Model: s.displayModel(record.ModelRef), Stats: prepared.session.Statistics()}
		}
		return commandExecution{result: result, record: record, prepared: prepared, mutation: mutation}
	case CommandRename:
		mutation := &store.CommandMutation{RenameSessionID: s.sessionID, Title: command.Title, TitleManual: true}
		if _, err := s.commands.management.ResolveSession(ctx, s.commands.scope(), s.sessionID); errors.Is(err, store.ErrSessionNotFound) {
			record := s.sessionRecordLocked(time.Time{})
			record.Title, record.TitleManual, record.LastRunAt = command.Title, true, time.Time{}
			mutation.CreateSession = &record
		} else if err != nil {
			return fail("storage_error", err.Error())
		}
		result.Message = "Session renamed."
		return commandExecution{result: result, mutation: mutation}
	case CommandHistory:
		record, empty, err := s.resolveSessionLocked(ctx, command.SessionID)
		if err != nil {
			return fail(commandErrorCode(err), "session is unavailable in this scope")
		}
		page := store.RunPage{Page: command.Page, PageSize: s.commands.options.PageSize}
		if !empty {
			page, err = s.commands.management.PageRuns(ctx, s.commands.scope(), record.ID,
				store.PageRequest{Page: command.Page, PageSize: s.commands.options.PageSize})
			if err != nil {
				return fail(commandErrorCode(err), err.Error())
			}
		}
		if entrance == EntranceGateway {
			page.Runs = gatewayHistory(page.Runs)
		}
		result.History = &HistoryList{SessionID: record.ID, Runs: page.Runs, Page: page.Page, PageSize: page.PageSize, Total: page.Total}
	case CommandStatus:
		timing, err := s.commands.management.LoadTiming(ctx, s.commands.scope(), s.sessionID)
		if err != nil && !errors.Is(err, store.ErrSessionNotFound) {
			return fail("storage_error", err.Error())
		}
		persistence := "memory"
		if s.commands.options.Persistent {
			persistence = "persistent"
		}
		if s.historyUncertain {
			persistence = "unknown"
		}
		result.Status = &CommandStatusInfo{CreatedAt: s.sessionCreatedAt, CompletedRunCount: timing.CompletedRuns, CompletedRunDuration: timing.CompletedDuration,
			SessionID: s.sessionID, Title: s.commands.title, Running: s.busy, RunID: s.activeRunID,
			Model: s.displayModel(s.modelRef), Turns: s.session.Turns(), Stats: s.session.Statistics(), Persistence: persistence, SaveStatus: s.lastSaveStatus}
	case CommandModel:
		result.Models = sortedCommandModels(s.commands.options.Models)
		result.Message = "Current model: " + s.displayModel(s.modelRef).ID + " (read-only)."
	case CommandCancel:
		if s.activeRunID == "" || s.cancel == nil {
			result.Code, result.Message = "no_active_run", "No active run."
		} else {
			result.Code, result.Message, result.RunID = "cancel_requested", "Cancellation requested.", s.activeRunID
		}
		return commandExecution{result: result, cancelID: result.RunID}
	case CommandClear:
		result.Action = ActionClear
	case CommandExit:
		result.Action = ActionExit
	default:
		return fail("unknown_command", "use /help to list commands")
	}
	return commandExecution{result: result}
}

// gatewayHistory 控制每页平台正文摘要，不主动外发 Thought 或工具载荷。
// 正文自身包含的敏感信息不在这里做语义猜测或改写。
func gatewayHistory(runs []store.RunRecord) []store.RunRecord {
	out := append([]store.RunRecord(nil), runs...)
	for i := range out {
		out[i].Reasoning, out[i].ToolEvents = "", nil
		out[i].Input, out[i].Reply = historyExcerpt(out[i].Input, 2000), historyExcerpt(out[i].Reply, 2000)
		if out[i].Error != "" {
			// 执行诊断可能包含人格/凭据文件路径或服务端地址；远端只给终态。
			out[i].Error = "Run ended with an error."
		}
	}
	return out
}

func historyExcerpt(text string, limit int) string {
	characters := []rune(text)
	if len(characters) > limit {
		return string(characters[:limit]) + "…"
	}
	return text
}
