---
title: 第二阶段：渠道网关接入计划
status: active
updated: 2026-10-06
summary: 第二阶段草案：微信扫码、真实收发、消息可靠性；第一阶段通过后另行授权
---

# 第二阶段：渠道网关接入计划

> 日期：2026-10-06。状态：后移的阶段草案，未开始实现；第一阶段 TUI 验收后另行确认启动。
> Goal：复用 TUI 已验证的模型适配与自研 Agent，通过微信 iLink Bot 完成真实消息闭环，并保留飞书、QQ 的扩展位置。
> 执行：使用 executing-plans 按单元推进，每个单元完成即停，等待用户审核；不因第一阶段完成自动启动本阶段。
> 架构：渠道只负责接入、身份、消息归一化与回复；应用层负责运行与可靠性；不复制 Agent，也不引入 Eino。

## 1. 阶段位置与历史兼容

用户于 2026-10-06 将首版改为 TUI。[第一阶段](phase-01-tui-agent.md) 的完成不依赖微信账号、扫码或渠道凭据。

保留原 G2a.1（扫码）、G2a.2（收发）、G2b（可靠性）的编号和语义，便于引用 G0 已完成的 [微信调研](decisions/0002-wechat.md)。这些编号不是新的实施授权；G3 工具能力现在先于 G2 实施。

G0 核实的是 Hermes 参考实现，不代表本项目已实测账号。原调研快照保留，进入本阶段前重新核对供应商接口与可用性，不能将可变 main 当永久协议规范。此前的 Eino 组件假设由 [0003](decisions/0003-model-runtime.md) 替代。

## 2. 接入方式与范围

微信继续采用用户已选择的 Hermes 式 iLink Bot：CLI 选渠道 → 展示二维码 → 手机扫码确认 → 保存 bot 凭据 → 长轮询接收文本 → Agent 回答 → 回复原会话。创建/绑定 bot 由平台完成，不假设独立“创建联系人”接口。

