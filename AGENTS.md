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

go run ./cmd/herald version            # CLI
go run ./cmd/herald config path
go run ./cmd/herald config show        # 脱敏输出
```

- Go 1.27.1，`GOPROXY=https://goproxy.cn,direct`。
- 阶段计划中的验收命令写作 `rtk go test ./...`；`rtk` 是本地命令包装，底层就是上面的 `go` 命令。

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
```

### 写配置的一致做法

`Save` 走 `writeFileAtomic`：同目录临时文件 → `Chmod` → `Write` → `Sync` → `Close` → `Rename` → fsync 目录。任何早期返回都 `defer os.Remove(tmp)`。**新增持久化不要直接用 `os.WriteFile` 覆盖目标文件**，否则会丢掉"写入失败保留原配置"这条保证。

`EnsureCredentialsDir` 在目录权限过宽时只**告警**，不静默收紧（`TestEnsureCredentialsDirWarnsButDoesNotTighten` 守住这条）。

### 日志与 CLI 输出

- **日志统一用 `go.uber.org/zap`**（2026-10-06 确定）。它在 **G1a-2** 随 `internal/telemetry/` 与 setup trace 一起引入，是 `go.mod` 的第一个外部依赖，需在该单元记录版本锁定与兼容性验证。
- **CLI 面向用户的输出继续用 `fmt`**：usage、`config show`、错误提示需要人类可读的对齐文本，而 zap 会往 sink 写 JSON。两者的分工是"给人看"与"给机器看"，不是新旧关系——不要为了统一把手面向用户的输出也改成结构化日志。

## 文档地图

| 路径 | 内容 |
| --- | --- |
| `docs/phase-01-weixin-agent.md` | 阶段计划：定位、架构选择、审核制度、trace 规范、指标协议、G0–G6 路线 |
| `docs/decisions/0001-scope.md` | 供应商预设（已冻结 2 家）、配置/凭据边界、评测预算、运行环境 |
| `docs/decisions/0002-wechat.md` | iLink 参考源码 blob SHA、4 个接口契约、首版边界、G2a fixture 设计 |
| `docs/reviews/G0.md` | G0 审核记录（已通过） |
| `docs/reviews/` | 每个审核单元的交付证据（G1a 起） |
| `docs/daily/` | 每日变更流水（`YYYY-MM-DD.md`） |
| `docs/runbooks/` | 启动与故障复现（尚未创建） |

## 当前进度与禁区

已完成：G0（计划与可行性，含 `docs/decisions/` 两份决策）、G1a-1（`internal/config`、`internal/provider`、`internal/channel`、`cmd/herald` 的 `version`/`config` 命令）。

**尚未实现，不要假设存在**：

- `herald setup` 向导（G1a-2）——当前返回"planned for G1a-2"错误。
- Eino、任何模型调用、任何 HTTP 客户端、`internal/telemetry/`、`internal/app/`、`internal/agent/`、`internal/store/`、`internal/eval/`。
- 微信扫码、收发、网关命令 `herald gateway …`（G2a）。
- 根目录**没有** `README.md`，`.claude/` 下**没有**规则文件。

`go.mod` **目前没有任何外部依赖**，只有 `module herald-agent` 与 `go 1.27.1`。**第一个外部依赖是 `go.uber.org/zap`，在 G1a-2 引入**；此后新增依赖（Eino 等）应发生在对应单元，并在该单元记录版本锁定与兼容性验证。

### 与需求文档的常见偏差

- 百炼/Qwen 与原生协议供应商**已明确延后**，`TestDeferredPresetsAreAbsent` 会拦截其意外回归。
- 计划里提到的命令（`herald gateway setup/start/status`、`cmd/eval`）都是**待实现**，不代表可用。
