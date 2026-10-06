---
title: 0003 模型运行时：OpenAI SDK 适配、自研运行时与 TUI 优先
status: active
updated: 2026-10-06
summary: 现行模型路线：openai-go SDK 传输、协议适配器与自研运行时、TUI 优先；接口契约、错误分类与预算
---

# 0003 模型运行时：OpenAI SDK 适配、自研运行时与 TUI 优先

- 日期：2026-10-06。
- 决策方向：用户确认模型传输与协议解析采用 **OpenAI 官方 Go SDK**，执行策略、预算、trace 与评测自研；用协议适配器封装多套模型 API，为未来 Anthropic/Gemini 协议预留同一接口；首版做 TUI，不先做微信登录。
- 状态：方向已确认，本文具体契约与拆分待审核；仅设计，没有实现或性能结论。
- 替代范围：0001 中的 Eino 组件绑定、原计划的 Eino 执行循环与微信优先顺序，以及本文早期版本的 Resty + 自研解析方案。G0/G1a 审核历史、首批两家预设、配置目录、凭据安全与逐单元审核制度保留。
- 关联：[第一阶段](../phase-01-tui-agent.md)、[第二阶段](../phase-02-channel-gateway.md)、[原范围决策](0001-scope.md)、[微信调研](0002-wechat.md)。

> 修订记录（2026-10-06，同日两次决策）：本文件最初确定 Resty + 自研请求/JSON/SSE 解析；当日经用户再次确认改为 **OpenAI 官方 Go SDK**，理由是把协议编码与 SSE 解析交给官方维护，项目聚焦执行策略、可观测性与量化实验。协议适配器、注册工厂、错误分类、执行预算与 TUI 优先决策全部保留；原「Resty 传输与自行解析」「自研 SSE 分帧」按新决策改写，变更原因见 §4。此前版本见 git 历史（`8a555b8`）。

## 1. 为什么这样选

项目目标是理解并展示 Agent 的执行策略、可观测性和量化改造。当前尚未引入 Eino 或任何模型调用，不需要重写已完成的设置与配置模块。自研一个小型运行时可让循环、工具调度、预算和 trace 成为可独立审核的工作。

不采用「所有依赖都自己造」：HTTP 传输、协议编码与 SSE 分帧使用 OpenAI 官方 SDK；CLI/日志沿用现有库。也不采用「写一个通用框架」：首版只做项目需要的接口和文本/工具能力，通用 DAG、动态插件、分布式调度不在范围内。

代价是跟随 SDK 版本演进（升级须单独审核）、传输定制只能经 SDK 提供的 option/中间件表达，以及非官方兼容供应商的差异仍需自验。没有 Eino 运行基线，不以「换库」推导任何性能或质量收益。

## 2. 用协议适配，而不是按品牌复制客户端

| 概念 | 含义 | 约束 |
| --- | --- | --- |
| Provider | 产品预设：品牌、默认地址、候选模型、凭据规则 | 不能代替协议或能力判断 |
| Protocol | 请求/响应与流事件的契约 | Chat Completions、Responses、Messages 等不能仅靠换 URL 互通 |
| Model | 运行时配置的模型 ID | 同供应商下不同模型能力可能不同，不在循环里硬编码名字 |
| Capability | 某模型配置经验证的文本/流式/工具/usage 能力 | 支持、不支持、未验证分开；HTTP 200 不代表全部能力可用 |
| Adapter | 实现项目模型接口的具体协议适配器 | 协议字段转换、事件归一化、终止语义、供应商特有差异在内部处理 |

采用三种足够的设计手法：

1. **Adapter（适配器）**：Agent 面向自有消息/响应/事件接口；适配器把协议字段和供应商错误转换为统一契约。未来接入 Anthropic Messages 或 Gemini 时新增适配器实现同一消费接口，调用方不改。
2. **Registry + Factory（注册表与工厂）**：启动时根据配置选择构造函数并注入凭据、SDK 客户端和计时/事件依赖。新增适配器注册后不修改 TUI 和 Agent 分支；未知 ID 明确失败，禁止悄悄回退。
3. **组合**：共用 SDK 客户端装配与事件归一化。DeepSeek 适配器组合兼容协议实现并处理经过验证的差异，不复制整套客户端或搭抽象基类。

模型 fake 是同一消费接口的另一个实现。首版不另外建立泛型插件系统、万能 `map[string]any` 参数通道或每方法一层的装饰器；trace 由明确的调用位置记录，避免重复计量。

## 3. 模块与接口契约

