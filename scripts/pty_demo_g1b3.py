#!/usr/bin/env python3
"""G1b.3 隔离 pty 验收：本地人工协议 fixture，无真实凭据或外部网络。

使用 --output 指定证据目录。可选 pyte 用于还原最终终端帧；未安装时仍保存原始输出。
"""
import argparse
from datetime import datetime
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import struct
import subprocess
import tempfile
import termios
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Fixture(BaseHTTPRequestHandler):
    scenario = "success"

    def log_message(self, *args):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert request["stream"] is True
        assert "reasoning_effort" not in request
        assert "thinking" not in request
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()

        def send(delta=None, finish=None, usage=None):
            chunk = {"choices": [{"index": 0, "delta": delta or {}, "finish_reason": finish}]}
            if usage is not None:
                chunk = {"choices": [], "usage": usage}
            self.wfile.write(("data: " + json.dumps(chunk, ensure_ascii=False) + "\r\n\r\n").encode())
            self.wfile.flush()

        try:
            time.sleep(0.25)
            if self.scenario == "markdown-open-fence":
                send({"content": "下面是 **Python、Java、C++** 三个语言。\n\n### 1. Python\n```python\ndef quick_sort(arr):\n    return arr"})
                send(finish="stop")
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()
                return
            if self.scenario != "answer-only":
                for thought in ("第一行：检查输入。\n", "第二行：保留事实。\n", "第三行：组织答案。\n", "第四行：输出中文与 emoji 🪶。"):
                    send({"reasoning_content": thought})
                    time.sleep(0.15)
            if self.scenario == "cancel":
                time.sleep(2)
                return
            if self.scenario != "reasoning-only":
                for text in ("## 流式回答\n\n", "**羽毛**与中文 🪶。\n\n", "- 第一项\n- 第二项\n\n", "```go\n", 'fmt.Println("Plume")\n', "```\n\n", "[文档](https://example.com/docs)\n"):
                    send({"content": text})
                    time.sleep(0.07)
                    if self.scenario == "disconnect":
                        return
            send(finish="stop")
            send(usage={"prompt_tokens": 12, "completion_tokens": 34, "total_tokens": 46})
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass


