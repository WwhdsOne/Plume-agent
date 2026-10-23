package app

import "testing"

// TestCommandScopeRequiresTrustedIdentity 守住本地固定键与 gateway 五段身份必填边界。
func TestCommandScopeRequiresTrustedIdentity(t *testing.T) {
	for _, scope := range []Scope{{}, LocalScope(), {Channel: "local"}} {
		// case: 本地身份不混入工作目录，工作区由存储查询独立筛选。
		key, err := scope.Key()
		if err != nil || key != LocalScopeKey || !scope.IsLocal() {
			t.Fatalf("local scope = %#v, key=%q error=%v", scope, key, err)
		}
	}
	valid := Scope{Channel: "wechat", AccountInstance: "account", Peer: "peer", Thread: "thread", Sender: "sender"}
	if err := valid.Validate(); err != nil || valid.IsLocal() {
		t.Fatalf("valid gateway scope rejected: %v", err)
	}
	for _, invalid := range []Scope{
		// case: 本地保留名不能携带远端身份；gateway 任意必填项缺失均拒绝。
		{Channel: "local", Sender: "pretend"},
		{AccountInstance: "a", Peer: "p", Thread: "t", Sender: "s"},
		{Channel: "c", Peer: "p", Thread: "t", Sender: "s"},
		{Channel: "c", AccountInstance: "a", Thread: "t", Sender: "s"},
		{Channel: "c", AccountInstance: "a", Peer: "p", Sender: "s"},
		{Channel: "c", AccountInstance: "a", Peer: "p", Thread: "t"},
		{Channel: "c", AccountInstance: "a", Peer: "p", Thread: "t", Sender: " \t"},
		// case: JSON 会替换非法 UTF-8，必须拒绝以免身份键产生碰撞。
		{Channel: "c", AccountInstance: "a", Peer: "p", Thread: "t", Sender: string([]byte{0xff})},
	} {
		if key, err := invalid.Key(); err == nil || key != "" {
			t.Fatalf("invalid scope accepted: %#v key=%q", invalid, key)
		}
	}
}

// TestCommandScopeKeysDoNotCollide 守住字段边界、渠道/账号/会话/线程/发送者均参与隔离。
func TestCommandScopeKeysDoNotCollide(t *testing.T) {
	scopes := []Scope{
		// case: 用分隔符拼接会撞键的不同五元组必须保持独立。
		{Channel: "a:b", AccountInstance: "c", Peer: "p", Thread: "t", Sender: "s"},
		{Channel: "a", AccountInstance: "b:c", Peer: "p", Thread: "t", Sender: "s"},
		{Channel: "a", AccountInstance: "b", Peer: "c:p", Thread: "t", Sender: "s"},
		{Channel: "a", AccountInstance: "b", Peer: "p", Thread: "c:t", Sender: "s"},
		{Channel: "a", AccountInstance: "b", Peer: "p", Thread: "t", Sender: "c:s"},
		{Channel: "a", AccountInstance: "b", Peer: "p", Thread: "t", Sender: "local"},
		{Channel: "a", AccountInstance: "b", Peer: "p", Thread: "t", Sender: "\uFFFD"},
	}
	seen := map[string]bool{LocalScopeKey: true}
	for _, scope := range scopes {
		key, err := scope.Key()
		if err != nil || seen[key] {
			t.Fatalf("scope key collision: %#v key=%q error=%v", scope, key, err)
		}
		seen[key] = true
		repeated, _ := scope.Key()
		if repeated != key {
			t.Fatalf("scope key is unstable: %q != %q", repeated, key)
		}
	}
}
