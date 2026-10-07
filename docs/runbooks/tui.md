---
title: TUI 聊天 runbook
status: active
updated: 2026-10-07
summary: plume chat 的启动方式、键位、离线演示、真实模型验证与常见故障复现
---

# TUI 聊天 runbook（G1b.2）

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

## 键位（阶段计划 §2）

| 键 | 行为 |
| --- | --- |
| `Enter` | 发送输入（空输入不产生请求） |
| `Ctrl+J` | 输入内换行 |
| `PgUp` / `PgDn` | 滚动聊天记录 |
| `Esc` | 取消当前 run（取消的轮次不进入历史） |
| `Ctrl+C` | 运行中取消；空闲时退出 |
| `Ctrl+N` | 新会话（清空上下文；运行中会先被拒绝并提示） |

同一会话串行：运行中再次 Enter 被拒绝并提示，不排队、不并发。

## pty 演示脚本

`--offline` 的完整演示需要在 pty 中跑（管道直连会被 TTY 判定拒绝，这是刻意行为）。
用 Python 分配 pty 并驱动：

```python
#!/usr/bin/env python3
# 手动运行；PLUME_HOME 指向隔离目录
import os, pty, time, subprocess, sys, select

env = dict(os.environ, TERM="xterm-256color")
master, slave = pty.openpty()
p = subprocess.Popen(["go", "run", "./cmd/plume", "chat", "--offline"],
                     stdin=slave, stdout=slave, stderr=slave, env=env,
                     cwd=os.getcwd())
time.sleep(8)  # 等 TUI 初始化（含终端能力查询超时）
for text in (b"hello\r", b"again\r"):
    os.write(master, text)
    time.sleep(4)
os.write(master, b"\x03")  # Ctrl+C 退出
deadline = time.time() + 12
out = b""
while time.time() < deadline:
    r, _, _ = select.select([master], [], [], 0.3)
    if r:
        try:
            chunk = os.read(master, 65536)
        except OSError:
            break
        if not chunk:
            break
        out += chunk
    if p.poll() is not None:
        break
p.wait(timeout=10)
text = out.decode("utf-8", "replace")
ok = "hello" in text and "offline" in text.lower() and "again" in text
print("TUI_DEMO_OK" if ok else "TUI_DEMO_FAIL")
sys.exit(0 if ok else 1)
```

判据：输出包含两次输入回显、offline 固定回答，进程干净退出。

## 常见问题复现

- **`chat needs an interactive terminal`**：stdout 不是 TTY（管道/CI）。这是刻意行为；纯管道场景请看 `internal/tui` 的 Update 测试。
- **`plume needs an interactive terminal`**：裸 `plume` 在非 TTY 下调用，同样明确退出。
- **`config has no default_model`**：配置存在但没选默认模型，重跑 `plume setup`。
- **`model config "x" not found`**：`--model` 只接受 config.json 里已保存的配置 ID，不是 API 模型名。
- **怀疑配置问题**：`plume config show`（脱敏），或用 `PLUME_HOME=$(mktemp -d)` 隔离复现。
- **trace**：`~/.plume/logs/setup.jsonl`（setup 过程）。聊天 run 事件当前经内存事件驱动 UI，模型 trace 落盘扩展随后续单元交付。
