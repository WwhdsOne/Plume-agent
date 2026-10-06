---
title: AGENTS.md
status: active
updated: 2026-10-06
summary: 仓库长期快照：语言约定、审核制度、架构边界、codegraph MCP、依赖与文档维护规则；动手前必读
---

# AGENTS.md

This file provides guidance to CodeBuddy Code when working with code in this repository.

## 语言：中文

- 所有回复、解释、代码注释、commit message 均使用中文。
- 变量名、函数名等遵循项目约定，可保留英文。
- 代码注释使用中文，除非项目已有英文注释惯例。
- 错误信息和日志的解读用中文说明。
- 代码内的错误字符串、日志字段名与 CLI 输出保持英文（Go 惯例，便于 grep 与测试断言）；给用户的解释用中文。

## 项目定位

`herald-agent`：用 Go 构建的个人 Agent，首版入口是 **TUI 聊天界面**，不是微信登录。模型层传输与协议解析采用 **OpenAI 官方 Go SDK（openai-go）**，其上以协议适配器归一化（为未来 Anthropic/Gemini 协议预留同一接口），Agent 循环自研，不引入 Eino。微信 iLink Bot、飞书、QQ 属于后续渠道扩展。目标是展示完整执行链路与可复现的量化改造收益。

上述是 2026-10-06 更新的目标架构；当前代码仅完成 G0/G1a。实际实现与设计差异见「当前进度与禁区」，以 `docs/decisions/0003-model-runtime.md` 为新路线依据。

模块名是 `herald-agent`，CLI 命令名是 `herald`。

## 常用命令

```bash
go build ./...                 # 编译全部
go vet ./...                   # 静态检查
gofmt -l .                     # 列出未格式化文件（应为空）
go test ./...                  # 全部测试
go test -race ./...            # 竞态检测；提交前应通过
go test ./internal/config -run TestSaveFailureKeepsExistingConfig   # 单个测试
go test ./internal/config -v                                        # 单包详细输出

./scripts/build.sh             # 构建带版本信息的 ./herald
./scripts/build.sh --install   # 安装到 GOBIN
./scripts/build.sh --debug     # 保留符号表（默认为发布式 -s -w）

go run ./cmd/herald setup              # 首次设置向导（需要交互式终端）
go run ./cmd/herald config path
go run ./cmd/herald config show        # 脱敏输出
go run ./cmd/herald version
```

- Go 1.27.1，`GOPROXY=https://goproxy.cn,direct`。
- 阶段计划中的验收命令写作 `rtk go test ./...`；`rtk` 是本地命令包装，底层就是上面的 `go` 命令。

### 构建与版本注入

**发布/安装一律走 `scripts/build.sh`，不要用裸 `go build`**——裸构建不会注入版本，`herald version` 会显示 `dev/unknown`。

- 版本号来自仓库根的 `VERSION` 文件（人工维护的语义版本），commit 取自 `git rev-parse --short HEAD`（工作区脏则加 `-dirty`），构建时间取当前 UTC。
- 三个值经 `-ldflags -X main.{version,commit,buildTime}` 注入 `cmd/herald/version.go`；变量默认值是 `dev`/`none`/`unknown`，未打戳时**如实显示**，不伪造版本号。
- 默认加 `-s -w`：约 6.75 MB；不加约 9.75 MB。
- 仓库根没有 git tag，所以 `git describe` 不可用——这是选 `VERSION` 文件而非 tag 驱动的原因。

### 验证交互式向导

`herald setup` 需要 TTY，非交互环境会直接报错退出（这是刻意行为，不是缺陷）。要在脚本里跑通完整流程，需要分配 pty **并应答终端能力查询**（`ESC]11;?`、`ESC[6n`），否则 termenv 会超时报错：

```
OSC 11 背景色查询 -> 回 \x1b]11;rgb:0000/0000/0000\x1b\\
ESC[6n 光标位置   -> 回 \x1b[1;1R
```

向导流程本身可在无 TTY 下测试：`internal/setup` 只依赖 `Prompter` 接口，`wizard_test.go` 用脚本化实现覆盖全部路径。

### 开发时不要污染真实配置

配置目录默认是 `~/.herald`。测试通过 `t.Setenv("HERALD_HOME", t.TempDir())` 隔离；手工试验时同样设置 `HERALD_HOME` 指向临时目录：

