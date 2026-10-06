# AGENTS.md

This file provides guidance to CodeBuddy Code when working with code in this repository.

## 语言：中文

- 所有回复、解释、代码注释、commit message 均使用中文。
- 变量名、函数名等遵循项目约定，可保留英文。
- 代码注释使用中文，除非项目已有英文注释惯例。
- 错误信息和日志的解读用中文说明。
- 代码内的错误字符串、日志字段名与 CLI 输出保持英文（Go 惯例，便于 grep 与测试断言）；给用户的解释用中文。

## 项目定位

`herald-agent`：用 Go + Eino 构建的个人 Agent，首个真实入口是**微信 iLink Bot 扫码网关**。目标是形成可展示完整执行链路与量化改造收益的项目。

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

## 审核制度（重要，先读再动手）

本仓库**不是**"想改就改"的代码库，而是按审核单元推进的阶段项目。动代码前必须读 `docs/phase-01-weixin-agent.md`（阶段计划）。

1. 每个审核单元开始前，先说明：本单元目标、拟改文件、运行方式、验收条件。
2. 实现一个能演示的小闭环，并针对会话隔离、幂等、重试、计算、评测统计等关键行为写测试。
3. 展示可复现命令、实际结果、成功与失败的 trace 证据。
4. 交付 `docs/reviews/Gx.md`（改动说明、证据路径、指标表、局限、待用户检查项）。
5. **完成即停下**，等用户回"通过 Gx / 继续下一部分"才推进下一单元。
6. 单元不可在未获同意时合并；若一个单元过大，拆成更小的可运行子单元逐个审核。

单元编号：`G0`（计划与可行性）→ `G1a`（首次设置向导与配置边界）→ `G1b`（Eino 模型工厂、离线闭环、trace、评测种子）→ `G2a.1`（扫码登录）→ `G2a.2`（真实收发）→ `G2b`（消息可靠性与会话隔离）→ `G3` … `G6`。

指标与比较协议见阶段计划 §7：基线阶段只报绝对值、变化列 N/A，不得为了填表编造"提升"；数据缺失记 `N/A`/`unknown`，不得当 0。

## 文档维护：必须更新 AGENTS.md 的情形

**AGENTS.md 是本仓库的长期快照，不是一次性文档。发生下列"重大变化"时必须更新它，且与触发它的改动放在同一次提交里——不要留到以后补。**

必须更新：

- 引入或移除外部依赖（例如 G1a-2 引入 `go.uber.org/zap`、G1b 引入 Eino）。
- 新增、删除或重命名包目录；或改变某个目录的职责边界。
- 配置 schema 变化（`SchemaVersion` 递增、字段增删、默认值语义变化）。
- 新增或改变用户可见的命令、参数、CLI 输出约定。
- "当前进度与禁区"里的条目状态翻转（某能力从"尚未实现"变为已实现，或反过来）。
- 改变既有约定：语言、日志/序列化库、持久化做法、配置目录解析规则、审核编号或推进规则。
- 阶段计划或 `docs/decisions/` 出现与本文件冲突的结论（**以决策文档为准，并立刻回来修正本文件**）。

不必更新（写进当日日志即可）：

- 单个审核单元内部的实现细节、测试增补、bug 修复。
- 已被 `docs/` 其他文档覆盖的细节。

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
| `internal/provider/` | 模型供应商预设与（G1b 起）Eino 模型工厂；不依赖微信 |
| `internal/channel/` | 渠道注册表与（G2a 起）消息适配器；不拼装 prompt |
| `internal/app/` | run 生命周期、超时、去重、会话串行化、重试与回复状态（G2a 起） |
| `internal/agent/` | Eino Agent、prompt 版本、工具装配、循环预算（G1b 起） |
| `internal/telemetry/` | Eino callbacks 与应用 span、脱敏、指标（G1a-2 起） |
| `internal/eval/` | 评分、统计、比较报告（G1b/G6） |

### 三个必须理解的设计点

**1. `provider` 与 `protocol` 分开存。** `ModelConfig.Provider` 是预设品牌（`deepseek` / `custom-openai`），`ModelConfig.Protocol` 是 Eino 适配器 ID（`deepseek` / `openai-compatible`）。首批两家组件底层都是 OpenAI 兼容 HTTP，真正的区分点是适配器，不是 Base URL——不能仅凭"能换 Base URL"就宣称支持任意供应商。

