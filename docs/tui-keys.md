---
title: TUI 键位契约与平台适配（G1b.2.1）
status: active
updated: 2026-10-06
summary: 聊天界面键位重设计：对齐 Codex CLI/Claude Code 惯例，mac/linux 优先，KeyMap 平台抽象为 Windows 预留
---

# TUI 键位契约与平台适配（G1b.2.1）

- 日期：2026-10-06。触发：用户反馈 G1b.2 初版键位（Ctrl+J 换行、Ctrl+C 语义、无输入历史）不适配 Mac，要求模仿 Codex CLI 与 Claude Code。
- 范围：**文档先行**，实现作为独立审核单元 G1b.2.1 交付。本文是键位的行为契约；实现通过 `internal/tui/keys.go` 的 `KeyMap` 平台抽象满足，验收测试锁定键位表不被顺手改动。

## 1. 参照工具的惯例（2026-10-06 复核）

| 键 | Codex CLI | Claude Code | 共识 |
| --- | --- | --- | --- |
| Esc | 中断当前 turn | 中断当前响应 | **中断** |
| Ctrl+C | 一次中断，两次退出 | 取消输入/生成（退出需两次或 Ctrl+D） | **一次中断/清空，两次退出** |
| Ctrl+D | 空提示符退出（或 /exit） | 退出会话（EOF） | **EOF 退出** |
| 多行 | 终端配置后 Option/类组合键 | Option/Alt+Enter（终端需 Option as Meta），或 `\`+Enter 续行兜底 | **Alt+Enter 优先，反斜杠兜底** |
| Shift+Tab | 循环 approval/auto-edit 模式 | 循环权限模式（normal/auto-accept/plan） | 模式系统预留，首版不绑定 |
| Ctrl+L | — | 清屏 | 重绘/清屏 |
| Up/Down | 任务内导航 | 输入历史导航 | **输入历史** |
| 斜杠命令 | /exit /quit 等 | /clear 等 | 首版不建框架，预留 |

来源：[Claude Code Interactive mode 官方文档](https://code.claude.com)、[Claude Code cheatsheet](https://support.claude.com)、[Codex CLI Cheat Sheet](https://computingforgeeks.com/codex-cli-cheat-sheet/)、[Codex CLI shortcuts（版本敏感）](https://github.com/ashokmanohar-ai/openai-codex-complete-guide/blob/main/docs/03-cli/shortcuts.md)。

## 2. herald 新键位表（mac / linux 初版）

| 键 | 动作 | 语义 |
| --- | --- | --- |
| `Enter` | 发送 | 空输入不产生请求；输入以 `\` 结尾时视为续行（见下） |
| `Alt/Option+Enter` | 输入内换行 | 终端须把 Option 设为 Meta（Terminal.app 勾选 "Use Option as Meta"；iTerm2 设 Left Option = ESC+）。识别为 `Key{Type: KeyEnter, Alt: true}` |
| `\` + `Enter` | 续行 | 去掉行尾 `\` 并换行；全终端可用的多行兜底（Claude Code 惯例） |
| `Esc` | 中断当前 run | 取消的轮次不入历史（现有语义保留） |
| `Ctrl+C` | 三段语义 | running：取消 run；idle 有草稿：清空草稿；空草稿 1s 内两次：退出 |
| `Ctrl+D` | 退出 | 仅空输入时；有输入时忽略（防误触丢草稿） |
| `Ctrl+L` | 重绘/清屏 | 清屏并重置聊天区滚动位置 |
| `Up` / `Down` | 输入草稿历史 | 会话内已提交输入的历史导航；编辑中的草稿暂存 |
| `PgUp` / `PgDn` | 滚动聊天记录 | 保留现有 |
| `Ctrl+N` | 新会话 | 保留现有（未来并入 slash 命令 `/new`） |
| `Shift+Tab` | 预留 | 模式/权限系统（G4+）引入前不绑定，KeyMap 中占位禁用 |
| 鼠标滚轮 | 滚动 | `tea.WithMouseCellMotion`（实现时验证与文本选择不冲突，冲突则不启用并记录） |

移除：`Ctrl+J`（换行职责移交 Alt+Enter / 反斜杠续行）、`Ctrl+C` 空闲单击退出（改为双击）。

## 3. 平台抽象（Windows 预留接口）

- 集中定义 `internal/tui/keys.go`：`type KeyMap struct { Submit, Newline, NewlineBackslash, Cancel, Quit, ClearScreen, HistoryUp, HistoryDown, ScrollUp, ScrollDown, NewSession key.Binding }`（bubbles/key）。
- 构造 `newKeyMap(goos string)`：`darwin`/`linux` 返回同一张表（差异只允许出现在 Help 文本的修饰键显示名 Option vs Alt）；`windows` 分支预留——返回同结构表并注释 `TODO(G-win)`：Windows 终端（ConPTY/Windows Terminal）的 Alt+Enter、Ctrl+C 行为差异留待专门验证，**不得**让 Update/View 逻辑因平台分支而变化。
- `Update` 只引用 KeyMap 的语义字段与 `key.Matches`，键名硬编码从 update.go 清除；测试用 `KeyMap` 表逐项断言绑定存在且语义正确，防止实现时顺手改键。

## 4. 实现注意（G1b.2.1 开工前确认）

1. bubbletea v1.3.10 **不支持** kitty keyboard protocol（v2 才有），`Shift+Enter` 在标准终端不可区分于 Enter——首版不承诺 Shift+Enter，文档与状态栏提示不出现它。
2. Alt+Enter 依赖终端的 Option-as-Meta 配置；检测不到时用户有反斜杠续行兜底，不作为缺陷。
3. Ctrl+C 双击窗口 1s；实现放 tui.Model（`lastCtrlC time.Time`），不引入 timer 消息。
4. 输入草稿历史属 UI 层状态（tui.Model），不进 app 会话历史；上/下导航到最新后恢复编辑中的草稿。
5. 键位变更须同步：本文、`docs/runbooks/tui.md` 键位表、欢迎行提示文案、AGENTS.md（若命令行交互约定变化）。

## 5. 验收要点（G1b.2.1）

- `go test -race ./internal/tui/...`：键位表测试 + Update 行为测试（三段 Ctrl+C、续行、历史导航、Ctrl+D 空输入判定）。
- pty 演示：Alt+Enter 换行（终端配置后）、`\`+Enter 续行、Esc 中断、双击 Ctrl+C 退出。
- mac（Terminal.app / iTerm2 各一）与 linux（任一终端）各验证一轮；Windows 结论记 N/A。