def run_case(binary, output, width, height, color, scenario):
    Fixture.scenario = scenario
    server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    name = f"{scenario}-{width}x{height}-{color}"
    with tempfile.TemporaryDirectory(prefix="plume-g1b3-") as home:
        cfg = {"schema_version": 1, "default_model": "fixture", "models": [{"id": "fixture", "provider": "deepseek", "protocol": "deepseek", "model": "deepseek-flash", "base_url": f"http://127.0.0.1:{server.server_port}"}], "tui": {"status_messages": {"waiting": ["waiting"], "thinking": ["Thought"]}}}
        Path(home, "config.json").write_text("{invalid json" if scenario == "offline" else json.dumps(cfg))
        env = dict(os.environ, PLUME_HOME=home, TERM="xterm-256color")
        env.pop("NO_COLOR", None)
        env.pop("COLORTERM", None)
        if color == "truecolor":
            env["COLORTERM"] = "truecolor"
        if color == "none":
            env["NO_COLOR"] = "1"
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
        command = [str(binary), "chat"]
        if scenario == "offline":
            command.append("--offline")
        process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave, env=env)
        os.close(slave)
        raw = bytearray()
        frames = []
        cancel_requested_ms = None

        def drain(seconds):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                if not select.select([master], [], [], 0.02)[0]:
                    continue
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    break
                raw.extend(chunk)
                if b"\x1b]11;?" in chunk:
                    os.write(master, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
                if b"\x1b[6n" in chunk:
                    os.write(master, b"\x1b[1;1R")
                if b"\x1b[c" in chunk:
                    os.write(master, b"\x1b[?1;2c")
                if b"\x1b[?u" in chunk:
                    os.write(master, b"\x1b[?0u")
            frames.append(bytes(raw))

        try:
            drain(1.2)
            os.write(master, "展示羽毛的流式回答\r".encode())
            drain(0.65)
            os.write(master, b"\x0f")  # Ctrl+O：有思考时主动展开
            if scenario == "cancel":
                # 记录按键发出时间，不能把整轮耗时误报为取消延迟。
                cancel_requested_ms = time.time_ns() / 1_000_000
                os.write(master, b"\x1b")
            drain(1.8)
            os.write(master, b"\x0f")
            drain(0.15)
            os.write(master, b"\x04")
            drain(0.5)
            process.wait(timeout=3)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            os.close(master)
            server.shutdown()
            server.server_close()
        output.mkdir(parents=True, exist_ok=True)
        (output / f"{name}.ansi").write_bytes(raw)
        plain = re.sub(r"\x1b\][^\x1b\x07]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]", "", raw.decode("utf-8", "replace"))
        (output / f"{name}.txt").write_text(plain)
        traces = list(Path(home, "logs").glob("chat-*.jsonl"))
        events = [json.loads(line) for trace in traces for line in trace.read_text().splitlines()]
        (output / f"{name}.jsonl").write_text("".join(json.dumps(e, ensure_ascii=False) + "\n" for e in events))
        terminal = [e for e in events if e["msg"] == "run_ended"]
        expected = {"success": "", "answer-only": "", "offline": "", "markdown-open-fence": "", "reasoning-only": "invalid_response", "disconnect": "stream_interrupted", "cancel": "cancelled"}[scenario]
        assert process.returncode == 0, (name, process.returncode)
        assert len(terminal) == 1 and terminal[0]["error_code"] == expected, (name, terminal)
        assert all("展示羽毛" not in json.dumps(e, ensure_ascii=False) for e in events)
        if scenario in ("success", "answer-only", "offline", "markdown-open-fence"):
            assert any(e["msg"] == "ui_first_answer" for e in events), name
        if scenario == "offline":
            assert "[offline]" in plain, name
        try:
            import pyte

            # Bubble Tea 的差分帧使用 CSI S/T 滚动，pyte 0.8.2 默认未实现。
            # 补这两个标准指令，避免屏幕重建出现重复行和开屏残留。
            class Screen(pyte.Screen):
                def scroll_up(self, count=1):
                    x, y = self.cursor.x, self.cursor.y
                    self.cursor.y = self.margins.bottom if self.margins else self.lines - 1
                    for _ in range(count or 1):
                        self.index()
                    self.cursor.x, self.cursor.y = x, y

                def scroll_down(self, count=1):
                    x, y = self.cursor.x, self.cursor.y
                    self.cursor.y = self.margins.top if self.margins else 0
                    for _ in range(count or 1):
                        self.reverse_index()
                    self.cursor.x, self.cursor.y = x, y

            class Stream(pyte.Stream):
                csi = dict(pyte.Stream.csi, S="scroll_up", T="scroll_down")

            for i, frame in enumerate(frames[:-1]):
                screen = Screen(width, height)
                stream = Stream(screen)
                stream.feed(frame.decode("utf-8", "replace"))
                (output / f"{name}-frame-{i}.txt").write_text("\n".join(screen.display))
        except ImportError:
            pass
        result = {"case": name, "status": "passed", "metrics": terminal[0]}
        if cancel_requested_ms is not None:
            ended_ms = datetime.strptime(terminal[0]["ts"], "%Y-%m-%dT%H:%M:%S.%f%z").timestamp() * 1000
            result["cancel_latency_ms"] = ended_ms - cancel_requested_ms
        return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=Path("./plume"))
    parser.add_argument("--output", type=Path, default=Path("docs/reviews/evidence/G1b.3/pty"))
    parser.add_argument("--offline-invalid-config", action="store_true", help="仅验证 offline 启动忽略畸形配置")
    parser.add_argument("--markdown-open-fence", action="store_true", help="验证未闭合代码围栏时标题和加粗仍渲染")
    args = parser.parse_args()
    if args.markdown_open_fence:
        result = run_case(args.binary.resolve(), args.output, 80, 24, "none", "markdown-open-fence")
        frame = args.output / "markdown-open-fence-80x24-none-frame-3.txt"
        assert frame.exists(), "此检查需要 pyte 还原最终终端帧"
        rendered = frame.read_text()
        assert "Python、Java、C++" in rendered and "quick_sort(arr)" in rendered
        assert "**Python" not in rendered and "### 1. Python" not in rendered and "```python" not in rendered
        (args.output / "markdown-open-fence-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        print("G1B3_MARKDOWN_OK open_fence_rendered=true")
        return
    if args.offline_invalid_config:
        result = run_case(args.binary.resolve(), args.output, 80, 24, "none", "offline")
        (args.output / "offline-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        print("G1B3_OFFLINE_OK invalid_config_ignored=true")
        return
    results = []
    for width, height in ((100, 32), (80, 24), (40, 24)):
        for color in ("truecolor", "256", "none"):
            results.append(run_case(args.binary.resolve(), args.output, width, height, color, "success"))
    cases = [json.loads(line) for line in Path("eval/datasets/stream.v1.jsonl").read_text().splitlines()]
    for case in cases:
        if case["scenario"] != "success":
            results.append(run_case(args.binary.resolve(), args.output, 80, 24, "none", case["scenario"]))
    (args.output / "results.json").write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n")
    print(f"G1B3_PTY_OK cases={len(results)}")


if __name__ == "__main__":
    main()