**2. `internal/config` 通过小接口做校验，不 import `provider`/`channel`。** 见 `config.go` 的 `ProviderCatalog`/`ChannelCatalog`。原因是 G1b 会给 `internal/provider` 引入 Eino 依赖；若 config 直接依赖它，纯配置包会被迫拖进 Eino，测试也会变慢。**新增校验时请沿用这个 seam，不要为了省事 import 注册表。**

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

- 终端交互统一用 `github.com/charmbracelet/huh`（G1a-2 引入 `v1.0.0`），只出现在 `cmd/herald/prompter.go`。
- **向导流程与终端库分离**：`internal/setup` 只依赖 `Prompter` 接口，新增一步交互时先加接口方法，再在 `prompter.go` 实现，不要把 huh 的类型渗进 `internal/setup`。
- **Base URL 不再逐次询问**（2026-10-06）：有预填默认值的预设直接跳过；只有无默认值的预设才问。已有配置里的地址与默认值不同时**原样保留**，不要"顺手"重置——`TestWizardPreservesExistingNonDefaultBaseURL` 守住这条。
- **模型走列表选择**：模型 ID 来自 `provider.Preset.Models`，列表末尾附"自定义…"才落到文本输入。新增预设时把候选模型写进 `Models`。
- 没有 TTY 时**先判断再退出**，不要进入 huh 让它阻塞。判定必须用 `mattn/go-isatty` 的 `IsTerminal`／`IsCygwinTerminal`，**不要用 `os.ModeCharDevice`**——`/dev/null` 也是字符设备，用它判断会让 `herald setup < /dev/null` 进入 huh 并挂住（已由 `TestSetupRefusesCharDeviceThatIsNotATerminal` 守住）。
- 在 pty 里跑向导必须应答终端能力查询，否则 termenv 超时退出（见「验证交互式向导」）。

## 文档地图

| 路径 | 内容 |
| --- | --- |
| `docs/phase-01-weixin-agent.md` | 阶段计划：定位、架构选择、审核制度、trace 规范、指标协议、G0–G6 路线 |
| `docs/decisions/0001-scope.md` | 供应商预设（已冻结 2 家）、配置/凭据边界、评测预算、运行环境 |
| `docs/decisions/0002-wechat.md` | iLink 参考源码 blob SHA、4 个接口契约、首版边界、G2a fixture 设计 |
| `docs/reviews/G0.md` | G0 审核记录（已通过） |
| `docs/reviews/G1a.md` | G1a 审核记录（已通过） |
| `docs/reviews/` | 每个审核单元的交付证据 |
| `docs/daily/` | 每日变更流水（`YYYY-MM-DD.md`） |
| `docs/runbooks/setup.md` | 首次设置向导的启动、验证与故障复现 |

## 当前进度与禁区

已完成：G0（计划与可行性，含 `docs/decisions/` 两份决策）、**G1a（首次设置向导与配置边界，已通过）**——含 `internal/config`、`internal/provider`、`internal/channel`、`internal/setup`、`internal/telemetry`、`cmd/herald` 的 cobra 命令树与 `herald setup`。

**尚未实现，不要假设存在**：

- Eino、任何**模型调用**、任何**出站 HTTP 客户端**（`herald setup` 只做本地校验，联网连通性检查属于 G1b）。
- `internal/app/`、`internal/agent/`、`internal/store/`、`internal/eval/`。
- 微信扫码登录与收发；网关命令 `herald gateway …`（G2a）。向导里微信只记录为"待登录"。
- 根目录**没有** `README.md`，`.claude/` 下**没有**规则文件。

### 外部依赖

`go.mod` 目前有**两组**直接依赖，均于 G1a-2 引入并验证（Go 1.27.1 / darwin-arm64 干净编译，`-s -w` 二进制增量实测）：

| 依赖 | 版本 | 用途 | 增量 |
| --- | --- | --- | --- |
| `go.uber.org/zap` | v1.28.0 | 结构化日志、setup trace | +3.48 MB |
| `github.com/charmbracelet/huh` | v1.0.0 | 终端交互（含 bubbletea/lipgloss 等传递依赖） | +1.60 MB |
| `github.com/spf13/cobra` | v1.10.2 | 命令行框架（含 pflag） | +0.96 MB |
| `github.com/mattn/go-isatty` | v0.0.20 | 判断 stdin 是否为真正的终端 | 可忽略 |

新增依赖（Eino 等）应发生在对应单元，并在该单元记录版本锁定与兼容性验证。

### 与需求文档的常见偏差

- 百炼/Qwen 与原生协议供应商**已明确延后**，`TestDeferredPresetsAreAbsent` 会拦截其意外回归。
- 计划里提到的命令（`herald gateway setup/start/status`、`cmd/eval`）都是**待实现**，不代表可用。
