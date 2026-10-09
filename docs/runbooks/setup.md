---
title: Runbook：plume setup
status: active
updated: 2026-10-07
summary: 首次设置向导的启动、验证、重置与 7 类故障复现（G1a 交付，G1b.2.1 迁 v2 栈后修订）
---

# Runbook：plume setup

首次设置向导的启动、验证与故障复现。适用于 G1a 交付的版本。

> 路线更新：项目首版已改为 TUI，但本文仍描述当前已交付的 G1a 命令。`plume chat` 和跳过渠道的目标流程尚未实现；实现后再更新本 runbook，不能按计划假设当前已有聊天功能。

## 前置

- **交互式终端**。向导用 huh 渲染 ↑/↓ 菜单，非 TTY 会直接报错退出（见下）。
- 安装二进制：`./scripts/build.sh --install`（不要用裸 `go build`，那样没有版本信息）。
- 只想试一下、不想动真实配置：`PLUME_HOME=$(mktemp -d) plume setup`。

## 正常流程

```bash
plume setup
```

| 步骤 | 交互 | 说明 |
| --- | --- | --- |
| 1 选择模型供应商 | ↑/↓ + Enter | 当前只有 `DeepSeek` 与 `自定义兼容服务` |
| 2 Base URL | 仅**无预填值**的预设会问 | DeepSeek 有默认值，直接跳过 |
| 3 选择模型 | ↑/↓ + Enter | 列表末尾「自定义…」才落到文本输入 |
| 4 API Key | 隐藏输入 | 已有密钥时**直接回车 = 保留原值**；自定义服务可选择不需要 Key |
| 5 保存模型配置 | 自动 | 本地校验通过后原子写入 |
| 6 选择渠道 | ↑/↓ + Enter | 飞书/QQ 显示「待支持」，选中会被拒绝 |
| 7 渠道设置 | 只读提示 | 微信首版只记录为「待登录」，真实扫码是 G2a.1 |
| 8 显示总结 | 自动 | 模型、渠道、trace 路径 |

Ctrl+C 可取消当前步骤：模型配置保存前取消，不提交本轮模型配置；模型已经保存、在渠道步骤取消时，**保留已保存的模型配置与凭据**，不回滚第一阶段保存。setup trace 仍可能写入，不能承诺取消后磁盘完全无变化。当前还没有显式「暂不接入渠道」菜单项，计划在 TUI 单元补充。

## 产物位置

```text
~/.plume/                    0700
  config.json                 0600   非敏感配置（含 schema_version）
  credentials/                0700
    deepseek-default          0600   密钥本体
  logs/                       0700
    setup.jsonl               0600   结构化 trace（JSON Lines）
```

`PLUME_HOME` 可覆盖整个位置；未设置时 POSIX 用 `~/.plume`，Windows 用 `%LOCALAPPDATA%\plume`。

## 验证

```bash
plume config show        # 脱敏输出：凭据只显示「已设置 (ref: ...)」
ls -l ~/.plume ~/.plume/credentials

# 确认密钥没有进入非敏感文件
grep -c 'sk-' ~/.plume/config.json      # 期望 0
grep -c 'sk-' ~/.plume/logs/setup.jsonl # 期望 0
```

## 重置

```bash
rm -rf ~/.plume          # 完全重置
plume setup              # 重新配置
```

## 故障复现与排查

### 1. 非交互终端直接报错

```bash
plume setup < /dev/null
# plume: setup needs an interactive terminal, but stdin is not a TTY; run `plume setup` in a shell (config path: ...)
# exit 1
```

这是**刻意行为**，不是缺陷。注意 `/dev/null` 是字符设备但不是终端，所以判定必须用
`mattn/go-isatty` 的 `IsTerminal`，不能用 `os.ModeCharDevice`。

### 2. 在脚本 / CI 里驱动向导

需要分配 pty（G1b.2.1 起向导基于 charm.land v2 终端栈，不再使用 termenv）。向导会发送
终端能力查询（OSC 11 背景色、DA1、kitty 键盘、光标位置），**应答可加快启动，不应答
也会在超时后继续**（G1b.2.1 实测无应答可完整走通）：

```text
收到  ESC]11;?   →  回 \x1b]11;rgb:0000/0000/0000\x1b\\
收到  ESC[6n     →  回 \x1b[1;1R
收到  ESC[c      →  回 \x1b[?62;c
收到  ESC[?u     →  回 \x1b[?0u（不支持 kitty 协议）
```

### 3. 想改已有配置的 Base URL

向导对**有默认值**的预设不再询问地址。要改就直接编辑：

```bash
$EDITOR ~/.plume/config.json      # 改 models[].base_url
plume config show                  # 确认
```

注意：重进向导**不会**把已有地址重置回默认值（这条有测试守住）；但向导也不提供输入入口。

### 4. 查看某次设置的 trace

```bash
tail -n 20 ~/.plume/logs/setup.jsonl
grep '"setup_id":"setup-20261006T051836-001"' ~/.plume/logs/setup.jsonl
```

一次运行的事件链：`setup_start → 各 step → setup_completed`，每个 step 带 `duration_ms` 与 `status`。
`SetupRecorder` 的方法**不接受密钥参数**，所以 trace 里不会有密钥正文。

### 5. 目录权限告警

`credentials/` 权限过宽时只**告警**、不静默收紧：

```bash
chmod 755 ~/.plume/credentials
plume setup    # 会打印一条 chmod 700 的建议，但不改
```

### 6. 换供应商后旧 Key 还能用吗

不能，也不应该复用。切换供应商会把凭据写到新的 ref（如 `custom-openai-default`），
旧 ref 原样保留但不再被引用。

### 7. 配置写入失败

`config.json` 用原子写：失败时**原配置逐字节不变**，且本次新写的凭据会被回收，不留孤儿文件。

## 相关

- 决策：`docs/decisions/0001-scope.md`（预设、配置边界）
- 审核：`docs/reviews/G1a.md`
- 阶段计划：`docs/plans/phase-01-tui.md` §2（目标行为，与本 runbook 的当前版本区分）
- 模型与入口决策：`docs/decisions/0003-model-runtime.md`
