package provider

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveProbeDeepseek 是显式联网 probe（0003 §4）：只有当环境变量
// 提供真实 Key 时才执行，否则跳过——离线测试与 CI 绝不产生付费调用。
//
//	DEEPSEEK_LIVE_KEY=sk-... go test ./internal/provider -run TestLiveProbe -v
//
// Key 只从环境变量读取，绝不写入仓库、配置或 trace；本测试运行至多
// 发起一次最小请求。建议使用可随时轮换的测试 Key。
func TestLiveProbeDeepseek(t *testing.T) {
	key := os.Getenv("DEEPSEEK_LIVE_KEY")
	if key == "" {
		t.Skip("set DEEPSEEK_LIVE_KEY to run the explicit live probe")
	}
	modelID := os.Getenv("DEEPSEEK_LIVE_MODEL")
	if modelID == "" {
		modelID = "deepseek-chat" // DeepSeek 官方稳定模型名，避免依赖预设建议项
	}

	factory := NewModelFactory(NewRegistry(), mapCreds{"live": key})
	spec := ModelSpec{
		Provider:  "deepseek",
		Protocol:  ProtocolDeepSeek,
		BaseURL:   "https://api.deepseek.com",
		Model:     modelID,
		APIKeyRef: "live",
	}

	start := time.Now()
	resp, err := factory.Probe(context.Background(), spec)
	if err != nil {
		t.Fatalf("live probe failed: %v", err)
	}
	t.Logf("model=%s finish=%s content=%q usage=%+v elapsed=%s",
		modelID, resp.FinishReason, resp.Message.Content, resp.Usage, time.Since(start).Round(time.Millisecond))
	if !resp.Usage.OK {
		t.Logf("note: live response did not include usage (unknown)")
	}
}
