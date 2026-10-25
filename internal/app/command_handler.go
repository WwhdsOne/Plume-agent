package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// commandState 的字段只在 Service.mu 内访问；一个服务固定一个可信身份域。
// volatile 仅保留本进程尚未命名、没有 run 的空会话，不制造磁盘空记录。
type commandState struct {
	options     CommandOptions
	management  store.ManagementStore
	key         string
	initialRef  store.ModelRef
	title       string
	manualTitle bool
	hasRun      bool // 首个起点成功后固定，失败 run 也算；不能用标题文案或成功轮数推断。
	volatile    map[string]store.SessionRecord
}

// ConfigureCommands 在入口启动时绑定命令所需的能力；关闭历史时调用方传
// 内存 Store。远端重开会恢复同域绑定，本地裸启动始终保留新会话语义。
func (s *Service) ConfigureCommands(options CommandOptions) error {
	key, err := options.Scope.Key()
	if err != nil {
		return err
	}
	if options.History == nil {
		options.History = store.NewMemory()
	}
	management, ok := options.History.(store.ManagementStore)
	if !ok {
		return errors.New("history backend does not support commands")
	}
	if options.PageSize == 0 {
		options.PageSize = 20
	}
	if options.PageSize < 1 || options.PageSize > 100 || options.Workspace == "" {
		return errors.New("invalid command options")
	}
	options.Models = append([]CommandModelInfo(nil), options.Models...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.closed || s.isClosing() || s.commands != nil {
		return errors.New("commands must be configured once while idle")
	}
	state := &commandState{options: options, management: management, key: key, initialRef: options.ModelRef,
		title: DefaultSessionTitle, volatile: make(map[string]store.SessionRecord)}
	s.commands, s.history, s.workspace, s.modelRef = state, options.History, options.Workspace, options.ModelRef
	// -c 在装配之前已经接管会话；失败 run 同样已经定名，不把下次输入当首次命名。
	if record, err := management.ResolveSession(context.Background(), state.scope(), s.sessionID); err == nil {
		state.title, state.manualTitle = record.Title, record.TitleManual
		state.hasRun = record.RunCount > 0
	} else if !errors.Is(err, store.ErrSessionNotFound) {
		return err
	}
	if key != LocalScopeKey {
		id, exists, err := management.GetBinding(context.Background(), state.scope())
		if err != nil {
			return err
		}
		if exists {
			record, err := management.ResolveSession(context.Background(), state.scope(), id)
			if err == nil {
				prepared, err := s.prepareSessionLocked(context.Background(), record, false)
				if err != nil {
					return err
				}
				s.installSessionLocked(record, prepared)
			} else if !errors.Is(err, store.ErrSessionNotFound) {
				return err
			}
			// 指向未命名空会话的绑定在进程结束后无上下文可恢复，首次消息
			// 按新会话准入；不得把这个 ID 解释成其他域的会话。
		}
	}
	state.rememberEmptyLocked(s)
	// -c 在配置指令前已接管会话；确认当前会话是否已有登记任务后再拍
	// 退出快照，避免无新任务直接退出时漏掉恢复提示。
	s.recordExitSession()
	return nil
}

func (c *commandState) scope() store.Scope {
	return store.Scope{ScopeKey: c.key, Workspace: c.options.Workspace}
}

func (c *commandState) rememberEmptyLocked(s *Service) {
	if s.session.Turns() == 0 {
		record := s.sessionRecordLocked(time.Time{})
		record.LastRunAt = time.Time{}
		c.volatile[s.sessionID] = record
	}
}

func (c *commandState) autoTitleLocked(input string) {
	if !c.manualTitle && !c.hasRun {
		c.title = AutomaticTitle(input)
	}
	c.hasRun = true
}

// HandleInput 是本地和渠道的共同入口：先识别指令，再进入普通消息的 busy
// 检查。命令收据不进入模型上下文，身份不匹配也不会发送模型请求。
func (s *Service) HandleInput(ctx context.Context, request CommandRequest) (response CommandResult) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	if request.Entrance == "" {
		request.Entrance = EntranceTUI
	}
	defer func() {
		for _, definition := range Commands(request.Entrance) {
			response.AllowedOperations = append(response.AllowedOperations, definition.Name)
		}
		if request.Entrance == EntranceGateway && response.Code != "ok" && response.Code != "cancel_requested" && response.Code != "no_active_run" {
			// 渠道错误只返回状态码与固定说明，数据库或装配错误可能含本机路径。
			response.Message = "Command failed: " + response.Code + ". Use /help for supported commands."
		}
	}()
	parsed, err := ParseInput(request.Text)
	if err != nil {
		var commandErr *CommandError
		if errors.As(err, &commandErr) {
			return commandFailure("", commandErr.Code, commandErr.Message)
		}
		return commandFailure("", "invalid_arguments", err.Error())
	}
	key, err := request.Scope.Key()
	if err != nil || (request.Entrance == EntranceGateway && request.Scope.IsLocal()) {
		return commandFailure(parsed.Command.Name, "invalid_identity", "trusted channel identity is required")
	}
	s.mu.Lock()
	if s.commands == nil || key != s.commands.key {
		s.mu.Unlock()
		return commandFailure(parsed.Command.Name, "invalid_identity", "identity does not match this service")
	}
	if parsed.Kind == InputMessage {
		s.mu.Unlock()
		id, err := s.Submit(ctx, parsed.Text)
		if err != nil {
			return commandFailure("", commandErrorCode(err), err.Error())
		}
		return CommandResult{Code: "ok", RunID: id, SessionID: s.SessionID()}
	}
	defer s.mu.Unlock()
	command := parsed.Command
	definition, ok := LookupCommand(string(command.Name))
	if !ok {
		return commandFailure(command.Name, "unknown_command", "use /help to list commands")
	}
	if !definition.Supports(request.Entrance) {
		return commandFailure(command.Name, "unsupported_entrance", "command is not supported in this entrance")
	}
	if request.RequestID == "" {
		request.RequestID = "local-" + randomHex(16)
	}
	if len(request.RequestID) > 512 {
		return commandFailure(command.Name, "invalid_arguments", "request ID exceeds limit")
	}
	fingerprint := commandFingerprint(request.Text, request.Entrance)
	old, exists, err := s.commands.management.GetReceipt(ctx, s.commands.scope(), request.RequestID)
	if err != nil {
		return commandFailure(command.Name, "storage_error", err.Error())
	}
	if exists {
		if old.Fingerprint != fingerprint {
			return commandFailure(command.Name, "request_conflict", "request ID was used with different content")
		}
		return s.replayCommandLocked(old)
	}
	if s.closed || s.isClosing() {
		return commandFailure(command.Name, "service_closed", "service is closed")
	}
	result := s.executeCommandLocked(ctx, command, request.Entrance)
	// 新建/恢复/重命名由 execute 内在同一事务提交业务变更与回执；
	// 其他结果（包含只读与 no-target 取消）同样固定第一次关联。
	return s.commitCommandLocked(ctx, request, fingerprint, result)
}