```bash
HERALD_HOME=$(mktemp -d) go run ./cmd/herald config show
```

### codegraph MCP（代码图谱，2026-10-06 接入）

`codegraph`（`~/.local/bin/codegraph`）作为 CodeBuddy 的 MCP server 使用，提供单个工具 `codegraph_explore`：一次调用返回相关符号的**逐行源码 + 调用路径 + 影响面**，用于替代 grep + Read 循环。

**配置位置在用户级，不在本仓库**：`~/.codebuddy/.mcp.json`（`mcpServers.codegraph` = `codegraph serve --mcp`，stdio）。本仓库**不**提交 `.mcp.json`，因为 `codegraph install` 的目标 agent 名单是硬编码的（Claude Code / Cursor / Codex CLI / opencode / Hermes / Gemini CLI / Antigravity / Kiro / Copilot），不含 CodeBuddy，只能手写配置。

```bash
codegraph init          # 建索引（当前项目 20 文件 / 322 节点 / 823 边）
codegraph status        # 看索引状态
codegraph sync          # 增量更新；serve 进程自带 watcher，会自动 sync
codegraph uninit        # 删 .codegraph/
```

- 索引落在 `.codegraph/`（SQLite，本项目约 1.2 MB），已列入 `.gitignore`；它**不是**仓库的产物，不要提交。
- 跨项目查询用 `projectPath` 参数指定另一个已建索引的仓库，不必全局安装。
- 新会话里 MCP server 需要重启 CodeBuddy 才会加载。
- `codebuddy mcp add-json --scope user …` 实测会挂起无输出（疑交互模式），改用直接编辑 `~/.codebuddy/.mcp.json`。

## 审核制度（重要，先读再动手）

本仓库**不是**"想改就改"的代码库，而是按审核单元推进的阶段项目。动代码前必须读 `docs/phase-01-tui-agent.md`（阶段计划）与 `docs/decisions/0003-model-runtime.md`（模型与入口决策）。

1. 每个审核单元开始前，先说明：本单元目标、拟改文件、运行方式、验收条件。
2. 实现一个能演示的小闭环，并针对会话隔离、幂等、重试、计算、评测统计等关键行为写测试。
3. 展示可复现命令、实际结果、成功与失败的 trace 证据。
4. 交付 `docs/reviews/Gx.md`（改动说明、证据路径、指标表、局限、待用户检查项）。
5. **完成即停下**，等用户回"通过 Gx / 继续下一部分"才推进下一单元。
6. 单元不可在未获同意时合并；若一个单元过大，拆成更小的可运行子单元逐个审核。

实施顺序：已通过 `G0` / `G1a` → `G1b.1`（OpenAI SDK 非流式请求与模型接口）→ `G1b.2`（最小 TUI 与会话）→ `G1b.3`（流式消费与增量展示）→ `G3`（自研工具循环）。每项独立审核，完成这些才是首版里程碑。

之后另行授权第二阶段：`G2a.1`（扫码登录）→ `G2a.2`（真实收发）→ `G2b`（可靠性）。保留原编号含义，G3 前移，不按数字自动推进。`G4a/G4b/G5/G6` 作为后续记忆、压缩、技能与实验路线储备。没有微信配置不得阻塞未来 TUI 启动。

指标与比较协议见阶段计划 §7：基线阶段只报绝对值、变化列 N/A，不得为了填表编造"提升"；数据缺失记 `N/A`/`unknown`，不得当 0。

## 文档维护：必须更新 AGENTS.md 的情形

**AGENTS.md 是本仓库的长期快照，不是一次性文档。发生下列"重大变化"时必须更新它，且与触发它的改动放在同一次提交里——不要留到以后补。**

必须更新：

- 引入或移除外部依赖（例如 G1a-2 引入 `go.uber.org/zap`、G1b.1 计划引入 openai-go）。
- 新增、删除或重命名包目录；或改变某个目录的职责边界。
- 配置 schema 变化（`SchemaVersion` 递增、字段增删、默认值语义变化）。
- 新增或改变用户可见的命令、参数、CLI 输出约定。
- "当前进度与禁区"里的条目状态翻转（某能力从"尚未实现"变为已实现，或反过来）。
- 改变既有约定：语言、日志/序列化库、持久化做法、配置目录解析规则、审核编号或推进规则。
- 阶段计划或 `docs/decisions/` 出现与本文件冲突的结论（**以决策文档为准，并立刻回来修正本文件**）。

