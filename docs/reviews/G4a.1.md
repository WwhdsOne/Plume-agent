---
title: G4a.1 持久历史与显式继续审核
status: passed
updated: 2026-10-12
summary: G4a.1 含全部修复于 2026-10-12 通过：SQLite 历史、显式继续；审核期证据删除，复现命令保留
---

# G4a.1 持久历史与显式继续审核

用户于 2026-10-11 授权实施 G4a.1（方案见 [历史会话与统一指令方案](../plans/2026-10-10-session-history-commands.md) §7）。本单元交付跨重启的会话持久化与 `plume -c` 显式继续；G4a.2（公共 `/` 指令、历史选择器）不在本单元，长期记忆与真实 gateway 仍需另行授权。

2026-10-11 独立复核发现缺陷后，用户授权全部修复与子 agent 自审；当前行为已包含下面的修复。原交付验证表保留为首次记录，最新验证、反例与复审结论统一见 [修复复审记录](G4a.1-fixes-2026-10-11.md)。用户于 2026-10-12 通过本单元及全部修复，授权提交推送，并授权随后实施 G4a.2；G4a.2 当前尚未编码或交付。

审核窗口证据按 [runbook](../runbooks/tui.md) 随通过提交删除；以下数值保留首次交付与修复时的真实记录，复跑产物应写到临时目录。这些本地证据此前未提交，删除后没有 Git 历史副本。

## 1. 改动与使用

### 持久层（新包 `internal/store`）

- **存储**：SQLite（`modernc.org/sqlite` v1.60.1，纯 Go 无 CGO），数据库默认在 `<配置目录>/sessions/history.db`，目录 0700、文件 0600。四张表（sessions/runs/model_calls/turns）经 `PRAGMA user_version=1` 前向迁移；库版本比程序新时拒绝写入（`ErrSchemaNewer`），不删库重建。
- **整库进程独占**：独立 `<db>.lock` 文件 + flock（不锁 db 文件本身——SQLite 用 POSIX fcntl 锁，混用是未定义行为）。第二进程被明确拒绝（`ErrHistoryInUse`），不等待不降级；非 unix 平台明确拒绝开启持久后端。崩溃后锁随进程释放，下次可正常取锁。
- **三段式落盘**：run 起点（含会话幂等创建）→ 模型调用起点（usage NULL=未知）→ 用量快照（按 call ID 整体替换，天然去重）→ 终态。成功轮次的消息组与终态在**一个事务**内提交（CompleteRun，turnSeq 在事务内递增）；失败/取消/中断经 FinishRun 保存展示记录，不写 turns。
- **崩溃恢复**：打开数据库后把全部遗留 running 标记 interrupted（InterruptRunning）；usage 为 NULL 的调用保持未知，不伪零。
- 内存后端（MemoryStore）实现同一契约，供测试与 offline 场景使用。
- 文件按真实路径规范化，URI 正确编码特殊文件名；符号链接别名共用锁，硬链接与非普通文件拒绝。新库、锁和 SQLite 辅助文件均落实 0600；比程序新的 schema 在改变日志模式前拒绝，保留原库。
- 消息使用独立 v1 DTO，严格兼容旧数组，恢复前校验角色、完整工具组、终态与轮次关联。加载各结果最多 min(请求预算,8MiB) 字节/10000 条记录，单 JSON 展开前最多累计 10000 数组元素；超限明确拒绝，不截断或自动修复。同毫秒的 run/调用以插入顺序稳定排序。

### 运行时集成（`internal/app`）

- 起点（StartRun）失败时 Submit 直接返回错误：不发模型请求、不执行工具、不占用执行权。
- 调用起点（StartModelCall）失败时取消整个 run，请求不发出；用量快照（SaveUsage）失败时中止后续步骤。
- 成功路径先落盘再改内存；CompleteRun 返回错误时核查同 run 的终态与完整轮次，确认已提交则同步内存，确认未提交才降级 failed 且保持原历史；无法确认时冻结发送/切换并提示重开。不会自动重跑模型或工具。终态保存使用独立有截止 context，不继承取消；`sessions.shutdown_timeout_ms`（默认 5000ms）同时约束整个退出收尾，超时不提前释放仍被使用的资源与数据库锁。
- 失败/取消的展示记录保存全 run 累计正文与 Thought，后续调用失败不会丢失已显示内容；仅成功完整轮次进入模型上下文。
- run ID 跨进程唯一（`run-<16hex>`，crypto/rand），call ID 仍为 `runID/model-N`；会话 ID 32hex。

