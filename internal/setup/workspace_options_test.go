package setup

import (
	"encoding/json"
	"plume-agent/internal/config"
	"testing"
)

func TestWizardPreservesWorkspaceToolsAndBudget(t *testing.T) {
	fake := defaultFake()
	fake.skipChannel = true
	h := newHarness(t, fake)
	var existing config.Config
	if err := json.Unmarshal([]byte(`{"schema_version":1,"agent":{"budget":{"model_calls":77}},"tools":{"enabled":[],"workspace":"/tmp"}}`), &existing); err != nil {
		t.Fatal(err)
	}
	res, err := h.wiz.Run(&existing)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Agent == nil || res.Config.Agent.Budget.ModelCalls != 77 || res.Config.Tools == nil || len(res.Config.Tools.Enabled) != 0 || res.Config.Tools.Workspace != "/tmp" {
		t.Fatal("setup lost workspace customization")
	}
	res.Config.Tools.Enabled = append(res.Config.Tools.Enabled, "read")
	res.Config.Agent.Budget.ModelCalls = 5
	if len(existing.Tools.Enabled) != 0 || existing.Agent.Budget.ModelCalls != 77 {
		t.Fatal("setup aliases existing config")
	}
}
