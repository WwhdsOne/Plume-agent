---
title: G4a.1 缺陷修复与子 agent 复审
status: passed
updated: 2026-10-12
summary: G4a.1 全部审核及 -c 初始 ctx 修复于 2026-10-12 通过；保留 1440 项竞态、lint 与 PTY 验证记录
---

# G4a.1 缺陷修复与子 agent 复审

用户授权修复 [独立复核报告](G4a.1-audit-2026-10-11.md) 中的全部问题，并启动子 agent 审核修复。原报告的 Standards 3 项、Spec 10 项已全部处理，其中数据库权限是重复发现；下表保留原编号，避免重复计数。子 agent 复审新增的三项缺陷也已修复并复验。用户于 2026-10-12 通过 G4a.1 及全部修复，授权提交推送，并授权随后实施 G4a.2；当前 G4a.2 尚未编码或交付。

审核窗口证据按 [runbook](../runbooks/tui.md) 随通过提交删除；本文件保留 2026-10-11 的原始测试计数、构建时戳及验证结论。复跑产物改写临时目录，原本地证据未提交，没有 Git 历史副本。

## 1. 原发现与修复对应

| 原编号 | 修复后的行为 | 主要回归证据 |
| --- | --- | --- |
| STD-1 / SPEC-1 | 在 SQLite 打开前以 0600 新建数据库，锁与辅助文件同为 0600；默认目录 0700，既有公共父目录不被改权限 | store `TestAuditCreatedDatabaseMode`、`TestSQLitePrivateFilesAndPublicParent` |
| STD-2 | Config.Save 与 Load 都校验 sessions；非法页大小、期限或路径不覆盖原文件；已知字段缺失/null 实际补默认并保留未知字段 | config `TestSaveInvalidSessionsKeepsExistingConfig`、`TestSessionsNullDefaultsKeepUnknownFields` |
| STD-3 | RunEnd 在保存结果确定后记录执行与保存两个维度；整体 status 不把保存失败算成功；起点/用量故障保留固定阶段与存储错误码 | app `TestHistoryFailureTraceKeepsExecutionAndSaveSeparate`、`TestHistoryEarlierStorageFailureRemainsInTrace` |
| SPEC-2 | 用 URL 编码构造 SQLite URI，`?`、`#`、空格和 Unicode 均保持配置的实际文件名 | store `TestAuditSpecialPathStaysAtConfiguredFile`、`TestSQLiteURIPaths` |
| SPEC-3 | 规范真实路径后取整库锁；符号链接别名使用同一锁，硬链接及非普通文件明确拒绝 | store `TestAuditSymlinkAliasSharesProcessLock`、`TestSQLiteAliasLockAcrossProcess`、`TestSQLiteHardlinkRejected` |
| SPEC-4 | 独立 v1 消息 DTO，严格兼容旧数组；检查角色、完整工具组、调用 ID、终态与轮次关联；读取前限制字节/条数/JSON 展开量；prompt 拒绝外来 system | store `TestRecoveryMessageFormat`、`TestRecoveryRejectsCorruptTurns`、`TestRecoveryPrechecksBeforeDecode`、`TestRecoveryLimitsJSONExpansion`；agent `TestPromptRejectsExternalSystemSources` |
| SPEC-5 | 同 ConfigID 也必须验证供应商、协议、模型与端点指纹；仅旧 ID 已不存在才允许匹配同指纹的重命名配置 | cmd `TestResumeSameConfigIDRejectsChangedDestination`；store `TestFingerprintEndpointCase` |
| SPEC-6 | CompleteRun 返回错误后核查同 run；确认提交则同步内存，确认未提交则不追加，无法确认则冻结发送与切换并提示重新打开 | app `TestAuditCommitUncertaintyCannotForkHistory`、`TestHistoryUnknownCommitFreezesRunAndSession`；store `TestRunStateConfirmation` |
| SPEC-7 | 工具装配与历史查询共用规范化的实际 tools.workspace，不再用启动目录替代工具根 | cmd `TestHistoryWorkspaceUsesEffectiveToolRoot` |
| SPEC-8 | nextSeq 转换为已完成轮数；状态栏从本次打开计时，原 CreatedAt 单独保留 | cmd 恢复装配测试；app 持久恢复测试增加轮数断言 |
| SPEC-9 | 在无活动 run 的会话切换点清空工具读取摘要；重置失败保留旧会话与草稿，新会话必须重新 read 后才能 edit/write | agent `TestRuntimeResetSessionStateClearsWorkspaceReads`；app `TestAuditNewSessionRequiresFreshRead`；TUI `TestNewSessionFailurePreservesTranscriptAndDraft` |
| SPEC-10 | shutdown_timeout_ms 约束取消、等待 run、终态保存与资源关闭的总收尾；历史关闭时也生效，超时不提前释放仍被使用的资源/数据库锁 | app `TestHistoryCloseHonorsTotalDeadline`；cmd 退出收尾测试 |

新增接口与非显然分支补了中文注释，说明提交核查、锁归属、加载预算、取消收尾与会话切换的约束。没有删掉原失败断言来获得通过；原审查反例已转为正式回归测试。

