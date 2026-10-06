---
title: TUI 键位契约与平台适配（G1b.2.1）
status: active
updated: 2026-10-06
summary: 终端栈升级 charm.land v2 全家桶并重设计键位：对齐 Codex CLI/Claude Code 惯例，Shift+Enter 三层渐进，KeyMap 抽象支撑 Windows
---

# TUI 键位契约与平台适配（G1b.2.1）

- 日期：2026-10-06。触发：用户反馈 G1b.2 初版键位（Ctrl+J 换行、Ctrl+C 语义、无输入历史）不适配 Mac，要求模仿 Codex CLI 与 Claude Code；随后用户拍板**终端栈整体升级 v2**（此前文档基于 bubbletea v1.3.10 无 kitty 协议的约束撰写，v2 GA 后约束解除）。
- 范围：**文档先行**，用户确认契约后实现。实现包含两部分：先迁 v2（保持行为不变），再落键位契约，同单元一起审核。本文是键位与迁移的行为契约。
- 单元拆分：本单元覆盖「v2 迁移 + 键位重设计」；Windows 平台的**实测验证**留给 G-win 专门单元（v2 已提供原生键盘增强，实现侧无需平台分支）。

## 1. 参照工具的惯例（2026-10-06 复核）

| 键 | Codex CLI | Claude Code | 共识 |
| --- | --- | --- | --- |
| Esc | 中断当前 turn | 中断当前响应 | **中断** |
| Ctrl+C | 一次中断，两次退出 | 取消输入/生成（退出需两次或 Ctrl+D） | **一次中断/清空，两次退出** |
| Ctrl+D | 空提示符退出（或 /exit） | 退出会话（EOF） | **EOF 退出** |
| 多行 | 终端配置后组合键 | Shift/Option+Enter（终端支持时），`\`+Enter 续行兜底 | **渐进换行** |
| Shift+Tab | 循环 approval/auto-edit 模式 | 循环权限模式（normal/auto-accept/plan） | 模式系统预留，首版不绑定 |
| Ctrl+L | — | 清屏 | 重绘/清屏 |
| Up/Down | 任务内导航 | 输入历史导航 | **输入历史** |
| 斜杠命令 | /exit /quit 等 | /clear 等 | 首版不建框架，预留 |

来源：[Claude Code Interactive mode 官方文档](https://code.claude.com)、[Claude Code cheatsheet](https://support.claude.com)、[Codex CLI Cheat Sheet](https://computingforgeeks.com/codex-cli-cheat-sheet/)、[Codex CLI shortcuts（版本敏感）](https://github.com/ashokmanohar-ai/openai-codex-complete-guide/blob/main/docs/03-cli/shortcuts.md)。

## 2. 终端栈升级 v2（迁移计划）

### 2.1 版本与模块路径

全家桶 v2 均已 GA（2026-10-06 检查：bubbletea `v2.0.10`、bubbles `v2.2.1`、lipgloss `v2.0.6`、huh `v2.0.3`）。**模块路径统一迁移到 vanity 域名 `charm.land`**：

| 依赖 | v1（当前，迁移后移除） | v2（目标） |
| --- | --- | --- |
| bubbletea | `github.com/charmbracelet/bubbletea` v1.3.10 | `charm.land/bubbletea/v2` v2.0.10 |
| bubbles | `github.com/charmbracelet/bubbles` v0.21.1 | `charm.land/bubbles/v2` v2.2.1 |
| lipgloss | `github.com/charmbracelet/lipgloss` v1.1.0 | `charm.land/lipgloss/v2` v2.0.6 |
| huh | `github.com/charmbracelet/huh` v1.0.0 | `charm.land/huh/v2` v2.0.3 |

huh **同步迁 v2**，不留 v1/v2 两套渲染栈并存（并存虽可行但重复占体积，且后续维护双份）。锁具体版本入 `go.mod/go.sum`，依赖增量实测记入审核文档。

### 2.2 API 迁移面（已逐项核实）

| 变化 | v1 | v2 | 影响文件 |
| --- | --- | --- | --- |
| KeyMsg 接口化 | `KeyMsg` 结构体（`Type/Runes/Alt`） | 接口：`Key() Key` + `String()`；`Key{Code, Mod, Text}` | `internal/tui/update.go` 全部按键判定 |
| View 类型 | `View() string` | `View() tea.View` | `internal/tui/view.go`、`TeaModel` |
| viewport 构造 | `viewport.New(w, h)` | `viewport.New(opts ...Option)` | `internal/tui/update.go` resize |
| textarea | 同构 | `charm.land/bubbles/v2/textarea`（SetValue/Value/Focus/InsertString/Reset 保留） | 低风险 |
| huh | v1 API | v2 同构（NewForm/NewGroup/NewSelect/ErrUserAborted 保留） | `cmd/herald/prompter.go` 小改 |
| Model 接口 | `Update(Msg) (Model, Cmd)` | 不变 | 无 |
| 键盘增强 | 无（kitty protocol 不支持） | 内置自动协商（kitty + Windows Console API），`KeyboardEnhancementsMsg` 可感知协商结果 | 新能力 |

### 2.3 迁移顺序（同单元两步）

1. **先迁 v2、保持行为不变**：替换 import、适配 KeyMsg/View/viewport/huh，全部既有测试改写后通过，pty 离线演示（G1b.2 的 6 项判据）复跑通过。此步**不改任何键位语义**。
2. **再落键位契约**（§3）：KeyMap 平台抽象 + 新键位 + 草稿历史。两步分别可验证，审核时一起交付。

## 3. herald 新键位表

### 3.1 换行：三层渐进（核心改进）

| 优先级 | 键 | 条件 |
| --- | --- | --- |
| 1 | `Shift+Enter` | 终端支持 kitty keyboard protocol（iTerm2 3.5+、WezTerm、Ghostty、kitty、Alacritty 等）——v2 自动协商，识别 `KeyEnter + ModShift` |
| 2 | `Option/Alt+Enter` | 终端把 Option 设为 Meta（Terminal.app 勾选 "Use Option as Meta"；iTerm2 设 ESC+）；识别 `KeyEnter + ModAlt` |
| 3 | `\` + `Enter` | 全终端兜底：去掉行尾 `\` 并换行 |

状态栏/欢迎文案只提示 Shift+Enter 与 `\` 续行，不罗列全部层级。**macOS 自带 Terminal.app 不支持 kitty 协议**，因此层级 2/3 不是可有可无，是 Terminal.app 用户的主路径。

### 3.2 完整键位表

| 键 | 动作 | 语义 |
| --- | --- | --- |
| `Enter` | 发送 | 空输入不产生请求；行尾 `\` 视为续行 |
| `Shift+Enter` / `Alt+Enter` / `\`+`Enter` | 换行 | 见 §3.1 |
| `Esc` | 中断当前 run | 取消的轮次不入历史（现有语义保留） |
| `Ctrl+C` | 三段语义 | running：取消 run；idle 有草稿：清空草稿；空草稿 1s 内两次：退出 |
| `Ctrl+D` | 退出 | 仅空输入时；有输入时忽略（防误触丢草稿） |
| `Ctrl+L` | 重绘/清屏 | 清屏并重置聊天区滚动位置 |
| `Up` / `Down` | 输入草稿历史 | 会话内已提交输入的历史导航；导航回到最新后恢复编辑中的草稿 |
| `PgUp` / `PgDn` | 滚动聊天记录 | 保留现有 |
| `Ctrl+N` | 新会话 | 保留现有（未来并入 slash 命令 `/new`） |
| `Shift+Tab` | 预留 | 模式/权限系统（G4+）引入前不绑定；v2 可独立识别该键，占位禁用 |
| 鼠标滚轮 | 滚动 | v2 `WithMouseCellMotion`（实现时验证与文本选择不冲突，冲突则不启用并记录） |

移除：`Ctrl+J`（换行职责移交渐进层级）、`Ctrl+C` 空闲单击退出（改为双击）。

## 4. 平台适配（Windows）

- v2 在 Windows 上通过 **ConPTY/Windows Console API 原生获取完整按键信息**（含修饰键组合），不再是"只有文档抽象"——G-win 单元的工作收敛为**实测验证**（Windows Terminal、ConHost 的按键行为）而非适配开发。
- 实现侧仍保留集中式 `internal/tui/keys.go`：`type KeyMap struct { Submit, NewlineShift, NewlineAlt, NewlineBackslash, Cancel, Quit, ClearScreen, HistoryUp, HistoryDown, ScrollUp, ScrollDown, NewSession key.Binding }`（bubbles/v2/key）。`Update` 只引用 KeyMap 语义字段，键名硬编码从 update.go 清除。
- `newKeyMap(goos string)`：darwin/linux 同表（差异仅允许在 Help 文本修饰键显示名 Option vs Alt）；windows 分支预留同结构表 + `TODO(G-win)` 实测注记。**不允许** Update/View 出现平台分支。
- 测试锁定 KeyMap 表逐项绑定存在且语义正确，防止实现时顺手改键。

## 5. 实现注意

1. v2 的键盘增强协商是自动的；`KeyboardEnhancementsMsg` 用于感知终端能力，仅影响提示文案（不支持 kitty 的终端不提示 Shift+Enter），不引入能力分支的界面差异。
2. Ctrl+C 双击窗口 1s，实现放 tui.Model（`lastCtrlC time.Time`），不引入 timer 消息。
3. 输入草稿历史属 UI 层状态（tui.Model），不进 app 会话历史。
4. v2 渲染管线变化可能导致帧输出与 v1 不同——pty 演示判据复跑时以**去控制序列后的纯文本**判定，不比对原始 ANSI。
5. 依赖增量实测（G1b.2 基线 10.70 MB）；v1 栈（bubbletea/bubbles/lipgloss/huh v1）应全部移除，若增量异常（v1/v2 并存残留）以 `go mod why` 排查。
6. 键位变更须同步：本文、`docs/runbooks/tui.md` 键位表、欢迎行提示文案、AGENTS.md（若命令行交互约定变化）。

## 6. 验收要点（G1b.2.1）

- `go test -race ./...` 全绿；既有 TUI/app/setup 测试在 v2 下全部改写通过；rg 全仓库无 `github.com/charmbracelet` v1 引用残留。
- 键位测试：KeyMap 表逐项 + Update 行为（三段 Ctrl+C、三层换行、Ctrl+D 空输入判定、草稿历史导航）。
- pty 演示：v2 迁移后先复跑 G1b.2 的 6 项判据（行为不变）；键位落地后加测 Alt+Enter 换行、`\`+Enter 续行、Esc 中断、双击 Ctrl+C 退出。
- Shift+Enter 在 kitty 协议终端（如 WezTerm/iTerm2）手动验证，Terminal.app 记录为不支持 kitty（用层级 2/3）；Windows 记 N/A（留 G-win）。
- mac（Terminal.app / iTerm2 各一）与 linux（任一终端）各验证一轮。
