package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
	"plume-agent/internal/tools"
	"reflect"
	"strings"
	"testing"
	"time"
)

func workspaceService(t *testing.T, client model.Client) (*Service, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	options := tools.DefaultWorkspaceOptions()
	options.Root = dir
	registry, err := tools.NewWorkspace(options)
	if err != nil {
		t.Fatal(err)
	}
	runtime := agent.New(client, "fixture")
	runtime.SetTools(registry)
	trace := new(bytes.Buffer)
	recorder := telemetry.NewModelRecorder(trace)
	runtime.SetRecorder(recorder, "fake", telemetry.ReasoningInfo{Source: "fixture"})
	s := NewService(runtime, recorder)
	t.Cleanup(func() { s.Close(); _ = runtime.Close(); _ = recorder.Close() })
	return s, dir, trace
}

// TestWorkspaceCompleteDevelopmentWorkflow 守住 G3 工具组闭环：/demo workspace 按序执行
// 六个工具并原子提交整轮历史，文件正文与命令不泄漏进工具摘要和 trace。
func TestWorkspaceCompleteDevelopmentWorkflow(t *testing.T) {
	s, dir, trace := workspaceService(t, model.NewToolDemo())
	if _, err := s.Submit(context.Background(), "/demo workspace"); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-s.Events():
			if event.Kind == EventToolUpdate && event.Tool.Status == "completed" {
				names = append(names, event.Tool.Name)
			}
			if event.Kind == EventToolUpdate && strings.Contains(event.Tool.Summary, "hello plume") {
				t.Fatal("file content leaked into tool summary")
			}
			if event.Kind == EventRunFailed {
				t.Fatal(event.Err)
			}
			if event.Kind == EventRunCompleted {
				expected := []string{"write", "glob", "grep", "read", "edit", "bash"}
				if !reflect.DeepEqual(names, expected) {
					t.Fatalf("tools=%v", names)
				}
				actual, err := os.ReadFile(filepath.Join(dir, "plume-demo.txt"))
				if err != nil || string(actual) != "goodbye plume\n" {
					t.Fatalf("file=%q %v", actual, err)
				}
				if len(s.Session().History()) != 14 || s.Session().Turns() != 1 || s.Session().Statistics().Calls != 7 {
					t.Fatal("incomplete committed tool group")
				}
				if !strings.Contains(event.Reply, "Workspace verified") {
					t.Fatalf("answer=%s", event.Reply)
				}
				text := trace.String()
				if strings.Contains(text, "hello plume") || strings.Contains(text, "goodbye plume") || strings.Contains(text, "grep -qx") {
					t.Fatal("body or command leaked into trace")
				}
				exportWorkspaceEvidence(t, "success", text, map[string]any{"tools": names, "model_calls": 7, "history_messages": 14, "verified_file": true, "terminal": "completed"})
				return
			}
		case <-deadline:
			t.Fatal("development workflow did not finish")
		}
	}
}

// TestWorkspaceCancellationKeepsSideEffectsButNotSuccessHistory 守住取消语义：工具副作用
// （已写入的文件）保留，但不提交成功历史，终态为取消失败。
func TestWorkspaceCancellationKeepsSideEffectsButNotSuccessHistory(t *testing.T) {
	call := func(id, name, args string) model.FakeScript {
		return model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: id, Name: name, Arguments: args}}}, FinishReason: model.FinishToolCalls}}
	}
	s, dir, trace := workspaceService(t, model.NewFake(call("c1", "write", `{"path":"created.txt","content":"retained"}`), call("c2", "bash", `{"command":"printf ready > ready.txt; sleep 30"}`)))
	if _, err := s.Submit(context.Background(), "test cancellation"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	cancelled := false
	for {
		select {
		case e := <-s.Events():
			if e.Kind == EventToolUpdate && e.Tool.Name == "bash" && e.Tool.Status == "running" {
				readyDeadline := time.Now().Add(time.Second)
				for {
					if _, err := os.Stat(filepath.Join(dir, "ready.txt")); err == nil {
						break
					}
					if time.Now().After(readyDeadline) {
						t.Fatal("bash never began")
					}
					time.Sleep(time.Millisecond)
				}
				s.Cancel()
				cancelled = true
			}
			if e.Kind == EventRunCompleted {
				t.Fatal("cancelled workflow committed")
			}
			if e.Kind == EventRunFailed {
				if !cancelled || model.ClassifyContext(e.Err) != model.ErrCancelled || len(s.Session().History()) != 0 {
					t.Fatalf("cancel terminal=%+v", e)
				}
				actual, _ := os.ReadFile(filepath.Join(dir, "created.txt"))
				if string(actual) != "retained" {
					t.Fatal("cancelled write was silently rolled back")
				}
				exportWorkspaceEvidence(t, "cancel", trace.String(), map[string]any{"terminal": "cancelled", "history_messages": 0, "file_side_effect_retained": true})
				return
			}
		case <-deadline:
			t.Fatal("cancel did not finish")
		}
	}
}

func exportWorkspaceEvidence(t *testing.T, name, trace string, result map[string]any) {
	t.Helper()
	dir := os.Getenv("PLUME_G3_WORKSPACE_REPORT_DIR")
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("report dir must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(trace), 0600); err != nil {
		t.Fatal(err)
	}
}
