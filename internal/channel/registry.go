// Package channel 保存消息渠道注册表。首版只启用微信；飞书和 QQ 也列出来，让向导能
// 把它们显示为"待支持"，同时既不接收其凭据，也不伪造"已配置"状态。
package channel

// Type 是一个渠道选项。Enabled 表示今天就能选中并配置；ComingSoon 表示向导会展示它，
// 但拒绝启用。
type Type struct {
	ID          string
	DisplayName string
	Enabled     bool
	ComingSoon  bool
}

// Registry 是静态、有序的渠道类型集合。
type Registry struct {
	types map[string]Type
	order []string
}

// NewRegistry 返回填入了首版渠道的注册表。
func NewRegistry() *Registry {
	r := &Registry{types: map[string]Type{}}
	r.Register(Type{ID: "weixin", DisplayName: "微信", Enabled: true})
	r.Register(Type{ID: "feishu", DisplayName: "飞书", ComingSoon: true})
	r.Register(Type{ID: "qq", DisplayName: "QQ", ComingSoon: true})
	return r
}

// Register 新增或替换一个渠道类型，并保留原来的位置。
func (r *Registry) Register(t Type) {
	if _, exists := r.types[t.ID]; !exists {
		r.order = append(r.order, t.ID)
	}
	r.types[t.ID] = t
}

// All 按注册顺序返回全部渠道类型。
func (r *Registry) All() []Type {
	out := make([]Type, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.types[id])
	}
	return out
}

// Lookup 返回指定 ID 的渠道类型。
func (r *Registry) Lookup(id string) (Type, bool) {
	t, ok := r.types[id]
	return t, ok
}

// Available 报告渠道类型是否已知，以及本 build 是否允许启用它。
// 它满足 config.ChannelCatalog。
func (r *Registry) Available(id string) (known, enabled bool) {
	t, ok := r.types[id]
	if !ok {
		return false, false
	}
	return true, t.Enabled
}
