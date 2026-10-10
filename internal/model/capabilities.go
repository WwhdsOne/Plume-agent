package model

// Capability 是一项模型能力（0003 §2）。能力按模型配置验证，不由
// 供应商品牌推断；HTTP 200 不代表全部能力可用。
type Capability string

// 能力维度值。语义由各适配器的验证逻辑定义；HTTP 200 不代表任一能力可用。
const (
	CapText             Capability = "text"
	CapStream           Capability = "stream"
	CapTools            Capability = "tools"
	CapUsage            Capability = "usage"
	CapReasoningOutput  Capability = "reasoning_output"
	CapReasoningEffort  Capability = "reasoning_effort"
	CapReasoningDisable Capability = "reasoning_disable"
)

// CapabilityState 把能力分为支持、不支持与未验证，三者不混用。
type CapabilityState string

// 能力三态值：只有经端点验证才记 supported，未验证不冒充支持。
const (
	CapSupported   CapabilityState = "supported"
	CapUnsupported CapabilityState = "unsupported"
	CapUnverified  CapabilityState = "unverified"
)

// CapabilitySet 是某模型配置的能力快照。首版只做记录与查询，
// 不参与路由决策。
type CapabilitySet struct {
	states map[Capability]CapabilityState
}

// NewCapabilitySet 返回全部能力均为 unverified 的快照。
func NewCapabilitySet() CapabilitySet {
	return CapabilitySet{states: map[Capability]CapabilityState{
		CapText:             CapUnverified,
		CapStream:           CapUnverified,
		CapTools:            CapUnverified,
		CapUsage:            CapUnverified,
		CapReasoningOutput:  CapUnverified,
		CapReasoningEffort:  CapUnverified,
		CapReasoningDisable: CapUnverified,
	}}
}

// Set 返回设置后的快照（值语义，便于测试构造）。
func (s CapabilitySet) Set(c Capability, st CapabilityState) CapabilitySet {
	if s.states == nil {
		s.states = map[Capability]CapabilityState{}
	}
	s.states[c] = st
	return s
}

// State 返回能力状态；未知能力视为 unverified。
func (s CapabilitySet) State(c Capability) CapabilityState {
	if s.states == nil {
		return CapUnverified
	}
	if st, ok := s.states[c]; ok {
		return st
	}
	return CapUnverified
}
