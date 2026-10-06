# 第一阶段：微信 Agent 开发计划

> 范围：首次模型/渠道配置、微信扫码接入、Eino Agent 基础能力、trace 与首批量化实验；本文是阶段计划，后续阶段另建文档。

> 状态：已确定 Hermes 式微信扫码接入；按用户要求仅初始化 Hello World 骨架，后续功能仍待逐项审核。
> 日期：2026-10-05。
> 执行约定：后续按 executing-plans 技能逐项推进；每个审核单元完成即停下，用户明确通过后才进入下一单元。技能的默认批量执行方式不得覆盖这个约定。

**Goal：** 用 Go + Eino 构建一个以微信为首个真实入口、具备工具调用、持久记忆和可复用技能的个人 Agent，形成能展示逐步开发过程、完整执行链路和量化改造收益的简历项目。

**Architecture：** 单进程模块化服务；配置向导分别选择模型供应商与渠道，供应商工厂创建 Eino 模型组件，渠道注册表创建消息适配器。应用层负责会话和运行状态，Eino ADK 负责 Agent 与工具编排；trace 和评测从第一段可执行链路开始接入。

**Tech Stack：** Go、Eino ADK、标准库 HTTP、SQLite、结构化 JSONL、OpenTelemetry、zap（结构化日志，2026-10-06 确定，在 G1a-2 随 telemetry 引入）；运行时通过向导配置模型供应商、API Key、模型名称与 Base URL。Jaeger 作为后续本地 trace 浏览器；首版不要求 Redis、向量数据库、Kubernetes 或独立前端。

## 1. 项目定位与范围

名称 `herald-agent`：Herald 有“信使”的含义，契合微信消息入口，Agent 表明项目定位。

