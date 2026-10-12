package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// failStore 在指定方法注入一次失败，验证 app 层的存储失败语义；
// 各开关在 Submit 前设置，run goroutine 内只读，无并发写。
type failStore struct {
	store.Store
	failStartRun    bool
	failStartCall   bool
	failSaveUsage   bool
	failCompleteRun bool
}

func (f *failStore) StartRun(ctx context.Context, session store.SessionRecord, run store.RunRecord) error {
	if f.failStartRun {
		return errors.New("injected start failure")
	}
	return f.Store.StartRun(ctx, session, run)
}

func (f *failStore) StartModelCall(ctx context.Context, call store.ModelCallRecord) error {
	if f.failStartCall {
		return errors.New("injected call failure")
	}
	return f.Store.StartModelCall(ctx, call)
}

func (f *failStore) SaveUsage(ctx context.Context, callID string, usage store.StoredUsage, at time.Time) error {
	if f.failSaveUsage {
		return errors.New("injected usage failure")
	}
	return f.Store.SaveUsage(ctx, callID, usage, at)
}

func (f *failStore) CompleteRun(ctx context.Context, run store.RunRecord, turn []model.Message) error {
	if f.failCompleteRun {
		return errors.New("injected complete failure")
	}
	return f.Store.CompleteRun(ctx, run, turn)
}

// newPersistedService 构造绑定持久层的 Service 与其 MemoryStore 底座。
func newPersistedService(t *testing.T, fake model.Client) (*Service, *store.MemoryStore) {
	t.Helper()
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	svc := NewService(agent.New(fake, "m"))
	svc.EnablePersistence(history, "/ws", store.ModelRef{ConfigID: "cfg-1", Provider: "p", Protocol: "openai-chat-completions", Model: "m1"}, time.Second)
	return svc, history
}

// terminal 从事件列表取出唯一终态事件。
func terminal(t *testing.T, events []Event) Event {
	t.Helper()
	last := events[len(events)-1]
	if last.Kind != EventRunCompleted && last.Kind != EventRunFailed {
		t.Fatalf("last event kind = %s, want terminal", last.Kind)
	}
	return last
}

// TestPersistenceSuccessCommitsRunAndTurns 守住成功闭环：起点、调用与用量先落盘，
// 终态 completed 后 LoadTurns 与内存历史一致（成功轮次才进 turns）。
func TestPersistenceSuccessCommitsRunAndTurns(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message:      model.Message{Role: model.RoleAssistant, Content: "answer one"},
		FinishReason: model.FinishStop,
		Usage:        model.Usage{OK: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}})
	svc, history := newPersistedService(t, fake)
	defer svc.Close()

	runID, err := svc.Submit(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Submit error = %v", err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunCompleted || last.Reply != "answer one" {
		t.Fatalf("terminal event = %+v", last)
	}

	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %v, %v", runs, err)
	}
	if runs[0].Status != store.RunCompleted || runs[0].Input != "hello" || runs[0].ID != runID {
		t.Fatalf("run record = %+v", runs[0])
	}
	calls, err := history.LoadCalls(context.Background(), svc.SessionID())
	if err != nil || len(calls) != 1 {
		t.Fatalf("LoadCalls = %v, %v", calls, err)
	}
	if calls[0].Usage == nil || calls[0].Usage.TotalTokens != 15 {
		t.Fatalf("stored usage = %+v", calls[0].Usage)
	}
	messages, turnSeq, err := history.LoadTurns(context.Background(), svc.SessionID())
	if err != nil || turnSeq != 2 {
		t.Fatalf("LoadTurns seq = %d, err = %v", turnSeq, err)
	}
	// 成功轮次恰好两条：user + assistant；失败/取消轮不在此处。
	if len(messages) != 2 || messages[0].Content != "hello" || messages[1].Content != "answer one" {
		t.Fatalf("persisted turn = %+v", messages)
	}
	if inMemory := svc.Session().History(); len(inMemory) != 2 {
		t.Fatalf("in-memory history = %d messages", len(inMemory))
	}
}