## 2. 复审发现及额外修复

三个实现子 agent 分别处理存储、恢复装配、配置/工具边界；完成后再由 Standards 与 Spec 两个只读子 agent 检查最新代码，并由第三个子 agent 补验其未实现的 app/cmd/TUI 边界。

| 复审发现 | 修复与复验 |
| --- | --- |
| 新 schema 的 WAL 库在拒绝前被 journal_mode=DELETE 改写 | 先校验 user_version，再改变日志模式。独立反例原本显示头部 2/2 → 1/1，修复后同反例通过；正式 `TestSQLiteNewerWALSchemaUnchanged` 保证文件保持原样 |
| 工具后下一次请求失败，持久化只保存被清空的 LastMessage，丢失已显示正文与 Thought | 展示落盘使用全 run 累计 Message；UI 终态继续使用最后一步。正式 `TestSpecFailedContinuationRetainsAllDisplayedText` 先失败后通过；恢复转录装载 Reasoning 并净化，cmd 三种未完成终态与 TUI 折叠/展开均已验证 |
| 用量/调用起点保存失败被最终 FinishRun 成功掩盖 | 保留 save_error_stage=call_start/usage 与 save_error_code=history_storage，终态 save_status=saved 仍可识别先前故障；两种故障注入先失败后通过 |

另补同毫秒串行 run、最近会话和调用的稳定排序：SQLite 以 rowid、内存以插入序打破时间戳平局，测试覆盖双后端。串行不能保证时间戳不同，原文档对此的错误限制已更正。

全仓检查曾暴露既有 Git 取消测试的启动调度抖动：第一次全仓检查与独立 10 次重复各有一次在 PID 产生前超时，未观察到取消后的进程回收失败。测试现在允许 5 秒启动等待、10 秒采集期限，仍等实际 PID 后主动取消并断言进程消失；生产取消逻辑未改。修订后连续 20 次竞态测试通过，期限行为仍由独立超时测试守住。

最终子 agent 结论：Standards 无未解决阻塞项；Spec 两项新增 P2 已修复且复验通过；第三轮 app/cmd/TUI 复审没有新的证据充分缺陷。此结论表示已检查范围内未发现阻塞问题，不表示软件没有任何潜在缺陷。

## 3. 实际验证

本节记录首轮审核修复；随后用户报告的 `-c` 初始 ctx 问题及最新验证见 §5。

| 命令或场景 | 实际结果 |
| --- | --- |
| `rtk go test -race ./... -count=1` | 最终 1430 passed，17 packages，退出 0；RTK 计数包含子测试 |
| `rtk go vet ./...` | 退出 0，无告警 |
| `rtk proxy go tool golangci-lint run` | 退出 0，0 issues |
| `rtk proxy gofmt -l .` / `rtk git diff --check` | 无输出 |
| store 最终竞态测试 | 69 passed |
| cmd 最终竞态测试 | 60 passed；含未完成转录/Thought 专项 4 passed |
| Git 取消测试 `-race -count=20` | 20 passed；修改后 app 全包竞态 79 passed |
| 独立 app/cmd/TUI 定向竞态 | 41 passed；额外 `/tmp` View 测试验证 Thought 折叠、展开、再折叠与净化 |
| `rtk proxy ./scripts/build.sh` | v0.1.0 / fd21e68-dirty / Go 1.27.1 / darwin-arm64，构建时间 2026-10-11T12:07:34Z；未安装到用户 GOBIN |
| 最新二进制 G4a.1 PTY | 6 场景全部 passed，`G4A1_PTY_OK cases=6` |

PTY 使用临时 PLUME_HOME、占位凭据和本地 OpenAI 兼容 fixture；不访问真实配置或付费模型。原结果记录为 `evidence/G4a.1/fixes/pty/results.json`（审核窗口产物随通过提交删除）：成功恢复两轮且只有一个会话；默认模型变化仍匹配原引用；Ctrl+N 两个会话各一轮；崩溃运行标记 interrupted 且未完成 usage 为 NULL；第二进程拒绝取锁；互斥参数和禁用历史均明确报错。

以下是故障注入测试实际 trace 的字段摘录（省略随机 ID、时戳与耗时）：

```json
{"status":"error","run_status":"completed","save_status":"failed","save_error_code":"history_storage","save_error_stage":"","error_code":"history_storage"}
{"status":"error","run_status":"failed","save_status":"saved","save_error_code":"history_storage","save_error_stage":"call_start","error_code":"history_storage"}
{"status":"error","run_status":"failed","save_status":"saved","save_error_code":"history_storage","save_error_stage":"usage","error_code":"history_storage"}
```

第一行区分模型完成与终态事务保存失败；后两行保留早期存储故障，不会因为终态保存成功而误报整轮成功。测试同时断言 trace 不含用户输入、回答正文；数据库中的展示正文与无正文 trace 边界一致。

可复现：

