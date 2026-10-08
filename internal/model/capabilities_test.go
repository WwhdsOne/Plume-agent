package model

import "testing"

func TestReasoningCapabilitiesStartUnverified(t *testing.T) {
	s := NewCapabilitySet()
	for _, cap := range []Capability{CapReasoningOutput, CapReasoningEffort, CapReasoningDisable} {
		if s.State(cap) != CapUnverified {
			t.Fatalf("%s state=%s", cap, s.State(cap))
		}
	}
}
