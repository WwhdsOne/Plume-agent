package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
	"plume-agent/internal/tools"
	"sort"
	"strings"
	"testing"
	"time"
)

type toolCase struct {
	ID        string `json:"case_id"`
	Behavior  string `json:"behavior"`
	Tool      string `json:"tool"`
	Arguments string `json:"arguments"`
	Contains  string `json:"contains"`
	Error     string `json:"error"`
	Models    int    `json:"models"`
	Tools     int    `json:"tools"`
}

func TestG3ToolsDataset(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "eval", "datasets", "tools.v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 30 {
		t.Fatal("expected 30 independent tool cases")
	}
	seen := map[string]bool{}
	var reports []map[string]any
	var durations []float64
	var traceEvidence bytes.Buffer
	for _, line := range lines {
		var c toolCase
		if json.Unmarshal([]byte(line), &c) != nil || c.ID == "" || seen[c.ID] {
			t.Fatal("invalid dataset")
		}
		seen[c.ID] = true
		t.Run(c.ID, func(t *testing.T) {
			first := model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: c.Tool, Arguments: c.Arguments}}}, FinishReason: model.FinishToolCalls}}
			final := model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "final answer"}, FinishReason: model.FinishStop}}
			scripts := []model.FakeScript{first, final}
			limits := agent.DefaultLimits()
			ctx, cancel := context.WithCancel(telemetry.WithRunID(context.Background(), c.ID))
			defer cancel()
			switch c.Behavior {
			case "tool":
			case "batch":
				first.Response.Message.ToolCalls = append(first.Response.Message.ToolCalls, model.ToolCall{ID: "c2", Name: "current_time", Arguments: `{}`})
			case "correct":
				second := model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c2", Name: "calculate", Arguments: `{"operation":"divide","a":1,"b":2}`}}}, FinishReason: model.FinishToolCalls}}
				scripts = []model.FakeScript{first, second, final}
			case "missing_id":
				first.Response.Message.ToolCalls[0].ID = ""
			case "duplicate":
				scripts = []model.FakeScript{first, first}
			case "truncated":
				first.Response.FinishReason = model.FinishLength
			case "model_budget":
				limits.ModelCalls = 1
			case "tool_budget":
				limits.ToolCalls = -1 // 内部 fixture 模拟完全耗尽；用户配置零表示无限。
			case "byte_budget":
				limits.RequestBytes = 1
			case "cancel":
				cancel()
			case "model_timeout":
				limits.ModelTimeout = time.Millisecond
				scripts = []model.FakeScript{{Stream: []model.FakeStep{{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "late"}, Delay: time.Second}}}}
			case "plain":
				scripts = []model.FakeScript{final}
			case "interrupted":
				scripts = []model.FakeScript{{Stream: []model.FakeStep{{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &first.Response.Message.ToolCalls[0]}}}}}
			default:
				t.Fatal("unknown behavior")
			}
			fake := model.NewFake(scripts...)
			runtime := agent.New(fake, "fixture")
			runtime.SetLimits(limits)
			runtime.SetTools(tools.Builtins(func() time.Time { return time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) }))
			var trace bytes.Buffer
			rec := telemetry.NewModelRecorder(&trace)
			runtime.SetRecorder(rec, "fake", telemetry.ReasoningInfo{Source: "fixture"})
			start := time.Now()
			rec.RunStart(c.ID, start)
			result, runErr := runtime.RunStream(ctx, nil, "G3 secret prompt", nil, nil)
			duration := time.Since(start)
			rec.RunEnd(c.ID, telemetry.RunMetrics{Duration: duration}, runErr)
			_ = rec.Close()
			code := ""
			if e, ok := errors.AsType[*model.Error](runErr); ok {
				code = string(e.Code)
			} else {
				code = string(model.ClassifyContext(runErr))
			}
			if code != c.Error {
				t.Fatalf("error %s want %s (%v)", code, c.Error, runErr)
			}
			if result.ModelCalls != c.Models || len(fake.Calls()) != c.Models || result.ToolCalls != c.Tools {
				t.Fatalf("calls %d/%d want %d/%d", result.ModelCalls, result.ToolCalls, c.Models, c.Tools)
			}
			if c.Contains != "" {
				found := strings.Contains(result.Message.Content, c.Contains)
				for _, call := range fake.Calls() {
					for _, msg := range call.Messages {
						if msg.Role == model.RoleTool && strings.Contains(msg.Content, c.Contains) {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("missing expected result %s", c.Contains)
				}
			}
			if runErr != nil && len(result.Messages) != 0 {
				t.Fatal("failed turn marked committable")
			}
			for _, secret := range []string{"G3 secret prompt", "untrusted-secret-name", "not-json-secret-body", c.Arguments} {
				if secret != "" && strings.Contains(trace.String(), secret) {
					t.Fatal("trace leaked original content")
				}
			}
			starts, ends, toolStarts, toolEnds := 0, 0, 0, 0
			for _, entry := range strings.Split(strings.TrimSpace(trace.String()), "\n") {
				var e map[string]any
				if json.Unmarshal([]byte(entry), &e) != nil {
					t.Fatal("invalid trace JSON")
				}
				switch e["msg"] {
				case "model_start":
					starts++
				case "model_completed", "model_failed":
					ends++
				case "tool_started":
					toolStarts++
				case "tool_completed", "tool_failed":
					toolEnds++
				}
			}
			if starts != c.Models || starts != ends || toolStarts != c.Tools || toolStarts != toolEnds {
				t.Fatal("incomplete trace lifecycle")
			}
			traceEvidence.Write(trace.Bytes())
			ms := float64(duration) / float64(time.Millisecond)
			durations = append(durations, ms)
			reports = append(reports, map[string]any{"case_id": c.ID, "status": "passed", "run_status": map[bool]string{true: "failed", false: "completed"}[runErr != nil], "expected_error": c.Error, "error_code": code, "duration_ms": ms, "model_calls": result.ModelCalls, "tool_attempts": result.ToolCalls, "trace_complete": true, "usage_known": false})
		})
	}
	if t.Failed() {
		return
	}
	if dir := os.Getenv("PLUME_G3_REPORT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		sort.Float64s(durations)
		quantile := func(q float64) float64 { return durations[int(math.Ceil(float64(len(durations))*q))-1] }
		report := map[string]any{"dataset": "tools.v1", "planned": 30, "passed": len(reports), "results": reports, "all_terminations_p50_ms": quantile(.5), "all_terminations_p95_ms": quantile(.95), "limits": agent.DefaultLimits(), "source": "local scripted fake; expected failures count as regression checks, not successful user tasks", "real_model_quality": "N/A", "fees": "unknown"}
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "tools-results.json"), append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tools-trace.jsonl"), traceEvidence.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
