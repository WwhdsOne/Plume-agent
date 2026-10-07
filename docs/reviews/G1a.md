---
title: G1a 审核记录：首次设置向导与配置边界
status: passed
updated: 2026-10-07
summary: G1a 已通过（2026-10-06）：配置、凭据、向导、setup trace 的交付与验证证据
---

# G1a 审核记录：首次设置向导与配置边界

- 单元：G1a（拆为两个可独立审核的子单元：G1a-1 配置与注册表基础、G1a-2 向导）
- 状态：**已通过**（用户 2026-10-06 回复「G1a通过」）
- 日期：2026-10-06
- 证据：`docs/decisions/0001-scope.md`、`docs/decisions/0002-wechat.md`、`docs/daily/2026-10-06.md`、`docs/runbooks/setup.md`

> 历史审核记录：G1a 实现与测量结果保持不变。后续路线已改为自研运行时与 TUI 优先（模型传输用 OpenAI 官方 SDK），见 `docs/decisions/0003-model-runtime.md`。现有向导仍有渠道步骤，TUI 与“暂不接入渠道”尚未实现。

## 1. 改动说明

### G1a-1：配置与注册表基础

| 文件 | 内容 |
| --- | --- |
| `internal/config/config.go` | schema（`Config`/`ModelConfig`/`ChannelConfig`）、`Validate` 一次性报全部问题 |
| `internal/config/store.go` | `PLUME_HOME` → `~/.plume` → `%LOCALAPPDATA%\plume`；原子写（temp→fsync→rename→fsync dir） |
| `internal/config/credentials.go` | 凭据 0700/0600、ref 校验（不可越出目录）、增删查 |
| `internal/provider/registry.go` | 预设注册表，可 `Register` 扩展 |
| `internal/channel/registry.go` | 微信启用；飞书/QQ 标记待支持 |
| `cmd/plume/` | 入口从根目录 `main.go` 迁入 |

**关键设计**：`internal/config` 通过 `ProviderCatalog`/`ChannelCatalog` 两个小接口做校验，**不 import** `provider`/`channel`——避免 G1b 的 Eino 依赖污染纯配置包。

### G1a-2：向导 + 日志 + CLI

| 文件 | 内容 |
| --- | --- |
| `internal/setup/wizard.go` | 向导流程，只依赖 `Prompter` 接口，可在无 TTY 下完整测试；两阶段落盘（先模型含凭据，后渠道） |
| `internal/telemetry/events.go` | zap 版 setup trace；`SetupRecorder` 方法**不接受密钥参数** |
| `cmd/plume/prompter.go` | huh 实现 |
| `cmd/plume/setup.go` | 装配、TTY 判定、`~/.plume/logs/setup.jsonl` |
| `cmd/plume/{main,config,version}.go` | cobra 命令树 |

### 按用户反馈的四处返工（同日）

1. **模型改为选择**：`Preset.Models` 提供候选，列表末尾「自定义…」才落到文本输入。
2. **有默认 URL 就跳过**：只有无预填值的预设才问地址；**已有配置里非默认的地址原样保留**。
3. **setup trace 保留**：澄清其用途是计划 §7 的「首次配置指标」（配置成功率/失败原因/耗时），与速度、token 无关；用户选择维持。
4. **CLI 改用 cobra**，`RunE` 只做取值转发。

### 构建与版本注入

`VERSION` 文件 + `scripts/build.sh` + `-ldflags -X` 注入 `version`/`commit`/`buildTime`；未打戳时如实显示 `dev`/`none`/`unknown`。

## 2. 证据路径（可复现）

| 项 | 命令 | 结果 |
| --- | --- | --- |
| 格式化 | `gofmt -l .` | 无输出 |
| 静态检查 | `go vet ./...` | 通过 |
| 测试 | `go test ./...` | **77 passed / 6 packages** |
| 竞态 | `go test -race ./...` | **77 passed / 6 packages** |
| 真实安装 | `plume setup`（无 `PLUME_HOME`）+ `plume config show` | exit 0；见下 |
| 非 TTY | `plume setup < /dev/null` | 立即报错，exit 1，不创建任何文件 |
| 版本戳 | `./scripts/build.sh --install` → `plume version` | 打印 `v0.1.0 / commit / built` |

