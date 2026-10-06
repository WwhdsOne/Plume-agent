package provider

import "testing"

func TestFirstReleasePresets(t *testing.T) {
	r := NewRegistry()
	if got := len(r.All()); got != 2 {
		t.Fatalf("len(All()) = %d, want 2 (DeepSeek + a generic OpenAI-compatible preset)", got)
	}

	ds, ok := r.Lookup("deepseek")
	if !ok {
		t.Fatal("deepseek preset is missing")
	}
	if ds.Protocol != "deepseek" {
		t.Errorf("deepseek protocol = %q, want %q", ds.Protocol, "deepseek")
	}
	if ds.DefaultBaseURL != "https://api.deepseek.com" {
		t.Errorf("deepseek default base URL = %q", ds.DefaultBaseURL)
	}
	if ds.Component == "" {
		t.Error("deepseek component import path is empty")
	}

	co, ok := r.Lookup("custom-openai")
	if !ok {
		t.Fatal("custom-openai preset is missing")
	}
	if co.Protocol != "openai-compatible" {
		t.Errorf("custom-openai protocol = %q, want %q", co.Protocol, "openai-compatible")
	}
	if co.DefaultBaseURL != "" {
		t.Errorf("custom-openai default base URL = %q, want empty (user must supply one)", co.DefaultBaseURL)
	}
}

// 百炼/Qwen 在 G0 已明确延后；守住这条，防止它被未经审核地重新加回来。
func TestDeferredPresetsAreAbsent(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"dashscope-beijing", "qwen"} {
		if _, ok := r.Lookup(id); ok {
			t.Errorf("preset %q is present but was deferred in docs/decisions/0001-scope.md", id)
		}
	}
}

func TestProtocolForAndDefaultBaseURL(t *testing.T) {
	r := NewRegistry()
	if p, ok := r.ProtocolFor("deepseek"); !ok || p != "deepseek" {
		t.Errorf("ProtocolFor(deepseek) = (%q, %v)", p, ok)
	}
	if _, ok := r.ProtocolFor("nope"); ok {
		t.Error("ProtocolFor(nope) reported ok, want false")
	}
	if got := r.DefaultBaseURL("custom-openai"); got != "" {
		t.Errorf("DefaultBaseURL(custom-openai) = %q, want empty", got)
	}
	if got := r.DefaultBaseURL("nope"); got != "" {
		t.Errorf("DefaultBaseURL(nope) = %q, want empty", got)
	}
}

func TestRegisterAddsAndReplacesWithoutDuplicating(t *testing.T) {
	r := NewRegistry()

	r.Register(Preset{ID: "extra", DisplayName: "Extra", Protocol: "openai-compatible"})
	if got := len(r.All()); got != 3 {
		t.Fatalf("len(All()) = %d, want 3", got)
	}
	if got := r.All()[2].ID; got != "extra" {
		t.Errorf("All()[2].ID = %q, want %q", got, "extra")
	}

	r.Register(Preset{ID: "deepseek", DisplayName: "DeepSeek (updated)", Protocol: "deepseek"})
	if got := len(r.All()); got != 3 {
		t.Fatalf("re-registering changed len(All()) to %d, want 3", got)
	}
	if got := r.All()[0].DisplayName; got != "DeepSeek (updated)" {
		t.Errorf("replacing a preset lost its position or value: %q", got)
	}
}
