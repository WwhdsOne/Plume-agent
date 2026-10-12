---
title: G4a.1 实现复核报告
status: historical
updated: 2026-10-12
summary: G4a.1 修复前独立复核快照；原缺陷已修复，当前验证见同日修复复审报告
---

# G4a.1 实现复核报告

**结论：暂不建议通过 G4a.1。** 主路径已完成，包边界和多数关键注释较完整；问题集中在模型引用、持久化失败和恢复边界。常规测试通过不能覆盖这些缺口。本次仅审核、补充复现证据，没有修复源码、提交推送或翻转单元状态。

审查对象是基于 `fd21e68f16a5433024a247fa227b8c7011c8ec01` 的当前工作区改动，包含尚未跟踪的 G4a.1 文件。依据为 [历史会话方案](../plans/2026-10-10-session-history-commands.md) §4.1–4.4、§7，及 [交付记录](G4a.1.md)。按 code-review 技能分别进行 Standards 与 Spec 审查，以下保留两个轴；同一权限问题在两轴分别列出，不重复计算为两个独立缺陷。

优先级：P1 为审核前必须解决的高影响问题，P2 为明确的功能或契约缺口。复现区分为“补测确认”和“静态确认”；未把静态检查冒充端到端实测。

## Standards

### STD-1 · P1 · 新建数据库没有落实 0600（补测确认）

位置：[sqlite.go](/Users/Learning/AI/AGENT/Plume-agent/internal/store/sqlite.go:40)。

`Chmod` 只处理已存在的数据库；首次文件由 SQLite 创建。在 `umask 022` 下实际得到 **0644**，违反方案 §4.1 的数据库及辅助文件 0600 约定。默认目录 0700 可以遮蔽访问，但配置允许数据库放在已有、可被其他用户遍历的目录，此时聊天、工具及 Reasoning 正文可被读取。

建议：安全创建新库并在创建时落实 0600，核验辅助文件权限；不能只在第二次打开时补权限。增加已有公共父目录下的首次创建测试。

### STD-2 · P2 · sessions 校验没有接入 Save 边界（补测确认）

位置：[config.go](/Users/Learning/AI/AGENT/Plume-agent/internal/config/config.go:210)。

`validateDisplayOptions` 校验 Agent、Tools、TUI，却没有调用 `Sessions.Validate()`。构造 `sessions.page_size=101` 后 `Save` 成功，下一次 `Load` 报 `sessions.page_size: expected integer in 1..100`。这会把原本可用的配置替换为程序自己拒绝加载的文件，违背既有“写入前校验、失败保留旧配置”边界。

建议：在共同校验入口接入 sessions，覆盖 page_size、shutdown_timeout_ms 和 history.path 的非法值，并验证失败后原文件字节保持不变。复现使用 101；零值受 `omitempty` 影响，不能用“零值必然使下一次 Load 失败”来解释。

### STD-3 · P2 · trace 缺少真实保存结果（静态确认）

位置：[service.go](/Users/Learning/AI/AGENT/Plume-agent/internal/app/service.go:309)、[stream.go](/Users/Learning/AI/AGENT/Plume-agent/internal/telemetry/stream.go:70)。

`RunEnd` 在 `CompleteRun` 之前写出。模型成功、终态保存失败时，UI 变成 failed，内存不提交，但 trace 已经写了 `status=ok`，也没有独立保存状态字段。方案 §4.4 要求同时记录运行终态和保存状态；当前记录无法区分“模型成功且保存成功”与“模型成功但历史未保存”。

建议：保留模型运行结果与保存结果两个维度，待保存结果确定后记录唯一终态 trace；补正常保存、明确失败和提交结果不确定三种断言，不把存储故障伪装成模型故障。

注释评价：新包职责、事务、取消和部分资源边界已经有较充分的中文注释，没有必要逐行补注释。但 `workspaceDir`、数据库权限和收尾期限等注释声称的保证与实现不符，应随修复一起校正。

## Spec

