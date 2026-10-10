package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// lines 把 trace 拆成逐条 JSON，顺带验证每行都是合法 JSON。
func lines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("trace line is not valid JSON: %v\nline: %s", err, line)
		}
		out = append(out, obj)
	}
	return out
}

// TestSetupRecorderWritesJSONLinesWithSetupID 守住 setup trace 的关联契约：
// 每条记录都是合法 JSON 且携带同一个 setup_id，重复 Start 不换 ID。
func TestSetupRecorderWritesJSONLinesWithSetupID(t *testing.T) {
	var buf bytes.Buffer
	rec := NewSetupRecorder(&buf)

	id := rec.Start()
	if id == "" {
		t.Fatal("Start() returned an empty setup_id")
	}
	if rec.ID() != id {
		t.Errorf("ID() = %q, want %q", rec.ID(), id)
	}
	// 重复 Start 不应换 ID。
	if again := rec.Start(); again != id {
		t.Errorf("second Start() = %q, want the same id", again)
	}

	done := rec.Step("provider")
	done(nil)

	entries := lines(t, buf.String())
	if len(entries) < 2 {
		t.Fatalf("got %d trace entries, want at least 2", len(entries))
	}
	for _, e := range entries {
		if e["setup_id"] != id {
			t.Errorf("entry %v has no matching setup_id", e)
		}
	}
}

// TestStepRecordsDurationAndStatus 守住步骤终态的完整记录：步骤名、error
// 状态、错误消息与 duration_ms 四项缺一不可，失败也不例外。
func TestStepRecordsDurationAndStatus(t *testing.T) {
	var buf bytes.Buffer
	rec := NewSetupRecorder(&buf)
	rec.Start()

	rec.Step("validate")(errors.New("boom"))

	entries := lines(t, buf.String())
	last := entries[len(entries)-1]
	if last["step"] != "validate" {
		t.Errorf("step = %v", last["step"])
	}
	if last["status"] != "error" {
		t.Errorf("status = %v, want error", last["status"])
	}
	if last["error"] != "boom" {
		t.Errorf("error = %v", last["error"])
	}
	if _, ok := last["duration_ms"]; !ok {
		t.Error("step entry has no duration_ms")
	}
}

// TestFinishIsIdempotent 守住 Finish 的幂等性：只允许写一条终态事件，
// 迟到的第二次 Finish 必须被丢弃。
func TestFinishIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	rec := NewSetupRecorder(&buf)
	rec.Start()

	rec.Finish(EventSetupCompleted, nil)
	rec.Finish(EventSetupFailed, errors.New("late"))

	entries := lines(t, buf.String())
	finishes := 0
	for _, e := range entries {
		if e["event_kind"] == "finish" {
			finishes++
		}
	}
	if finishes != 1 {
		t.Errorf("got %d finish entries, want exactly 1", finishes)
	}
}

// TestRecorderAPIHasNoSecretParameter 是一个结构性断言：脱敏靠的是"记录器的方法
// 不接受密钥"，而不是靠调用方自觉。这段代码能编译，就说明签名里没有 secret 入口。
func TestRecorderAPIHasNoSecretParameter(t *testing.T) {
	var buf bytes.Buffer
	rec := NewSetupRecorder(&buf)
	rec.Start()

	// 只记录引用名与成败，不记录内容。
	rec.CredentialStored("deepseek-default", nil)
	rec.CredentialStored("deepseek-default", errors.New("disk full"))
	rec.Event("channel_selected", Field("channel_type", "weixin"))

	entries := lines(t, buf.String())
	for _, e := range entries {
		if _, ok := e["secret"]; ok {
			t.Error("trace entry contains a secret field")
		}
	}
	// Field 只是 zap.String 的包装，值仍由调用方负责。
	if got := Field("k", "v").Key; got != "k" {
		t.Errorf("Field key = %q", got)
	}
}