| 模块 | 对调用方暴露 | 内部负责 |
| --- | --- | --- |
| `internal/model` | 消息、请求、响应、流、能力与错误的项目类型 | 统一语义，不 import TUI/微信类型 |
| `internal/model/endpoint` | 校验/规范化后的端点与装配好的 SDK 选项 | URL 安全规则、重定向/TLS/重试策略、超时与上下文；不解释工具语义 |
| `internal/model/openai` | 项目模型接口的 Chat Completions 实现 | 基于 openai-go SDK 的请求编码、响应/流事件归一化、apierror 分类 |
| `internal/model/deepseek` | 同一接口的 DeepSeek 实现 | 组合兼容实现，封装供应商差异和能力验证 |
| `internal/provider` | 预设与模型工厂 | 配置 ID 解析、凭据解析、选择 Adapter；保留 config 小接口隔离 |
| `internal/agent` | 运行输入与结构化运行事件 | 模型—工具循环、预算、终止条件 |
| `internal/app` / `internal/tui` | 会话/run 服务 / 终端呈现 | 会话提交 / 输入渲染；不自行发送模型 HTTP |

模型消费接口在 G1b.1 定义为两个操作：一次完整生成 `Generate` 和打开流式生成 `Stream`；两者均接受 context、相同的规范化请求。G1b.1 的流式操作明确返回 unsupported，G1b.3 才开启流式能力，不能用整包响应冒充流。

请求包含模型、按顺序的 messages、可选工具声明和已支持的生成参数。消息至少支持 system/user/assistant/tool、文本、tool calls 和 tool-call 关联 ID；不以单一字符串抹平工具消息。响应包含完整 assistant 消息、finish reason、可选 usage 与供应商请求 ID；不暴露 openai-go 类型给 Agent。

流采用拉取式读取与显式 Close：读取下一个结构化事件、读取终态、关闭资源。所有权交给调用者，必须在成功/失败/取消时关闭；Close 幂等；context 取消可打断阻塞读取。避免无法控制的后台生产 goroutine；若 TUI 桥接使用队列，容量有限并支持取消。

规范化事件至少包括文本增量、工具调用增量（含稳定 index/ID）、usage 更新和模型结束。usage 以可选值表达，不把未提供当 0；最终快照不能与之前累计值重复相加。模型结束与 run 结束不同：模型要求工具调用时该 run 尚未完成。

首版仅消费单一候选回答；适配器仅在协议支持时发送候选数量参数，不向所有供应商强塞 `n=1`。多 choice、图像、音频等返回若会影响语义则返回 unsupported/invalid_response，不静默丢弃。供应商特有选项通过有类型的内部配置实现；未经支持的选项明确拒绝，而非静默忽略。

### 首批覆盖与扩展

首批继续只开放 DeepSeek 和自定义兼容服务，文本 → 流式 → 工具调用分别验收。未来可以增加 Responses、Anthropic Messages、Gemini 等协议，但此处只预留同一接口，不表示已经支持；新增协议须单独核对官方文档、增加 fixture 与审核，不要求它们模仿 Chat Completions。

接入另一家同协议服务也需验证路径、鉴权、字段、流终态、工具和 usage；预设的推荐能力不自动成为某个端点/模型的实测能力。未验证能力需显式探测或测试，不能在首次调用时承诺已支持。

## 4. OpenAI SDK 传输与端点安全

首批使用 OpenAI 官方 Go SDK `github.com/openai/openai-go`，锁定 **v1.12.0**（2026-10-06 检查时的最新 GA 发布，非 rc），锁入 `go.mod/go.sum`；必须通过 Go 1.27.1 编译、竞态与依赖增量实测，不能使用浮动 `@latest` 作为可复现证据。SDK 承担 HTTP 发送、JSON 编解码、SSE 分帧与结构化错误；项目承担端点安全、请求规范化、事件归一化、错误分类、trace 与预算。

> 版本注记（2026-10-06）：选定 GA 的 v1 而非任何预发布版本。SDK 升级属于依赖变更，须单独审核并重跑兼容 fixture。DeepSeek 与自定义兼容服务同属 openai-chat-completions 协议族，由同一 SDK 适配器覆盖；供应商差异（如 DeepSeek 扩展字段）在专用适配逻辑中处理。

选型代价与边界：传输细节受 SDK 抽象约束，不能任意改写；SDK 行为异常时先用兼容 fixture 固化现象，再决定升级、绕过或报告上游，不悄悄打补丁。

首版工程限制（属于设计初值，不是供应商限制）：