### SPEC-1 · P1 · 数据库权限与设计不符（补测确认）

方案 §4.1 要求“数据库及辅助文件 0600”。此项与 STD-1 是同一问题，复现与建议见上。

### SPEC-2 · P2 · SQLite URI 没有转义文件路径（补测确认）

位置：[sqlite.go](/Users/Learning/AI/AGENT/Plume-agent/internal/store/sqlite.go:49)。

`"file:" + path` 把普通文件路径直接当 URI。配置路径为 `history#test.db` 时打开成功，但指定文件不存在，实际写到了 `history`；`?` 同样会进入 URI 参数解释。用户指定位置、权限处理位置和锁文件所对应的数据库因此可能不一致。

建议：正确构造并编码 SQLite DSN，验证 `#`、`?`、空格和 Unicode 路径均落到指定文件；不要修改用户合法的文件名来绕过问题。

### SPEC-3 · P2 · 数据库符号链接别名绕过整库锁（补测确认）

位置：[sqlite.go](/Users/Learning/AI/AGENT/Plume-agent/internal/store/sqlite.go:45)。

先打开 `history.db`，再通过指向它的 `alias.db` 打开同库，第二次仍成功，因为分别锁了两个路径的 `.lock`。方案 §4.1 要求同一数据库在进程生命周期内独占；SQL 事务锁不能替代 Agent 执行所有权，否则两个运行时仍可同时执行 Shell 或文件副作用。

建议：以规范数据库身份取锁，解析已有文件和父目录的别名；补跨进程别名测试，并明确处理或拒绝硬链接别名的边界。

### SPEC-4 · P2 · 恢复没有完成语义校验、格式隔离和有界读取（补测及静态确认）

位置：[sqlite.go](/Users/Learning/AI/AGENT/Plume-agent/internal/store/sqlite.go:232)、[prompt.go](/Users/Learning/AI/AGENT/Plume-agent/internal/agent/prompt.go:104)。

`LoadTurns` 仅做 JSON 解码，孤立 tool 消息仍被成功恢复；下一次请求的工具组校验可能拦住它，但恢复本身没有按 §4.2 拒绝损坏历史。更严重的是历史 system 角色被保留，现有 `PromptBuilder.Build` 接受后生成两个 system 来源，违反 §4.3“已保存的 system 文本不作为下一轮指令来源”。补测分别确认了孤立 tool 被接纳和两个 system 消息。

此外，`turns.messages` 直接序列化运行时 `[]model.Message`，没有独立消息存储格式版本；`LoadTurns`、`ListRuns`、`LoadCalls` 全量加载后才在下一次请求检查预算。数据库 schema 版本不能替代消息格式版本，后置请求预算也不能限制恢复时的内存占用。这两项是 §4.2 的静态缺口。

建议：建立有版本的存储消息结构，恢复前校验允许的角色、完整轮次和工具关联；拒绝历史 system 指令来源。先有界读取、累计字节并拒绝超限，不删半组消息或暗中截断。G4a.2 的列表分页可以继续后置，但当前 `-c` 的读取边界应先落实。

### SPEC-5 · P1 · 相同配置 ID 跳过模型兼容指纹（补测确认）

位置：[session_resume.go](/Users/Learning/AI/AGENT/Plume-agent/cmd/plume/session_resume.go:97)。

只要 `candidate.ID == ref.ConfigID` 就返回，完全跳过 provider、协议、模型和 endpoint 指纹检查。保留原配置 ID、把 Base URL 从旧服务改到新服务后，旧会话仍被接受并装配到新服务，可能把历史正文发往用户未为该历史选择的端点。模型 ID 或供应商变化同样受影响。

方案 §4.3 明确要求引用存在**且指纹匹配**，不允许静默迁移历史。建议：ID 命中仍校验指纹，变化时返回 `resume_incompatible`；分别覆盖地址、供应商、协议和模型变更。仅 default_model 改变的已有测试不能守住这条边界。

### SPEC-6 · P1 · 提交结果不确定时允许内存和磁盘分叉（故障注入确认）

