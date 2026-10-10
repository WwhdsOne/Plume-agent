package model

import "testing"

// 守住能力快照的起步状态：三项推理能力初始均为 unverified——能力只能
// 按模型配置验证后置位，构造不得默认宣称支持。
func TestReasoningCapabilitiesStartUnverified(t *testing.T) {
	s := NewCapabilitySet()
	for _, cap := range []Capability{CapReasoningOutput, CapReasoningEffort, CapReasoningDisable} {
		if s.State(cap) != CapUnverified {
			t.Fatalf("%s state=%s", cap, s.State(cap))
		}
	}
}
