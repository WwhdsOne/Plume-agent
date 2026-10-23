---
title: G4a.2 公共指令与 TUI 历史选择审核
status: passed
updated: 2026-10-23
summary: G4a.2 已通过并获提交授权：普通与竞态各1638项、12个PTY场景通过；标题与取消终态已修复
---

# G4a.2 公共指令与 TUI 历史选择审核

用户于 2026-10-12 授权实施，2026-10-23 要求“提交后开始实现下一个需求”。按仓库逐单元审核的“通过/继续”规则，本单元记录为通过并获提交授权；以下正式验证在提交前完成。人工 kitty/Windows 体验仍未验证，不以授权或自动测试代替实测。下一项具体目标待明确。

G4a.1 与全部修复已提交并推送 `97568ec`；本单元在 `codex/g4a2-commands` 实现。当前行为见 [公共指令契约](../specs/session-commands.md)，完成步骤见 [实施计划](../plans/2026-10-12-g4a2-implementation.md)。

## 改动与使用

- app 提供公共命令目录、严格解析、可信 Scope、结构化回执和候选来源接口。管理、预览和指令错误不调用模型，不进入成功上下文；`//` 转义普通正文。
- store 迁移 v1→v2，保留旧 local 历史；先按权限域和规范工作区过滤再解析短 ID。绑定、命名、收据同事务，核查确认后再同步内存。取消重投固定首次 run 或无目标，不能取消后来任务。
- CLI 固定启动配置快照与新建模型基准：裸启动 default_model、显式 `--model`、`-c` 恢复所选引用。之后恢复其他兼容历史不改变新建基准，工具许可采用当前快照，读取证明重新建立。
- TUI 提供建议、帮助/状态/模型面板、会话选择/预览与分页；Enter 使用实际输入，Tab 补全或预览，Esc 关闭展示，Ctrl+C 取消运行，Ctrl+N 复用 `/new`。成功才替换会话，迟到事件按会话/run 身份及展示代际过滤。
- 未命名无 run 空会话只驻内存、不列目录；手工命名或首个 run 起点才落盘。关闭历史与 offline 使用内存后端。无新增 Go 依赖，配置 schema 保持 v1，`sessions.page_size` 用于目录和历史分页。
- fake gateway 验证身份隔离、固定目标取消、收据重放及显示边界；没有真实平台收发或 `plume gateway` 命令。

在 TUI 输入 `/help`、`/new 临时讨论`、`/rename 方案复核`、`/sessions`、`/resume [id]`、`/history [page] [--session id]`、`/status`、只读 `/model`、`/cancel`（`/stop`）。TUI 另支持 `/clear`、`/exit`（`/quit`）。选择器用 Up/Down、Tab、Enter、PgUp/PgDn、Esc；预览 Ctrl+O 展开 Thought/工具摘要。

## 提交前修复与复审

1. 首条正文恰为默认标题或空白时，后续正文曾再次修改内存标题，造成 `/status` 与持久目录分叉。以首次已登记 run 状态固定标题，失败 run 同样计入；起点保存失败回滚该状态，恢复按 RunCount 重建。回归同时核对两种后端、目录和状态，覆盖手工命名、新建与恢复。
2. SDK 在响应头前取消时，归一化 `model.Error` 曾丢失标准错误身份，数据库和 trace 误记 failed。新增 `Error.Is` 保留取消/期限身份，不暴露 SDK 原始错误；真实 HTTP 回归验证 cancelled、零成功 turns、独立的 saved 状态。timeout 与用户取消分别匹配标准错误。
3. 三项异步 TUI 回归的夹具使用非法 ID、遗漏默认 session 缓存聚合。修正夹具后通过；临时撤去迟到回执保护及恢复提示处理时三项均失败，恢复保护后三项均通过，确认断言守住真实行为。
4. 独立规格与交叉复审检查了作用域、绑定/收据事务、固定目标取消、提交核查、运行时释放及远端显示边界。前轮旧运行时句柄积累、取消确认未知后的补发和远端内部路径问题已修复；本轮标题与取消修复也完成交叉复审。

相关测试：`internal/app/command_title_test.go`、`cancel_http_regression_test.go`、`command_gateway_test.go`、`command_recovery_test.go`、`internal/model/errors_test.go`、`internal/tui/command_async_regression_test.go` 与 store 管理/迁移测试。

## 正式验证

2026-10-23 于 darwin/arm64、Go 1.27.1 执行。测试数为 RTK 含子测试汇总，不作为质量提升指标；旧开发轮次计数不再作为正式结论。

| 验证 | 实际结果 |
| --- | --- |
| `rtk go test ./...` | 1638 项通过，17 个包 |
| `rtk go test -race ./...` | 1638 项通过，17 个包 |
| `rtk go vet ./...` | 退出 0，无输出 |
| `rtk proxy go tool golangci-lint run` | 0 issues |
| `rtk proxy gofmt -l cmd internal` / `rtk git diff --check` | 均无输出 |
| `rtk proxy ./scripts/build.sh` | v0.1.0 / 97568ec-dirty / 2026-10-23T09:21:50Z；未安装到 GOBIN |
| SQLite 迁移、同域查询、事务确认与收据反例 | 全仓测试覆盖并通过 |
| fake gateway 隔离、重放、固定取消与显示边界 | 全仓测试覆盖并通过 |
| G4a.2 本地 HTTP fixture + 隔离 PTY | 6/6 场景通过 |
| G4a.1 持久历史 PTY 回归 | 6/6 场景通过 |
| 路线图数据范围检查 | 仅元数据和 ROADMAP 数据变化，渲染器与 HEAD 一致 |
| 路线图浏览器显示 | N/A；浏览器列表为空，本机 Chrome 自动化调用超时 |