位置：[service.go](/Users/Learning/AI/AGENT/Plume-agent/internal/app/service.go:317)。

`CompleteRun` 返回错误后只降级事件、不追加内存，随后清掉 busy；没有按 run ID 查询提交结果，也没有阻止继续。注入“底层已经提交 q1/a1，但调用方收到提交确认错误”后，磁盘保存 q1/a1，内存为空，下一次 q2 仍被发送且缺少 q1/a1；重启后又恢复 q1/a1，形成两套上下文。

方案 §4.2 第 5 步专门要求：确认已提交则同步内存，明确未提交则保持失败，仍不确定则禁止新 run 和会话切换。建议按该顺序实现，不通过重跑模型或工具补历史。补充明确未提交、已提交但返回错误、查询也失败三种场景。

### SPEC-7 · P2 · 历史工作区取启动目录，未取实际工具根（静态确认）

位置：[session_resume.go](/Users/Learning/AI/AGENT/Plume-agent/cmd/plume/session_resume.go:27)、[chat.go](/Users/Learning/AI/AGENT/Plume-agent/cmd/plume/chat.go:195)、[runtime_tools.go](/Users/Learning/AI/AGENT/Plume-agent/cmd/plume/runtime_tools.go:29)。

方案 §2 要求工作区为最终工具工作目录的规范路径。`workspaceDir()` 读取 `os.Getwd()`，工具却使用 `tools.workspace`。同一启动目录下把工具根从 A 改到 B，`-c` 仍能找到 A 的历史，实际工具却操作 B；不同启动目录指向同一工具根时又找不到同一历史。

建议统一从最终工具根取得规范身份，供历史保存、查询和执行共同消费，并测试自定义绝对/相对工具根及符号链接。

### SPEC-8 · P2 · 恢复后的轮次数和会话计时不正确（补测及静态确认）

位置：[sqlite.go](/Users/Learning/AI/AGENT/Plume-agent/internal/store/sqlite.go:255)、[session_resume.go](/Users/Learning/AI/AGENT/Plume-agent/cmd/plume/session_resume.go:131)、[session_manager.go](/Users/Learning/AI/AGENT/Plume-agent/internal/app/session_manager.go:38)。

`LoadTurns` 返回下一轮序号 `seq+1`，调用方却把它作为 `Session.Turns()` 的已提交轮次数。补测：只保存一轮，恢复后得到两轮；零轮同样会变成一轮。

恢复还把数据库的 `CreatedAt` 用作状态栏 `StartedAt`，将退出期间算入 session 时长，违反 §4.3“本次打开该会话的时长，恢复时重新计时”。建议区分已完成轮数与下一序号、历史创建时间与本次打开时间；分别测试空/单/多轮和隔日恢复。

### SPEC-9 · P2 · Ctrl+N 保留旧会话的先读后改缓存（补测确认）

位置：[session_manager.go](/Users/Learning/AI/AGENT/Plume-agent/internal/app/session_manager.go:92)、[chat.go](/Users/Learning/AI/AGENT/Plume-agent/cmd/plume/chat.go:210)。

`NewSession` 只替换 Session 与 ID，继续使用同一个 runtime/tools 注册表，未重建工具执行状态。旧会话实际 read 后 Ctrl+N，新会话直接 overwrite 同一文件仍成功，复现返回 `ok:true`；应返回 `read_required`。这违反 §4.3 的切换/恢复会话工具状态边界。

建议在无执行中 run 的切换点重建或明确重置工具读取摘要状态，并保证重建失败时保留原会话；测试新会话需要重新 read、旧会话仍保存且没有额外副作用。

### SPEC-10 · P2 · shutdown_timeout_ms 未限制完整退出收尾（静态确认）

位置：[service.go](/Users/Learning/AI/AGENT/Plume-agent/internal/app/service.go:361)、[service.go](/Users/Learning/AI/AGENT/Plume-agent/internal/app/service.go:420)。

