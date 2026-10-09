package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plume-agent/internal/config"
	"plume-agent/internal/soul"
)

func TestWizardInitializesSoulAfterModelAndPreservesRerun(t *testing.T) {
	fake := defaultFake()
	fake.skipChannel = true
	h := newHarness(t, fake)
	res, err := h.wiz.Run(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("PLUME_HOME"), "soul.md")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != soul.DefaultTemplate || res.SoulPath != path || !res.SoulCreated {
		t.Fatalf("soul not initialized: err=%v path=%q created=%v", err, res.SoulPath, res.SoulCreated)
	}
	trace := h.traceText()
	if !strings.Contains(trace, "initialize_soul") || strings.Index(trace, "save_config") > strings.Index(trace, "initialize_soul") || strings.Contains(trace, "沟通风格") {
		t.Fatal("missing ordered body-free soul trace")
	}
	custom := "# Soul\ncustom personality sentinel\n"
	if err := os.WriteFile(path, []byte(custom), 0o640); err != nil {
		t.Fatal(err)
	}
	fake.keep = true
	fake.secret = ""
	res, err = h.wiz.Run(res.Config)
	if err != nil || res.SoulCreated || res.SoulPath != path {
		t.Fatalf("rerun err=%v created=%v path=%q", err, res.SoulCreated, res.SoulPath)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != custom || strings.Contains(h.traceText(), "custom personality sentinel") {
		t.Fatal("custom personality replaced or traced")
	}
}

func TestWizardSoulDisableAndCustomPath(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "custom"}[enabled], func(t *testing.T) {
			fake := defaultFake()
			fake.skipChannel = true
			h := newHarness(t, fake)
			existing := &config.Config{Agent: config.DefaultAgent()}
			existing.Agent.Soul = &config.SoulConfig{Enabled: enabled, Path: "personality/custom.md", MaxBytes: 12345}
			res, err := h.wiz.Run(existing)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(os.Getenv("PLUME_HOME"), "personality", "custom.md")
			_, statErr := os.Stat(path)
			if enabled && (statErr != nil || res.SoulPath != path || !res.SoulCreated) {
				t.Fatalf("custom soul err=%v path=%q", statErr, res.SoulPath)
			}
			if !enabled && (!errors.Is(statErr, os.ErrNotExist) || res.SoulPath != "" || res.SoulCreated) {
				t.Fatalf("disabled soul initialized: err=%v path=%q", statErr, res.SoulPath)
			}
			if res.Config.Agent.SoulConfig().MaxBytes != 12345 {
				t.Fatal("setup lost soul customization")
			}
			res.Config.Agent.Soul.Path = "changed.md"
			if existing.Agent.Soul.Path != "personality/custom.md" {
				t.Fatal("setup aliases existing soul configuration")
			}
		})
	}
}

func TestWizardSoulCancellationAndInitializationFailure(t *testing.T) {
	for _, stage := range []string{"model", "channel"} {
		t.Run(stage, func(t *testing.T) {
			fake := defaultFake()
			fake.failAt = stage
			h := newHarness(t, fake)
			res, err := h.wiz.Run(nil)
			path := filepath.Join(os.Getenv("PLUME_HOME"), "soul.md")
			_, statErr := os.Stat(path)
			if stage == "model" && (!errors.Is(err, ErrCancelled) || res != nil || !errors.Is(statErr, os.ErrNotExist)) {
				t.Fatalf("model cancellation wrote soul: err=%v stat=%v", err, statErr)
			}
			if stage == "channel" && (!errors.Is(err, ErrChannelSkipped) || res == nil || statErr != nil) {
				t.Fatalf("channel cancellation lost saved soul: err=%v stat=%v", err, statErr)
			}
		})
	}
	fake := defaultFake()
	fake.skipChannel = true
	h := newHarness(t, fake)
	path := filepath.Join(os.Getenv("PLUME_HOME"), "soul.md")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := h.wiz.Run(nil)
	if err == nil || res == nil || res.ModelID == "" {
		t.Fatalf("soul failure discarded saved model: res=%v err=%v", res, err)
	}
	saved, loadErr := config.Load()
	if loadErr != nil || saved.DefaultModel != res.ModelID {
		t.Fatalf("model not retained: err=%v", loadErr)
	}
	if !strings.Contains(h.traceText(), "initialize_soul") || !strings.Contains(h.traceText(), "setup_failed") {
		t.Fatal("soul failure trace missing")
	}
}

func TestWizardPreservesExistingEmptySoul(t *testing.T) {
	fake := defaultFake()
	fake.skipChannel = true
	h := newHarness(t, fake)
	path := filepath.Join(os.Getenv("PLUME_HOME"), "soul.md")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := h.wiz.Run(nil)
	if err != nil || res.SoulCreated || res.SoulPath != path {
		t.Fatalf("empty soul initialization: err=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatal("empty soul replaced")
	}
	encoded, err := json.Marshal(res.Config)
	if err != nil || !strings.Contains(string(encoded), `"soul"`) {
		t.Fatal("soul settings not saved")
	}
}
