---
title: G1b.2.1 审核交付：终端栈 v2 迁移与键位重设计
status: pending-review
updated: 2026-10-07
summary: charm.land v2 全家桶迁移（v1 全移除）+ 键位契约落地 + 输入区动态高度最多 4 行；测试与 pty 证据齐备，待用户审核
---

# G1b.2.1 审核交付

- 日期：2026-10-07。用户授权动工，并追加契约外要求：**输入区随换行/折行长高、一次最多可见 4 行**（已先写入契约 `docs/tui-keys.md` §3.3 再实现）。
- 交付物：迁移（v2 全家桶，行为不变）+ 键位契约落地，同单元一起交付。

## 1. 改动说明

### 1.1 依赖迁移（第一步：行为不变）

| 依赖 | v1（已移除） | v2（引入） |
| --- | --- | --- |
| bubbletea | `github.com/charmbracelet/bubbletea` v1.3.10 | `charm.land/bubbletea/v2` v2.0.10 |
| bubbles | `github.com/charmbracelet/bubbles` v0.21.1 | `charm.land/bubbles/v2` v2.2.1 |
| lipgloss | `github.com/charmbracelet/lipgloss` v1.1.0 | `charm.land/lipgloss/v2` v2.0.6 |
| huh | `github.com/charmbracelet/huh` v1.0.0 | `charm.land/huh/v2` v2.0.3 |

实测适配的 v2 API 变化（与契约 §2.2 一致，个别点比契约预估更具体）：

- `Model.View()` 返回 `tea.View`（用 `tea.NewView(s)` 包装）；alt screen 与鼠标模式在 `TeaModel.View` 的 `tea.View` 字段上声明（`v.AltScreen = true`、`v.MouseMode = tea.MouseModeCellMotion`）——**没有** `WithAltScreen`/`WithMouseCellMotion` 程序选项。
- `KeyMsg` 接口化：Update 分发用具体类型 `tea.KeyPressMsg`（release 事件不误触发）；修饰键判定走 `Key{Code, Mod, Text}`，`String()` 产出 `shift+enter`/`alt+enter`/`ctrl+c` 等规范键名。
- viewport：`viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))`；`HalfViewUp/Down` 更名 `HalfPageUp/Down`。
- textarea v2 原生 `DynamicHeight + MinHeight + MaxHeight`，输入区 1→4 行动态高度无需手写同步逻辑。
- huh v2 API 同构，`prompter.go` 仅改 import。

### 1.2 键位契约落地（第二步）