type preparedSession struct {
	runtime  *agent.Runtime
	session  *Session
	runs     []store.RunRecord
	recovery *SessionRecovery
}

// commandExecution 暂存准备好的切换；只有收据及业务事务提交确认后才安装。
type commandExecution struct {
	result   CommandResult
	mutation *store.CommandMutation
	prepared *preparedSession
	record   store.SessionRecord
	cancelID string
}

func commandFailure(name CommandName, code, message string) CommandResult {
	return CommandResult{Command: name, Code: code, Message: message}
}

func commandErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrHistoryUncertain):
		return "history_uncertain"
	case errors.Is(err, store.ErrSessionNotFound):
		return "session_not_found"
	case errors.Is(err, store.ErrAmbiguousSessionID):
		return "session_ambiguous"
	case errors.Is(err, store.ErrInvalidPage):
		return "invalid_arguments"
	default:
		return "operation_failed"
	}
}

func commandFingerprint(text string, entrance Entrance) string {
	// 编码元组避免分隔符碰撞；关联包括入口，远端不能重放 TUI 专属动作。
	encoded, _ := json.Marshal([]string{text, string(entrance)})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func replayCommand(receipt store.Receipt) CommandResult {
	var result CommandResult
	if err := json.Unmarshal(receipt.Payload, &result); err != nil {
		return commandFailure("", "corrupt_receipt", "stored command receipt is invalid")
	}
	result.Replayed = true
	// 收据重投仅复用结果，不重复执行终端动作或换回旧会话。
	result.Action, result.Switch = ActionNone, nil
	return result
}

// replayCommandLocked 补发可能在提交核查失败窗口尚未送出的取消信号。
// 只允许收据固定的 run ID 仍为当前活动任务，绝不改成取消“现在的”任务。
func (s *Service) replayCommandLocked(receipt store.Receipt) CommandResult {
	result := replayCommand(receipt)
	if result.Command == CommandCancel && result.Code == "cancel_requested" && result.RunID != "" && result.RunID == s.activeRunID && s.cancel != nil {
		s.cancel()
	}
	return result
}

func (s *Service) commitCommandLocked(ctx context.Context, request CommandRequest, fingerprint string, execution commandExecution) CommandResult {
	result := execution.result
	payload, err := json.Marshal(result)
	if err != nil {
		s.discardPrepared(execution.prepared)
		return commandFailure(result.Command, "operation_failed", "encode command result failed")
	}
	mutation := store.CommandMutation{}
	if execution.mutation != nil {
		mutation = *execution.mutation
	}
	mutation.Receipt = store.Receipt{ScopeKey: s.commands.key, Workspace: s.workspace, RequestID: request.RequestID,
		Fingerprint: fingerprint, Command: string(result.Command), Payload: payload, CreatedAt: time.Now()}
	receipt, replayed, err := s.commands.management.ApplyCommand(ctx, mutation)
	if err != nil {
		// 提交错误可能出现在提交之后。按同一关联核查，不重跑准备或业务操作。
		confirmed, exists, queryErr := s.commands.management.GetReceipt(ctx, s.commands.scope(), request.RequestID)
		if queryErr != nil {
			s.historyUncertain = true
			s.discardPrepared(execution.prepared)
			return commandFailure(result.Command, "history_uncertain", "command commit could not be confirmed; reopen history")
		}
		if !exists || confirmed.Fingerprint != fingerprint {
			s.discardPrepared(execution.prepared)
			code := "storage_error"
			if errors.Is(err, store.ErrReceiptConflict) {
				code = "request_conflict"
			}
			return commandFailure(result.Command, code, err.Error())
		}
		receipt = confirmed
	}
	if replayed {
		s.discardPrepared(execution.prepared)
		return s.replayCommandLocked(receipt)
	}
	if execution.prepared != nil {
		s.commands.rememberEmptyLocked(s)
		s.installSessionLocked(execution.record, execution.prepared)
	}
	if mutation.RenameSessionID == s.sessionID {
		s.commands.title, s.commands.manualTitle = mutation.Title, mutation.TitleManual
		delete(s.commands.volatile, s.sessionID)
	}
	if execution.cancelID != "" && execution.cancelID == s.activeRunID && s.cancel != nil {
		s.cancel()
	}
	return result
}

func (s *Service) discardPrepared(prepared *preparedSession) {
	if prepared != nil && prepared.runtime != nil {
		// 即使关闭失败，也保留句柄到整体退出收尾再试，不影响当前会话。
		if err := prepared.runtime.Close(); err != nil {
			s.retiredRuntimes = append(s.retiredRuntimes, prepared.runtime)
		}
	}
}

func (s *Service) installSessionLocked(record store.SessionRecord, prepared *preparedSession) {
	old := s.runtime
	s.runtime, s.session, s.sessionID = prepared.runtime, prepared.session, record.ID
	s.lastSaveStatus = ""
	s.sessionCreatedAt, s.modelRef = record.CreatedAt, record.ModelRef
	s.commands.title, s.commands.manualTitle = record.Title, record.TitleManual
	s.commands.hasRun = record.RunCount > 0
	// 会话身份与 hasRun 必须同时来自新会话，不能沿用切换前的登记状态。
	s.recordExitSession()
	s.commands.rememberEmptyLocked(s)
	// 切换只允许 idle；旧工具根已无人执行，应立即释放，不能让长期 TUI
	// 每次 /new 都累积一个 os.Root。关闭失败才留给整体退出期限内再试。
	if err := old.Close(); err != nil {
		s.retiredRuntimes = append(s.retiredRuntimes, old)
	}
}

// CloseRuntimes 由入口在 CloseContext 已确认执行停止后调用，覆盖会话切换
// 新建的全部工具根；不能只关闭启动时捕获的旧 runtime。
func (s *Service) CloseRuntimes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return ErrBusy
	}
	var result error
	for _, runtime := range append(s.retiredRuntimes, s.runtime) {
		result = errors.Join(result, runtime.Close())
	}
	return result
}