// TestPersistenceSQLiteEndToEnd 用真实 SQLite 后端走一遍成功与失败轮次，
// 验证 EnablePersistence 不依赖内存后端的特殊行为。
func TestPersistenceSQLiteEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	history, err := store.OpenSQLite(path)
	if err != nil {
		t.Skipf("sqlite unavailable in this environment: %v", err)
	}
	t.Cleanup(func() { _ = history.Close() })

	fake := model.NewFake(
		model.FakeScript{Err: errors.New("boom")},
		model.FakeScript{Response: &model.ChatResponse{
			Message:      model.Message{Role: model.RoleAssistant, Content: "recovered"},
			FinishReason: model.FinishStop,
		}},
	)
	svc := NewService(agent.New(fake, "m"))
	defer svc.Close()
	svc.EnablePersistence(history, "/ws", store.ModelRef{ConfigID: "cfg-1"}, time.Second)

	if _, err := svc.Submit(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed {
		t.Fatalf("first run kind = %s, want failed", last.Kind)
	}
	if _, err := svc.Submit(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if last := terminal(t, drainEvents(t, svc)); last.Kind != EventRunCompleted {
		t.Fatalf("second run kind = %s, want completed", last.Kind)
	}

	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListRuns = %v, %v", runs, err)
	}
	if runs[0].Status != store.RunFailed || runs[1].Status != store.RunCompleted {
		t.Fatalf("run statuses = %s, %s", runs[0].Status, runs[1].Status)
	}
	messages, turnSeq, err := history.LoadTurns(context.Background(), svc.SessionID())
	if err != nil || turnSeq != 2 || len(messages) != 2 {
		t.Fatalf("turns = %d messages seq %d err %v; only the successful run persists", len(messages), turnSeq, err)
	}
	if messages[0].Content != "second" {
		t.Fatalf("persisted user input = %q", messages[0].Content)
	}
}

// TestPersistenceStartRunFailureRejectsSubmit 守住起点语义：StartRun 失败时
// Submit 返回错误、不发送模型请求、不占用执行权。
func TestPersistenceStartRunFailureRejectsSubmit(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "x"}}})
	history := &failStore{Store: store.NewMemory(), failStartRun: true}
	t.Cleanup(func() { _ = history.Close() })
	svc := NewService(agent.New(fake, "m"))
	defer svc.Close()
	svc.EnablePersistence(history, "/ws", store.ModelRef{}, time.Second)

	if _, err := svc.Submit(context.Background(), "hello"); err == nil {
		t.Fatal("Submit must fail when StartRun fails")
	}
	if svc.Busy() {
		t.Fatal("failed start must not hold the run slot")
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("model was called %d times after storage failure", len(calls))
	}
}

// TestPersistenceStartModelCallFailureSkipsRequest 守住调用起点语义：写入失败
// 时取消整个 run，模型请求不发出，终态 failed 落盘且无 turns。
func TestPersistenceStartModelCallFailureSkipsRequest(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "x"}}})
	svc, history := newPersistedService(t, fake)
	failing := &failStore{Store: history, failStartCall: true}
	svc.EnablePersistence(failing, "/ws", store.ModelRef{}, time.Second)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed || last.Err == nil {
		t.Fatalf("terminal event = %+v, want failed with storage error", last)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("model was called %d times; request must not be sent", len(calls))
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunFailed {
		t.Fatalf("runs = %v err = %v; want single failed record", runs, err)
	}
	if _, turnSeq, _ := history.LoadTurns(context.Background(), svc.SessionID()); turnSeq != 1 {
		t.Fatalf("turnSeq = %d; failed run must not persist turns", turnSeq)
	}
	if svc.Session().Turns() != 0 {
		t.Fatal("in-memory history must stay empty")
	}
}

