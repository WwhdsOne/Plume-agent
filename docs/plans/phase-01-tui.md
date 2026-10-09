---
title: 第一阶段：TUI Agent 开发计划
status: active
updated: 2026-10-09
summary: 第一阶段审核计划：G1b.4、G3/G3.1 已通过，后续渠道/记忆/技能需另行授权
---

# 第一阶段：TUI Agent 开发计划

> 日期：2026-10-06。范围：终端聊天、OpenAI SDK 模型适配、自研 Agent 循环、trace 和基线评测；微信登录不属于本阶段。
> 状态（2026-10-09）：G0 至 G1b.4、G3/G3.1 已通过。用户审核通过六种开发工具、可配置宽松预算及其基础工具循环；后续阶段仍待独立授权。单元状态以 [审核记录](../reviews/) 为准。原微信优先/Eino 方案由 [决策 0003](../decisions/0003-model-runtime.md) 替代；模型传输采用 OpenAI 官方 SDK。
> 执行约定：使用 executing-plans 按审核单元推进；每个单元完成即停，用户明确通过后才进入下一单元，不使用默认批量执行。本文是阶段计划，各单元开工前再细化测试与实现步骤。

**Goal：** 用 Go 构建可在 TUI 中多轮聊天、执行受控工具、解释每一步执行过程的个人 Agent，并用固定任务集衡量后续改造的收益与退步。

**Architecture：** TUI 通过应用层调用自研 Agent；Agent 只依赖自有模型接口和工具接口。模型层用 OpenAI 官方 Go SDK（openai-go）完成 HTTP 发送与协议/SSE 解析，经端点安全校验与事件归一化，通过协议适配器和注册工厂扩展模型 API。trace 与评测从第一条调用链开始建设。

**Tech Stack：** Go、`github.com/openai/openai-go` v1.12.0、cobra、charm.land v2 终端栈（Bubble Tea / Bubbles / Lip Gloss / huh）、Glamour v2.0.1、zap/JSONL。OpenTelemetry、SQLite 留后续单元，不使用 Eino。

## 1. 项目定位、现状与范围

