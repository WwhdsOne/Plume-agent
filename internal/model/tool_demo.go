package model

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// ToolDemo 是明确标注的离线脚本，不从普通自然语言猜测工具调用。
// /demo 指令仅选择协议 fixture，实际工具仍由 Agent 注册表执行。
type ToolDemo struct {
	mu  sync.Mutex
	seq int
}

func NewToolDemo() *ToolDemo { return &ToolDemo{} }

func (d *ToolDemo) Generate(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if ctx.Err() != nil {
		return nil, NewError(ClassifyContext(ctx.Err()))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	input := ""
	steps := 0
	for _, v := range slices.Backward(req.Messages) {
		if v.Role == RoleUser {
			input = v.Content
			break
		}
		if v.Role == RoleTool {
			steps++
		}
	}
	last := Message{}
	if len(req.Messages) > 0 {
		last = req.Messages[len(req.Messages)-1]
	}
	response := &ChatResponse{Message: Message{Role: RoleAssistant, Content: OfflineReply}, FinishReason: FinishStop, Provider: "fake", Protocol: ProtocolOpenAIChatCompletions}
	call := func(name, args string) {
		d.seq++
		response.Message = Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: fmt.Sprintf("demo-%d", d.seq), Name: name, Arguments: args}}}
		response.FinishReason = FinishToolCalls
	}
	switch strings.TrimSpace(input) {
	case "/demo calculate":
		if last.Role == RoleUser {
			call("calculate", `{"operation":"multiply","a":6,"b":7}`)
		} else {
			response.Message.Content = "[offline demo] 6 × 7 = 42"
		}
	case "/demo time":
		if last.Role == RoleUser {
			call("current_time", `{}`)
		} else {
			response.Message.Content = "[offline demo] UTC: " + last.Content
		}
	case "/demo error":
		if last.Role == RoleUser {
			call("calculate", `{"operation":"divide","a":1,"b":0}`)
		} else if strings.Contains(last.Content, "division_by_zero") {
			call("calculate", `{"operation":"divide","a":1,"b":2}`)
		} else {
			response.Message.Content = "[offline demo] Corrected denominator: 1 / 2 = 0.5"
		}
	case "/demo budget":
		if steps < 12 {
			call("current_time", `{}`)
		} else {
			response.Message.Content = "[offline demo] 12 tools completed with unlimited default budget"
		}
	case "/demo workspace":
		var previous struct {
			OK   bool   `json:"ok"`
			Code string `json:"error"`
		}
		if last.Role == RoleTool && (json.Unmarshal([]byte(last.Content), &previous) != nil || !previous.OK) {
			response.Message.Content = "[offline demo] Workspace tool failed: " + previous.Code
			break
		}
		workflow := []struct{ name, args string }{
			{"write", `{"path":"plume-demo.txt","content":"hello plume\n"}`},
			{"glob", `{"pattern":"plume-demo.txt"}`},
			{"grep", `{"pattern":"hello","glob":"plume-demo.txt","fixed_strings":true}`},
			{"read", `{"path":"plume-demo.txt"}`},
			{"edit", `{"path":"plume-demo.txt","old_string":"hello","new_string":"goodbye"}`},
			{"bash", `{"command":"grep -qx 'goodbye plume' plume-demo.txt"}`},
		}
		if steps < len(workflow) {
			call(workflow[steps].name, workflow[steps].args)
		} else {
			response.Message.Content = "[offline demo] Workspace verified: plume-demo.txt now contains goodbye plume"
		}
	case "/demo cancel":
		if last.Role == RoleUser {
			call("current_time", `{}`)
		} else {
			response.Message.Content = "[offline demo] delayed reply"
		}
	}
	return response, nil
}

func (d *ToolDemo) Stream(ctx context.Context, req ChatRequest) (EventStream, error) {
	response, err := d.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	if response.FinishReason == FinishStop {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == RoleUser {
				if strings.TrimSpace(req.Messages[i].Content) == "/demo cancel" {
					return newFakeStream(ctx, FakeScript{Stream: []FakeStep{{Event: Event{Kind: EventTextDelta, TextDelta: response.Message.Content}, Delay: 30 * time.Second}, {Event: Event{Kind: EventModelDone, FinishReason: FinishStop}}, {Event: Event{Kind: EventStreamEnded}}}})
				}
				break
			}
		}
	}
	return newFakeStream(ctx, FakeScript{Response: response})
}