PTY 使用 Python 3.9.6、pyte 0.8.2、wcwidth 0.2.13，TERM=xterm-256color、COLORTERM=truecolor，窗口覆盖 100×32、48×20、100×30、120×36。版本与 SHA256 记录在临时 results.json；这些结果来自本地 fixture，不代表真实供应商性能。

| 场景 | 实际核查 |
| --- | --- |
| 命令、选择器与恢复 | 未知/非法参数/不存在 ID 为结构化错误且模型调用 0；中文命名、空会话目录、完整上下文恢复与 `//` 转义通过 |
| 历史分页 | page_size=2，3 个 run；48×20 窗口新旧页顺序正确 |
| 运行中只读与取消 | `/cancel`、状态面板内 Ctrl+C 两次 run 均 cancelled，成功 turns=0 |
| 内存与 offline | 禁用历史不建库；offline 不读真实配置，显式演示仍可运行 |
| 启动模型基准 | 裸启动、`-c`、显式 `--model` 的请求模型序列 B,A,B,A,A,B；管理额外模型调用 0 |
| 不兼容历史 | 可预览；恢复报 resume_incompatible，原会话、绑定、转录、草稿保留，额外模型调用 0 |
| G4a.1 六场景 | 持久恢复、引用匹配、Ctrl+N、崩溃 interrupted/usage NULL、第二进程锁及参数拒绝均通过 |

成功 trace 摘要：`model_completed` → `run_ended(run_status=completed, save_status=saved)`。取消路径：`model_failed(error_code=cancelled)` → `run_ended(run_status=cancelled, save_status=saved)`。七份 trace 摘录合计 run_started/run_ended 各 17、model_start 23、model_completed 21、model_failed 2、tool_started/tool_completed 各 6；15 个 run 完成、2 个取消、17 个保存成功，生命周期闭合。管理错误不制造模型 span。

## 复现与证据治理

```bash
rtk go test ./...
rtk go test -race ./...
rtk go vet ./...
rtk proxy go tool golangci-lint run
rtk proxy gofmt -l cmd internal
rtk git diff --check
rtk proxy ./scripts/build.sh
rtk proxy go test ./internal/app -run '^$' -bench BenchmarkSessionCommandsSQLite -benchmem -benchtime=200ms -count=1

rtk proxy python3 -m pip install --target /tmp/plume-g4a2-pty-deps pyte==0.8.2 wcwidth==0.2.13
rtk proxy env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-g4a2-pty-deps python3 scripts/pty_demo_g4a2.py --output /tmp/plume-g4a2-final-pty
rtk proxy env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-g4a2-pty-deps python3 scripts/pty_demo_g4a1.py --output /tmp/plume-g4a1-regression-pty
```

本轮临时结果位于 `/tmp/plume-g4a2-final-pty/{results,verification-summary}.json` 和 `/tmp/plume-g4a1-regression-pty/results.json`；帧与 trace 同目录。临时文件不保证长期存在，复现入口是仓库脚本。预跑的 `docs/reviews/evidence/G4a.2/` 按 [通过即删规则](../runbooks/tui.md#证据治理通过即删) 随通过提交删除，不将临时录像加入仓库。

## 指标与限制

基线只报绝对值，变化均 N/A；测量口径见 [指标协议](../specs/metrics.md)。

| 指标 | 本轮绝对值与条件 | 变化 |
| --- | --- | --- |
| PTY 场景成功率 | G4a.2 6/6，G4a.1 回归 6/6 | N/A |
| 管理额外模型/工具调用 | 场景和 fake 单测断言 0；普通正文及显式 offline 演示单独计数 | N/A |
| SQLite 新建管理开销 | 543771 ns/op、39951 B/op、549 allocs/op；484 次 | N/A |
| SQLite 恢复管理开销 | 585561 ns/op、52379 B/op、834 allocs/op；429 次 | N/A |
| Trace 生命周期完整性 | 本轮摘录 17/17 run、23/23 model、6/6 tool 闭合；不代表故障下无丢失 | N/A |
| 取消响应分位数、首答案/完成、采集开销 | N/A，未做独立计时实验 | N/A |
| Token、费用、真实渠道投递、记忆与技能 | N/A；fixture 计数不充当真实计费或平台效果 | N/A |

SQLite benchmark 在 Apple M5 Pro/darwin-arm64、Go 1.27.1、benchtime=200ms、count=1 下运行，包含真实 SQLite 事务与工作区运行时装配；仅单次本地样本，不给 p95 或提升结论。

当前仍无真实 gateway、平台鉴权、inbox/outbox 或跨进程会话所有权；fake 收据不证明平台 exactly-once。`/model` 只读，不能迁移同一会话模型；配置不热加载，人格在下一 run 重新读取。恢复超出字节/记录限制会拒绝，没有自动压缩或长期记忆；CompletionProvider 没有动态 MCP/skill 加载器。

真实终端手感、kitty Shift+Enter、Windows/Linux 实机验证仍为 N/A；人工中文窄屏、补全、草稿与统计体验可后续补验。该限制不被自动 PTY 或本次提交授权抹去。