// TestPersistenceSaveUsageFailureFailsRun 守住用量快照语义：保存失败中止
// 后续步骤，run 落盘 failed，内存历史不追加。
func TestPersistenceSaveUsageFailureFailsRun(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message:      model.Message{Role: model.RoleAssistant, Content: "x"},
		FinishReason: model.FinishStop,
		Usage:        model.Usage{OK: true, TotalTokens: 7},
	}})
	svc, history := newPersistedService(t, fake)
	svc.EnablePersistence(&failStore{Store: history, failSaveUsage: true}, "/ws", store.ModelRef{}, time.Second)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed {
		t.Fatalf("terminal kind = %s, want failed", last.Kind)
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunFailed {
		t.Fatalf("runs = %v err = %v", runs, err)
	}
	if svc.Session().Turns() != 0 {
		t.Fatal("in-memory history must stay empty on usage save failure")
	}
}

// TestPersistenceCompleteRunFailureDegradesEvent 守住终态事务语义：成功轮次
// 落盘失败时事件降级为 failed，不得伪称 completed。
func TestPersistenceCompleteRunFailureDegradesEvent(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message:      model.Message{Role: model.RoleAssistant, Content: "done"},
		FinishReason: model.FinishStop,
	}})
	svc, history := newPersistedService(t, fake)
	svc.EnablePersistence(&failStore{Store: history, failCompleteRun: true}, "/ws", store.ModelRef{}, time.Second)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed || last.Err == nil {
		t.Fatalf("terminal event = %+v, want degraded failed", last)
	}
	if svc.Session().Turns() != 0 {
		t.Fatal("complete-failure must not append in-memory history")
	}
}

// TestPersistenceCancelRecordsCancelled 守住取消语义：取消的 run 终态
// cancelled 落盘、不产生 turns，进程退出收尾不依赖已取消的请求 context。
func TestPersistenceCancelRecordsCancelled(t *testing.T) {
	blocking := newBlockingFake()
	svc, history := newPersistedService(t, blocking)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	<-blocking.started
	svc.Cancel()
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed {
		t.Fatalf("terminal kind = %s, want failed (cancelled)", last.Kind)
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunCancelled {
		t.Fatalf("runs = %v err = %v; want cancelled", runs, err)
	}
	if _, turnSeq, _ := history.LoadTurns(context.Background(), svc.SessionID()); turnSeq != 1 {
		t.Fatalf("turnSeq = %d; cancelled run must not persist turns", turnSeq)
	}
}

// TestAdoptSessionContinuesTurnSequence 守住恢复语义：旧进程落盘成功轮次后，
// 新进程按 LoadTurns/LoadCalls 恢复会话，新 run 归属同一会话且 turnSeq
// 从恢复值继续递增，不覆盖旧轮次。
func TestAdoptSessionContinuesTurnSequence(t *testing.T) {
	fake := model.NewFake(
		model.FakeScript{Response: &model.ChatResponse{
			Message:      model.Message{Role: model.RoleAssistant, Content: "past answer"},
			FinishReason: model.FinishStop,
			Usage:        model.Usage{OK: true, TotalTokens: 42},
		}},
		model.FakeScript{Response: &model.ChatResponse{
			Message:      model.Message{Role: model.RoleAssistant, Content: "continued"},
			FinishReason: model.FinishStop,
		}},
	)
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })

	// 第一段：旧进程跑一个成功轮次后退出。
	first := NewService(agent.New(fake, "m"))
	first.EnablePersistence(history, "/ws", store.ModelRef{}, time.Second)
	if _, err := first.Submit(context.Background(), "past question"); err != nil {
		t.Fatal(err)
	}
	if last := terminal(t, drainEvents(t, first)); last.Kind != EventRunCompleted {
		t.Fatalf("first run kind = %s", last.Kind)
	}
	oldID := first.SessionID()
	first.Close()

	// 第二段：新进程从持久层恢复（cmd 层 -c 装配的同构路径）。
	messages, turnSeq, err := history.LoadTurns(context.Background(), oldID)
	if err != nil || turnSeq != 2 || len(messages) != 2 {
		t.Fatalf("restored turns = %d seq %d err %v", len(messages), turnSeq, err)
	}
	calls, err := history.LoadCalls(context.Background(), oldID)
	if err != nil || len(calls) != 1 {
		t.Fatalf("restored calls = %v err %v", calls, err)
	}
	usage := make(map[string]model.Usage)
	var lastCall string
	for _, c := range calls {
		if c.Usage != nil {
			usage[c.ID] = c.Usage.Usage()
		} else {
			usage[c.ID] = model.Usage{} // nil 快照保持未知，不补零
		}
		lastCall = c.ID
	}
	svc := NewService(agent.New(fake, "m"))
	defer svc.Close()
	svc.EnablePersistence(history, "/ws", store.ModelRef{}, time.Second)
	if err := svc.AdoptSession(oldID, RestoreSession(messages, turnSeq-1, usage, lastCall, time.Now())); err != nil {
		t.Fatal(err)
	}
	if got := svc.session.Turns(); got != 1 {
		t.Fatalf("restored completed turns = %d, want 1", got)
	}

	if _, err := svc.Submit(context.Background(), "new question"); err != nil {
		t.Fatal(err)
	}
	if last := terminal(t, drainEvents(t, svc)); last.Kind != EventRunCompleted {
		t.Fatalf("terminal kind = %s", last.Kind)
	}
	if svc.SessionID() != oldID {
		t.Fatalf("session id = %s, want adopted %s", svc.SessionID(), oldID)
	}
	// 持久层两轮共存，turnSeq 继续：旧 user/assistant + 新 user/assistant。
	persisted, turnSeq, err := history.LoadTurns(context.Background(), oldID)
	if err != nil || turnSeq != 3 {
		t.Fatalf("turnSeq = %d err = %v; restored turn must continue numbering", turnSeq, err)
	}
	if len(persisted) != 4 || persisted[0].Content != "past question" || persisted[2].Content != "new question" {
		t.Fatalf("persisted messages = %+v", persisted)
	}
	// 恢复的用量参与统计：新事件的累计用量含旧调用（42 token）。
	stats := svc.Session().Statistics()
	if stats.TotalTokens < 42 {
		t.Fatalf("accumulated total tokens = %d; restored usage must count", stats.TotalTokens)
	}
}

