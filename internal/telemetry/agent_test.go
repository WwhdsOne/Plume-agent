package telemetry

import (
	"bytes"
	"plume-agent/internal/model"
	"strings"
	"testing"
	"time"
)

// TestG3TraceMetadataOnly 守住 G3 工具循环 trace 的"只落元数据"契约：
// 事件名、schema 版本与耗时不缺位，schema 描述与参数等正文绝不进 trace。
func TestG3TraceMetadataOnly(t *testing.T) {
	var out bytes.Buffer
	r := NewModelRecorder(&out)
	r.Prompt("r", "v1", "hash", 100, 2, 3, []model.ToolDeclaration{{Name: "calculate", Description: "secret-description", Parameters: `{"private":"secret-schema"}`}})
	r.Tool("r", "c", "calculate", "started", "", 0)
	r.Tool("r", "c", "calculate", "failed", "invalid_arguments", time.Millisecond)
	text := out.String()
	if strings.Contains(text, "secret") {
		t.Fatal("prompt/schema body leaked")
	}
	for _, want := range []string{"prompt_built", "tool_started", "tool_failed", "schema_version", "duration_ms"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