### 恢复（`plume -c` / `plume chat -c`）

- 恢复实际 tools.workspace（EvalSymlinks 规范化）最近有 run 的本地会话；没有可继续历史时提示后开新会话。
- 模型引用以 **ConfigID 定位并核对端点指纹（provider/协议/模型/BaseURL）**；同 ID 内容改变立即报 `resume_incompatible`。只有旧 ID 已不存在，才允许按完全相同指纹定位到重命名配置；不静默换模型。`--model` 显式指定与引用不一致同样拒绝。
- 恢复内容：成功轮次消息预灌模型上下文、用量快照恢复统计（未知保持未知）、TUI 记录区预置转录（系统行带短 ID 与 run 计数，user/assistant 按序，cancelled/interrupted/failed 带终态标记及 Thought；正文与 Thought 经 sanitize 净化）。输入草稿与 Up/Down 草稿历史不从磁盘恢复。轮数使用已完成轮数，session 时长从本次打开计时，原创建时间另存。
- `default_model` 改为其他配置后 `-c` 仍按会话引用恢复原模型（ConfigID 匹配优先于默认值）。
- `--offline` 与 `-c` 互斥；`sessions.history.enabled=false` 时 `-c` 明确报错，且不创建数据库文件。offline 不读配置不落盘。

### 会话管理

- 裸 `plume` / `plume chat`：新会话 ID，行为不变。
- **Ctrl+N 建立新会话**：新的随机 ID 与空内存历史，清理工具先读后改的证明；旧会话保留在持久层（不再原地清空）。busy、已关闭或提交状态不确定时拒绝，重置失败保留记录与草稿。`-c` 恢复的是最近有 run 的会话，新旧会话数据互不混淆。
- `sessions.page_size`（默认 20）本单元仅落盘校验，浏览分页属 G4a.2。
- telemetry 的 RunStart/RunEnd 事件新增 session_id 字段；RunEnd 在保存结果确定后记录 run_status/save_status，存储故障使用 save_error_code；调用起点/用量快照失败另记 save_error_stage=call_start/usage，终态 saved 不掩盖先前故障。正文与凭据依旧不进 trace。

## 2. 验证结果

下表为首次交付记录，未包含本次修复后的新增回归用例；当前完整竞态、lint 与 PTY 结果见 [修复复审记录](G4a.1-fixes-2026-10-11.md)。测试计数统一采用 RTK 包含子测试的汇总，不能把首次表内 411 与后续汇总作增长指标。

| 验证 | 实际结果 |
| --- | --- |
| `go test -race ./...` | 411 项，17 包通过，0 失败 |
| `go vet ./...` / `gofmt -l .` | 通过 / 无输出 |
| `go tool golangci-lint run` | 0 issues |
| `./scripts/build.sh` | v0.1.0、Go 1.27.1、darwin/arm64 |
| store 契约测试（双后端同套） | 10 组通过（含锁独占、schema 更新拒绝、损坏文件拒绝、usage 去重、终态幂等） |
| app 持久化集成 | 8 项通过（起点失败拒请求、调用起点失败不发请求、快照失败中止、终态失败降级、取消落盘、跨进程恢复续序、新会话语义） |
| cmd 恢复装配 | 5 项通过（ConfigID/指纹匹配、不兼容拒绝、转录构建、预灌与用量、显示辅助） |
| TUI 转录注入 | 1 项通过（类别/净化/终态标记/不污染草稿历史） |
| 隔离 PTY 场景（本地 fixture，不联网） | 6 场景通过：`G4A1_PTY_OK cases=6` |

