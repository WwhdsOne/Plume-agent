#!/usr/bin/env python3
# G1b.2.1 pty 演示：v1 判据复跑 + 新键位（Alt+Enter 换行、`\`+Enter 续行、双击 Ctrl+C 退出）
# 用法：PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b221.py
import os, pty, time, subprocess, sys, select, fcntl, termios, struct

env = dict(os.environ, TERM="xterm-256color")
master, slave = pty.openpty()
# 设置 pty 窗口 80x24，让 WindowSizeMsg 给出正常布局
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))

p = subprocess.Popen(["./plume", "chat", "--offline"],
                     stdin=slave, stdout=slave, stderr=slave, env=env,
                     cwd=os.getcwd())
os.close(slave)

out = b""
def drain(seconds):
    global out
    deadline = time.time() + seconds
    while time.time() < deadline:
        r, _, _ = select.select([master], [], [], 0.2)
        if r:
            try:
                out += os.read(master, 65536)
            except OSError:
                return False
        if p.poll() is not None:
            return False
    return True

drain(6)                      # 等初始化（含终端能力查询超时窗口）
os.write(master, b"hello\r");  drain(3)          # 判据 1：普通发送
os.write(master, b"multi\x1b\r"); drain(1)       # Alt+Enter（ESC+CR，Meta 终端编码）→ 换行
os.write(master, b"line\r");   drain(3)          # 判据 2：多行一起提交
os.write(master, b"cont\\\r"); drain(1)          # `\`+Enter 续行兜底 → 不发送
os.write(master, b"nued\r");   drain(3)          # 判据 3：续行后一起提交
os.write(master, b"\x1b");     drain(1)          # Esc 空闲按下：无副作用
os.write(master, b"\x03");     drain(0.3)        # Ctrl+C 空闲单击：不退出（新语义）
os.write(master, b"\x03")                        # 0.3s 后第二击：双击退出（窗口 1s）
deadline = time.time() + 12
while time.time() < deadline and p.poll() is None:
    r, _, _ = select.select([master], [], [], 0.3)
    if r:
        try:
            out += os.read(master, 65536)
        except OSError:
            break
try:
    p.wait(timeout=10)
except subprocess.TimeoutExpired:
    p.kill()

text = out.decode("utf-8", "replace")
ok = ("hello" in text
      and "multi" in text and "line" in text
      and "cont" in text and "nued" in text
      and "offline" in text.lower()
      and p.returncode == 0)
print("TUI_DEMO_OK" if ok else "TUI_DEMO_FAIL", "returncode =", p.returncode)
sys.exit(0 if ok else 1)