不必更新（写进当日日志即可）：

- 单个审核单元内部的实现细节、测试增补、bug 修复。
- 已被 `docs/` 其他文档覆盖的细节。

### 文档头规范（frontmatter，2026-10-06 确立）

**所有 Markdown 文档（`AGENTS.md` 与 `docs/**/*.md`）必须以 YAML frontmatter 开头**，作为机器检索层；正文的人读头部（历史声明、关联链接）保留不变，两者职责不同：frontmatter 给 agent/工具 grep，正文给人读背景。

```yaml
---
title: 文档标题（与 H1 一致）
status: 状态枚举，见下表
updated: YYYY-MM-DD（最后一次实质修改日期）
summary: 一句话简介，不超过 80 字，说明这份文档是什么、解决什么
---
```

`status` 枚举（只能取这些值，便于 `grep -r '^status:'` 检索）：

| 值 | 含义 | 适用 |
| --- | --- | --- |
| `active` | 现行有效，按它工作 | AGENTS.md、阶段计划、runbook、现行决策 |
| `superseded-part` | 部分内容已被后续决策替代，正文须标明哪部分仍有效 | 被部分替代的决策（如 0001） |
| `historical` | 仅历史记录，不得当作当前指令；使用前须重新核对 | 调研快照、被完全替代的方案 |
| `passed` | 审核单元已通过 | docs/reviews/ |
| `pending-review` | 已交付，待用户审核 | docs/reviews/ |
| `log` | 流水记录，按时间追加 | docs/daily/ |

规则：

- **新建文档必须带头**；实质修改文档时同步更新 `updated`；`status` 翻转与其他维护纪律同批提交，不留到以后补。
- `summary` 只写一行，优先包含检索关键词（单元编号、主题），不写修辞。
- `docs/roadmap.html` 不是 Markdown，用文件顶部的 HTML 注释携带同样的 `title/status/updated/summary` 四字段。
- frontmatter 是检索入口，不替代正文的详细声明；两者冲突时以正文为准并立刻修正 frontmatter。

### 路线图（docs/roadmap.html）

`docs/roadmap.html` 是**数据驱动的单文件树状路线图**（浏览器直接打开，无外部依赖）：G0→G1a→G1b.1/.2/.3→G3 主干、第二阶段 G2a/G2b、储备 G4a/G4b/G5/G6，每个单元标注状态与日期。

**维护规则（2026-10-06 确立）：**

- 任何审核单元**状态翻转**（未开始→交付待审核→通过；或后移/取消）时，必须同步更新 roadmap.html 中 `ROADMAP` 数据的 `status` 与 `updated` 字段，**并与触发它的改动放在同一次提交里**。
- 新增审核单元（如拆分子单元、新阶段立项）时，同步在对应 stage 的 `nodes`/`branches` 里补条目。
- 状态口径以 `docs/reviews/` 与本文件「当前进度与禁区」为准；roadmap.html 是可视化快照，冲突时以文本记录为准，发现不一致立即修正。
- 只改 `ROADMAP` 数据区，不要顺手改渲染代码；改完用浏览器打开确认显示正常。

### 每日变更总结

`docs/daily/YYYY-MM-DD.md` 按天记录当天实际发生的变化：完成的审核单元、决策变更、代码与文档增删、遗留项。它是**流水**，不是快照；AGENTS.md 从这些流水里提炼出仍然成立的事实。

写法：当天有实质改动就追加一条；一天内多次改动合并进同一个文件，不要按会话拆。没有改动的日子不必写空文件。

## 架构

### 目录职责（边界即设计）