- **`internal/tui/keys.go`（新建）**：`KeyMap` 12 个语义绑定（Submit/NewlineShift/NewlineAlt/NewlineBackslash/Cancel/Quit/ClearScreen/HistoryUp/HistoryDown/ScrollUp/ScrollDown/NewSession），`newKeyMap(goos)` darwin/linux 同表（差异仅帮助文本 Option vs Alt），windows 分支留 `TODO(G-win)`。Update/View 无键名硬编码、无平台分支（Ctrl+C 三段语义按契约不在 KeyMap 字段表内，在 `handleCtrlC` 用键名判定）。
- **三层渐进换行**（`update.go`）：`shift+enter`（kitty 终端，v2 自动协商）与 `alt+enter` 直接插入换行；Enter 时若输入以 `\` 结尾则去掉 `\` 并换行（兜底层级，不发送）。
- **Ctrl+C 三段**：running → 取消 run；idle 有草稿 → 清空草稿；空草稿 1s 内两次 → 退出（`lastCtrlC` 时间戳，无 timer 消息；其他按键重置双击窗口）。
- **Ctrl+D**：空输入退出，有草稿忽略（防误触丢内容）。
- **Ctrl+L**：清屏（`tea.ClearScreen`）并回聊天记录底部。
- **Up/Down 草稿历史**：UI 层状态（`history/historyIdx/draft`），进入历史前保存编辑中草稿，翻回最新恢复；会话内 200 条上限、连续重复去重；Ctrl+N 新会话一并清空。
- **输入区高度**：`DynamicHeight=true, MinHeight=1, MaxHeight=4`；高度变化经 `syncLayout()` 联动 viewport 重排。
- **移除**：Ctrl+J 换行、Ctrl+C 空闲单击退出。
- **欢迎行/占位文案**同步新键位（只提示 Shift+Enter 与 `\` 续行，契约 §3.3）。

改动文件：`internal/tui/{keys.go 新建, model.go, update.go, view.go, update_test.go}`、`cmd/plume/{chat.go, prompter.go}`、`go.mod/go.sum`、`scripts/pty_demo_g1b221.py`（新建）。

## 2. 证据

### 2.1 测试（可复现命令）

```bash
go build ./... && go vet ./... && gofmt -l .   # 全部干净
go test -race ./...                            # 14 包全绿
rg 'github.com/charmbracelet/(bubbletea|bubbles|lipgloss|huh)' --type go   # 无输出（v1 无残留）
go mod why github.com/charmbracelet/bubbletea  # main module does not need
```

- `internal/tui` 16 个测试全过，其中 G1b.2 既有测试全部改写为 v2 构造，新增 7 类：KeyMap 表逐项（含 darwin/linux 帮助文本差异）、三层换行（含 `\` 续行不发送→再 Enter 发送）、Ctrl+C 三段（含 1s 窗口外/中间插键重置）、Ctrl+D 仅空输入、Ctrl+L 回底、草稿历史导航（保存/恢复/到顶不动/空历史）、输入区高度 1→4→1。

### 2.2 pty 演示（离线 fake）

```bash
./scripts/build.sh && PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b221.py
# → TUI_DEMO_OK returncode = 0
```

覆盖：普通发送回显 + offline 固定回答（G1b.2 判据复跑）、Alt+Enter 换行后多行一起提交、`\`+Enter 续行不发送且续行内容一起提交、Esc 空闲无副作用、单击 Ctrl+C 不退出、0.3s 双击 Ctrl+C 干净退出。

补充证据（同一环境手动抓帧）：输入区两行状态（`┃ 1 multi / ┃ 2`）与多行聊天行（`You > multi / line`）在渲染帧中可见；**kitty CSI u 编码的 Shift+Enter（`\x1b[13;2u`）在未协商的 pty 上被 v2 解析器正确识别**，`kitty`+CSI u+`u` 提交为两行。

### 2.3 setup 向导（huh v2 冒烟）

pty 驱动完整走通 DeepSeek → 模型 → API Key → 「暂不接入渠道」→ 总结，退出码 0、`config.json` 写入 `default_model`、密钥不落 config.json；**应答与不应答终端能力查询两种方式均可完成**（v2 不再因 termenv 阻塞）。

## 3. 指标表（基线协议：绝对值 + 实测增量）

| 指标 | 值 | 说明 |
| --- | --- | --- |
| 二进制大小（`-s -w`） | 11.62 MB | G1b.2 基线 10.70 MB，v2 迁移实测 +0.92 MB，无 v1/v2 并存残留 |
| 测试 | 14 包全绿（`-race`） | tui 包 16 个（原 9 + 新增 7） |
| v1 残留 | 0 | 源码与 go.mod 均无 |
| 键位判定延迟 | N/A | 纯字符串匹配，无计时需求 |
| 流式/取消耗时 | N/A | G1b.3 范围 |

## 4. 局限

1. **Shift+Enter 真实 kitty 终端未人工验证**（WezTerm/iTerm2/Ghostty）：pty 里已用 CSI u 编码验证解析路径，真实终端的协商结果留待用户手测；Terminal.app 不支持 kitty，走 Alt+Enter / `\`+Enter（契约预期）。
2. **鼠标滚轮与文本选择的冲突无法在 pty 验证**（需真人拖选）：已按契约启用 `MouseModeCellMotion`，若实测冲突，终端里按住 Shift 选择即可，或后续关闭并记录。
3. ~~Up/Down 被草稿历史占用后，多行输入内的上下移光标不可用~~ **已修复（2026-10-07 用户反馈）**：改为边缘导航——光标在输入第一行时 Up 才翻历史、最后一行时 Down 才翻回，多行输入中间先移动光标（对齐 Codex CLI）；契约 §3.2、runbook、AGENTS.md 同步更新，`TestHistoryNavigationIsEdgeOnly` 守住。
4. `\`+Enter 的续行判定基于**整个输入值的行尾**（光标不在末尾时也按行尾处理）；按字符级判定复杂度不成比例，契约原文即"行尾 `\`"。
5. Ctrl+C 双击窗口按契约固定 1s；pty 脚本里两次按键间隔需 <1s（初版演示脚本间隔恰好 ≥1s 未退出，已修正——行为符合契约）。
6. 聊天 run 事件落盘 trace 仍属后续单元（与迁移无关，未动）。

## 5. 待用户检查项

1. 真实终端手测：Shift+Enter（iTerm2/WezTerm 任一 kitty 终端）、Terminal.app 的 Option+Enter 与 `\`+Enter、双击 Ctrl+C 手感（1s 窗口）、Up/Down 历史、输入区 4 行上限。
2. `docs/runbooks/setup.md` 仍有一处 G1b.2 遗留失实（"渠道步骤尚无「暂不接入渠道」菜单项"，实际 G1b.2 已加）——不属本单元范围，建议随下次文档清理修正。
3. 通过后按流程推进 G1b.3（流式消费）。