真实路径下的落盘（实测，之后已清理）：

```text
0700  dir   ~/.plume/
0600  file  ~/.plume/config.json
0700  dir   ~/.plume/credentials/
0600  file  ~/.plume/credentials/deepseek-default
0700  dir   ~/.plume/logs/
0600  file  ~/.plume/logs/setup.jsonl
```

pty 端到端（脚本化驱动真实 huh）事件链：

```text
setup_start → select_provider → resolve_base_url(source=preset_default) → select_model
→ input_credential → validate → store_credential → save_config
→ select_channel → save_channel → setup_completed
```

## 3. 指标表

基线阶段，只报绝对值，变化列 N/A（遵循阶段计划 §7）。数据缺失记 N/A，不当 0。

| 指标 | 值 | 变化 | 说明 |
| --- | --- | --- | --- |
| 单元测试数 | 77 | N/A | 6 个包 |
| 直接外部依赖 | 4 | N/A | zap / huh / cobra / isatty |
| 依赖体积增量 | zap +3.48 MB、huh +1.60 MB、cobra +0.96 MB | N/A | `-s -w` 实测，相对空二进制 1.20 MB |
| 打戳二进制体积 | 6,751,906 B | N/A | 默认 `-s -w` |
| 裸构建体积 | 9,747,010 B | N/A | 无 `-s -w` |
| 配置写入原子性 | 通过 | N/A | 写失败时原配置逐字节不变 |
| 凭据权限 | 0700 / 0600 | N/A | 真实路径实测 |
| 配置/trace 密钥泄漏 | 0 处 | N/A | 哨兵值扫描 |
| 非交互终端行为 | 立即报错退出 | N/A | 不阻塞、不写盘 |
| 首次配置成功率 | N/A | N/A | 无真实用户样本 |
| 配置恢复耗时 | N/A | N/A | 未单独计量 |

## 4. 局限

- **联网连通性检查未做**：向导只做本地校验，真实模型连通性属 G1b。
- **微信只是「待登录」**：扫码、收发是 G2a。
- **向导只建一个默认模型配置**：结构支持多命名配置，入口未做。
- **Base URL 无修改入口**：有默认值的预设向导不问地址（用户决定），只能手改 `config.json`。
- **setup trace 不服务性能指标**：它是「首次配置指标」的证据，别指望从中得到 token/延迟数据。
- **未做 Windows 实机验证**：目录解析、权限分支有代码但没在 Windows 上跑过。
- **「首次配置指标」没有样本**：成功率、恢复耗时、校验失败原因分布都记 N/A。
- **交互手感未经真人验证**：pty 演示是我脚本驱动的，↑/↓ 与配色需要你在真终端确认。

## 5. 需要用户检查的内容

- [ ] 在真终端跑一次 `plume setup`（或 `PLUME_HOME=$(mktemp -d) plume setup`），确认菜单手感与隐藏输入。
- [ ] 确认依赖取舍：zap +3.48 MB / huh +1.60 MB / cobra +0.96 MB。
- [ ] 确认 Base URL 跳过策略可接受（代价：改地址要手改 `config.json`）。
- [ ] 确认 `~/.plume` 的目录布局与权限符合预期。
- [ ] 回「通过 G1a」以关闭本单元。

## 6. 下一步

后续路线更新为 G1b.1：非流式模型请求（OpenAI 官方 SDK）、自有模型接口、trace 与 12 个离线种子；之后分别审核 G1b.2 TUI、G1b.3 流式、G3 工具循环。详见 `docs/phase-01-tui-agent.md`。这次变更不重新审核或重测已通过的 G1a。