| 目录 | 职责与边界 |
| --- | --- |
| `cmd/herald/` | CLI 入口与命令分发；不承载业务逻辑 |
| `internal/config/` | 配置 schema、校验、原子持久化、凭据引用；不承载 Agent 逻辑 |
| `internal/setup/` | 首次设置向导的流程；只依赖 `Prompter` 接口，不依赖终端库 |
| `internal/provider/` | 预设、ModelFactory 装配适配器、显式 Probe、凭据解析接口；保持零依赖（不 import config） |
| `internal/model/` | G1b.1 已建：自有契约（消息/请求/响应/流/错误/能力）、endpoint 安全装配、openai-chat-completions 适配器（openai-go SDK）、deepseek 组合适配器、脚本化 fake |
| `internal/tui/` | G1b.2 已建：Bubble Tea 聊天界面（记录/输入/状态栏、控制序列净化）；不发送模型 HTTP、不执行工具，副作用经 Hooks 注入 |
| `internal/channel/` | 现有渠道注册表；第二阶段才实现消息适配器，不拼装 prompt |
| `internal/app/` | G1b.2 已建：会话历史、run 生命周期（串行、取消、有界事件流）；后续扩展持久化与渠道调度 |
| `internal/agent/` | G1b.2 已建最小单轮（历史+输入→Generate）；G3 扩展工具循环、prompt 版本与预算 |
| `internal/telemetry/` | setup trace 与模型调用 trace（G1b.1 ModelRecorder）；后续增加应用/工具事件与 run 关联 |
| `internal/eval/` | G1b.1 已建：12 个离线种子数据集（`eval/datasets/smoke.v1.jsonl`）与运行器；后续评分、统计、比较报告 |

### 三个必须理解的设计点

**1. 预设品牌、协议与模型能力分开。** `ModelConfig.Provider` 是品牌（`deepseek` / `custom-openai`）。为兼容现有 schema v1，`Protocol` 暂保留历史适配选择器值（`deepseek` / `openai-compatible`）；G1b.1 工厂将它们映射到自研 DeepSeek/兼容适配器及内部 `openai-chat-completions` 协议，不改用户文件。未来协议语义/字段变化单独审核迁移。一个协议可服务多家供应商，不能仅凭换 Base URL 宣称支持任意模型。原 Eino 路径字段 `Preset.Component` 已于 2026-10-06 随路线切换移除（含过时注释与测试断言）。

**2. `internal/config` 通过小接口做校验，不 import `provider`/`channel`。** 见 `config.go` 的 `ProviderCatalog`/`ChannelCatalog`。此 seam 隔离预设校验与未来模型 SDK/协议实现，让配置测试不需要联网。**新增校验沿用这条约定；模型消费接口不暴露 openai-go/TUI 类型。**

**3. 配置里永不出现密钥值。** `~/.herald/config.json` 只存 `api_key_ref` 这类引用；密钥本身在 `~/.herald/credentials/`（目录 0700、文件 0600）。`config show` 只输出"已设置/未设置 (ref: …)"。token、二维码内容、登录 URL 同样不得进入日志或 trace。

### 配置目录解析

`HERALD_HOME`（展开 `~` 与 `$VAR`）→ 否则 POSIX `~/.herald` → 否则 Windows `%LOCALAPPDATA%\herald`。

**不使用 `os.UserConfigDir()`**：macOS 上它落到 `~/Library/Application Support`（含空格，且与 Hermes/Codex 的约定的家目录点目录不一致）。Hermes/Codex 分别用 `~/.hermes`(`HERMES_HOME`)、`~/.codex`(`CODEX_HOME`)。

目录布局：

```text
~/.herald/
  config.json          # 含 schema_version，非敏感
  credentials/         # 0700，文件 0600
  logs/                # 0700；setup.jsonl 等结构化 trace，文件 0600
```

### 写配置的一致做法

`Save` 走 `writeFileAtomic`：同目录临时文件 → `Chmod` → `Write` → `Sync` → `Close` → `Rename` → fsync 目录。任何早期返回都 `defer os.Remove(tmp)`。**新增持久化不要直接用 `os.WriteFile` 覆盖目标文件**，否则会丢掉"写入失败保留原配置"这条保证。

`EnsureCredentialsDir` 在目录权限过宽时只**告警**，不静默收紧（`TestEnsureCredentialsDirWarnsButDoesNotTighten` 守住这条）。

### 日志与 CLI 输出

- **日志统一用 `go.uber.org/zap`**（2026-10-06 确定，G1a-2 引入 `v1.28.0`）。`internal/telemetry` 用它写 JSON Lines trace；`SetupRecorder` 的方法**刻意不接受密钥参数**，脱敏靠类型签名而不是靠调用方自觉。
- **CLI 面向用户的输出继续用 `fmt`**：usage、`config show`、错误提示需要人类可读的对齐文本，而 zap 会往 sink 写 JSON。两者的分工是"给人看"与"给机器看"，不是新旧关系——不要为了统一把手面向用户的输出也改成结构化日志。

### 命令行层

