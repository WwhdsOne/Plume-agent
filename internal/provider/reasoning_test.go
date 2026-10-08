package provider

import (
	"testing"

	"plume-agent/internal/model"
)

func TestReasoningPolicyRequiresVerifiedEndpointAndModel(t *testing.T) {
	for _, tc := range []struct {
		url, name, requested, effective string
		explicit, rejected              bool
	}{
		{"https://api.deepseek.com", "deepseek-flash", "", "high", false, false},
		{"https://api.deepseek.com/v1/", "deepseek-v4-pro", "medium", "high", true, false},
		{"https://api.deepseek.com", "deepseek-flash", "none", "none", true, false},
		{"https://proxy.example/v1", "deepseek-flash", "", "", false, false},
		{"https://proxy.example/v1", "deepseek-flash", "none", "", true, true},
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