func (s *Service) prepareSessionLocked(ctx context.Context, record store.SessionRecord, empty bool) (*preparedSession, error) {
	if validate := s.commands.options.ValidateModel; validate != nil {
		if err := validate(record.ModelRef); err != nil {
			return nil, fmt.Errorf("resume_incompatible: %w", err)
		}
	}
	build := s.commands.options.BuildRuntime
	if build == nil {
		return nil, errors.New("runtime factory is unavailable")
	}
	prepared := &preparedSession{session: NewSession()}
	if !empty {
		messages, nextSeq, err := s.history.LoadTurns(ctx, record.ID, s.commands.options.LoadLimits)
		if err != nil {
			return nil, err
		}
		calls, err := s.history.LoadCalls(ctx, record.ID, s.commands.options.LoadLimits)
		if err != nil {
			return nil, err
		}
		prepared.runs, err = s.history.ListRuns(ctx, record.ID, s.commands.options.LoadLimits)
		if err != nil {
			return nil, err
		}
		// 与 -c 共用有界展示合并：旧中断进度保留，尾候选不越过新成功 run。
		if prepared.runs, prepared.recovery, err = LoadRecovery(ctx, s.history, record.ID, prepared.runs, s.commands.options.LoadLimits); err != nil {
			return nil, err
		}
		usage, lastCall := make(map[string]model.Usage), ""
		for _, call := range calls {
			if call.Usage != nil {
				usage[call.ID] = call.Usage.Usage()
			} else {
				usage[call.ID] = model.Usage{}
			}
			lastCall = call.ID
		}
		prepared.session = RestoreSession(messages, nextSeq-1, usage, lastCall, time.Now())
	}
	var err error
	prepared.runtime, err = build(ctx, record.ModelRef)
	if err != nil {
		s.discardPrepared(prepared)
		return nil, err
	}
	if prepared.runtime == nil || prepared.runtime == s.runtime {
		return nil, errors.New("runtime factory must return a fresh runtime")
	}
	return prepared, nil
}