- CLI 用 `github.com/spf13/cobra`（G1a-2 引入 `v1.10.2`）。命令构造集中在 `cmd/herald/{main,setup,config}.go`，每个命令一个 `newXxxCmd()`；`SilenceErrors`/`SilenceUsage` 都开着，错误只由 `main` 打印一次。
- **业务逻辑不写进 cobra 的 `RunE`**：`RunE` 只做参数取值与转发（`cmd.InOrStdin()` / `cmd.OutOrStdout()`），实现留在 `internal/`。

### 交互层

- **设置表单**用 `github.com/charmbracelet/huh`（G1a-2 引入 `v1.0.0`），只出现在 `cmd/herald/prompter.go`；不能把现有表单称为聊天 TUI。
- **聊天 TUI（G1b.2 已实现）**用 Bubble Tea（bubbles v0.21.1 连带锁定 bubbletea v1.3.10、lipgloss v1.1.0，与 huh 共存已由全量测试验证）。`internal/tui` 的 Model/Update 是纯状态转移，副作用只经 `Hooks`（Submit/Cancel/ResetSession）注入；`TeaModel` 是到 tea.Model 的适配层（指针接收者，`tea.NewProgram(&tui.TeaModel{...})` 传指针）。普通日志/trace 写文件，不破坏屏幕；模型输出经 `sanitize` 过滤终端控制序列后才渲染。
- 未来裸 `herald` 默认进入 TUI，缺配置提示 setup；`herald chat --offline` 不需要配置/Key。setup 计划增加「暂不接入渠道」，不伪造渠道账号、不要求扫码、不清理旧渠道。**G1b.2 已实现以上行为（待审核）**：裸 `herald` 配置就绪+TTY 直接进入聊天。
- **向导流程与终端库分离**：`internal/setup` 只依赖 `Prompter` 接口，新增一步交互时先加接口方法，再在 `prompter.go` 实现，不要把 huh 的类型渗进 `internal/setup`。
- **Base URL 不再逐次询问**（2026-10-06）：有预填默认值的预设直接跳过；只有无默认值的预设才问。已有配置里的地址与默认值不同时**原样保留**，不要"顺手"重置——`TestWizardPreservesExistingNonDefaultBaseURL` 守住这条。
- **模型走列表选择**：模型 ID 来自 `provider.Preset.Models`，列表末尾附"自定义…"才落到文本输入。新增预设时把候选模型写进 `Models`。
- 没有 TTY 时**先判断再退出**，不要进入 huh 让它阻塞。判定必须用 `mattn/go-isatty` 的 `IsTerminal`／`IsCygwinTerminal`，**不要用 `os.ModeCharDevice`**——`/dev/null` 也是字符设备，用它判断会让 `herald setup < /dev/null` 进入 huh 并挂住（已由 `TestSetupRefusesCharDeviceThatIsNotATerminal` 守住）。
- 在 pty 里跑向导必须应答终端能力查询，否则 termenv 超时退出（见「验证交互式向导」）。

## 文档地图

| 路径 | 内容 |
| --- | --- |
| `docs/phase-01-tui-agent.md` | 第一阶段：OpenAI SDK 模型调用、TUI、流式、自研工具循环、trace/指标及逐单元审核 |
| `docs/phase-02-channel-gateway.md` | 第二阶段草案：微信扫码/收发/可靠性；TUI 首版后另行授权 |
| `docs/decisions/0001-scope.md` | G0 历史范围；配置/预算继续有效，Eino 绑定由 0003 替代 |
| `docs/decisions/0002-wechat.md` | 保留 iLink 快照/契约/fixture，实际接入已后移 |
| `docs/decisions/0003-model-runtime.md` | 当前模型架构、设计模式、OpenAI SDK 传输、配置兼容和 TUI 优先决策 |
| `docs/reviews/G0.md` | G0 审核记录（已通过） |
| `docs/reviews/G1a.md` | G1a 审核记录（已通过） |
| `docs/reviews/` | 每个审核单元的交付证据 |
| `docs/daily/` | 每日变更流水（`YYYY-MM-DD.md`） |
| `docs/runbooks/setup.md` | 首次设置向导的启动、验证与故障复现 |
| `docs/roadmap.html` | 树状路线图：全部审核单元的状态快照（数据驱动，状态翻转时必须同步更新） |

## 当前进度与禁区

