---
title: TUI 聊天 runbook
status: active
updated: 2026-10-07
summary: plume chat 的启动方式、键位、离线演示、真实模型验证与常见故障复现
---

# TUI 聊天 runbook（G1b.2.1 起按 v2 栈与键位契约）

## 启动方式

```bash
plume                        # 配置就绪 + TTY：直接进入聊天（默认模型）
plume chat                   # 显式入口，同上
plume chat --model deepseek-default   # 指定 config.json 里的模型配置 ID
plume chat --offline         # 脚本化 fake：无配置/无 Key/不联网
plume setup                  # 首次设置；渠道步骤可选「暂不接入渠道」
```

隔离试验（不污染真实配置）：

```bash
PLUME_HOME=$(mktemp -d) go run ./cmd/plume setup
PLUME_HOME=$(mktemp -d) go run ./cmd/plume chat --offline
```

## 键位（契约 docs/tui-keys.md §3）

| 键 | 行为 |
| --- | --- |
| `Enter` | 发送输入（空输入不产生请求） |
| `Shift+Enter` | 换行（kitty 协议终端：iTerm2 3.5+、WezTerm、Ghostty、kitty、Alacritty） |
| `Option/Alt+Enter` | 换行（终端把 Option 设为 Meta 时） |
| `\` + `Enter` | 换行兜底：行尾 `\` 被去掉并换行，全部终端可用 |
| `Up` / `Down` | 边缘导航：光标在输入第一行时 Up 翻草稿历史、最后一行时 Down 翻回；多行输入中间则先移动光标（回到最新恢复编辑中草稿） |
| `PgUp` / `PgDn` | 滚动聊天记录 |
| `Esc` | 取消当前 run（取消的轮次不进入历史） |
| `Ctrl+C` | 三段：运行中取消；空闲有草稿清空草稿；空草稿 1s 内两次退出 |
| `Ctrl+D` | 退出（仅空输入时；有草稿时忽略） |
| `Ctrl+L` | 清屏并回到聊天记录底部 |
| `Ctrl+N` | 新会话（清空上下文与草稿历史；运行中会先被拒绝并提示） |
| 鼠标滚轮 | 滚动聊天记录（与终端文本选择冲突时按住 Shift 选择） |

输入区高度随换行/折行自动长高，一次最多可见 4 行，超出在输入框内滚动。

同一会话串行：运行中再次 Enter 被拒绝并提示，不排队、不并发。

## pty 演示脚本

`--offline` 的完整演示需要在 pty 中跑（管道直连会被 TTY 判定拒绝，这是刻意行为）。
G1b.2.1 起用仓库内脚本（覆盖 v1 判据复跑 + 新键位）：

```bash
PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b221.py
# 期望输出：TUI_DEMO_OK returncode = 0
```

脚本流程：普通发送 → Alt+Enter 换行提交 → `\`+Enter 续行提交 → Esc 空闲无副作用 → 单击 Ctrl+C 不退出 → 双击 Ctrl+C 干净退出。

判据：输出包含各次输入回显、offline 固定回答，进程干净退出。kitty 协议的
Shift+Enter（CSI u `\x1b[13;2u`）已在 G1b.2.1 证据中单独验证；真实终端里按
iTerm2/WezTerm 手测（Terminal.app 不支持 kitty，走层级 2/3）。

## 常见问题复现

- **`chat needs an interactive terminal`**：stdout 不是 TTY（管道/CI）。这是刻意行为；纯管道场景请看 `internal/tui` 的 Update 测试。
- **`plume needs an interactive terminal`**：裸 `plume` 在非 TTY 下调用，同样明确退出。
- **`config has no default_model`**：配置存在但没选默认模型，重跑 `plume setup`。
- **`model config "x" not found`**：`--model` 只接受 config.json 里已保存的配置 ID，不是 API 模型名。
- **怀疑配置问题**：`plume config show`（脱敏），或用 `PLUME_HOME=$(mktemp -d)` 隔离复现。
- **trace**：`~/.plume/logs/setup.jsonl`（setup 过程）。聊天 run 事件当前经内存事件驱动 UI，模型 trace 落盘扩展随后续单元交付。