`plume-agent` 是以可解释执行过程和量化实验为重点的简历项目。借鉴 [Hermes](https://github.com/NousResearch/hermes-agent) 的个人 Agent 与多入口思路，不复刻全部功能。

已完成并审核通过：G0/G1a 的配置基础；G1b.1 的非流式模型接口、模型 trace 与离线评测 runner；G1b.2 的最小单轮 Agent、多轮会话及聊天 TUI；G1b.2.1 的 v2 终端栈和键位；G1b.2.2 的点阵羽毛开屏与雾青主题。证据保留在 [审核目录](../reviews/)，不改写历史测量结果。

G1b.3 已实现流式模型调用、Markdown 答案、思考分区与强度配置、阶段文案、脱敏 run/模型/UI 首答案 trace；证据和局限见 [G1b.3](../reviews/G1b.3.md)。G3/G3.1 工具循环与六种开发工具已实现并通过审核；微信登录仍未实现。

首个演示：设置模型 → 打开 TUI → 多轮输入并看到流式回答 → 查询时间或计算 → 看到工具状态、结果与 run ID → 用 trace 定位一次失败 → 查看基线指标。

首版做单实例、单个活动本地会话、文本模型与开发工具。用户于 2026-10-09 审核通过 G3.1 的 read/grep/glob/edit/write/bash 和可配置宽松预算；见[归档实施计划](../archive/plans/2026-10-09-g3-workspace-tools.md)。跨重启恢复、长期记忆、技能、多模态、多 Agent、后台任务及消息网关仍不作为首版验收前提。

### 架构取舍

| 方案 | 获得的能力 | 承担的成本 | 结论 |
| --- | --- | --- | --- |
| OpenAI SDK + 协议适配器 + 小型运行时 | 协议/流解析由官方维护；执行策略、trace 与评测可逐项解释、测试 | 跟随 SDK 演进；传输定制经 SDK option 表达 | 用户已选择（2026-10-06，替代早先 Resty 方案） |
| Eino ADK/组件 | 复用框架的运行时和组件 | 学习和适配框架接口，项目控制点经框架提供 | 本阶段不引入，保留历史决策来源 |
| 从零写通用 Agent 框架 | 通用编排与插件扩展 | 验证面过大，延迟可演示闭环 | 不采用 |

自研不自动意味着更快或更省 token；当前不存在 Eino 运行基线，不能宣称“相比 Eino 提升”。设计模式服务实际变化：适配器隔离协议，注册工厂装配实现，组合复用传输与解析；不堆砌继承层级。

## 2. 首次配置与 TUI 体验

### 目标命令（尚未实现的部分见下）

```text
plume setup                 # 已实现，模型配置完成即可使用 TUI，渠道可跳过
plume                       # 已实现，交互终端无配置自动 setup；配置就绪时打开聊天
plume chat                  # 已实现，显式打开 TUI，使用 default_model
plume chat --model <配置ID>  # 已实现，只选择已保存配置，不通过参数传入 Key
plume chat --offline        # 已实现，scripted fake 演示，无配置/Key/联网要求
plume config show           # 已实现，脱敏查看配置
```

非 TTY 调用聊天入口明确报错并退出，不进入交互等待；CI 通过应用接口或评测入口运行，终端交互证据另用真实 pty。`--offline` 与 `--model` 互斥，离线模式不读配置，不保存虚假模型配置或覆盖用户配置。

裸 `plume` 无配置且 stdin/stdout 都是 TTY 时运行设置向导；无配置的非 TTY 调用只显示帮助与 `plume setup` 提示。向导正常结束后返回终端，并打印 `plume`（开始聊天）、`plume setup`（再次设置）和 `plume config show`（查看配置）三条提示，不自动开启聊天。

### 设置向导的目标行为

保留 G1a 的供应商列表、候选模型/自定义输入、隐藏 Key、默认 Base URL、原子保存和 setup trace。模型保存后即具备 TUI 启动条件；渠道步骤允许选择“暂不接入渠道”，这是跳过渠道设置，**不把 TUI 伪装成微信或持久化一个假渠道**。旧渠道记录原样保留；不要求删除微信配置才能聊天。

首批预设继续是 `deepseek` 和 `custom-openai`。有默认 URL 时不逐次询问，已有非默认 URL 不静默重置；切换供应商不复用旧 Key。模型名允许自定义，不依赖模型列表接口成功。

配置目录继续使用 `PLUME_HOME` → POSIX `~/.plume` / Windows `%LOCALAPPDATA%\plume`，保留现有回退规则。`config.json` 只保存凭据引用，credentials 目录 0700、文件 0600；取消后保留此前已成功保存的数据，不将两阶段保存描述为全局事务。

TUI 启动检查 schema、所选模型及凭据；不探测网关、不检查微信登录凭据是否有效。结构损坏仍报错，但未登录或尚未接入的渠道不阻塞本地聊天。模型配置在下次启动聊天会话时生效，首版不做运行中热切换。

可选模型连通性检查只在用户显式选择后发送短请求，提示可能产生费用；未验证、验证失败和验证成功分开显示。默认测试全离线。

### 聊天界面与生命周期

首版采用单屏：可滚动的聊天记录、输入区、状态栏；状态栏展示模型、运行状态、run ID、耗时和已知 usage。工具执行显示名称、校验/执行/完成/失败状态与脱敏结果摘要，不显示模型未公开的内部思维链。

G1b.3 已实现基础 Markdown、接口实际返回的思考预览与请求前准备状态。详细行为、`tui.status_messages` 及模型级 `reasoning_effort` 见 [技术契约](../specs/tui/streaming.md)。默认偏好 high、none 关闭，setup 不询问；参数经能力验证，未验证端点不盲发默认参数。已随 G1b.3 审核通过。

- 键位以 [TUI 键位契约](../specs/tui/keys.md)（G1b.2.1，含终端栈升级 charm.land v2）为准，对齐 Codex CLI / Claude Code 惯例：Enter 发送；换行三层渐进（Shift+Enter（kitty 终端）→ Alt/Option+Enter → `\`+Enter 兜底）；Esc 中断 run；Ctrl+C 一次中断/清空且两次退出；Ctrl+D 空输入退出；Ctrl+L 重绘；Up/Down 输入草稿历史；PgUp/PgDn 滚动；Ctrl+N 新会话；键位经 `internal/tui/keys.go` 的 KeyMap 平台抽象，mac/linux 优先，v2 原生 ConPTY 增强为 Windows 预留。空输入不产生请求。
- Esc 取消当前 run；Ctrl+C 按键位契约执行取消/清空/双击退出。退出时取消请求、关闭流、刷出 trace 并恢复终端。
- Ctrl+N 在空闲时创建新会话并清空当前上下文；运行中提示先取消，不暗中丢弃任务。
- 首版同会话串行。运行中可编辑下一条草稿，但再次发送会被明确拒绝，不创建并发模型请求或隐式队列。
- 模型失败/断流/取消保留已显示的部分内容并标记“不完整”，不当作成功回答写入下一轮模型上下文。只把完整提交的轮次加入历史；再次发送是新的 run，不自动重放工具。
- 首版不做隐式摘要或静默删历史；模型报告上下文超限时展示可诊断错误并允许新建会话，压缩策略留给独立实验。
- 网络调用不阻塞 UI 更新；终端缩放不丢失输入。模型/工具输出先过滤终端控制序列，禁止其执行 OSC、修改剪贴板或控制光标。
- 普通日志与 trace 写文件，不直接穿插到 TUI 屏幕；UI 状态消费结构化事件，不能靠解析日志驱动。

TUI 已在 G1b.2.1 统一采用 charm.land v2 栈；主题以 G1b.2.2 契约为准。新增 Markdown 依赖须验证兼容与体积增量，不能重新引入 v1 栈。

## 3. 开发与审核制度

1. 开工前说明本单元目标、拟改文件、运行命令和验收条件；先写失败测试，再实现最小闭环。
2. 每次只实现一个审核单元，不自动进入下一单元，不把文档授权视为代码实现授权。
3. 交付可复现命令、实际结果、成功与失败 trace、全部适用指标；没有实现的能力记 N/A。
4. 编写 `docs/reviews/<单元ID>.md`，列出限制和用户检查项，等待明确“通过/继续”。
5. 基线只报绝对值，变化列 N/A；不编造收益，也不将测试通过当作用户审核通过。

G0/G1a 的历史编号与通过状态保留；原 G1b 拆为 G1b.1–G1b.3，2026-10-08 增加 G1b.4 可配置底部状态栏并插入 G3 前；G3 仍代表工具能力，但前移到微信之前。G2a.1/G2a.2/G2b 的原有渠道含义保留在 [第二阶段计划](phase-02-channel.md)，编号不再代表执行先后。

## 4. 运行链路与模块职责

```text
模型配置 → 注册工厂 → 协议适配器 → OpenAI SDK HTTP（端点校验）
                         ↑
TUI → app（会话/run）→ agent（循环/预算）→ 模型接口 / 受控工具
 ↑                         ↓
 └──── 结构化运行事件 ──────┴→ telemetry → trace / eval

后续微信、飞书、QQ：渠道适配器 → 同一 app / agent，不复制运行时
```

| 模块 | 职责与约束 |
| --- | --- |
| setup/config | 向导、schema、校验、凭据引用与原子保存；不依赖模型 SDK/TUI 类型 |
| provider | 保留供应商预设；通过注册工厂解析凭据、装配适配器；不向 Agent 暴露密钥 |
| model（已建） | 自有消息/请求/响应/能力/错误契约；端点安全、协议适配与 SDK 装配；G1b.3 扩展流式 |
| tui（已建） | 输入、状态更新、渲染、取消意图；不拼 HTTP、prompt 或执行工具 |
| app（已建） | 会话历史、run 生命周期、串行限制；后续扩展持久化与渠道调度 |
| agent（已建最小单轮） | G1b.3 流消费；G3 扩展统一请求拼装、工具循环、prompt 版本、轮数与执行预算 |
| tools（拟新增） | 参数校验与确定性计算，遵守 context 取消；不直接控制 UI/渠道 |
| telemetry | 已有 setup/模型 trace；计划扩展流式计量、应用与工具事件 |
| eval（已建） | 已有离线种子与 runner；后续评分、可比性检查与统计；不承载线上业务 |
| channel | 现有预设保留；实际消息适配器在第二阶段实现 |

模型层采用 Adapter + Registry/Factory + 组合。一个线协议可以服务多个供应商品牌；供应商差异在专用适配逻辑/能力中表达，不按模型名复制客户端，也不以换 URL 冒充全协议支持。详细契约与兼容方案见 [0003](../decisions/0003-model-runtime.md)。

## 5. 第一阶段审核单元

G1b.1/G1b.2 的清单保留原规划口径，实际通过状态及限制以对应 `docs/reviews/` 为准；当前 G3/G3.1 已通过，不能把未回填的历史清单当作重新实施授权。

### G0 / G1a：已完成，保留证据

不重做已通过的配置与向导。后续只针对 TUI 必需的改动增加回归：跳过渠道、模型独立就绪校验、旧配置读取和入口提示。已实现与目标行为必须分别说明。

### G1b.1：OpenAI SDK 请求、非流式解析与可测量模型接口

**拟改文件：** `go.mod`、`go.sum`、`internal/provider/{registry,factory,probe}.go`、`internal/model/{types,errors,capabilities,fake}.go`、`internal/model/endpoint/endpoint.go`、`internal/model/openai/chat.go`、`internal/model/deepseek/chat.go`、对应 `_test.go`、`internal/telemetry/model.go`、`eval/datasets/smoke.v1.jsonl`。这些是规划路径，不预建空包。

- [ ] 锁定 `github.com/openai/openai-go` v1.12.0，记录 Go 1.27.1 编译、竞态与依赖增量；显式禁用 SDK 自动重试（默认 2 次）与 debug 输出；不引入 Eino 或其他模型 SDK。
- [ ] 保留 schema v1 的 `protocol` 值，工厂按 0003 映射到内部 `openai-chat-completions` 协议族与同一 SDK 适配器；补旧配置 fixture。（Component 元数据与过时注释已于 2026-10-06 随路线切换提前移除。）
- [ ] 先用 `httptest.Server` 断言 URL 路径（根地址/`/v1`/尾斜线/代理前缀）、鉴权、模型 ID、消息体；再实现兼容非流式请求/解析与 DeepSeek 适配。fake 与真实适配器实现同一消费接口。
- [ ] 明确 context 取消、超时、响应体上限（Content-Length 中间件预检）、非 JSON 错误、usage 缺失（unknown 不当 0）、鉴权失败、限流、无效响应、禁止自动重定向；SDK `apierror` 按 0003 §6 映射错误分类。
- [ ] 增加可选联网 probe，不能在配置保存或离线测试中隐式付费调用。
- [ ] 建立 12 个离线种子：正常回答 4、空/无效输入 2、模型错误 2、超时/取消 2、trace 关联/脱敏 2；报告绝对值和适用项。
- [ ] 交付 `docs/reviews/G1b.1.md` 后停止。此时可通过测试展示请求闭环，不宣称已完成聊天 TUI 或工具循环。

**验收：** `rtk go test ./internal/model/... ./internal/provider/... ./internal/telemetry/...`、`rtk go test -race ./...`；默认全离线。新增 Go 包存在后才执行这些命令。

### G1b.2：最小可交互 TUI 与多轮会话

**拟改文件：** `cmd/plume/{main,chat,setup,prompter}.go`、`internal/setup/wizard.go`、`internal/config/config.go`、`internal/tui/{model,update,view}.go`、`internal/app/{service,session}.go`、`internal/agent/runtime.go`、对应 `_test.go`、`docs/runbooks/tui.md`、`README.md`。

- [ ] 先写无 TTY 的状态更新测试与 fake 模型脚本；实现聊天记录、输入、滚动、缩放、忙碌状态和取消。此单元先显示完整回答，流式增量在 G1b.3 审核。
- [ ] 按 §2 调整向导的跳过渠道路径；验证旧配置不被改写、没有任何渠道也能启动 TUI、未登录微信不影响 TUI。
- [ ] 实现裸 `plume` / `chat` 入口和 `--offline`；2026-10-09 入口修订为交互终端无配置自动 setup，非 TTY 只显示提示；非 TTY 聊天立即退出，不写真实配置。
- [ ] 增加每轮 run ID、上下文关联、唯一终态与模型事件；连续两轮可引用上一轮信息，新建会话不串上下文。
- [ ] 覆盖忙碌时重复发送、取消后继续聊天、错误不污染历史、退出释放资源、日志不破坏界面；输出控制字符按安全文本呈现。
- [ ] 在隔离 `PLUME_HOME` 的真实 pty 演示，并请用户检查终端手感；交付 `docs/reviews/G1b.2.md` 后停止。

**目标演示命令：** `rtk go run ./cmd/plume chat --offline`；真实模型由用户手动运行 `rtk go run ./cmd/plume chat --model <配置ID>`。测试命令：`rtk go test ./internal/tui/... ./internal/app/... ./internal/agent/... ./cmd/plume/...`。

### G1b.3：SDK 流式消费、思考展示与 Markdown

**状态：已通过（2026-10-08）。** 用户于 2026-10-08 授权执行 [技术契约](../specs/tui/streaming.md)，实现与证据见 [G1b.3 审核记录](../reviews/G1b.3.md)，同日审核通过（含审核期反馈修复）。

**实际改动范围：** model 协议流与 fake、provider 能力映射、agent/app 生命周期、config/setup/CLI、TUI 状态与 Markdown、telemetry；新增流式 fixture 与 pty 脚本，Glamour 锁定 v2.0.1。实际路径与证据见审核记录。

- [x] 本地 HTTP fixture 覆盖分帧、UTF-8、多行 data、CRLF、空 delta、finish/DONE、usage、畸形数据、超大流与 EOF；拒绝伪 DONE、重复键和 SDK 类型强制转换。
- [x] Next/Event/Err/Close 拉取式流，16 MiB 实际读取上限、取消和幂等关闭；Generate/Stream 结果一致性测试。
- [x] 思考和答案独立增量，同分片不丢失、不猜测正文；实际四阶段不会从 responding 回退到 thinking。
- [x] `>`、`●`、`Thought` 同列，三点动画左槽与正文左对齐；三行预览、自动折叠、Ctrl+O 手动优先。
- [x] schema v1 可选文案与默认 high/none 控制；setup 不询问且保留配置，能力验证先于 HTTP，offline 不读取配置。
- [x] Markdown、50ms 合并刷新、已完成回复缓存、上滚冻结、Unicode 列宽与纯文本降级；窄表与代码源文保护。
- [x] 准备/模型首思考/模型首答案/UI 首答案分别计量，trace 脱敏、usage unknown、不重复计量。
- [x] 取消/超时/断流、背压、唯一终态与历史原子提交；取消部分内容保留显示但不写历史，不自动重连。
- [x] 测试、竞态检测、静态/格式检查、基准、版本构建与 13 个 pty 场景；真实供应商性能记 N/A。
- [ ] 用户审核实现和真实终端手感。交付后停止，不自动进入 G3。

**验收：** 按技术契约 §9 执行协议/状态/配置/渲染/取消测试、`rtk go test -race ./...`、静态与格式检查、`rtk go test ./internal/model/... ./internal/tui/... -bench . -benchmem`、版本构建和隔离 pty 演示；同一 fixture 的非流式/流式最终答案、思考、finish reason 和 usage 一致。基准不代表外部 API 性能。交付实际证据后再次等待用户审核，不将本次文档审核等同实现验收。

### G1b.4：可配置底部状态栏（紧急插入 G3 前）

**状态：已通过（2026-10-09）。** 用户审核 [技术契约](../specs/tui/statusline.md) 后授权实施。供应商、API 模型名、可选上下文百分比/用量及进度条、两种缓存占比、Git/uv、会话与 run 计时已接入；完整默认项实际写入 config.json。10-09 按用户反馈调整格式及初始零值，随后要求默认隐藏 ctx、保留缓存条；context 项可设 enabled:true 恢复。实现证据见 [G1b.4](../reviews/G1b.4.md)。

**实现范围：** config 默认补齐与校验、CLI 装配/show、模型 Usage 缓存归一化、app 调用统计与异步本地采集、TUI 两行/窄屏布局及真实光标。当前没有发送前估算器，使用供应商实际 PromptTokens；不新增外部依赖。

- [x] 四阶段文案与新 status_line 配置共存，默认项完整落盘；保留用户数组选择/自定义值，旧配置原子补齐且重复读取不改写，offline 保持不读配置。
- [x] 上下文按 10-09 用户最新要求默认隐藏，完整配置 context.enabled:false，设 true 可恢复 `ctx: x.x% used/capacity`、固定一位小数、不显示 `(last)`；context_format:bar 保留进度条，两种格式沿用阈值颜色。默认最左 Provider 标签；缓存为可配置条体、一位小数及命中量/对应输入，分母按用户选择沿用 CC/Pi 命中率。官方 DeepSeek 两预设默认容量 1M，其余未知；新会话缓存显示 `cache [░░░░░░░░░░]0.0% 0/0`，恢复上下文后为 `ctx: 0.0% 0/1M`。缓存默认会话累计、可选最近调用；实际字段缺失区分 unknown，累计去重并标 partial。隐藏不改变用量采集或缓存计算；发送前估算未实现，保留 future 接口边界。
- [x] 10-09 修复生成期间 ctx/cache 提前变 unknown：两字段分别保留初始零或请求前快照，收到对应统计再更新，终态仍缺数据才按实际口径显示 unknown/partial；准备阶段取消且未调用模型时保留原值。显示快照不写入真实 usage 或影响调用累计。
- [x] Git/uv 异步只读采集，超时/禁用/退出与过期快照测试通过；不存在的环境不能标成 active，未知数据不影响聊天。
- [x] 优先单行、放不下才两行，自定义字段顺序/标签/显隐/优先级/格式；窄屏最多一行，布局和真实光标正确，Thought 进度不重复，关闭状态栏仍能看到关键错误。
- [x] 按技术契约执行测试、竞态/vet/格式检查、隔离 pty、版本构建，交付 docs/reviews/G1b.4.md；完成即停止，用户通过后才另行推进 G3。

本轮交付状态栏实现，不新增外部命令插件、不连接 MCP、不实施工具循环；实现待用户审核，不能将方案批准等同本单元通过。

### G3：自研 Agent 循环与受控工具（前移至微信之前）

2026-10-09 用户审核通过 G3.1 及其基础 G3 循环；初始交付见 [G3](../reviews/G3.md)，现行交付见 [G3.1](../reviews/G3.1.md)，行为见 [工具循环契约](../specs/agent/tools.md)，初始步骤见[归档实施计划](../archive/plans/2026-10-09-g3-tools.md)。本期保留完整历史，必要组超过字节预算明确失败，不实现隐式裁剪。

**上下文规划：** 按 [Agent 上下文与工具请求拼装契约](agent-context.md) 统一组织 messages 与 tools。提前约定 `soul.md` 人格、长期记忆、MCP tools 和 skill 的来源边界；G3.1 已扩展为六种开发工具与可选计算器/时间，不读取或创建未来来源，不提前搭空框架。

**拟改文件：** `internal/agent/{runtime,prompt,budget}.go`、`internal/tools/{registry,calculator,clock}.go`、模型适配器的工具请求/响应编码、对应 `_test.go`、`internal/telemetry/agent.go`、`internal/tui/update.go`、`internal/eval/smoke_test.go`、`eval/datasets/tools.v1.jsonl`。

- [x] Agent 统一拼装基础规则、历史、当前输入及 tools；声明与执行来自同一注册表，稳定排序，循环不重复添加基础规则；工具轨迹保留角色/ID，必要历史组超限拒绝，trace 不存 prompt 正文。
- [x] 用 scripted fake 与本地 SDK fixture 验证“模型 → 工具 → 模型 → 回复”；工具调用由完整协议字段驱动，不从自然语言里猜。真实供应商验证仍不包含在本地结果中。
- [x] 计算器只接受明确运算和数值，覆盖除零/溢出；时间工具使用可注入时钟。G3.1 经追加授权实现 read/grep/glob/edit/write/bash，宿主 shell 的边界见契约。
- [x] 工具 ID 关联、参数 schema、allowlist、单工具超时、最大轮数、最大模型调用数和整体 deadline 都有测试；默认串行执行多个工具，宽松默认值实际写入配置。
- [x] 流式参数在完整组装并校验前绝不执行；未知工具、无效参数、模型截断、重复调用 ID 和预算耗尽有明确终止/反馈策略。
- [x] 工具失败是结构化结果；模型重试不能隐式重跑已完成工具。取消/失败的轮次不提交到会话历史，保留其 trace；已发生副作用不回滚。
- [x] TUI 显示工具状态与摘要；12 个基础种子加工具集分别统计，交付 G3/G3.1 审核记录；2026-10-09 用户审核通过，不自动推进后续单元。

### 第一个里程碑的完成条件

G1b.1、G1b.2、G1b.3、插入的 G1b.4、G3 各自通过审核；用户在**不配置微信、不扫码**的情况下完成配置、真实 TUI 多轮/流式聊天、可配置状态栏与一次工具调用；fake 可独立演示成功、失败、超时、取消。交付逐步 trace 和首份绝对值基线，无任何收益承诺。

第一阶段通过后，另行确认是否开始 [第二阶段渠道计划](phase-02-channel.md)。G4a（持久历史/记忆）、G4b（上下文压缩）、G5（已审核技能）、G6（实验与简历整理）作为后续路线储备；具体实现各自另建阶段计划，不提前搭空框架。

## 6. “详细每一步 trace”的验收定义

Trace 记录可观测输入输出、状态转移和实际工具执行，不承诺获取模型未公开的内部思维链。由自研运行时、模型适配器、工具和应用层显式记录，不再依赖 Eino callbacks。

| 步骤 | 首版证据 |
| --- | --- |
| 配置与可选 probe | setup ID、供应商/模型、校验/保存/联网检查及耗时，不记录 Key |
| TUI 输入 | 会话/run/message ID、输入校验、提交/拒绝/取消、首文本绘制与完成状态 |
| 上下文准备 | 完整历史轮数、截断策略、prompt 版本/哈希；失败部分不混入历史 |
| 每次模型调用 | provider、内部协议、模型配置摘要、attempt、起止/首思考/首答案耗时、usage、finish reason、错误分类 |
| 流式处理 | 帧数/字节数、解析失败位置摘要、正常结束/断流/取消、是否输出了部分内容 |
| 每次工具调用 | call ID、工具名、校验、脱敏参数、结果摘要、耗时和终态 |
| run 结束 | 唯一 completed/failed/cancelled 终态及 reason，历史提交状态，UI 消费结果 |

公共字段：`trace_id`、`span_id`、`parent_span_id`、`run_id`、`message_id`、脱敏 `session_id`、`step_seq`、`attempt`、`event_type`、时间戳、单调耗时、状态、错误分类、代码/配置/数据集版本。取消、超时、轮数限制分别记录原因；并发的未来实现以父子关系表示，不能用序号伪造串行顺序。

每个用户提交建立一个 run；一次工具后的再次模型请求有独立 span，网络 attempt 不是新的逻辑 run。进程崩溃留下未闭合链要计入不完整，不能伪补成功。模拟回放读取录制结果，不重放副作用。

演示/评测全量记录生命周期。流式正文默认只存有界摘要、计数和哈希，不把每个网络字节都写入 trace。usage 缺失为 unknown，估算带 estimator/version；usage 尾帧未收到不能凭输出长度冒充计费值。

默认不存真实原文，正文调试需显式启用、访问限制与到期清理。Key、Authorization、凭据文件内容、二维码/登录 URL 永不记录；错误正文与 URL 同样经过脱敏和长度限制。第三方 HTTP debug/curl dump 默认关闭。

G1b.3 拟新增的思考全文与自定义状态文案同样不默认写 trace；记录真实阶段 ID 和时间，不记录动画帧或用文案推导性能指标。

后续网关单独增加 connection/setup trace、去重/排队/发送 span；不把整个长轮询生命周期当成无限长 span。TUI 与网关可复用 run 事件，但不能混用端到端延迟定义。

## 7. 指标与比较规则

指标字典、数据与实验协议、每次改造的固定报告格式统一见 [指标与比较协议](../specs/metrics.md)（2026-10-09 从本文抽出，供第一/第二阶段共用）。摘要：基线只报绝对值、变化列 N/A；数据缺失记 `N/A`/`unknown`，不得当 0；性能以延迟与成功率为主指标，二进制体积不作为指标。

## 8. 后续优化候选（不是首版功能清单）

每次先写实验卡：瓶颈证据、假设、只改什么、主指标、质量护栏、样本/预算与回滚方式。以下幅度是目标假设，不是收益承诺。

| 实验 | 改造 | 主指标假设 | 必须并列查看 |
| --- | --- | --- | --- |
| E01 上下文压缩 | 近期窗口 + 版本化摘要 | 完整会话输入 token −20% | 摘要成本、遗漏、延迟 |
| E02 工具并行 | 独立只读工具有界并发 | 多工具 p95 −15% | 限流、次序、资源/错误率 |
| E03 技能复用 | 已审核步骤模板 | 同类任务成功率 +5 个百分点 | 简单任务成本、泛化 |
| E04 工具缓存 | 用户/工具版本/参数/有效期明确的缓存 | 可缓存任务外部调用数 −20% | 陈旧结果、冷/热缓存、隔离 |
| E05 Trace 写入 | 同完整度下比较同步与有界批量写 | 采集延迟开销 −20% | 丢事件、崩溃、磁盘 |
| E06 TUI 渲染 | 合并高频文本增量后刷新 | 渲染 CPU −20% | 首文本/完成延迟、取消、丢事件 |

未达标则撤销或保留为显式实验选项，不覆盖 baseline。模型路由、多 Agent、向量检索有实测需要才立项。

## 9. 目录与展示材料

当前入口是 `cmd/plume/`。`internal/config`、`setup`、`provider`、`channel`、`telemetry`、`model`、`tui`、`app`、`agent`、`eval` 均已建立；G1b.3 在现有边界上扩展，不预建空实现。

`internal/tools/` 留 G3；后续 `internal/store`、`memory`、`skills` 各自另行审核。应用接口不返回 openai-go、Bubble Tea 等第三方类型。

首版演示：TUI 多轮/流式回答、一次工具调用、一次取消/失败定位、一份可复现基线。简历只使用真实实现与测量数据；微信效果不能在渠道尚未实现时写为成果。

## 10. 本次交付与下一步（2026-10-08）

- [x] 保留 SDK + 协议适配器/自研运行时、TUI 优先及微信后移路线，同步已经通过的单元现状。
- [x] 交付 G1b.3 技术契约：流式事件、基础 Markdown、thinking 预览、请求前准备状态、可选文案与默认 high/可关闭的思考强度配置。
- [x] 定义首思考/首答案指标、兼容边界、拟改文件与离线验收矩阵。
- [x] 用户审核并授权执行 [技术契约](../specs/tui/streaming.md)。
- [x] 实现 G1b.3 并交付实际证据；等待实现审核，不自动进入 G3。

本次新增 Glamour v2.0.1，完成 G1b.3 代码、回归测试、本地 HTTP fixture 与 pty 验收；未调用真实模型或执行扫码。指标、局限及待用户检查项见 [审核记录](../reviews/G1b.3.md)。