已完成：G0（计划与可行性，含 `docs/decisions/` 两份决策）、**G1a（首次设置向导与配置边界，已通过）**——含 `internal/config`、`internal/provider`、`internal/channel`、`internal/setup`、`internal/telemetry`、`cmd/herald` 的 cobra 命令树与 `herald setup`。**G1b.1（OpenAI SDK 非流式模型接口，已通过）**——含 `internal/model/`（契约、endpoint、openai/deepseek 适配器、fake）、`internal/provider` 工厂与 probe、`internal/telemetry` 模型 trace、`eval/datasets/smoke.v1.jsonl` 与 runner。**G1b.2（最小可交互 TUI 与多轮会话，已通过）**——含 `internal/tui`（Bubble Tea 聊天界面）、`internal/app`（会话与 run 生命周期）、`internal/agent`（最小单轮）、`internal/model` LoopFake、`herald chat`/裸 `herald` 入口、`--offline`、setup「暂不接入渠道」、README 与 TUI runbook。审核期间修复了模型名装配 bug（agent 绑定 API 模型名而非配置 ID，`buildRuntime` 测试守住）。

**尚未实现，不要假设存在**：

- **流式模型调用**（`Client.Stream` 当前一律返回 unsupported，G1b.3 启用）、**工具循环与工具声明**（请求携带 tools 返回 unsupported，G3）、cmd/eval 比较命令。
- 聊天 run 事件的落盘 trace（UI 当前消费内存事件）；模型独立就绪校验（启动只校验配置与凭据存在，不发探测请求）。
- `internal/tools/`、`internal/store/`。
- 微信扫码登录与收发；网关命令 `herald gateway …`（第二阶段 G2a）。向导里微信只记录为"待登录"。
- 根目录 `README.md` 已于 G1b.2 建立；`.claude/` 下**没有**规则文件。

### 外部依赖

`go.mod` 目前有**两组**直接依赖，均于 G1a-2 引入并验证（Go 1.27.1 / darwin-arm64 干净编译，`-s -w` 二进制增量实测）：

| 依赖 | 版本 | 用途 | 增量 |
| --- | --- | --- | --- |
| `go.uber.org/zap` | v1.28.0 | 结构化日志、setup trace | +3.48 MB |
| `github.com/charmbracelet/huh` | v1.0.0 | 终端交互（含 bubbletea/lipgloss 等传递依赖） | +1.60 MB |
| `github.com/spf13/cobra` | v1.10.2 | 命令行框架（含 pflag） | +0.96 MB |
| `github.com/openai/openai-go` | v1.12.0 | 模型传输与协议（G1b.1，Chat Completions 适配器） | +1.70 MB（`-s -w` 实测 6.41→8.11 MB） |
| `github.com/charmbracelet/bubbles` | v0.21.1 | TUI 组件（textarea/viewport；G1b.2 提升，连带 bubbletea v1.3.10、lipgloss v1.1.0） | +2.59 MB（实测 8.11→10.70 MB） |
| `github.com/mattn/go-isatty` | v0.0.20 | 判断 stdin/stdout 是否为真正的终端 | 可忽略 |

新增依赖应发生在对应单元，并记录版本锁定、兼容性和实测依赖增量。2026-10-06 的路线切换同步只移除了代码中的 Eino 元数据/注释（`Preset.Component` 等），没有安装依赖或变更 `go.mod`。**模型传输已拍板 OpenAI 官方 Go SDK（`github.com/openai/openai-go`，锁定 v1.12.0，2026-10-06 决策），替代早先的 Resty 方案（原 v2/v3 比较作废）；G1b.1 引入时实测依赖增量。SDK 自动重试必须显式禁用（默认 2 次），DeepSeek 与自定义兼容服务同走 `openai-chat-completions` 协议适配器。**

### 与需求文档的常见偏差

- 百炼/Qwen 与原生协议供应商**已明确延后**，`TestDeferredPresetsAreAbsent` 会拦截其意外回归。
- 计划里提到的 `herald gateway setup/start/status`、`cmd/eval` 都是**待实现**，不代表可用。
- 代码中的 Eino 注释与 `Preset.Component` 已于 2026-10-06 清理完毕；CLI 入口简介与 setup 渠道步骤已于 G1b.2 同步为 TUI 优先/可选跳过。不能据此恢复旧路线，也不能声称文档变更已经实现新行为。
- 根目录 `README.md` 已于 G1b.2 建立；`.claude/` 下**没有**规则文件。