```bash
rtk go test -race ./... -count=1
rtk go vet ./...
rtk proxy go tool golangci-lint run
rtk proxy gofmt -l .
rtk git diff --check
rtk proxy go test ./internal/app -run '^TestHistory(FailureTraceKeepsExecutionAndSaveSeparate|EarlierStorageFailureRemainsInTrace)$' -count=1 -v
rtk proxy ./scripts/build.sh
rtk proxy env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-tui-regression-deps python3 scripts/pty_demo_g4a1.py --output /tmp/plume-g4a1-fixes-pty
```

最后一条的 pyte/wcwidth 依赖目录可按本机安装位置替换。审核期证据按 [runbook](../runbooks/tui.md) 随通过提交删除，上述命令重新验证当前实现，不能将新产物冒充原审核录像。

## 4. 兼容性与边界

- 配置仍为 schema v1，SQLite user_version 仍为 1；消息 DTO 使用独立 JSON 字段 version=1，旧数组只在严格校验后兼容读取，不写回或静默修复损坏消息。
- 恢复加载每个结果最多 min(agent.budget.request_bytes,8MiB) 正文字节、10000 条记录，单 JSON 展开前累计数组元素最多 10000；超限拒绝恢复，不截断或代替自动压缩。
- 提交结果不确定使用 Store 故障注入验证，未声称诱发过真实磁盘故障。无法确认时冻结，重新打开后由数据库已确认的完整轮次恢复。
- 整库仍为一个持久聊天进程独占；非 Unix 拒绝持久化。本次实测 darwin/arm64，未做 Windows 或 Linux 实机、真实供应商验证。
- 若底层清理完全不响应取消，退出期限让调用方返回错误，保留尚在使用的资源直到进程结束；不在同进程中把旧执行权交给新会话。
- 本修复记录只覆盖 G4a.1。G4a.2 的公共 `/resume`、历史浏览分页与 fake gateway 已于 2026-10-12 获授权，待本单元提交推送后实施；真实 gateway、长期记忆、压缩与技能仍另行授权。真实终端体验、整库独占取舍与平台实测局限保留。

同步更新了 AGENTS.md、历史方案、阶段计划、配置契约、runbook、原审核记录与文档索引，清除“仅授权文档”、正文不进库、任意提交错误都未提交、同毫秒不可能等过时描述。

## 5. 追加修复：`-c` 首次画面显示 ctx usage unknown

用户随后报告使用 `-c` 启动时 ctx 显示 `usage unknown`。根因在 TUI 构造：SQLite、恢复装配和 app.SessionStats 均有已知 LastUsage，但 NewWithOptions 只复制统计，没有填入 contextTokens 与最近调用 usage；有历史调用时，空会话初始化也不会执行。

修复仅在 InitialStats 初始化处预灌末次 LastUsage：输入已知时以末次 PromptTokens 填 ctx，不用累计输入；缓存深复制可选指针并同时预灌最近调用显示。无调用仍初始化零，最后调用缺失 usage 仍为 unknown，下一请求等待时保留恢复快照；恢复不发模型请求，也不制造 usage 事件或新调用统计。状态栏契约同步补充这一恢复行为。

回归证据：

- `TestContinueRestoresContextBeforeAnyRequest` 真实关闭/重新打开 SQLite，经 `findResumableSession → adoptResumedSession → NewWithOptions → View` 复现；修复前即使 app 统计已知，首次 View 仍显示 `ctx: usage unknown`，修复后立即显示进度条及 `3/1M`，且 fake 调用数为零。
- `TestInitialStatusRestoresLatestUsage` 覆盖空会话、两次已知调用、末次未知、明确输入零，两种 CC/Pi 缓存 scope、外部初始快照指针修改及下一请求等待。定向测试和独立子 agent 竞态复验均为 10 passed；复审无阻塞发现。
- 最终全仓 `rtk go test -race ./... -count=1` 为 1440 passed / 17 packages；vet、gofmt 与 diff 检查通过，lint 0 issues。
- build.sh 已重新构建仓库 `./plume`，时间 2026-10-11T12:28:27Z，v0.1.0/fd21e68-dirty；未安装 GOBIN。最新二进制六项 PTY 全通过，新增首次恢复帧断言 `ctx: ... 10/1M`、`cache: 40%`，当时 fixture 请求数仍为 1。原结果记录为 `evidence/G4a.1/fixes/ctx-resume/pty/results.json`；该产物与旧首轮证据按通过即删规则删除，数值保留为当时验证记录。

复现命令：

```bash
rtk go test -race ./cmd/plume ./internal/tui -run '^Test(ContinueRestoresContextBeforeAnyRequest|InitialStatusRestoresLatestUsage)$' -count=1
rtk proxy ./scripts/build.sh
rtk proxy env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-ctx-resume-deps python3 scripts/pty_demo_g4a1.py --output /tmp/plume-g4a1-ctx-resume-pty
```

PTY 依赖 pyte 0.8.2/wcwidth 0.9.2，仅安装在临时测试目录；没有修改项目依赖或真实用户配置。以上为 2026-10-11 修复验证记录；G4a.1 含本次修复于 2026-10-12 审核通过，用户已授权提交推送。