对标 NousResearch 的 Hermes Agent，用户已明确微信入口要模仿其网关扫码流程。借鉴消息网关、工具执行、跨会话记忆、可复用技能的能力边界；不是对 Hermes 全部功能做逐项复刻。参考项目的说明见 [Hermes 官方仓库](https://github.com/NousResearch/hermes-agent)。

首个演示场景：运行首次设置向导 → 选择模型供应商、输入 API Key 与模型名称、确认预填 Base URL → 选择微信渠道 → 终端显示二维码 → 用户用微信扫码并确认 → 在微信中获得/连接 bot 聊天对象 → 启动网关并向 bot 发送文字 → Eino 生成回答并回复。开发者可追踪配置校验与扫码状态，以及每条消息的输入、模型调用、耗时、token、异常与发送结果。

后续场景：用户让 Agent 查询时间、计算结果、读取自己确认保存的偏好，再按一份版本化技能完成固定工作流。通过同一批任务展示上下文压缩、工具并行、技能复用的收益与代价。

首版范围：单实例、少量测试用户、文本消息、单 Agent、受控工具。群聊、多模态、自主定时任务、任意 Shell、自动安装技能、多 Agent 协作都不纳入首个里程碑。

### 架构选择

| 方案 | 优点 | 代价 | 结论 |
| --- | --- | --- | --- |
| Eino ADK 单 Agent + 渠道适配层 | 先跑通真实业务；记忆、工具和 trace 可逐项解释 | 复杂流程需要后续显式建模 | 推荐起点 |
| 从 Eino Graph 手写全部循环 | 路由与状态转移可精细控制 | 起步需承担更多循环与错误处理逻辑 | 有实测瓶颈再局部引入 |
| 从多 Agent 系统开始 | 可以展示角色分工 | token、延迟和调试成本提高，收益难归因 | 作为后续独立实验候选 |

Eino 官方提供 ADK、组件与编排能力，首版使用其 Agent 实现执行循环，而非仅把 Eino 用作模型 HTTP 包装。[Eino 概览](https://www.cloudwego.io/docs/eino/overview/)

## 2. 首次配置与可扩展渠道

### 首次设置向导

交互式终端中，供应商和渠道列表使用 ↑ / ↓ 移动、Enter 确认，Ctrl+C 取消当前设置；API Key 隐藏输入，模型名称与 Base URL 使用文本输入框。首次直接运行 `herald` 且无配置时进入向导；已有完整配置时正常启动网关，配置不完整时提示继续相应设置。`herald setup` 可随时重新配置。非交互终端不等待按键输入，缺少配置时给出明确提示并退出。

```text
herald setup
  1. 选择模型供应商
  2. 确认该供应商预填的 Base URL（可编辑）
  3. 输入 API Key（隐藏输入）
  4. 输入模型名称/模型 ID
  5. 校验必填项，选择是否进行一次模型连通性检查，保存模型配置
  6. 选择渠道：微信；飞书、QQ 显示“待支持”且不可启用
  7. 运行所选渠道的设置流程：微信请求二维码并等待扫码确认
  8. 显示模型、渠道各自的配置/验证状态及启动命令

herald model setup       # 单独新增或调整模型配置
herald gateway setup     # 单独配置或重连渠道，复用首次向导的渠道设置流程
```

向导保存的是可修改的用户配置，不能把某一家供应商、某个模型或微信类型写死在 Agent 核心。模型已配置但渠道未完成时允许退出并继续本地调试；再次进入从已有状态继续，不能要求重新输入有效 API Key。渠道配置失败不撤销已保存的模型配置；模型配置更新失败也不覆盖原有效配置。

### 模型供应商预设与工厂

每个供应商预设包含稳定 ID、显示名称、协议类型、默认 Base URL、可选地域/业务空间参数、凭据要求及模型工厂。初期使用静态注册表；新增预设无需修改向导主流程。供应商和模型名分开保存，模型 ID 允许手动输入，不依赖模型列表 API 可用。

首批预设（2026-10-06 G0 冻结：只上两家，各家绑定自己的 Eino 组件；百炼/Qwen 及其他延后，详见 `docs/decisions/0001-scope.md` §1）：

| 预设 | Eino 组件 | Base URL 的初始值 | 配置要求 |
| --- | --- | --- | --- |
| DeepSeek | `components/model/deepseek` | `https://api.deepseek.com` | API Key + 用户输入的模型 ID |
| 自定义兼容服务 | `components/model/openai` | 无预填值，由用户提供 | Base URL + 模型 ID；默认要求 API Key，本地无鉴权服务可显式选择无需 Key |

延后（本阶段不做，资料保留在 G0 决策文档）：阿里云百炼（北京共享端点）`https://dashscope.aliyuncs.com/compatible-mode/v1`，纳入时用 `components/model/qwen`。

默认值依据 [DeepSeek 官方接口说明](https://api-docs.deepseek.com/) 和 [百炼 Base URL 文档](https://help.aliyun.com/en/model-studio/base-url)，检查日期为 2026-10-05（G0 于 2026-10-06 复核）。百炼还区分地域和业务空间，不能将同一地址套用于全部账号。[百炼地域与端点](https://help.aliyun.com/zh/model-studio/regions/)

预填值是可编辑默认值。配置中保存最终解析的 URL，升级预设不能悄悄改变已有用户的请求目的地。切换供应商时重新应用对应默认值，防止把上一家供应商的地址和 API Key 配给下一家；Key 按模型配置隔离，不自动复用。

协议类型与供应商品牌分开：首批兼容服务通过对应 Eino 扩展装配模型，后续原生协议供应商新增工厂；不能仅替换 Base URL 就声称任意供应商可用。（2026-10-06 确认：首批 2 家预设各自绑定专用 Eino 组件——DeepSeek→`components/model/deepseek`、自定义→`components/model/openai`；`protocol` 存适配器 ID，不折叠成单一 openai 组件。百炼/Qwen 延后。已核实两家组件底层都是 OpenAI 兼容 HTTP，区分点在适配器与各家特有配置面。）模型的流式、工具调用、usage 等能力分别记录为支持、不支持或未验证；能完成普通对话不等于已经通过工具调用验收。

本地校验检查必填项、合法 URL 和配置引用；默认使用 HTTPS，本机回环服务允许 HTTP。API Key 隐藏输入，不作为命令行参数传递。联网检查由用户选择，说明会发送一次短测试请求且可能产生少量费用；超时、鉴权失败、模型不存在或不可访问分别显示可诊断状态。跳过检查可保存为“未验证”，不能显示为“已连通”。

### 配置与凭据边界

仓库外配置目录采用 Hermes/Codex 式的单一家目录点目录：`HERALD_HOME` 非空时用它，否则 POSIX `~/.herald`、Windows `%LOCALAPPDATA%\herald`；不使用 `os.UserConfigDir()`（macOS 上是含空格的 `~/Library/Application Support`）。首版使用带 `schema_version` 的 `config.json`，保存非敏感配置；API Key 和微信凭据分别保存在该目录的 `credentials/` 下，目录权限 0700、文件权限 0600（类 Unix 平台），不将明文凭据混入普通配置。后续允许用命令行标志显式指定目录。

- 模型配置：`id`、`provider`、`protocol`、`base_url`、`model`、`api_key_ref`；`default_model` 引用一个配置 ID。首版向导先创建一个默认配置，结构允许以后新增命名配置。
- 渠道配置：`id`、`type`、`enabled`、`model_ref`、`credential_ref`、渠道专有设置；首版启用一个微信实例，`model_ref` 默认指向默认模型配置。
- 通用调度只读取稳定 ID 和模型引用；`context_token` 等微信字段由微信适配器管理。
- 更新采用原子写入；取消和写入失败保留原配置。配置不包含密钥值，状态输出只显示“已设置/未设置”及脱敏标识。

模型配置变更在网关重启后生效，首版不做运行中热切换。模型供应商、模型 ID、配置哈希进入实验 manifest，API Key 及凭据引用的实际内容不进入 trace 或评测文件。

### 渠道注册表与后续飞书、QQ

渠道注册表提供渠道 ID、显示名称、可用状态、配置向导、适配器工厂及能力描述。首版注册可用的 `weixin`；`feishu`、`qq` 只作为待支持选项，不接收其凭据、不创建虚假已配置状态。

渠道边界包含配置/验证、启动/停止接收、标准化入站消息、发送回复、查询连接状态。微信在适配器内部使用长轮询；未来飞书、QQ 可在各自适配器内选用相应官方支持的传输方式，具体协议届时验证，不让网关核心依赖微信轮询接口。

标准化消息包含渠道类型、渠道实例 ID、外部消息 ID、会话 ID、发送者 ID、文本和回复关联信息。所有身份与去重键带渠道实例命名空间，不能将微信用户 ID 与飞书/QQ 用户 ID 自动视为同一人。首版保持一个活动渠道；多渠道同时运行、账号绑定和跨渠道记忆共享作为后续独立阶段审核。

扩展验收采用一个测试专用适配器：通过注册即可走同一个应用服务、Eino 运行时、trace 与回复路径，无需在 Agent 核心加入 `if weixin/feishu/qq`。仅提取当前需要的边界，不构建动态插件加载系统。

### 微信接入：Hermes 式 iLink Bot 扫码网关（已选定）

用户确认的体验是：在命令行选择微信、展示二维码、手机扫码确认后使用 bot 作为聊天对象。首版采用与 Hermes 相同的 iLink Bot 接入方向，使用 Go 实现协议适配层，Agent 核心仍由 Eino 运行。

### 已核实的参考机制

2026-10-05 查阅 Hermes 自身文档确认：其微信适配器使用 iLink Bot API，扫码后连接独立 bot 身份；向导保存账号凭据，网关启动后长轮询收消息，无需公网 webhook。文档同时说明出站回复使用对话方的 `context_token`。[Hermes 微信文档](https://hermes-agent.nousresearch.com/docs/zh-Hans/user-guide/messaging/weixin)

Hermes 源码包含 `ilink/bot/get_bot_qrcode`、`ilink/bot/get_qrcode_status`、`ilink/bot/getupdates` 和 `ilink/bot/sendmessage`，以及同步游标 `get_updates_buf` 的恢复逻辑。[Hermes 微信适配器源码](https://github.com/NousResearch/hermes-agent/blob/main/gateway/platforms/weixin.py)

以上是对参考实现的核实，尚未用本项目和用户账号实测。扫码后 bot 的具体创建/绑定动作由平台完成；本项目验收用户能看到并与 bot 聊天，不假定存在独立的“创建联系人”API。实现时记录参考源码 commit，核对字段、鉴权、状态及错误处理，不将可变 main 分支当作永久协议规范。

### 预定命令与体验

```text
herald gateway setup
  选择 Weixin → 请求二维码 → 在终端渲染
  → 等待扫码 → 等待手机确认 → 保存登录结果 → 显示连接成功

herald gateway start
  恢复凭据 → 长轮询接收 bot 私信 → Eino 执行 → 回复原会话

herald gateway status
  显示连接状态、最近成功收发时间和脱敏账号，不显示凭据
```

这些是拟实现的 CLI，当前不可运行。二维码渲染失败提供本次登录 URL；二维码过期允许重新获取，用户可随时取消。凭据有效时重启直接恢复，鉴权过期时提示重新扫码并停止无意义重试。

### 适配器约束与验收

| 部分 | 计划行为 | 验收证据 |
| --- | --- | --- |
| 扫码登录 | 将等待扫码、等待确认、成功、过期、取消、失败映射为可见状态 | 终端可扫二维码；成功状态 trace；过期/取消 fixture 测试 |
| 本地凭据 | 保存 bot 账号、token、已校验的服务地址到 `~/.herald/credentials/`（仓库外）；文件权限仅当前用户可读写，原子更新 | 重启恢复；权限检查；仓库和日志无密钥 |
| 收消息 | 长轮询 `getupdates`，区分正常空轮询、网络错误、限流和鉴权失败 | 正常空轮询不计业务失败；断线恢复测试 |
| 回复 | `sendmessage` 使用匹配账号及对话方的上下文凭据 | 连续私聊及重启后回复；缺失/失效 token 有明确状态 |
| 恢复与去重 | 持久化同步游标、上下文凭据、入站消息及去重键；先可靠落盘消息再推进游标 | 注入崩溃后不因游标提前提交而丢掉本地待处理消息 |

`context_token` 是渠道回复凭据，与 LLM token 用量和 Agent 会话历史分开管理；它和登录 token 均不进入 trace 正文。接收循环与 Eino 执行解耦，慢模型调用不能阻塞下一次长轮询。单 bot 账号只运行一个接收循环。

首版验收私聊文本，接入方向已经确定，不再要求用户在公众号/企业微信之间选择。账号可用性、实际回复限制和异常返回在接入单元实测；未得到真实收发证据时只标记“模拟通过”。

G0 已确认首批供应商预设、评测预算与本机出站网络（见 `docs/decisions/0001-scope.md`）：首批 2 家预设各自绑定专用 Eino 组件（DeepSeek + 自定义兼容，百炼/Qwen 延后），单次联网评测无费用上限，本机含 TUN 代理环境可用于评测。用户实际使用的供应商和模型仍通过设置向导选择，无需在代码开发前固定为一家。无需准备公网回调域名。

## 3. 开发与审核制度

每个审核单元执行如下流程，单元不可在未经同意时合并：

1. 开始前说明本单元目标、拟改文件、运行方式和验收条件。与本计划有偏差时先更新方案。
2. 实现一个能演示的小闭环；对会话隔离、幂等、重试、计算和评测统计等关键行为写针对性测试。
3. 展示可复现命令、实际结果、一条成功 trace、一条相关失败 trace，以及当前适用的指标。
4. 交付 `docs/reviews/Gx.md`：改动说明、证据路径、指标表、局限、需要用户检查的内容。
5. 停止开发下一单元。用户回复“通过 Gx / 继续下一部分”后才推进；要求修改则继续当前单元。

基线阶段展示绝对值，变化列为 N/A；新功能首次出现也标记 N/A。不能为了填满表格编造“提升”。代码通过测试不等于用户已经审核通过。

审核单元是功能边界，不预设必须一天完成；若单元涉及内容过多，先拆成更小的可运行子单元，由用户逐个审核。

## 4. 运行链路与模块职责

```text
首次配置向导 → 模型供应商/Key/模型/Base URL + 渠道选择
  → 微信扫码并保存凭据 → 网关读取配置，创建模型与渠道适配器
  → 长轮询
  → 微信 bot 私信 → 渠道标准化 → 入站去重与任务记录 → 会话调度
  → Eino Agent → 模型/受控工具 → 结果持久化 → 渠道回复

所有节点 → 同一逻辑 run 的 trace / 事件记录 → 查询、评测、比较报告
```

| 模块 | 职责与边界 |
| --- | --- |
| setup/config | 首次向导、配置版本与校验、凭据引用、原子保存；不承载 Agent 逻辑 |
| provider | 供应商预设、默认 Base URL、协议选择、Eino 模型工厂；不依赖微信 |
| channel | 渠道注册表、渠道设置、接入协议、身份验证、消息标准化、回复；不拼装 prompt |
| app | run 生命周期、超时、去重、会话串行化、重试及回复状态 |
| agent | Eino Agent、模型配置、prompt 版本、工具装配和循环预算 |
| tools | 参数校验与确定性业务执行，遵守 context 取消；不直接向微信发送消息 |
| store | 消息、任务、会话、记忆及迁移；初期 SQLite |
| telemetry | Eino callbacks 与应用 span 对接、脱敏、指标与查询 |
| eval | 固定样例执行、评分、统计比较与报告生成；不承载线上业务 |

会话键至少包含渠道、渠道账号、会话标识、用户标识；首版仅私聊，不以昵称充当身份键。同一会话串行执行，不同会话使用有界并发，防止历史写入顺序混乱。

外部消息标识与渠道账号构成去重键。记录 `received → queued → running → completed/failed → reply_pending → replied/reply_failed` 等状态；具体允许转移在 G2 写成表并测试。计算完成与发送完成分开记录。

重试处理要区分模型失败、工具失败和发送失败。回复重试复用已有结果，不重新执行 Agent。若发送接口不支持幂等且出现“请求超时但对端可能已接收”，记录 `delivery_unknown`，采用保守重试/人工重放策略，不声称外部端到端 exactly-once。

## 5. 分阶段路线与审核单元

### G0：审核计划与接入可行性

**当前交付：** `docs/phase-01-weixin-agent.md` 和 Hello World 骨架。微信方向已确认；审核后先锁定参考协议与本地配置，再进入 G1a。真实扫码和收发分别在 G2a.1、G2a.2 验收，不以账号尚未实测阻塞离线骨架。

- [x] 用户确认 Hermes 网关式终端二维码 + 微信扫码连接 bot，已核实参考实现使用 iLink Bot API。
- [x] 确认首批供应商预设、配置边界、评测预算与运行环境；记录为 `docs/decisions/0001-scope.md`。实际模型由运行时向导选择。（2026-10-06 用户确认：首批 2 家预设—DeepSeek + 自定义兼容，各绑定专用 Eino 组件，百炼/Qwen 延后；目录用 `~/.herald`；联网评测无费用上限；本机可评测）
- [x] 记录 iLink 参考源码 commit、接口契约与模拟样例设计，形成 `docs/decisions/0002-wechat.md`；真实接入证据在 G2a.1/G2a.2 补齐。（2026-10-06 用户确认）
- [x] 用户审核通过 G0。（2026-10-06）

### G1a：首次设置向导与配置边界（独立审核）

**拟建/修改：** `cmd/herald/main.go`、`cmd/herald/setup.go`、`internal/setup/wizard.go`、`internal/config/config.go`、`internal/config/store.go`、`internal/config/credentials.go`、`internal/provider/registry.go`、`internal/channel/registry.go`、对应 `_test.go`、`internal/telemetry/events.go`、`docs/runbooks/setup.md`。此时将根目录 Hello World 入口迁入 `cmd/herald/`。

- [ ] 先确定配置 schema、供应商与渠道注册项及用户可见向导顺序；冻结首批供应商预设并记录 URL 来源。
- [ ] 实现供应商选择、Base URL 预填/修改、API Key 隐藏输入、模型名输入与配置保存/恢复；日志从此阶段就有脱敏 setup trace。
- [ ] 引入 `go.uber.org/zap` 作为日志库（2026-10-06 确定），记录版本锁定与兼容性验证——这是 `go.mod` 的第一个外部依赖。zap 只用于日志与 trace 事件；CLI 面向用户的输出（usage、`config show`、错误提示）继续用 `fmt`，不要改成 JSON 输出。
- [ ] 实现渠道选择和渠道配置入口；微信标记为“待登录”，其真实扫码在 G2a.1 完成，飞书和 QQ 标记为待支持且禁用。不能将占位入口视为已接入。
- [ ] 用交互输入与存储 fixture 验证默认 URL、修改 URL、切换供应商、不复用错误 Key、重新进入向导、取消、文件写失败及凭据权限；配置和 trace 中不得出现测试密钥值。
- [ ] 验证新增测试供应商/渠道注册项无需改向导分发逻辑；新增渠道不影响已有模型配置。
- [ ] 提交可操作向导、脱敏配置样例、setup trace 和 `docs/reviews/G1a.md`，停止等待审核。此单元只做本地校验，联网模型检查随 G1b 接入。

**用户能看到：** 启动向导选择供应商，输入 Key 与模型名，接受或修改默认 URL，再选择微信；关闭后重新打开能恢复已保存的配置。

### G1b：Eino 模型工厂、离线闭环、trace 和评测种子（独立审核）

**拟建/修改：** `go.mod`、`go.sum`、`cmd/herald/main.go`、`internal/provider/factory.go`、`internal/provider/factory_test.go`、`internal/provider/probe.go`、`internal/provider/probe_test.go`、`internal/agent/runtime.go`、`internal/agent/runtime_test.go`、`internal/telemetry/events.go`、`internal/telemetry/events_test.go`、`internal/eval/smoke_test.go`、`eval/datasets/smoke.v1.jsonl`、`README.md`、`.gitignore`、`.env.example`。

- [ ] 锁定 Go/Eino/模型扩展版本；根据对应版本编译验证，记录兼容性。模型测试使用符合该版本接口的 scripted fake，Agent 循环仍经 Eino 执行。
- [ ] 根据 G1a 保存的供应商、协议、URL、模型 ID 和凭据引用创建 Eino 模型组件；以 mock HTTP 验证不同预设的请求目的地、鉴权与模型 ID，无硬编码密钥或模型名。
- [ ] 接入可选短请求连通性检查，区分本地校验通过、联网验证成功、未验证和验证失败；默认离线测试不消耗真实模型费用。
- [ ] 提供本地文本入口，贯通输入、Eino 调用、输出和结构化事件；首次即包含开始、结束、错误和取消状态。
- [ ] 建立 12 个离线种子用例：正常回答 4 个、空/无效输入 2 个、模型错误 2 个、超时/取消 2 个、trace 关联及脱敏 2 个。
- [ ] 验证事件没有泄露测试密钥，每次 run 都有唯一终态；失败样例也可查询。
- [ ] 保存首份绝对值报告和 `docs/reviews/G1b.md`，停止等待审核。

**用户能看到：** 无微信账号和付费 API 也可运行的 Eino 闭环，一条成功和一条超时的事件链。

**预定验收命令：** 项目目录执行 `rtk go test ./...`、`rtk go test -race ./...`、`rtk go run ./cmd/herald --mode local --model fake --message '你好'`。前两项退出码 0，最后一项输出回复和可定位的 run ID。这些是将要实现的入口，本轮未执行。

### G2a.1：命令行扫码连接 bot（独立审核）

**拟建/修改：** `cmd/herald/gateway.go`、`internal/channel/wechat/client.go`、`internal/channel/wechat/login.go`、`internal/channel/wechat/credentials.go`、对应 `_test.go`、`docs/runbooks/wechat.md`。

- [ ] 在 G1a 渠道注册表与向导基础上实现微信的二维码请求/渲染、状态轮询、手机确认和凭据保存；`herald setup` 与 `gateway setup` 复用同一流程，以小型 Go 二维码库承担渲染。
- [ ] 用 HTTP fixture 验证登录成功、待确认、二维码过期、取消、网络超时、错误响应及已有凭据保护；敏感数据只用于终端登录显示，不写入日志。
- [ ] 在真实终端展示二维码，由用户扫码完成登录；核对 bot 聊天对象与本地脱敏账号状态，不替用户操作手机授权。
- [ ] 验证重启可读取有效凭据，`gateway status` 可区分已配置与实际在线，不能把“存在凭据文件”当成“连接正常”。
- [ ] 提交登录状态 trace、实测记录和 `docs/reviews/G2a.1.md`，停止等待审核。

**用户能看到：** 与 Hermes 接近的选择渠道、扫码、手机确认、连接成功流程。此单元只验收登录，不宣称已经完成 Eino 聊天。

### G2a.2：微信真实收发 + Eino 模型回答（独立审核）

**拟建/修改：** `internal/channel/message.go`、`internal/channel/wechat/adapter.go`、`internal/channel/wechat/adapter_test.go`、`internal/app/service.go`、`internal/app/service_test.go`、`internal/provider/factory.go`、`internal/agent/runtime.go`、`cmd/herald/main.go`、`docs/runbooks/wechat.md`。

- [ ] 使用已保存凭据实现 `gateway start`、长轮询收消息、账号与会话映射、文本回复；复用 G2a.1 客户端，保存每个对话方的回复上下文凭据。
- [ ] 由渠道的 `model_ref` 解析模型配置，复用 G1b 模型工厂；真实调用与离线 fake 结果分别报告。通过测试专用渠道验证应用服务与 Agent 核心不依赖微信字段。
- [ ] 分开配置登录轮询、消息长轮询、单次发送和 Agent deadline；正常空轮询不触发错误退避，网络错误和限流有界退避，鉴权失效提示重登。
- [ ] 完成至少 10 条真实文本消息闭环，保留脱敏回执与 trace 关联。测试故障至少覆盖无效凭证、模型超时、发送失败。
- [ ] 提交 `docs/reviews/G2a.2.md`，停止等待审核。

**用户能看到：** 在所选微信入口发送消息并收到模型回答；每条消息有收发证据与完整调用链。10 条用于连通性验收，不用于宣称稳定性或 p95 显著改善。

### G2b：消息可靠性与会话隔离

**拟建/修改：** `internal/store/sqlite.go`、`internal/store/migrations/001_messages.sql`、`internal/app/inbox.go`、`internal/app/worker.go`、`internal/app/session_queue.go`、`internal/app/delivery.go` 及对应 `_test.go` 文件。

- [ ] 持久化入站、运行与出站状态；将轮询批次入站记录与同步游标推进纳入同一事务，并保存必要的上下文凭据；实现去重、有界 worker、同会话串行化及可恢复任务。
- [ ] 对照状态转移表测试重复投递、进程重启、顺序消息、不同用户隔离和过载拒绝/排队。
- [ ] 注入“入站落盘后崩溃”“模型完成后崩溃”“发送响应丢失”，验证恢复路径，不重复执行已经完成的 Agent。
- [ ] 展示离线重复投递 100 次仍只生成一个逻辑任务，以及多用户场景不串话；外部回复的重复风险单独说明。
- [ ] 提交 `docs/reviews/G2b.md`，停止等待审核。

**第一个里程碑完成条件：** G1a、G1b、G2a.1、G2a.2、G2b 分别通过审核，有可恢复的模型/渠道配置向导、终端扫码登录、微信 bot 真实回复、错误路径、基础可靠性、trace 与测量证据。

### G3：可控工具调用

**拟建/修改：** `internal/tools/calculator.go`、`internal/tools/clock.go`、对应 `_test.go`、`internal/agent/runtime.go`、`internal/telemetry/eino_callbacks.go`、`eval/datasets/tools.v1.jsonl`。

- [ ] 先接入计算器和时区时间查询。计算器仅接受明确运算与数值参数，覆盖除零和溢出；时间用可注入时钟固定评测结果。
- [ ] 设定工具 allowlist、参数限制、单工具超时、最大循环次数和单 run 预算，终止原因进入 trace。
- [ ] 评测正确选工具、正确参数、不需工具、工具错误、连续工具调用及越权请求；成功标准检查实际调用和答案，不能只检查回答语气。
- [ ] 展示一次“模型 → 工具 → 模型 → 回复”的完整链；提交 `docs/reviews/G3.md` 并等待审核。

### G4a：会话历史与持久记忆

**拟建/修改：** `internal/memory/store.go`、`internal/memory/service.go`、`internal/memory/service_test.go`、`internal/store/migrations/002_memory.sql`、`internal/tools/memory.go`、`eval/datasets/memory.v1.jsonl`。

- [ ] 区分短期会话历史和长期用户事实；首版长期写入来自用户明确要求，记录来源、所有者和版本。
- [ ] 支持保存、读取、修改、删除事实；模型生成的推测不自动成为用户事实。
- [ ] 验证重启后恢复、跨会话读取、用户隔离、冲突覆盖与删除后不再召回；清理规则涵盖派生摘要。
- [ ] 用固定多轮任务衡量事实召回和错误记忆；提交 `docs/reviews/G4a.md` 并等待审核。

### G4b：上下文预算与压缩实验

**拟建/修改：** `internal/memory/context.go`、`internal/memory/context_test.go`、`eval/experiments/E01-context.md`。

- [ ] 基线为预算内原始历史；候选为近期窗口 + 摘要，固定同一批可同时运行的会话。
- [ ] 统计摘要生成本身的模型调用、延迟和费用，既报告单轮指标，也报告完整会话总成本。
- [ ] 比较 token、费用、p95、事实召回与任务成功率；另报超长会话能力，不混入原本可运行的基线集合。
- [ ] 根据实验判据保留或撤销候选，记录负收益；提交 `docs/reviews/G4b.md` 并等待审核。

### G5：可复用技能与人工审核

**拟建/修改：** `internal/skills/loader.go`、`internal/skills/registry.go`、对应 `_test.go`、`skills/daily-brief/SKILL.md`、`eval/datasets/skills.v1.jsonl`。

- [ ] 定义文本技能清单：名称、版本、适用条件、允许工具、步骤和输出格式；仅加载项目内已审核版本。
- [ ] 用固定素材的每日简报作为第一个技能，trace 记录选择的技能及内容哈希。
- [ ] 对比无技能与有技能在同类保留任务上的步骤遵循、成功率、token 和延迟。
- [ ] 如增加从历史运行生成技能，只生成候选和 diff；用户审核后才激活，不自动覆盖运行中技能。
- [ ] 提交 `docs/reviews/G5.md` 并等待审核。

### G6：优化实验与简历交付

**拟建/修改：** `cmd/eval/main.go`、`internal/eval/runner.go`、`internal/eval/compare.go`、`internal/eval/compare_test.go`、`eval/experiments/`、`docs/demo.md`、`docs/resume.md`、`README.md`。评测 runner 的最小实现可随 G3 提前加入；本阶段完善统一比较入口。

- [ ] 一次只做一个有假设的实验，每个实验作为独立审核单元 G6-Ex；候选清单见第 8 节。
- [ ] 增加本地 OpenTelemetry 导出与 Jaeger 浏览，复用既有 trace ID；不能为了展示而另造一套业务记录。
- [ ] 准备 3–5 分钟演示：微信触发、工具调用、记忆恢复、一次失败定位、一份有实际收益或退步的比较报告。
- [ ] 完善本地启动说明、测试数据来源、架构取舍与复现实验命令；简历只使用已测量且注明样本和环境的数据。
- [ ] 提交各实验审核记录及最终 `docs/reviews/G6.md`。

## 6. “详细每一步 trace”的验收定义

Trace 指可观测的执行事件，不承诺获取模型未公开的内部思维链。记录模型可见请求、公开响应、工具选择及实际执行结果，并解释发生了什么。

Eino callbacks 用于捕获 Agent、ChatModel、Tool 生命周期；接入、队列、存储和发送由应用层补充 span。必须匹配实际锁定版本的回调接口，特别验证流式完成、错误和取消路径。[Eino Agent Callback 文档](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/adk_agent_callback/)

每个 run 至少覆盖：

| 步骤 | 需要记录的证据 |
| --- | --- |
| 首次配置与模型检查 | setup ID、供应商 ID、模型 ID、渠道类型、校验/保存/联网检查状态及耗时；不记录输入的 API Key 或凭据正文 |
| 网关登录与恢复 | 独立 setup/connection trace，记录二维码请求、状态变化、确认、凭据写入成败、重连；不记录二维码内容、登录 URL 或 token |
| 接收与验证 | 渠道、消息 ID、接收时间、脱敏身份、校验结果；不保存签名密钥 |
| 去重与排队 | dedup key、是否命中、排队耗时、原逻辑 run ID、尝试次数 |
| 会话与上下文 | 历史条数、使用的记忆 ID/版本、截断或摘要策略、prompt 哈希 |
| 每次模型调用 | 模型与配置、输入/输出的受控记录、起止时间、token usage、finish reason、异常 |
| 每次工具调用 | tool call ID、工具名、校验结果、脱敏参数、结果摘要、耗时、取消及重试 |
| 结果与发送 | 最终状态、输出摘要、持久化结果、发送尝试、渠道返回 ID 或未知投递状态 |

公共字段：`trace_id`、`span_id`、`parent_span_id`、`run_id`、`message_id`、脱敏 `session_id`、`step_seq`、`attempt`、`event_type`、时间戳、耗时、状态、错误分类、代码/配置/数据集版本。登录/连接 trace 使用 `setup_id`/`connection_id`，此时尚不存在的消息和 run 字段省略；收到消息后新建消息 trace 并关联连接，避免创建跨整个网关生命周期的无限长 span。并发工具以父子关系和时间线表示，不能用全局序号伪造串行执行顺序。

重投递有独立接入 span，并关联原 run；重启恢复保留 run ID 和 span link。持续时间使用进程内单调时钟，跨重启分开记录，不直接相减不可靠的时钟值。

演示/评测环境全量采集。模型 usage 未提供时标记 unknown；估算值带 estimator/version，不当作计费真值。流式接口额外记录首 token 耗时；微信用户可见延迟仍以最终渠道确认的回复为准。

默认脱敏并限制正文长度；公开演示使用合成消息。真实正文的本地调试保存需要显式配置、访问限制和到期清理；token、凭证、原始登录材料永不进入 trace。大结果保存受控引用与哈希，标明截断；不声称每个字节均被保留。

验收：完整率检查每个预期节点的 start 与唯一终态、父子关联、已声明的跳过路径；成功、错误、超时、取消各有样例。模拟回放默认读取已保存的模型/工具结果，不重放有副作用的动作。

## 7. 指标与比较规则

### 固定指标字典

| 指标 | 定义 | 趋势 |
| --- | --- | --- |
| 任务成功率 | 满足用例全部检查点的 run / 所有计划执行的 run；错误和超时计失败 | 越高越好 |
| 工具选择/参数正确率 | 与用例允许的工具及参数约束一致的任务 / 适用任务；正确不调用也计入 | 越高越好 |
| 端到端延迟 p50/p95 | 入站被本服务接收到渠道 API 确认接受回复；不等同于用户已读 | 越低越好 |
| Agent 执行延迟 p50/p95 | 开始执行至回答完成，不含排队与渠道发送 | 越低越好 |
| Token / 模型调用数 | 每个任务及完整会话的输入/输出 usage 与调用次数，包含失败重试和摘要 | 越低越好，但需质量约束 |
| 费用/任务、费用/成功任务 | 总费用除以全部任务数、成功任务数；注明币种、价格日期和缓存计价 | 越低越好 |
| 吞吐 | 固定并发、相同超时下，每单位时间完成且成功的任务数 | 越高越好 |
| 重复执行/重复发送率 | 多次执行同一逻辑任务或重复确认发送的数量 / 对应逻辑任务数；未知投递另列 | 越低越好 |
| 记忆召回/错误率 | 正确使用目标事实、使用错误或跨用户事实的任务占各适用集合的比例 | 前者高、后者低 |
| Trace 完整率 | 通过节点及关联检查的 run / 所有 run；进程崩溃留下未闭合链也计不完整 | 越高越好 |
| Trace 开销 | 相同负载下采集开/关的延迟、吞吐、CPU、内存和存储增量 | 越低越好 |
| 微信网关接入指标 | 二维码请求耗时、确认成功到凭据可用耗时、凭据恢复耗时、网络恢复到轮询成功耗时、收发成功率；人工扫码等待单列 | 系统耗时低、成功率高 |
| 首次配置指标 | 配置成功率、校验失败原因、配置恢复耗时、模型连通性检查耗时；人工输入时间与系统处理时间分开 | 成功率高、系统耗时低 |

成功率之外，延迟报告必须同时列出失败率和超时数，不能通过快速失败制造“延迟下降”。仅成功任务的分位数标注其分母，并并列报告所有任务的终止耗时。

### 数据与实验协议

1. G1b 的 12 个种子用例用于 Agent 工程回归；G1a 的配置向导测试单独统计。G3 起扩充版本化核心集至 100 个：普通对话 15、工具 30、记忆 20、技能 15、可靠性与异常 20。未实现能力标记不适用，逐阶段公布适用集合，比较时使用相同 case ID；新增能力集单独列出。
2. 按类别固定拆分开发集 60 和保留集 40；生成近似题也按来源分组，避免同题变体跨集合。优化只使用开发集调试，候选冻结后运行保留集；看过并据此调整后的集合不再称为未见测试集。
3. 确定性逻辑用规则和固定工具结果评分；开放回答用预先定义的人工 rubric，分别检查正确性、指令遵循和完整性。若使用模型裁判，锁定裁判模型和 prompt，抽查至少 20%，费用单列。
4. 配对运行 baseline 与 candidate，同一 case 相同素材、模型版本、参数、工具状态、环境、并发和超时；交错执行 A/B 降低时间段偏差。真实模型默认每例 3 次，预算不足则明确为探索性结果。
5. 保存机器/OS/Go/Eino 版本、代码 commit 或工作区内容哈希、配置哈希、数据集哈希、模型 ID、日期、并发、随机种子（若提供商支持）、缓存状态、原始逐例结果与 trace 引用。温度为 0 也不保证完全确定。
6. 延迟分位数标注算法和样本量；尾延迟优化原则上使用每配置至少 200 次可比请求。置信区间采用按 case 分组的配对 bootstrap，避免把同一题的重复执行当作独立题目；样本不足时注明证据有限。
7. 报告所有预先定义的适用指标，包含退步与失败案例。数据缺失为 N/A 或 unknown，不能当成 0；基线为 0 时相对变化记 N/A，仅报告绝对差。

### 每次改造的固定报告

`eval/reports/<experiment-id>/` 保存 `manifest.json`、`baseline.jsonl`、`candidate.jsonl`、`comparison.md`，公开版本均脱敏。

| 指标 | 改前 | 改后 | 绝对差 | 相对变化 | 样本/区间 | 判定 |
| --- | --- | --- | --- | --- | --- | --- |
| 所有适用指标逐项列出 | 实测 | 实测 | 新值−旧值 | (新值−旧值)/旧值 | 实测统计 | 改善/退步/不确定 |

百分比指标的绝对差使用百分点，例如 80% → 84% 为 +4 个百分点、相对 +5%；延迟 10s → 8s 为 −2s、−20%，表示改善。以上仅为算术示例，不是本项目测量结果。

预定比较入口为 `rtk go run ./cmd/eval compare --baseline <基线目录> --candidate <候选目录>`；应校验数据集与环境可比性，不满足时拒绝生成无说明的汇总比较。

默认采纳门槛（G0 可调整）：目标指标达到实验预设幅度；任务成功率的配对差值 95% 区间下界不低于 −3 个百分点；其他非目标质量指标点估计不得下降超过 3 个百分点；p95 与每成功任务费用不得恶化超过 10%；用户隔离、越权、去重回归测试无新增失败。未满足证据量时标记“不确定”，扩大评测需遵守预算；取舍需要在该单元审核中说明。

## 8. 可实际验证的优化候选

每项在动手前写实验卡：瓶颈证据、假设、只改变什么、主指标、质量护栏、样本与预算、回滚方式。下面的幅度是目标假设，不是收益承诺。

| 实验 | 具体改造 | 主指标目标 | 可能退步/必须并列查看 |
| --- | --- | --- | --- |
| E01 上下文压缩 | 近期窗口 + 版本化摘要，对比预算内完整历史 | 完整会话输入 token 降低至少 20% | 摘要费用、事实遗漏、首轮延迟 |
| E02 工具并行 | 仅对独立只读工具使用有界并发；依赖工具保持顺序 | 多工具任务 p95 降低至少 15% | 限流、错误率、结果次序和资源占用 |
| E03 技能复用 | 已审核步骤模板代替每次重新规划 | 同类任务成功率提升至少 5 个百分点 | 简单任务成本上升、步骤僵化、泛化下降 |
| E04 工具结果缓存 | 仅缓存可明确界定有效期的只读结果，键含用户/工具版本/参数 | 可缓存任务外部调用数降低至少 20% | 陈旧答案、命中率、隔离、内存；冷/热缓存分开报告 |
| E05 Trace 开销 | 同样采集完整度下，对比同步写与有界批量写 | 采集引入的延迟开销降低至少 20% | 丢事件、队列溢出、崩溃丢失与磁盘增长 |

不为凑简历预设“必须全部优化成功”。实验未达标则记录结果并撤销或保留为显式实验配置，baseline 不被悄悄覆盖。模型路由、多 Agent、向量检索只有在当前指标暴露需要时再单独立项。

## 9. 预期目录与最终展示材料

下列是逐步创建的目标结构，当前仅有 `.gitignore`、`go.mod`、根目录 `main.go` 和 `docs/phase-01-weixin-agent.md`。后续实现 CLI 时再将入口移至 `cmd/herald/`，不预建空实现模块：

```text
herald-agent/
  go.mod
  docs/phase-01-weixin-agent.md
  README.md
  cmd/herald/         # 本地与微信运行入口
  cmd/eval/           # 评测与比较入口
  internal/setup/    # 首次配置向导
  internal/config/   # 配置校验、持久化与凭据引用
  internal/provider/ # 供应商预设与 Eino 模型工厂
  internal/channel/  # 消息渠道
  internal/app/      # 运行与会话调度
  internal/agent/    # Eino 装配
  internal/tools/    # 受控工具
  internal/store/    # SQLite 与迁移
  internal/memory/   # 历史及长期记忆
  internal/skills/   # 技能加载与版本
  internal/telemetry/# Trace 与指标
  internal/eval/     # 评分与统计
  eval/datasets/     # 固定用例
  eval/experiments/  # 假设、改造和取舍
  eval/reports/      # 原始结果及比较报告
  docs/decisions/    # 决策和接入验证
  docs/reviews/      # 每个单元的审核证据
  docs/runbooks/     # 启动及故障复现
  skills/            # 已审核的产品内技能
```

最终简历材料围绕三个可证明的点组织：Eino Agent 的真实微信闭环；可定位到每次模型/工具/发送的可观测性；固定数据和环境下的量化优化。每个数字都能链接到实验报告，每个能力都能用演示或测试复现。

## 10. 本轮交付与下一步

- [x] 新建项目目录，按用户要求命名为 `herald-agent`。
- [x] 写入定位、阶段计划、逐项审核制度、trace 规范和指标比较协议。
- [x] 根据用户反馈锁定 Hermes 式 iLink Bot 扫码入口，补充官方项目文档和源码依据。
- [x] 按用户要求初始化 Go 模块，根目录 `main.go` 仅打印 `Hello, World!`，阶段计划位于 `docs/phase-01-weixin-agent.md`。
- [x] 补充 `.gitignore`，忽略构建产物、本地配置和运行数据。
- [x] 计划补充首次模型/渠道配置向导、默认 Base URL、供应商工厂，以及飞书/QQ 渠道扩展边界；尚未实现。
- [x] 用户审核更新后的计划。（2026-10-06）
- [x] 审核后完成 G0 接入验证；G0 已通过，逐单元开展实现，当前进入 G1a。（2026-10-06）

当前仅完成 Hello World 骨架，未接入 Eino、微信、模型、trace 或评测功能。计划中的功能命令和收益目标均不代表已实现或已验证。
