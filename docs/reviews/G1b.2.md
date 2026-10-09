---
title: G1b.2 审核记录：最小可交互 TUI 与多轮会话
status: passed
updated: 2026-10-07
summary: G1b.2 交付：Bubble Tea 聊天界面、plume/chat 入口、--offline、setup 跳过渠道、会话与 run 生命周期；已通过审核
---

# G1b.2 审核记录：最小可交互 TUI 与多轮会话

- 日期：2026-10-06。
- 依据：[第一阶段计划](../plans/phase-01-tui.md) §2/§5 G1b.2、[决策 0003](../decisions/0003-model-runtime.md) §3/§8。
- 状态：**已通过审核**（2026-10-06 用户确认"通过"）。审核期间发现的模型名装配 bug 已随单元修复（见 §6 与 daily 记录），修复后经真实配置端到端验证。

## 1. 本单元做了什么

模仿 Hermes 的零参数体验：终端输入 `plume` 直接进入聊天 TUI（配置就绪 + TTY）。多轮对话（非流式整段回答）、Esc 取消、Ctrl+N 新会话、会话串行；`--offline` 用脚本化 fake 演示；setup 向导渠道步骤增加显式「暂不接入渠道」；CLI 简介与代码同步为 TUI 优先。

架构流：`cmd/plume`（装配）→ `app.Service`（run 生命周期、串行、取消、事件）→ `agent.Runtime`（最小单轮：历史+输入 → `model.Generate`）→ `model.Client`（G1b.1 适配器）。TUI 只消费事件更新界面，不发送 HTTP、不执行工具。

## 2. 改动清单

| 位置 | 内容 |
| --- | --- |
| `internal/app/session.go` | 会话历史：只追加完整提交的轮次，失败/取消不留半截上下文；Reset 清空 |
| `internal/app/service.go` | Submit（run ID、busy 拒绝）、Cancel、有界事件 channel、Close 等待收尾 |
| `internal/agent/runtime.go` | 最小模型轮次：历史+输入合并、绑定模型 ID（不做运行中热切换） |
| `internal/tui/{model,update,view}.go` | Bubble Tea 单屏：聊天记录（viewport 滚动）、输入区（textarea）、状态栏（模型/状态/run ID/耗时/usage）；键位 §2；Hooks 注入副作用 |
| `internal/tui/sanitize.go` | 模型输出剥离 ANSI CSI/OSC/C0 控制序列（防 OSC 执行/光标操纵） |
| `cmd/plume/chat.go` | `chat` 命令：`--offline`（LoopFake）/`--model <配置ID>` 互斥；模型配置解析、凭据接线、事件桥 |
| `cmd/plume/main.go` | 裸 `plume`：配置就绪+TTY 直接进 TUI；无配置提示 setup；非 TTY 明确退出；简介改 TUI 优先 |
| `internal/setup/wizard.go` + `cmd/plume/prompter.go` | `SelectChannelOrSkip`：显式「暂不接入渠道」（区别于 Esc 取消），trace 记录 `channel_skipped_explicitly` |
| `internal/model/fake.go` | `LoopFake`：无限重复脚本响应，支撑 `--offline` |
| `go.mod` | `bubbles v0.21.1`（连带 bubbletea 锁定 v1.3.10、lipgloss v1.1.0）提升为直接依赖 |
| `README.md`、`docs/runbooks/tui.md` | 新建：快速开始、键位、pty 演示脚本、故障复现 |

## 3. 行为证据

### 3.1 离线测试（全量 + race）

```
go test -race -timeout 300s ./...    # 12 个包全部 ok（含新增包）
go vet ./... ; gofmt -l .            # 通过 / 空
```

新增测试：app 5 项（成功提交历史、连续两轮携带上下文、busy 拒绝、失败不污染历史、取消不入历史、重置后不串上下文）、agent 2 项（历史合并+模型 ID、空输入拒绝）、tui 8 项（提交/空输入/busy 拒绝/完成事件+usage/失败事件/Esc 取消钩子/Ctrl+N 语义/控制序列净化）、setup 1 项（显式跳过渠道：模型保留、零渠道、trace 事件、说明提示）。

### 3.2 真实 pty 演示（隔离 PLUME_HOME，`--offline`）

判据 6/6 通过（脚本见 runbook）：

```
PASS user line 'You > hello'
PASS user line 'You > again'
PASS assistant reply 'Plume >'
PASS offline fixed reply
PASS status bar idle
PASS welcome visible
TUI_DEMO_OK
```

两轮输入回显、两次 fake 回答、状态栏回到 idle、欢迎词全部在真实终端渲染出现。

### 3.3 依赖增量（同参数 `-s -w` 实测）

| 版本 | 二进制大小 |
| --- | --- |
| G1b.1 | 8,501,730 B ≈ 8.11 MB |
| G1b.2 | 11,213,538 B ≈ 10.70 MB |
| **增量** | **+2,711,808 B ≈ +2.59 MB** |

说明：bubbletea 本身已是 huh 的传递依赖（G1a 时已计入），本单元增量来自 textarea/viewport 组件树与其依赖升级（bubbles v0.21.1、lipgloss v1.1.0、go-colorful v1.3.0、go-runewidth v0.0.19）。huh 兼容性由全量测试验证（setup 向导测试全绿）。

## 4. 局限（如实记录）

1. **非流式整段回答**：流式增量显示在 G1b.3。
2. **聊天 run 事件未落盘**：模型 trace 落盘（telemetry→文件）随后续单元交付；本单元 UI 消费内存事件，`plume setup` 的 setup trace 不受影响。
3. **模型选择不做就绪探测**：TUI 启动校验配置结构与凭据存在性，不发探测请求；首次真实调用失败以错误行呈现。
4. **`--offline` 是固定回答**：LoopFake 不理解输入内容，仅演示界面与生命周期。
5. **事件 channel 满时丢弃**：仅发生在 UI 已停止消费的退出路径（有界缓冲 8；正常消费下每 run 最多 2 事件）。
6. 真实模型下的 TUI 手感由你手动验证（`plume` 或 `plume chat --model <ID>`），费用自担。

## 5. 审核期间修复记录（用户实测发现）

用户真实使用中首次发送即收到 `400 invalid_request_error`：请求 `model` 字段传了**配置 ID**（`deepseek-default`）而非 **API 模型名**（`deepseek-flash`）。修复：装配逻辑抽为 `buildRuntime`（可测），agent 绑定 `selected.Model`；新增 `cmd/plume/chat_test.go` 守住该约定。修复后经 pty + 真实配置端到端验证（回答 "pong"、usage 36/31/67、无错误行）。提交 `36677d4`。

## 6. 审核结论

**通过**（2026-10-06）。后续键位体验改造（对齐 Codex CLI / Claude Code 惯例、mac/linux 优先、Windows 预留）另立单元 [G1b.2.1](../specs/tui/keys.md)，不回改本单元记录。