- 一次模型调用含读取全过程 deadline 默认 120s；run 默认 180s；工具默认 5s。单步使用两者中较早的期限，由 context 施加，超时类别进入 trace。
- 非流式响应体由 SDK 读完整后解析；当响应声明 Content-Length 超过 8 MiB 时在中间件预检中拒绝并返回 `response_too_large`；无 Content-Length 时不做逐字节截断（已知局限，流式字节上限见 §5）。错误正文最多读取 16 KiB，并先脱敏再裁剪日志。
- URL 必须保留用户配置的路径前缀；不猜测补 `/v1`。SDK 在 base URL 后追加资源路径；兼容适配器用测试覆盖根地址、`/v1`、尾斜线和代理前缀。不在日志中输出 URL 中的敏感参数。
- 沿用 HTTPS、回环 HTTP 例外规则；拒绝 URL userinfo/fragment。通过受控 `http.Client` 注入禁止自动重定向的策略，避免携带凭据转往其他地址；不关闭 TLS 校验。现有配置不安全时明确报错，不静默改 URL。
- SDK 客户端构造后视为只读共享；每个模型配置构造独立客户端，Key 在构造时绑定，不做运行中热切换，切换供应商天然不复用 Key。
- SDK 自动重试显式关闭（`WithMaxAttempts(1)`，SDK 默认 2 次）；debug 输出默认关闭。HTTP 2xx 后的协议错误仍是失败，不能只按状态码统计成功。

以上限制先作为代码常量并在运行 manifest 中记录；未经需求验证，不把全部开关加入配置 schema。

## 5. 流式事件与协议语义的分层

SSE 字节分帧由 SDK 完成；项目的流式层在 SDK 事件之上做归一化：网络 chunk 不是事件边界，更不等于一个 token。归一化层输出文本增量、工具调用增量（含稳定 index/ID）、usage 更新与结束事件；无法表达的未知事件返回明确的 unsupported 事件或错误，不静默丢弃。

终态判定归项目：结束事件/`[DONE]` 之外，EOF 不能普遍等同成功，缺失协议必要结束证据时返回 `stream_interrupted`；usage 尾帧未收到记 unknown，不凭输出长度冒充计费值。

适配器把 SDK 迭代器包装为拉取式项目流：读取下一个结构化事件、读取终态、关闭资源；Close 幂等，context 取消可打断阻塞读取。流式总读取上限 16 MiB（经受控 transport 的计数 reader 实现），超限终止并返回 `response_too_large`。

首批工具参数分片按 call index/ID 组装，完整 JSON 且 schema 校验通过后才可执行；未完成、重复冲突的 ID、缺失参数或截断均不触发工具。空文本 delta 和纯 usage 帧不计首文本时间，纯元数据不能让 UI 提前显示「回答完成」。

流一旦开始，断流/取消不自动重连，不把两次生成拼为同一回复。已显示部分结果留在 UI 并标记不完整，usage 未收到记 unknown。用户重试创建新 run，关联前一个 run 但不自动复用未提交的工具轨迹。

## 6. 错误、重试和执行预算

统一错误携带分类、provider/内部协议、HTTP 状态（若有）、供应商请求 ID（若有）、是否已输出、可选 Retry-After，以及脱敏摘要。至少区分：invalid_config、unsupported、authentication、rate_limited、upstream、transport、timeout、cancelled、invalid_response、response_too_large、stream_interrupted。SDK 的 `apierror.Error` 按状态映射：401/403 → authentication、429 → rate_limited、4xx → invalid_response、5xx → upstream；网络层错误按超时/取消/传输细分。

「可能值得重试」不等于「可以安全重试」。首版不自动重试模型 POST、流或工具；网络超时可能已消耗费用。SDK 自动重试保持关闭，手动重试是一条新 run。后续增加重试必须单独审核：只由一层控制，明确场景、次数/退避/deadline、重计费风险和 attempt trace，不能叠加 SDK/Agent/网关三层重试。

自研循环的顺序：准备上下文 → 调用模型 → 若是最终文本则完成；若请求工具则校验并串行执行 → 以 call ID 回填结果 → 再调用模型。每轮有独立模型 span，工具结果与请求严格对应。

初始限制：每 run 最多 8 次模型调用、8 次工具执行；整体 180s；单工具 5s。开始下一步前检查剩余次数与时间；总工具数预检失败不执行该批工具。finish reason 为截断/内容阻断等不当作完整成功。工具参数错误或未知工具可作为结构化错误结果返回模型纠正，仍受预算约束；重复冲突 call ID、协议损坏终止 run，不猜测修复。