// TestNewSessionSemantics 守住 Ctrl+N 语义：busy/closed 拒绝，新会话拿到新 ID，
// 旧会话保留在持久层，后续 run 落到新会话。
func TestNewSessionSemantics(t *testing.T) {
	blocking := newBlockingFake()
	svc, history := newPersistedService(t, blocking)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	<-blocking.started
	oldID := svc.SessionID()
	if err := svc.NewSession(); !errors.Is(err, ErrBusy) {
		t.Fatalf("NewSession while busy = %v, want ErrBusy", err)
	}
	svc.Cancel()
	drainEvents(t, svc)

	if err := svc.NewSession(); err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	if svc.SessionID() == oldID || svc.SessionID() == "" {
		t.Fatalf("new session id = %q (old %q)", svc.SessionID(), oldID)
	}
	if svc.Session().Turns() != 0 {
		t.Fatal("new session must start with empty history")
	}

	// 旧会话的取消记录仍在持久层可查（ListRuns 旧 ID 保留）。
	runs, err := history.ListRuns(context.Background(), oldID)
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunCancelled {
		t.Fatalf("old session runs = %v err = %v", runs, err)
	}
	if _, found, _ := history.LatestSession(context.Background(), "/ws"); !found {
		t.Fatal("latest session must still find the old session record")
	}
	blocking.releaseNow()
	// 已关闭的服务拒绝再建会话：Close 后的 Ctrl+N 不产生孤儿会话 ID。
	svc.Close()
	if err := svc.NewSession(); err == nil {
		t.Fatal("NewSession after Close must fail")
	}
}
