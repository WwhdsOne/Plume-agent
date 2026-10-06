# Runbook：herald setup

首次设置向导的启动、验证与故障复现。适用于 G1a 交付的版本。

## 前置

- **交互式终端**。向导用 huh 渲染 ↑/↓ 菜单，非 TTY 会直接报错退出（见下）。
- 安装二进制：`./scripts/build.sh --install`（不要用裸 `go build`，那样没有版本信息）。
- 只想试一下、不想动真实配置：`HERALD_HOME=$(mktemp -d) herald setup`。

## 正常流程

```bash
herald setup
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

Ctrl+C 随时可取消：**退出码 0，磁盘上不写任何内容**。

## 产物位置

```text
~/.herald/                    0700
  config.json                 0600   非敏感配置（含 schema_version）
  credentials/                0700
    deepseek-default          0600   密钥本体
  logs/                       0700
    setup.jsonl               0600   结构化 trace（JSON Lines）
```

`HERALD_HOME` 可覆盖整个位置；未设置时 POSIX 用 `~/.herald`，Windows 用 `%LOCALAPPDATA%\herald`。

## 验证

```bash
herald config show        # 脱敏输出：凭据只显示「已设置 (ref: ...)」
ls -l ~/.herald ~/.herald/credentials

# 确认密钥没有进入非敏感文件
grep -c 'sk-' ~/.herald/config.json      # 期望 0
grep -c 'sk-' ~/.herald/logs/setup.jsonl # 期望 0
```

## 重置

```bash
rm -rf ~/.herald          # 完全重置
herald setup              # 重新配置
```

## 故障复现与排查

### 1. 非交互终端直接报错

```bash
herald setup < /dev/null
# herald: setup needs an interactive terminal, but stdin is not a TTY; run `herald setup` in a shell (config path: ...)
# exit 1
```

这是**刻意行为**，不是缺陷。注意 `/dev/null` 是字符设备但不是终端，所以判定必须用
`mattn/go-isatty` 的 `IsTerminal`，不能用 `os.ModeCharDevice`。

### 2. 在脚本 / CI 里驱动向导

需要分配 pty，**并且应答终端能力查询**，否则 termenv 会超时报错：

```text
收到  ESC]11;?   →  回 \x1b]11;rgb:0000/0000/0000\x1b\\
收到  ESC[6n     →  回 \x1b[1;1R
收到  ESC[c      →  回 \x1b[?62;c
```

不回这些查询时，向导会在启动阶段就退出（不是卡在菜单上）。

### 3. 想改已有配置的 Base URL

向导对**有默认值**的预设不再询问地址。要改就直接编辑：

```bash
$EDITOR ~/.herald/config.json      # 改 models[].base_url
herald config show                  # 确认
```

注意：重进向导**不会**把已有地址重置回默认值（这条有测试守住）；但向导也不提供输入入口。

### 4. 查看某次设置的 trace

```bash
tail -n 20 ~/.herald/logs/setup.jsonl
grep '"setup_id":"setup-20261006T051836-001"' ~/.herald/logs/setup.jsonl
```

一次运行的事件链：`setup_start → 各 step → setup_completed`，每个 step 带 `duration_ms` 与 `status`。
`SetupRecorder` 的方法**不接受密钥参数**，所以 trace 里不会有密钥正文。

### 5. 目录权限告警

`credentials/` 权限过宽时只**告警**、不静默收紧：

```bash
chmod 755 ~/.herald/credentials
herald setup    # 会打印一条 chmod 700 的建议，但不改
```

### 6. 换供应商后旧 Key 还能用吗

不能，也不应该复用。切换供应商会把凭据写到新的 ref（如 `custom-openai-default`），
旧 ref 原样保留但不再被引用。

### 7. 配置写入失败

`config.json` 用原子写：失败时**原配置逐字节不变**，且本次新写的凭据会被回收，不留孤儿文件。

## 相关

- 决策：`docs/decisions/0001-scope.md`（预设、配置边界）
- 审核：`docs/reviews/G1a.md`
- 阶段计划：`docs/phase-01-weixin-agent.md` §2
