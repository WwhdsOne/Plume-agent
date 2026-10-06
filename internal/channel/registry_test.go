package channel

import "testing"

func TestFirstReleaseChannels(t *testing.T) {
	r := NewRegistry()
	if got := len(r.All()); got != 3 {
		t.Fatalf("len(All()) = %d, want 3 (weixin + two placeholders)", got)
	}

	wx, ok := r.Lookup("weixin")
	if !ok {
		t.Fatal("weixin channel is missing")
	}
	if !wx.Enabled || wx.ComingSoon {
		t.Errorf("weixin = %+v, want the only enabled, non-placeholder channel", wx)
	}

	for _, id := range []string{"feishu", "qq"} {
		c, ok := r.Lookup(id)
		if !ok {
			t.Fatalf("channel %q is missing", id)
		}
		if c.Enabled {
			t.Errorf("channel %q is enabled, want it disabled until it is actually implemented", id)
		}
		if !c.ComingSoon {
			t.Errorf("channel %q is not marked as coming soon", id)
		}
	}
}

func TestAvailable(t *testing.T) {
	r := NewRegistry()

	if known, enabled := r.Available("weixin"); !known || !enabled {
		t.Errorf("Available(weixin) = (%v, %v), want (true, true)", known, enabled)
	}
	if known, enabled := r.Available("feishu"); !known || enabled {
		t.Errorf("Available(feishu) = (%v, %v), want (true, false)", known, enabled)
	}
	if known, enabled := r.Available("nope"); known || enabled {
		t.Errorf("Available(nope) = (%v, %v), want (false, false)", known, enabled)
	}
}

func TestRegisterAddsAndReplacesWithoutDuplicating(t *testing.T) {
	r := NewRegistry()

	r.Register(Type{ID: "test-channel", DisplayName: "Test", Enabled: true})
	if got := len(r.All()); got != 4 {
		t.Fatalf("len(All()) = %d, want 4", got)
	}

	r.Register(Type{ID: "weixin", DisplayName: "微信 (updated)", Enabled: true})
	if got := len(r.All()); got != 4 {
		t.Fatalf("re-registering changed len(All()) to %d, want 4", got)
	}
	if got := r.All()[0].DisplayName; got != "微信 (updated)" {
		t.Errorf("replacing a channel lost its position or value: %q", got)
	}
}