参考：[Hermes 微信文档](https://hermes-agent.nousresearch.com/docs/zh-Hans/user-guide/messaging/weixin)、[适配器源码](https://github.com/NousResearch/hermes-agent/blob/main/gateway/platforms/weixin.py)。接口快照、鉴权与 fixture 见 0002；本次只保留既有调研，不声称重新验证接口。

首个渠道只做一个活动微信实例、私聊文本；群聊、媒体、多渠道并行、账号跨平台绑定和共享记忆不在本阶段。飞书/QQ 保留禁用预设；接入各自协议时另立审核单元，不收集未支持渠道的凭据。

```text
herald gateway setup    # 选择渠道、扫码/重连；独立于 TUI 模型设置
herald gateway start    # 恢复凭据、接收、调用已有 app/agent、回复
herald gateway status   # 脱敏连接状态及最近收发时间
```

这些命令尚未实现。裸 `herald` 仍以 TUI 为默认入口，不因用户保存过微信配置而自动启动网关。首版不承诺 TUI 和网关跨进程同时写同一会话存储；如需同时运行须增加所有权/锁定审核。

## 3. 模块与可靠性约束

- 渠道注册项包含稳定 ID、名称、可用状态、设置流程、Adapter 工厂及能力说明。新增测试渠道可复用 app/agent，不在运行时增加品牌分支。
- 标准消息包括渠道类型/实例 ID、外部消息 ID、会话 ID、发送者、文本、回复关联。身份与去重键带入口命名空间，不把本地 TUI 用户自动绑定微信用户。
- 通用模型使用配置的 `model_ref`，不让渠道构造 prompt。微信 `context_token` 属于回复凭据，不能混入 Agent 历史或模型 token 计量。
- 长轮询与模型调用解耦；单 bot 账号只有一个接收循环。同会话串行、跨会话有界并发；长期连接不是无限长 trace。
- 登录/轮询/发送/Agent deadline 分开；空轮询是正常状态。消息入站、去重和游标推进采用持久化事务，先可靠落盘再推进游标。
- 运行状态与投递状态分开。发送重试复用已完成结果，不能重跑 Agent。对无法确认是否投递的非幂等请求记录 `delivery_unknown`，不承诺外部端到端 exactly-once。
- 渠道重试策略独立于模型请求的默认不自动重试，禁止叠加导致重复调用/计费。

## 4. 审核单元

### G2a.1：扫码连接 bot

**拟改文件：** `cmd/herald/gateway.go`、`internal/channel/wechat/{client,login,credentials}.go`、对应 `_test.go`、`docs/runbooks/wechat.md`。

- [ ] 复核 0002 的接口快照，记录本次参考 commit、状态字段与鉴权契约。
- [ ] 实现二维码获取/终端渲染、状态轮询、手机确认和凭据保存；复用 G1a 的安全存储约定。
- [ ] HTTP fixture 覆盖待扫码、待确认、成功、过期、取消、超时、非法响应和已有凭据保护；二维码 URL/token 不进入 trace。
- [ ] 由用户操作手机扫码，核对 bot 聊天对象。凭据有效时恢复，失效时提示重登，不把“文件存在”当在线。
- [ ] 提交成功/失败状态 trace、实测记录和 `docs/reviews/G2a.1.md`，停止等待审核。

此单元只验收登录，不宣称已完成收发。二维码失败可提供当次登录链接，仅终端展示，不进入日志。

### G2a.2：真实收发与已有 Agent 复用

**拟改文件：** `internal/channel/{message,registry}.go`、`internal/channel/wechat/adapter.go`、`internal/app/{service,session}.go`、`cmd/herald/gateway.go`、对应 `_test.go`、`docs/runbooks/wechat.md`。

- [ ] 实现 gateway start、长轮询、会话映射、文本发送和按账号/对话保存回复上下文。
- [ ] 通过 `model_ref` 使用第一阶段工厂与运行时；用测试渠道验证无微信字段泄漏到 Agent/TUI 模型契约。
- [ ] 覆盖正常空轮询、网络/限流退避、无效凭证、模型超时、发送失败；不同类型的重试各自测试。
- [ ] 完成至少 10 条真实消息闭环，保留脱敏回执和 trace；只作为连通性证据，不宣称 p95 显著改善或生产可靠性。
- [ ] 提交 `docs/reviews/G2a.2.md`，停止等待审核。持久化去重与崩溃恢复尚未完成时明确其局限。

### G2b：消息可靠性与会话隔离

**拟改文件：** `internal/store/sqlite.go`、`internal/store/migrations/001_messages.sql`、`internal/app/{inbox,worker,session_queue,delivery}.go`、对应 `_test.go`。

- [ ] 先制定状态转移表，再实现入站/运行/出站持久化、同批消息与游标的事务、去重、有界 worker 和可恢复任务。
- [ ] 注入入站落盘后崩溃、模型完成后崩溃、发送响应丢失，分别验证恢复与未知投递；已完成 Agent 不因发送失败重复执行。
- [ ] 覆盖不同用户/渠道命名空间隔离、同会话顺序、过载与重启；离线重复投递 100 次仍只有一个逻辑任务。
- [ ] 单列外部发送重复风险，提交 `docs/reviews/G2b.md` 后停止。

**第二阶段里程碑：** 三个 G2 单元分别通过，有真实扫码、回复、恢复/去重/隔离、失败路径和可关联的测量证据；TUI 基础回归无新增失败。

## 5. trace 与指标补充

复用第一阶段 §6/§7，不改变同名模型/工具指标的定义。新增：

- setup/connection trace：二维码请求、扫码状态、确认到凭据可用、凭据恢复和重连；人工扫码时间单列。
- 入站 trace：身份验证、去重、排队、原逻辑 run/span link。
- 出站 trace：尝试次数、渠道返回 ID、确认接受/失败/未知投递，不记录 token/context_token。
- 渠道端到端延迟：本服务收到消息至渠道 API 确认接受回复，不等同于用户已读，也不能与 TUI 渲染延迟直接合并。
- 收发成功率、重复执行/重复发送率、未知投递数、断网到轮询恢复时间。报告所有失败和超时，遵守相同 case/环境/样本的比较规则。

尚未进入本阶段时这些指标为 N/A，不用 TUI 测试结果冒充渠道实测。