配置只用于终态数据库保存的独立 context；`Close()` 仍无截止地 `wg.Wait()`。等待模型/工具或资源清理不返回时，不受该配置限制。方案 §4.4 要求“收尾总期限默认 5000ms”，而当前实际只是保存期限。

建议让退出流程统一管理总期限，明确到期退出及未保存提示；执行尚未确认停止时不能先释放数据库锁并继续另一个会话。需增加阻塞清理的退出测试；本项未做真实挂死工具的端到端复现。

## 验证与证据

独立执行结果：

| 检查 | 结果 | 说明 |
| --- | --- | --- |
| `rtk go test -race ./...` | 通过 | RTK 报 1323 passed / 17 packages，包含子测试；未把补充反例放进原源码目录 |
| `rtk go vet ./...` | 通过 | 无告警 |
| `rtk proxy go tool golangci-lint run` | 通过 | 原始命令退出 0，`0 issues.` |
| `rtk proxy gofmt -l`（涉及的 Go 包） | 通过 | 空输出 |
| `rtk git diff --check` | 通过 | 空输出 |
| store 审核补测 | 4 失败 | 新库权限、损坏工具关联、特殊路径、数据库别名锁 |
| cmd/app/config/agent 审核补测 | 6 失败 | 变更 endpoint、恢复轮数、提交结果不确定、非法 sessions 保存、历史 system 来源、新会话 read 缓存 |

补测故意断言应有行为，其失败是缺陷证据。提交结果不确定使用 Store 装饰器故障注入，并非声称已经诱发真实 SQLite 设备故障。未使用真实供应商、真实用户配置或用户工作区文件。本次未独立重跑交付中的全部六个 PTY 场景或构建安装，也未验证 Windows；这些不应扩写为已验证项。

原复现源码以 `.go.txt` 保存在 `evidence/G4a.1/audit/{cmd-plume,app,config,agent,store}.go.txt`，不参与原仓库 `go test ./...`；原始测试运行目录为 `/tmp/plume-g4a1-review.0O8usn` 和 `/tmp/plume-spec-review.Wj0VXr`。这些本地审核产物此前未提交，已按通过即删规则随 2026-10-12 通过提交删除，没有 Git 历史副本。旧反例依赖当时接口，不再提供指向已删除文件的链接或复制命令。

当前行为应由已转入仓库的正式回归测试复验；用例对应关系见 [修复复审](G4a.1-fixes-2026-10-11.md) §1–2。当前回归命令如下，其通过不改变上表记录的原缺陷反例结果：

```bash
rtk go test -race ./cmd/plume ./internal/app ./internal/config ./internal/agent ./internal/store -count=1
```

## 文档与复审要求

- AGENTS.md 的 sessions 段写“凭据与正文不进库”，与实际保存会话正文冲突，应改成“凭据不进库，正文仅存历史库、不进 trace”；阶段计划仍有“目前仅文档”的旧描述，需一起核对。
- 本报告不替用户批准单元。G4a.1 继续保持待审核，G4a.2 不推进，现有交付证据保留。
- 修复后将这些反例转为正常回归测试，并重新执行完整竞态、lint 和交互恢复验证；重新核对模型目的地、内存/磁盘一致性、文件权限与执行锁。不能仅修改注释或删除失败断言来达成通过。

Standards：3 项，最严重为新库权限；Spec：10 项，最严重为模型指纹旁路和提交结果不确定时历史分叉。两轴的数据库权限为重复项。

## 后续修复

用户随后授权全部修复与子 agent 自审；上述发现保留为原审查快照，现有代码已转换为正式回归测试。修复对应关系、额外发现及最新验证见 [G4a.1 修复复审](G4a.1-fixes-2026-10-11.md)。本报告保持 `historical`，上面的“不建议通过”与“待审核”仅描述修复前快照；用户于 2026-10-12 通过 G4a.1 及全部修复，并授权随后实施 G4a.2。当前状态与验证以 [G4a.1 审核记录](G4a.1.md)、修复复审及仓库正式测试为准。