首版只读工具，不自动重试工具。未来引入有副作用工具必须增加确认、幂等/结果不确定策略，不因当前已有循环而自动获得执行授权。准确 token 预算在 usage/估算器不足时不能承诺硬限制；首版可靠限制是调用数、工具数与时间，报告实际已知用量。

## 7. 兼容现有配置与代码

当前 schema 是 v1，`ModelConfig.Protocol` 已保存 `deepseek` / `openai-compatible`，代码曾经把它解释为 Eino 适配器 ID。改路线不应使用户重新输入 Key 或手动改文件。

首批保持这两个持久化值及 schema v1，工厂引入兼容映射：

| 现有 provider / protocol | 运行时适配器 | 内部协议族 |
| --- | --- | --- |
| `deepseek` / `deepseek` | DeepSeek Adapter | `openai-chat-completions`，含已验证的供应商差异 |
| `custom-openai` / `openai-compatible` | 兼容 Chat Adapter | `openai-chat-completions` |

`protocol` 的历史存储语义暂保留为兼容适配选择器；trace 同时记录该值和内部协议族，不把品牌名当新的线协议。未来加入真正不同协议或修改持久化语义时，以独立 schema 迁移审核，不能直接覆写旧值。

`Preset.Component`（Eino 导入路径字符串）已于 2026-10-06 随路线切换提前移除（`internal/provider/registry.go`、`internal/config/config.go`），早于 G1b.1 开工；工厂/旧配置 fixture 工作仍留在 G1b.1。config 仍通过小 catalog 接口校验，不 import provider 的网络实现。

G1b.2 增加「暂不接入渠道」设置路径，TUI 只要求模型就绪。`channels` 可为空；已有渠道记录保留，未登录状态不影响 TUI，不自动开启网关。缺失/损坏 schema 或所选模型配置错误仍明确失败。

兼容测试：现有 v1 文件逐字节读回不变；旧 Key 引用可解析；DeepSeek/自定义地址不变；零渠道配置可聊天；微信未登录可聊天；未知适配器拒绝；切换供应商不复用 Key；保存取消/失败保护已落盘配置。

## 8. TUI 是首版入口，网关是后续入口

现有 huh 保留给 setup 表单，聊天 TUI 拟用 Bubble Tea。其 Model/Update/View 模式用于 UI 状态，不承载模型 HTTP 或工具业务；异步结果转换为 UI 消息。官方参考：[Bubble Tea](https://github.com/charmbracelet/bubbletea)。

> 修订注记（2026-10-06，G1b.2.1 立项）：上文「实现时匹配项目已锁定的终端依赖，不直接复制最新版不同主版本 API」基于当时 huh v1/Bubble Tea v1 的现状；经用户拍板，终端栈整体升级至 charm.land v2 全家桶（全部 GA）并重设计键位，以 G1b.2.1 单独审核，详见 `docs/tui-keys.md`。本条其余约束（TUI 不承载 HTTP/工具业务）不变。

应用层产生 run 事件，TUI 负责展示；同一 app/agent 以后可被微信适配器调用。首版串行多轮、可取消、流式内容和工具状态展示，不建立虚构的 `tui` 外部账号/凭据。键位、入口和历史提交规则见第一阶段 §2。

当前入口短描述仍写「微信为首个入口」、setup 仍有渠道步骤；Eino 注释与 `Preset.Component` 已于 2026-10-06 清理。剩余两项是 **G1b.1/G1b.2 的实现待办**，不能因为文档已变更就声称代码已经同步。

## 9. trace、指标与审核

自研运行时显式记录模型/工具/应用生命周期，保留现有 setup trace；不通过读终端输出反推执行状态。各调用 start 与唯一终态、父子关系、取消/错误和脱敏必须可测试。SDK 调用返回（或流迭代器建立）不代表流已读取完成，耗时在实际结束处计量。

首批基线包括固定请求/解析 fixture 正确率、12 个 smoke、TUI 首文本/完成/取消耗时、模型与工具调用数、已知 usage/费用、trace 完整率和本地基准。只比较相同适用数据集与环境，未知为 unknown/N/A，依赖增量与性能变化均实际测量。

实现顺序：G1b.1 非流式模型接口 → G1b.2 最小 TUI → G1b.3 流式消费与增量 UI → G3 工具循环。**每项独立审核；本文不是开始实施这些功能的授权。** 第一阶段审核通过后才讨论 G2 微信与后续渠道。
