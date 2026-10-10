package provider

import (
	"testing"

	"plume-agent/internal/model"
)

// 显式 reasoning_effort 只对已验证的 DeepSeek 官方端点 + 白名单模型放行，未验证端点不盲发。
func TestReasoningPolicyRequiresVerifiedEndpointAndModel(t *testing.T) {
	for _, tc := range []struct {
		url, name, requested, effective string
		explicit, rejected              bool
	}{
		{"https://api.deepseek.com", "deepseek-flash", "", "high", false, false},
		// case: 有效值把 medium 归一为 high，medium 不单独透传。
		{"https://api.deepseek.com/v1/", "deepseek-v4-pro", "medium", "high", true, false},
		{"https://api.deepseek.com", "deepseek-flash", "none", "none", true, false},
		// case: 未验证端点未显式配置时静默跳过（有效值为空，不发送强度）。
		{"https://proxy.example/v1", "deepseek-flash", "", "", false, false},
		// case: 未验证端点上任何显式强度（含关闭用的 none）都被拒绝。
		{"https://proxy.example/v1", "deepseek-flash", "none", "", true, true},
		// case: 端点已验证但模型不在白名单时，显式请求同样拒绝。
		{"https://api.deepseek.com", "unknown-model", "high", "", true, true},
	} {
		spec := ModelSpec{Provider: "deepseek", Protocol: ProtocolDeepSeek, BaseURL: tc.url, Model: tc.name}
		if tc.explicit {
			spec.ReasoningEffort = &tc.requested
		}
		policy, err := ResolveReasoning(spec)
		if (err != nil) != tc.rejected {
			t.Fatalf("%+v: policy=%+v err=%v", tc, policy, err)
		}
		if !tc.rejected && policy.Effective != model.ReasoningEffort(tc.effective) {
			t.Fatalf("%+v: policy=%+v", tc, policy)
		}
	}
}
