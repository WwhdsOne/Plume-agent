package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/tui"
)

// seedHistory 用真实契约接口预置一个有历史的会话，返回会话 ID。
func seedHistory(t *testing.T, history store.Store, ref store.ModelRef) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	sessionID := "seed-session-0001"
	if err := history.StartRun(ctx, store.SessionRecord{ID: sessionID, Workspace: "/ws", Title: "New conversation", ModelRef: ref, CreatedAt: now, LastRunAt: now}, store.RunRecord{ID: "run-seed-1", SessionID: sessionID, Input: "seed question", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := history.StartModelCall(ctx, store.ModelCallRecord{ID: "run-seed-1/model-1", RunID: "run-seed-1", SessionID: sessionID, Sequence: 1, SavedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := history.SaveUsage(ctx, "run-seed-1/model-1", store.StoredUsage{OK: true, PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}, now); err != nil {
		t.Fatal(err)
	}
	if err := history.CompleteRun(ctx, store.RunRecord{ID: "run-seed-1", SessionID: sessionID, Input: "seed question", Status: store.RunCompleted, Reply: "seed answer", StartedAt: now, FinishedAt: now},
		[]model.Message{{Role: model.RoleUser, Content: "seed question"}, {Role: model.RoleAssistant, Content: "seed answer"}}); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

// testResumeConfig 构造带两个模型的配置：deepseek-default 与同端点别名。
func testResumeConfig(baseURL string) *config.Config {
	return &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  "deepseek-default",
		Models: []config.ModelConfig{
			{ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek", BaseURL: baseURL, Model: "deepseek-flash"},
			{ID: "other", Provider: "custom-openai", Protocol: "openai-compatible", BaseURL: "https://elsewhere.example", Model: "gpt-x"},
		},
	}
}

// TestFindResumableSessionByConfigIDAndFingerprint 守住模型引用匹配：
// 配置 ID 与指纹一致才恢复；ID 丢失但端点指纹一致也恢复，不静默换模型。
func TestFindResumableSessionByConfigIDAndFingerprint(t *testing.T) {
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	ref := store.ModelRef{ConfigID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek", BaseURL: "https://api.example/v1", Model: "deepseek-flash"}
	seedHistory(t, history, ref)
	ctx := context.Background()

	// case: 配置 ID 命中。
	resume, err := findResumableSession(ctx, history, testResumeConfig("https://api.example/v1"), "", "/ws")
	if err != nil || resume == nil {
		t.Fatalf("resume = %v, err = %v", resume, err)
	}
	if resume.selected.ID != "deepseek-default" {
		t.Fatalf("selected = %s", resume.selected.ID)
	}

	// case: ID 已改名，指纹（provider/协议/模型/端点）仍命中。
	renamed := store.ModelRef{ConfigID: "renamed-away", Provider: "deepseek", Protocol: "deepseek", BaseURL: "https://api.example/v1", Model: "deepseek-flash"}
	history2 := store.NewMemory()
	t.Cleanup(func() { _ = history2.Close() })
	seedHistory(t, history2, renamed)
	resume, err = findResumableSession(ctx, history2, testResumeConfig("https://api.example/v1"), "", "/ws")
	if err != nil || resume == nil {
		t.Fatalf("fingerprint resume = %v, err = %v", resume, err)
	}
	if resume.selected.ID != "deepseek-default" {
		t.Fatalf("fingerprint selected = %s", resume.selected.ID)
	}
}

// TestResumeSameConfigIDRejectsChangedDestination 守住配置 ID 不是授权边界：
// 供应商、协议、模型或端点发生变化时，不能把旧会话送到新目的地。
func TestResumeSameConfigIDRejectsChangedDestination(t *testing.T) {
	ref := store.ModelRef{ConfigID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek", BaseURL: "https://api.example/v1", Model: "deepseek-flash"}
	for _, tc := range []struct {
		name   string
		change func(*config.ModelConfig)
	}{
		// case: 同 ID 的供应商被修改。
		{"provider", func(m *config.ModelConfig) { m.Provider = "custom-openai" }},
		// case: 同 ID 的协议被修改。
		{"protocol", func(m *config.ModelConfig) { m.Protocol = "openai-compatible" }},
		// case: 同 ID 的 API 模型被修改。
		{"model", func(m *config.ModelConfig) { m.Model = "another-model" }},
		// case: 同 ID 的服务地址被修改。
		{"endpoint", func(m *config.ModelConfig) { m.BaseURL = "https://new-service.example/v1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := store.NewMemory()
			t.Cleanup(func() { _ = history.Close() })
			seedHistory(t, history, ref)
			cfg := testResumeConfig(ref.BaseURL)
			tc.change(&cfg.Models[0])
			if _, err := findResumableSession(context.Background(), history, cfg, "", "/ws"); err == nil || !strings.Contains(err.Error(), "resume_incompatible") {
				t.Fatalf("changed %s accepted: %v", tc.name, err)
			}
		})
	}
}

// TestHistoryWorkspaceUsesEffectiveToolRoot 守住自定义工具根的身份：
// 启动目录与工具根不同时，历史查询仍与实际执行目录一致。
func TestHistoryWorkspaceUsesEffectiveToolRoot(t *testing.T) {
	launch := t.TempDir()
	root := t.TempDir()
	t.Chdir(launch)
	cfg := &config.Config{Tools: config.DefaultTools(), Agent: config.DefaultAgent()}
	cfg.Tools.Workspace = root
	cfg.Tools.Enabled = []string{}
	runtime := agent.New(model.NewFake(model.FakeScript{}), "m")
	if err := configureRuntimeTools(runtime, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := workspaceDir(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("history workspace = %q, tool workspace = %q", got, want)
	}
}

// TestHistoryWorkspaceCanonicalRoots 守住相对路径和符号链接的共同身份，
// 不存在或普通文件路径必须明确失败，不能回退到启动目录。
func TestHistoryWorkspaceCanonicalRoots(t *testing.T) {
	launch := t.TempDir()
	root := filepath.Join(launch, "project")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(launch, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(launch, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(launch)
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		root  string
		valid bool
	}{
		// case: 绝对根不受启动目录影响。
		{root, true},
		// case: 相对路径按当前启动目录解析。
		{"project", true},
		// case: 别名与实际工具根共享历史身份。
		{alias, true},
		// case: 缺失的目录拒绝启动。
		{"missing", false},
		// case: 普通文件不能作为工具根。
		{file, false},
	} {
		t.Run(tc.root, func(t *testing.T) {
			cfg := &config.Config{Tools: config.DefaultTools()}
			cfg.Tools.Workspace = tc.root
			got, err := workspaceDir(cfg)
			if tc.valid && (err != nil || got != want) {
				t.Fatalf("workspace = %q, %v, want %q", got, err, want)
			}
			if !tc.valid && err == nil {
				t.Fatalf("invalid root accepted: %q", got)
			}
		})
	}
}

// TestAdoptResumedSessionTurnCountAndOpeningClock 覆盖零、单、多轮及隔日
// 打开，确保下一序号不被当成已完成轮数，离线时间不计入状态栏。
func TestAdoptResumedSessionTurnCountAndOpeningClock(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			history := store.NewMemory()
			t.Cleanup(func() { _ = history.Close() })
			ctx := context.Background()
			createdAt := time.Now().Add(-24 * time.Hour)
			record := store.SessionRecord{ID: "clock-session", Workspace: "/ws", CreatedAt: createdAt, LastRunAt: createdAt}
			for i := 0; i < count; i++ {
				run := store.RunRecord{ID: fmt.Sprintf("turn-%d", i), SessionID: record.ID, Input: "q", StartedAt: createdAt}
				if err := history.StartRun(ctx, record, run); err != nil {
					t.Fatal(err)
				}
				run.Status, run.Reply, run.FinishedAt = store.RunCompleted, "a", createdAt
				if err := history.CompleteRun(ctx, run, []model.Message{{Role: model.RoleUser, Content: "q"}, {Role: model.RoleAssistant, Content: "a"}}); err != nil {
					t.Fatal(err)
				}
			}
			service := app.NewService(agent.New(model.NewFake(model.FakeScript{}), "m"))
			defer service.Close()
			openedAt := time.Now()
			if err := adoptResumedSession(ctx, service, history, &resumedSession{record: record}); err != nil {
				t.Fatal(err)
			}
			if got := service.Session().Turns(); got != count {
				t.Fatalf("turns = %d, want %d", got, count)
			}
			if got := service.Session().Statistics().StartedAt; got.Before(openedAt) || got.After(time.Now()) {
				t.Fatalf("opening clock = %s", got)
			}
		})
	}
}

// TestResumeRejectsOversizedHistoryBeforeAdoption 守住三个恢复读取入口的
// 字节边界：超限拒绝整次恢复，当前会话与历史原始内容保持不变。
func TestResumeRejectsOversizedHistoryBeforeAdoption(t *testing.T) {
	for _, section := range []string{"runs", "turns", "calls"} {
		t.Run(section, func(t *testing.T) {
			history, err := store.OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = history.Close() })
			ctx := context.Background()
			cfg := testResumeConfig("https://api.example")
			cfg.Agent = config.DefaultAgent()
			cfg.Agent.Budget.RequestBytes = 1024
			now := time.Now()
			record := store.SessionRecord{ID: "bounded-session", Workspace: "/ws", ModelRef: modelRefOf(&cfg.Models[0]), CreatedAt: now, LastRunAt: now}
			run := store.RunRecord{ID: "bounded-run", SessionID: record.ID, Input: "q", StartedAt: now}
			if section == "runs" {
				run.Input = strings.Repeat("q", 2048)
			}
			if err := history.StartRun(ctx, record, run); err != nil {
				t.Fatal(err)
			}
			if section == "calls" {
				// case: 超长调用身份也属于恢复统计的字节边界。
				if err := history.StartModelCall(ctx, store.ModelCallRecord{ID: "bounded-run/" + strings.Repeat("c", 2048), RunID: run.ID, SessionID: record.ID, Sequence: 1, SavedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			answer := "a"
			if section == "turns" {
				answer = strings.Repeat("a", 2048)
			}
			run.Status, run.Reply, run.FinishedAt = store.RunCompleted, "a", now
			if err := history.CompleteRun(ctx, run, []model.Message{{Role: model.RoleUser, Content: run.Input}, {Role: model.RoleAssistant, Content: answer}}); err != nil {
				t.Fatal(err)
			}
			resume, err := findResumableSession(ctx, history, cfg, "", "/ws")
			if section == "runs" {
				// case: 展示转录超限，构造 TUI 之前直接拒绝。
				if !errors.Is(err, store.ErrHistoryLimit) {
					t.Fatalf("run read error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			service := app.NewService(agent.New(model.NewFake(model.FakeScript{}), "m"))
			defer service.Close()
			oldID := service.SessionID()
			if err := adoptResumedSession(ctx, service, history, resume); !errors.Is(err, store.ErrHistoryLimit) {
				t.Fatalf("%s read error = %v", section, err)
			}
			if service.SessionID() != oldID || len(service.Session().History()) != 0 {
				t.Fatal("failed adoption changed active session")
			}
		})
	}
}

// TestFindResumableSessionIncompatible 守住不兼容语义：模型引用已不存在时
// 明确报 resume_incompatible，不静默换模型；显式 --model 与引用不一致同样拒绝。
func TestFindResumableSessionIncompatible(t *testing.T) {
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	seedHistory(t, history, store.ModelRef{ConfigID: "gone", Provider: "anthropic", Protocol: "anthropic", BaseURL: "https://api.example", Model: "claude-x"})
	ctx := context.Background()

	// case: 引用无法解析。
	_, err := findResumableSession(ctx, history, testResumeConfig("https://api.example"), "", "/ws")
	if err == nil || !strings.Contains(err.Error(), "resume_incompatible") {
		t.Fatalf("err = %v, want resume_incompatible", err)
	}

	// case: --model 指向与引用不同的配置。
	history2 := store.NewMemory()
	t.Cleanup(func() { _ = history2.Close() })
	seedHistory(t, history2, store.ModelRef{ConfigID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek", BaseURL: "https://api.example", Model: "deepseek-flash"})
	_, err = findResumableSession(ctx, history2, testResumeConfig("https://api.example"), "other", "/ws")
	if err == nil || !strings.Contains(err.Error(), "resume_incompatible") {
		t.Fatalf("err = %v, want resume_incompatible for mismatched --model", err)
	}

	// case: 无历史时返回 nil（调用方提示后开新会话）。
	empty := store.NewMemory()
	t.Cleanup(func() { _ = empty.Close() })
	resume, err := findResumableSession(ctx, empty, testResumeConfig("https://api.example"), "", "/ws")
	if err != nil || resume != nil {
		t.Fatalf("resume = %v, err = %v; want nil,nil when no history", resume, err)
	}
}

// TestFindResumableSessionBuildsTranscript 守住转录构建：系统行带短 ID 与
// run 计数；completed 且有回复的 run 出 user+assistant；cancelled 的 run
// assistant 行带终态标记；completed 但空回复不出 assistant 行。
func TestFindResumableSessionBuildsTranscript(t *testing.T) {
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	ctx := context.Background()
	now := time.Now()
	sessionID := "sess-transcript-0001"
	ref := store.ModelRef{ConfigID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek", BaseURL: "https://api.example", Model: "deepseek-flash"}

	session := store.SessionRecord{ID: sessionID, Workspace: "/ws", Title: "New conversation", ModelRef: ref, CreatedAt: now, LastRunAt: now}
	// run-1：成功轮次（有回复）。
	if err := history.StartRun(ctx, session, store.RunRecord{ID: "run-tr-1", SessionID: sessionID, Input: "q1", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := history.CompleteRun(ctx, store.RunRecord{ID: "run-tr-1", SessionID: sessionID, Input: "q1", Status: store.RunCompleted, Reply: "a1", StartedAt: now, FinishedAt: now},
		[]model.Message{{Role: model.RoleUser, Content: "q1"}, {Role: model.RoleAssistant, Content: "a1"}}); err != nil {
		t.Fatal(err)
	}
	// run-2：取消，保留部分回复。
	if err := history.StartRun(ctx, session, store.RunRecord{ID: "run-tr-2", SessionID: sessionID, Input: "q2", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := history.FinishRun(ctx, store.RunRecord{ID: "run-tr-2", SessionID: sessionID, Input: "q2", Status: store.RunCancelled, Reply: "partial", StartedAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}

	resume, err := findResumableSession(ctx, history, testResumeConfig("https://api.example"), "", "/ws")
	if err != nil || resume == nil {
		t.Fatalf("resume = %v, err = %v", resume, err)
	}
	got := resume.transcript
	if len(got) != 5 {
		t.Fatalf("transcript lines = %d, want 5 (system + user/assistant + user/assistant): %+v", len(got), got)
	}
	if got[0].Kind != tui.TranscriptSystem || !strings.Contains(got[0].Text, "sess-tr") || !strings.Contains(got[0].Text, "2 runs") {
		t.Fatalf("system line = %+v", got[0])
	}
	if got[1].Kind != tui.TranscriptUser || got[1].Text != "q1" || got[2].Kind != tui.TranscriptAssistant || got[2].Text != "a1" || got[2].Terminal != "" {
		t.Fatalf("completed run lines = %+v %+v", got[1], got[2])
	}
	if got[3].Kind != tui.TranscriptUser || got[3].Text != "q2" || got[4].Terminal != "cancelled" {
		t.Fatalf("cancelled run lines = %+v %+v", got[3], got[4])
	}
}

// TestFindResumableSessionPreservesThoughtAndTerminal 守住未完成展示记录：
// 只有 Thought 也必须恢复，多步骤累计正文不能丢段，状态与原始文本同时保留；
// 净化由 TUI 边界完成，这些失败内容不会进入成功模型历史或自动重放。
func TestFindResumableSessionPreservesThoughtAndTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    store.RunStatus
		reply     string
		reasoning string
		terminal  string
	}{
		// case: 模型失败前只有思考，仍恢复 assistant Thought 与 failed 标记。
		{"failed-thought-only", store.RunFailed, "", "checking first step", "failed"},
		// case: 取消前已累计多个工具步骤的答案和思考，不只保留末段。
		{"cancelled-multiple-steps", store.RunCancelled, "first answer\nsecond answer", "first thought\n\x1b[31msecond thought\x1b[0m", "cancelled"},
		// case: 中断记录里的 Thought 同样可见，不自动补发原输入。
		{"interrupted-thought-only", store.RunInterrupted, "", "unfinished thought", "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := store.NewMemory()
			t.Cleanup(func() { _ = history.Close() })
			ctx := context.Background()
			now := time.Now()
			cfg := testResumeConfig("https://api.example")
			record := store.SessionRecord{ID: "thought-session", Workspace: "/ws", ModelRef: modelRefOf(&cfg.Models[0]), CreatedAt: now, LastRunAt: now}
			run := store.RunRecord{ID: "thought-run", SessionID: record.ID, Input: "question", StartedAt: now}
			if err := history.StartRun(ctx, record, run); err != nil {
				t.Fatal(err)
			}
			run.Status, run.Reply, run.Reasoning, run.FinishedAt = tc.status, tc.reply, tc.reasoning, now
			if err := history.FinishRun(ctx, run); err != nil {
				t.Fatal(err)
			}
			resume, err := findResumableSession(ctx, history, cfg, "", "/ws")
			if err != nil {
				t.Fatal(err)
			}
			if resume == nil || len(resume.transcript) != 3 {
				t.Fatalf("transcript = %+v, want system/user/assistant", resume)
			}
			got := resume.transcript[2]
			if got.Kind != tui.TranscriptAssistant || got.Text != tc.reply || got.Reasoning != tc.reasoning || got.Terminal != tc.terminal {
				t.Fatalf("assistant transcript = %+v, want reply %q, reasoning %q, terminal %q", got, tc.reply, tc.reasoning, tc.terminal)
			}
			fake := model.NewFake(model.FakeScript{})
			service := app.NewService(agent.New(fake, "m"))
			defer service.Close()
			if err := adoptResumedSession(ctx, service, history, resume); err != nil {
				t.Fatal(err)
			}
			if len(service.Session().History()) != 0 || service.Session().Turns() != 0 || len(fake.Calls()) != 0 {
				t.Fatal("failed display content entered model history or replayed automatically")
			}
		})
	}
}

// TestAdoptResumedSessionPreloadsHistory 守住恢复装载：成功轮次与用量预灌
// 内存，会话 ID 切到持久 ID，未知用量（nil 快照）保持未知不补零。
func TestAdoptResumedSessionPreloadsHistory(t *testing.T) {
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	ctx := context.Background()
	now := time.Now().Add(-24 * time.Hour)
	sessionID := "sess-adopt-000001"
	ref := store.ModelRef{ConfigID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek", BaseURL: "https://api.example", Model: "deepseek-flash"}

	if err := history.StartRun(ctx, store.SessionRecord{ID: sessionID, Workspace: "/ws", Title: "New conversation", ModelRef: ref, CreatedAt: now, LastRunAt: now}, store.RunRecord{ID: "run-ad-1", SessionID: sessionID, Input: "q1", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	// 两个调用：一个有用量快照，一个 nil（未知）。
	if err := history.StartModelCall(ctx, store.ModelCallRecord{ID: "run-ad-1/model-1", RunID: "run-ad-1", SessionID: sessionID, Sequence: 1, SavedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := history.SaveUsage(ctx, "run-ad-1/model-1", store.StoredUsage{OK: true, PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}, now); err != nil {
		t.Fatal(err)
	}
	if err := history.StartModelCall(ctx, store.ModelCallRecord{ID: "run-ad-1/model-2", RunID: "run-ad-1", SessionID: sessionID, Sequence: 2, SavedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := history.CompleteRun(ctx, store.RunRecord{ID: "run-ad-1", SessionID: sessionID, Input: "q1", Status: store.RunCompleted, Reply: "a1", StartedAt: now, FinishedAt: now},
		[]model.Message{{Role: model.RoleUser, Content: "q1"}, {Role: model.RoleAssistant, Content: "a1"}}); err != nil {
		t.Fatal(err)
	}

	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "next"}}})
	service := app.NewService(agent.New(fake, "m"))
	defer service.Close()
	openedAt := time.Now()
	if err := adoptResumedSession(ctx, service, history, &resumedSession{record: store.SessionRecord{ID: sessionID, ModelRef: ref, CreatedAt: now}, selected: &config.ModelConfig{ID: "deepseek-default"}}); err != nil {
		t.Fatal(err)
	}
	if service.SessionID() != sessionID {
		t.Fatalf("session id = %s, want %s", service.SessionID(), sessionID)
	}
	if got := service.Session().Turns(); got != 1 {
		t.Fatalf("restored turns = %d, want 1 completed turn", got)
	}
	if started := service.Session().Statistics().StartedAt; started.Before(openedAt) || started.After(time.Now()) {
		t.Fatalf("session clock = %s, want current opening after %s", started, openedAt)
	}
	if messages := service.Session().History(); len(messages) != 2 || messages[0].Content != "q1" {
		t.Fatalf("preloaded history = %+v", messages)
	}
	stats := service.Session().Statistics()
	if stats.TotalTokens != 7 {
		t.Fatalf("restored total tokens = %d, want 7 (unknown call must not be counted as zero-added)", stats.TotalTokens)
	}
}

// TestShortIDAndModelRefOf 守住显示辅助：短 ID 取前 8 位，引用快照不含凭据。
func TestShortIDAndModelRefOf(t *testing.T) {
	if shortID("12345678abcdefgh") != "12345678" {
		t.Fatalf("shortID = %q", shortID("12345678abcdefgh"))
	}
	if shortID("short") != "short" {
		t.Fatalf("shortID(short) = %q", shortID("short"))
	}
	m := &config.ModelConfig{ID: "cfg", Provider: "p", Protocol: "openai-compatible", BaseURL: "https://api.example/", Model: "m1"}
	ref := modelRefOf(m)
	if ref.ConfigID != "cfg" || ref.Model != "m1" || ref.BaseURL != "https://api.example/" {
		t.Fatalf("ref = %+v", ref)
	}
	if modelRefOf(nil) != (store.ModelRef{}) {
		t.Fatal("nil config must yield zero ref")
	}
}
