package agent

import "time" // 导入 time 包，用于定义各种超时时长

// Limits 是执行上限，不是 token 估算。启动装配后不可变。
type Limits struct {
	// ModelCalls：允许调用模型的次数上限
	// ToolCalls：允许调用工具的次数的上限
	// RequestBytes：单次请求体允许的最大字节数
	// ArgumentBytes：单次工具调用参数允许的最大字节数
	// ResultBytes：单次工具调用结果允许的最大字节数
	// RunTimeout：整个运行（Run）的总超时时间
	// ModelTimeout：单次模型调用的超时时间
	// ToolTimeout：单次工具调用的超时时间
	ModelCalls, ToolCalls, RequestBytes, ArgumentBytes, ResultBytes int
	ToolCallsPerStep                                                int // 单次模型响应最多可声明的工具项数
	RunTimeout, ModelTimeout, ToolTimeout                           time.Duration
}

// DefaultLimits 返回一套默认的执行上限配置
func DefaultLimits() Limits {
	return Limits{
		ModelCalls:       0,                  // 零表示不限制整轮模型次数
		ToolCalls:        0,                  // 零表示不限制整轮工具次数
		ToolCallsPerStep: 64,                 // 单响应仍限制工具项数，保护协议组装资源
		RequestBytes:     8 << 20,            // 默认请求体上限 8 MiB
		ArgumentBytes:    1 << 20,            // 默认工具参数上限 1 MiB
		ResultBytes:      64 << 10,           // 默认工具结果上限 64 KiB
		RunTimeout:       0,                  // 零表示没有整轮墙钟期限，取消仍生效
		ModelTimeout:     1800 * time.Second, // 单次模型完整调用上限，与空闲超时不同
		ToolTimeout:      180 * time.Second,  // 支持编译、测试等本地命令
	}
}

// SetLimits 用传入的 Limits 覆盖 Runtime 当前的执行上限配置
func (r *Runtime) SetLimits(l Limits) { r.limits = l }

// RunTimeout 供 app 从接受输入起施加同一整体期限，覆盖事件桥准备等待。
// 返回 Runtime 当前配置的整体运行超时时长
func (r *Runtime) RunTimeout() time.Duration { return r.limits.RunTimeout }