// visibleSessionsLocked 只分页读取已落盘的目录；默认空会话不列出。
// 重命名或预览不更新 LastRunAt，不能抢占 -c 最近有 run 的选择。
func (s *Service) visibleSessionsLocked(ctx context.Context, page int) (*SessionList, error) {
	size := s.commands.options.PageSize
	stored, err := s.commands.management.ListSessions(ctx, s.commands.scope(), store.PageRequest{Page: page, PageSize: size})
	if err != nil {
		return nil, err
	}
	list := &SessionList{Page: page, PageSize: size, Total: stored.Total}
	for _, record := range stored.Sessions {
		list.Items = append(list.Items, s.sessionSummary(record))
	}
	return list, nil
}

func (s *Service) sessionSummary(record store.SessionRecord) SessionSummary {
	summary := SessionSummary{ID: record.ID, Title: record.Title, Provider: record.ModelRef.Provider, Model: record.ModelRef.Model,
		CreatedAt: record.CreatedAt, LastRunAt: record.LastRunAt, RunCount: record.RunCount, TurnCount: record.TurnCount, Resumable: true}
	if validate := s.commands.options.ValidateModel; validate != nil {
		if err := validate(record.ModelRef); err != nil {
			summary.Resumable, summary.Reason = false, "resume_incompatible"
		}
	}
	return summary
}

func (s *Service) displayModel(ref store.ModelRef) CommandModelInfo {
	if describe := s.commands.options.DescribeModel; describe != nil {
		return describe(ref)
	}
	for _, info := range s.commands.options.Models {
		if info.ID == ref.ConfigID {
			return info
		}
	}
	return CommandModelInfo{ID: ref.ConfigID, Provider: ref.Provider, Model: ref.Model}
}

func (s *Service) resolveSessionLocked(ctx context.Context, id string) (store.SessionRecord, bool, error) {
	if id == "" {
		id = s.sessionID
	}
	record, err := s.commands.management.ResolveSession(ctx, s.commands.scope(), id)
	if err != nil && !errors.Is(err, store.ErrSessionNotFound) {
		return record, false, err
	}
	var matches []store.SessionRecord
	for candidateID, candidate := range s.commands.volatile {
		if len(id) >= 8 && len(candidateID) >= len(id) && candidateID[:len(id)] == id {
			matches = append(matches, candidate)
		}
	}
	if err == nil {
		for _, candidate := range matches {
			if candidate.ID != record.ID {
				return store.SessionRecord{}, false, store.ErrAmbiguousSessionID
			}
		}
		return record, false, nil
	}
	if len(matches) != 1 {
		if len(matches) > 1 {
			return store.SessionRecord{}, false, store.ErrAmbiguousSessionID
		}
		return store.SessionRecord{}, false, store.ErrSessionNotFound
	}
	return matches[0], true, nil
}

// 对候选排序不依赖 map 遍历，便于入口稳定显示与固定回执。
func sortedCommandModels(models []CommandModelInfo) []CommandModelInfo {
	copyModels := append([]CommandModelInfo(nil), models...)
	sort.SliceStable(copyModels, func(i, j int) bool { return copyModels[i].ID < copyModels[j].ID })
	return copyModels
}