## 3. 可复现命令与历史证据说明

全部用临时 PLUME_HOME、本地 OpenAI 兼容 fixture（占位凭据非真实 Key），不访问真实凭据、不调用付费模型：

```bash
rtk proxy ./scripts/build.sh
rtk proxy python3 scripts/pty_demo_g4a1.py --output /tmp/plume-g4a1-pty
# 期望：G4A1_PTY_OK cases=6
# 原审核产物位于 docs/reviews/evidence/G4a.1/pty/，按通过即删规则删除
```

六个场景（对齐方案 §7 G4a.1 验收表）：

1. **persist-and-resume**：成功轮次先落盘再进内存；`-c` 预灌上下文，第二轮请求携带旧 user/assistant（fixture 断言 messages）；turns=2、sessions=1。
2. **model-ref-match**：default_model 切到 B 后 `-c` 仍恢复 A 的会话与端点（primary 2 次调用、fallback 0 次）。
3. **ctrl-n-new-session**：Ctrl+N 新会话与旧会话在持久层分开（sessions=2、各 1 轮）；`-c` 恢复最新且转录不含旧会话消息。
4. **crash-interrupted**：同会话先成功一轮再运行中 kill -9；`-c` 打开时遗留 running 标记 interrupted、旧轮完好（无半组 turns）、未完成调用 usage 保持 NULL。
5. **second-process-lock**：持锁 TUI 运行中第二进程非零退出并明确报 `history database is locked`；持锁进程退出后可重新取锁。
6. **flag-errors**：`--offline -c` 互斥拒绝；禁用历史后 `-c` 在 pty 里明确报 `session history is disabled` 且不创建数据库文件。

数据库断言直接读 `sessions/history.db`（runs/turns/model_calls/sessions 表），见脚本内 `query`。

## 4. 局限与审核项

- **单进程独占**：整库锁语义是"同一时刻一个持久化聊天进程"；跨进程并发所有权（多窗口共享历史）留给 G2b。
- **会话标题固定** "New conversation"，`/rename` 与浏览分页（`page_size` 生效）属 G4a.2；本单元 `-c` 只恢复"最近有 run"的会话，不能选择。
- **恢复只回成功轮次**：失败/取消/中断以转录展示（带终态标记），不进入模型上下文；输入草稿、草稿历史、工具事件不恢复。
- **无 token 估算**：沿用既有——恢复后仅展示供应商实际 usage 与用户配置容量，不猜容量（见 AGENTS.md 禁区）。
- **崩溃窗口**：起点已写、终态未写的 run 下次打开标记 interrupted；串行 run 也可能同毫秒，已用数据库 rowid/内存插入序稳定排序，不依赖随机 ID 排序。
- **平台**：本次实测 darwin/arm64；Unix 锁实现覆盖 darwin/linux 构建标签，但不将其视为 Linux 实机验证。非 unix 平台明确拒绝持久化，Windows 未实测。
- **迁移**：只有 v1；未来 schema 变化必须走新增版本号的前向迁移，旧程序遇新库拒绝（ErrSchemaNewer 已测）。
- 模型质量与真实供应商延迟不由本地 fixture 推导（指标协议见 `docs/specs/metrics.md`，本单元无对比列）。

## 5. 通过说明与人工体验核对

用户于 2026-10-12 审核通过 G4a.1 及全部修复，授权提交推送。下列人工体验与平台局限保留为后续使用时的核对事项，不阻塞已通过状态。

1. `plume -c` 在真实工作目录的实际体验（转录可读性、终态标记样式）。
2. `~/.plume/sessions/history.db` 的存在与权限（0600）符合预期；如需完全关闭持久化，`sessions.history.enabled=false` 后删除该文件即可。
3. 整库锁的取舍：第二个 `plume` 进程直接拒绝是否符合使用习惯（备选：只读浏览不锁写）。
4. G4a.2（公共 `/` 指令与 TUI 历史选择）已获同日授权，待本单元提交推送后按方案实施，交付后另行审核。
