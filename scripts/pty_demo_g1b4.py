#!/usr/bin/env python3
"""G1b.4 隔离 PTY 验收：临时配置/工作目录、本地 SSE fixture 与真实终端帧。

先运行 rtk ./scripts/build.sh。pyte 0.8.2 用于重建退出前的 alt-screen；
可用 PYTHONPATH=/tmp/plume-splash-preview-deps 指向临时安装，不改项目依赖。
"""
import argparse
import codecs
from datetime import datetime
import fcntl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.metadata
import json
import os
from pathlib import Path
import pty
import select
import struct
import subprocess
import tempfile
import termios
import threading
import time

import pyte
from wcwidth import wcswidth


SECRET = "fixture-only-credential-do-not-log-g1b4"
PROMPT = "G1B4_PTY_PROMPT：展示状态栏"


class Screen(pyte.Screen):
    # Bubble Tea 使用 CSI S/T 差分滚动，pyte 0.8.2 需要补齐这两个标准指令。
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


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        try:
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            assert self.path == "/chat/completions"
            assert request["stream"] is True and request["model"] == "deepseek-flash"
            assert "reasoning_effort" not in request and "thinking" not in request
            assert self.headers.get("Authorization") == "Bearer " + SECRET
            self.server.request_count += 1
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()

            def send(delta=None, finish=None, usage=None):
                chunk = {"choices": [{"index": 0, "delta": delta or {}, "finish_reason": finish}]}
                if usage is not None:
                    chunk = {"choices": [], "usage": usage}
                self.wfile.write(("data: " + json.dumps(chunk, ensure_ascii=False) + "\n\n").encode())
                self.wfile.flush()

            send({"content": "本地状态栏验收回答。"})
            if self.server.hold_usage:
                self.server.response_started.set()
                if not self.server.release.wait(5):
                    raise TimeoutError("usage gate was not released")
            if self.server.scenario == "cancel":
                self.server.release.wait(3)
                return
            time.sleep(0.2)
            if self.server.scenario == "disconnect":
                return
            send(finish="stop")
            second = self.server.request_count > 1
            usage = {"prompt_tokens": 60 if second else 20, "completion_tokens": 5, "total_tokens": 65 if second else 25}
            if self.server.scenario != "unknown":
                usage["prompt_tokens_details"] = {"cached_tokens": 48 if second else 8}
            send(usage=usage)
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as error:
            # 不回显请求/鉴权内容；失败原因只保留异常类型。
            self.server.errors.append(type(error).__name__)


class Terminal:
    def __init__(self, binary, work, env, width, height, offline=False):
        self.width, self.height = width, height
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
        command = [str(binary), "chat"] + (["--offline"] if offline else [])
        self.process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave, cwd=work, env=env)
        os.close(slave)
        self.raw = bytearray()
        self.screen = Screen(width, height)
        self.stream = Stream(self.screen)
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self.replied = {}

    def send(self, data):
        os.write(self.master, data)

    def drain(self, seconds):
        deadline = time.monotonic() + seconds
        queries = {b"\x1b]11;?": b"\x1b]11;rgb:0000/0000/0000\x1b\\", b"\x1b[6n": b"\x1b[1;1R",
                   b"\x1b[c": b"\x1b[?1;2c", b"\x1b[?u": b"\x1b[?0u"}
        while time.monotonic() < deadline:
            if not select.select([self.master], [], [], 0.02)[0]:
                continue
            try:
                chunk = os.read(self.master, 65536)
            except OSError:
                return
            self.raw.extend(chunk)
            self.stream.feed(self.decoder.decode(chunk))
            for query, response in queries.items():
                count = self.raw.count(query)
                if count > self.replied.get(query, 0):
                    self.send(response * (count - self.replied.get(query, 0)))
                    self.replied[query] = count

    def capture(self, output, name, stage, rows):
        # 必须在退出 alt-screen 前捕获，不能拿退出后清空的屏幕当验收帧。
        assert b"\x1b[?1049h" in self.raw, (name, "alt-screen not entered")
        assert b"\x1b[?1049l" not in self.raw, (name, "captured after alt-screen exit")
        lines = self.screen.display
        assert len(lines) == self.height and all(wcswidth(line) == self.width for line in lines), (name, "frame geometry")
        borders = [i for i, line in enumerate(lines) if line == "─" * self.width]
        assert borders == [self.height - rows - 3, self.height - rows - 1], (name, stage, borders)
        assert self.screen.cursor.y == self.height - rows - 2, (name, stage, "input cursor", self.screen.cursor.y)
        assert 0 <= self.screen.cursor.x < self.width
        frame = "\n".join(lines) + "\n"
        assert SECRET not in frame and SECRET.encode() not in self.raw, name
        (output / f"{name}-{stage}.txt").write_text(frame)
        (output / f"{name}-{stage}.ansi").write_bytes(self.raw)
        return lines

    def close(self):
        try:
            if self.process.poll() is None:
                self.send(b"\x04")
                self.drain(0.25)
                self.process.wait(timeout=3)
        finally:
            if self.process.poll() is None:
                self.process.kill()
                self.process.wait()
            os.close(self.master)


def local_git(work):
    # 所有命令是参数化调用，临时仓库不继承真实仓库身份或 hook。
    for args in (("init", "--quiet", "--initial-branch=fixture-branch"),
                 ("add", "tracked.txt"),
                 ("-c", "user.name=PTY Fixture", "-c", "user.email=pty@example.invalid",
                  "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "建立验收临时仓库")):
        subprocess.run(["rtk", "proxy", "git", *args], cwd=work, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def events_from(home):
    return [json.loads(line) for path in (home / "logs").glob("chat-*.jsonl") for line in path.read_text().splitlines()]


def wait_terminal(terminal, home, name, count=1):
    deadline = time.monotonic() + 6
    while time.monotonic() < deadline:
        terminal.drain(0.05)
        ended = [e for e in events_from(home) if e["msg"] == "run_ended"]
        if len(ended) >= count:
            terminal.drain(0.15)
            return ended
        assert terminal.process.poll() is None, (name, "process exited before run terminal")
    raise AssertionError((name, "run terminal timeout"))


def item(field, label=None, **overrides):
    result = {"id": field, "enabled": True}
    if label is not None:
        result["label"] = label
    result.update(overrides)
    return result


def footer_colors(terminal, output, name, stage, rows):
    # 检查 pyte 当前屏幕的单元格属性，不能用历史 ANSI 字符串冒充可见配色。
    row = terminal.height - rows
    text = terminal.screen.display[row]
    # pyte 将 ANSI 93（亮黄色）命名为 brightbrown，按标准颜色码核对语义。
    assert "ctx:" not in text, (name, stage, "default context item visible", text)
    expected = [("Provider:", "5bc8c8", None), ("cache", "5bc8c8", None)]
    expected += [("[░░░░░░░░░░]0.0% 0/0", "7dd3d8", "cache ")] if stage == "startup" else [
        ("[████░░░░░░]40.0% 8/20", "7dd3d8", "cache ")]
    samples = []
    for value, color, anchor in expected:
        start = text.index(anchor) + len(anchor) if anchor else text.index(value)
        assert text[start:start + len(value)] == value, (name, stage, value, text)
        foregrounds = [terminal.screen.buffer[row][column].fg for column in range(start, start + len(value))]
        assert all(foreground == color for foreground in foregrounds), (name, stage, value, foregrounds, color)
        samples.append({"text": value, "row": row, "column": start, "foregrounds": foregrounds})
    colors = sorted({terminal.screen.buffer[row][column].fg for column in range(terminal.width)
                     if terminal.screen.buffer[row][column].data.strip()})
    assert {"5bc8c8", "7dd3d8"}.issubset(colors), (name, stage, colors)
    proof = {"source": "pyte visible footer cells", "colors": colors, "samples": samples,
             "color_names": {"brightbrown": "ANSI 93 bright yellow"}}
    (output / f"{name}-{stage}-colors.json").write_text(json.dumps(proof, ensure_ascii=False, indent=2) + "\n")
    return proof


def run_case(binary, output, case):
    name, width, height, scenario = case["name"], case["width"], case["height"], case.get("scenario", "success")
    server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    server.scenario, server.request_count, server.errors, server.release = scenario, 0, [], threading.Event()
    server.hold_usage, server.response_started = case.get("hold_usage", False), threading.Event()
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    result = {}
    with tempfile.TemporaryDirectory(prefix="plume-g1b4-") as temporary:
        root = Path(temporary)
        home, work = root / "home", root / "workspace"
        home.mkdir(mode=0o700)
        work.mkdir()
        (work / "tracked.txt").write_text("initial\n")
        local_git(work)
        (work / "tracked.txt").write_text("dirty\n")
        (work / "uv.lock").write_text("version = 1\n")
        (work / ".venv").mkdir()
        (home / "credentials").mkdir(mode=0o700)
        (home / "credentials" / "fixture-key").write_text(SECRET)
        (home / "credentials" / "fixture-key").chmod(0o600)
        cfg = {"schema_version": 1, "default_model": "fixture", "models": [{"id": "fixture", "provider": "deepseek", "protocol": "deepseek",
               "model": "deepseek-flash", "base_url": f"http://127.0.0.1:{server.server_port}", "api_key_ref": "fixture-key",
               "context_window_tokens": case.get("capacity", 100)}], "tui": {"status_messages": {"preparing": ["preparing"], "waiting": ["waiting"], "thinking": ["Thought"], "responding": ["responding"]}}}
        if "status_line" in case:
            cfg["tui"]["status_line"] = case["status_line"]
        initial = "{invalid json" if scenario == "offline" else json.dumps(cfg, ensure_ascii=False)
        (home / "config.json").write_text(initial)
        env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        env.update(PLUME_HOME=str(home), VIRTUAL_ENV=str(work / ".venv"), TERM="xterm-256color", NO_COLOR="1")
        env.pop("COLORTERM", None)
        if case.get("truecolor"):
            env.pop("NO_COLOR", None)
            env["COLORTERM"] = "truecolor"
        terminal = Terminal(binary, work, env, width, height, scenario == "offline")
        cancelled_ms = None
        pending_frames = []

        def capture_pending(call):
            # 先让正文进入真实终端，再放行 usage，避免靠固定延时猜测生成阶段。
            deadline = time.monotonic() + 4
            while not server.response_started.is_set():
                assert time.monotonic() < deadline, (name, "response did not start")
                terminal.drain(0.05)
            terminal.drain(0.15)
            lines = terminal.capture(output, name, f"call-{call}-before-usage", case["rows"])
            status = "\n".join(lines[-case["rows"]:])
            expected_tokens = "0" if call == 1 else "20"
            expected_ratio = "0.0" if call == 1 else "20.0"
            if case.get("capacity") == 1000000:
                expected_ratio = "0.0"
            capacity = "1M" if case.get("capacity") == 1000000 else "100"
            expected_cache = "cache [░░░░░░░░░░]0.0% 0/0" if call == 1 else "cache [████░░░░░░]40.0% 8/20"
            assert f"ctx: {expected_ratio}% {expected_tokens}/{capacity}" in status, (name, call, status)
            assert expected_cache in status and "unknown" not in status and "partial" not in status, (name, call, status)
            assert "本地状态栏验收回答。" in "\n".join(lines), (name, "answer was not rendered")
            pending_frames.append(status)
            server.release.set()

        try:
            terminal.drain(0.8)
            startup = terminal.capture(output, name, "startup", case["rows"])
            if width >= 80:
                startup_frame = "\n".join(startup)
                assert any("\u2800" <= char <= "\u28ff" for char in startup_frame), (name, "startup feather missing")
                assert "╰" in startup_frame and "╯" in startup_frame, (name, "startup rounded border missing")
            if "startup_check" in case:
                case["startup_check"]("\n".join(startup[-case["rows"]:]), "\n".join(startup))
            if case.get("truecolor"):
                footer_colors(terminal, output, name, "startup", case["rows"])
            terminal.send(PROMPT.encode() + b"\r")
            if server.hold_usage:
                capture_pending(1)
            if scenario == "cancel":
                terminal.drain(0.3)
                terminal.capture(output, name, "running", case["rows"])
                cancelled_ms = time.time_ns() / 1_000_000
                terminal.send(b"\x1b")
            ended = wait_terminal(terminal, home, name)
            assert len(ended) == 1, (name, ended)
            if case.get("calls", 1) == 2:
                first = terminal.capture(output, name, "first-call", case["rows"])
                assert "cache [████░░░░░░]40.0% 8/20" in "\n".join(first[-case["rows"]:]), (name, first)
                server.release.clear()
                server.response_started.clear()
                terminal.send(PROMPT.encode() + b"\r")
                if server.hold_usage:
                    capture_pending(2)
                ended = wait_terminal(terminal, home, name, count=2)
                assert len(ended) == 2, (name, ended)
            expected_error = {"cancel": "cancelled", "disconnect": "stream_interrupted"}.get(scenario, "")
            assert all(event["error_code"] == expected_error for event in ended), (name, ended)
            lines = terminal.capture(output, name, "pre-exit", case["rows"])
            status = "\n".join(lines[-case["rows"]:]) if case["rows"] else ""
            case["check"](status, "\n".join(lines))
            assert "?" not in status, (name, "question mark placeholder", status)
            colors = footer_colors(terminal, output, name, "pre-exit", case["rows"]) if case.get("truecolor") else None
            events = events_from(home)
            trace = "".join(json.dumps(event, ensure_ascii=False) + "\n" for event in events)
            assert SECRET not in trace and PROMPT not in trace and str(work) not in trace, name
            assert server.errors == [], (name, server.errors)
            assert server.request_count == (0 if scenario == "offline" else case.get("calls", 1)), (name, "request count", server.request_count)
            (output / f"{name}.jsonl").write_text(trace)
            if scenario == "offline":
                assert (home / "config.json").read_text() == initial, "offline modified invalid config"
            else:
                persisted = json.loads((home / "config.json").read_text())
                assert "status_line" in persisted["tui"] and "context_bar" in persisted["tui"]["status_line"]
                expected_scope = case.get("status_line", {}).get("cache_scope", "session")
                assert persisted["tui"]["status_line"]["cache_scope"] == expected_scope
                expected_format = case.get("status_line", {}).get("cache_format", "bar")
                assert persisted["tui"]["status_line"]["cache_format"] == expected_format
                expected_context = case.get("status_line", {}).get("context_format", "usage")
                assert persisted["tui"]["status_line"]["context_format"] == expected_context
                assert persisted["tui"]["status_line"]["cache_bar"] == case.get("status_line", {}).get("cache_bar", {"width": 10, "style": "unicode"})
                if "items" not in case.get("status_line", {}):
                    context_items = [entry for entry in persisted["tui"]["status_line"]["items"] if entry["id"] == "context"]
                    assert len(context_items) == 1 and context_items[0]["enabled"] is False, (name, context_items)
                assert all("context_window_tokens" in model for model in persisted["models"])
                (output / f"{name}-config.json").write_text(json.dumps(persisted, ensure_ascii=False, indent=2) + "\n")
            result = {"case": name, "status": "passed", "width": width, "height": height, "status_rows": case["rows"],
                      "request_count": server.request_count, "cursor": {"x": terminal.screen.cursor.x, "y": terminal.screen.cursor.y},
                      "status_frame": status, "terminal": ended[-1]}
            if colors is not None:
                result["footer_colors"] = colors
            if pending_frames:
                result["before_usage_frames"] = pending_frames
            if cancelled_ms is not None:
                ended_ms = datetime.strptime(ended[0]["ts"], "%Y-%m-%dT%H:%M:%S.%f%z").timestamp() * 1000
                result["cancel_latency_ms"] = ended_ms - cancelled_ms
        finally:
            terminal.close()
            (output / f"{name}.ansi").write_bytes(terminal.raw)
            server.release.set()
            server.shutdown()
            server.server_close()
        assert terminal.process.returncode == 0, (name, terminal.process.returncode)
    return result


def contains(*values):
    def check(status, frame):
        assert all(value in status for value in values), (values, status)
    return check


def default_check(status, frame):
    contains("Provider: deepseek", "deepseek-flash", "cache [████░░░░░░]40.0% 8/20", "git:fixture-branch*", "env:uv/.venv(active)", "session:", "idle")(status, frame)
    assert "ctx:" not in status and "ctx(last)" not in status, status


def default_startup_check(status, frame):
    contains("Provider: deepseek", "cache [░░░░░░░░░░]0.0% 0/0")(status, frame)
    assert "ctx:" not in status, status


def custom_check(status, frame):
    values = ["缓存: 8 (40%)", "容量: [#----] 20%", "模型:deepseek-flash", "累计:25"]
    assert all(value in status for value in values), status
    assert [status.index(value) for value in values] == sorted(status.index(value) for value in values), status
    assert "git:" not in status and "think:" not in status and "env:" not in status, status


def disabled_check(status, frame):
    assert not status and "ctx:" not in frame and "cache [" not in frame and "session:" not in frame, frame
    assert "本地状态栏验收回答" in frame, frame


def cancel_check(status, frame):
    assert "[cancelled]" in frame and "ctx: usage unknown" in status and "20.0%" not in status and "?" not in status, frame


def disconnect_check(status, frame):
    assert "stream_interrupted" in frame and "ctx: usage unknown" in status and "20.0%" not in status and "?" not in status, frame


def offline_check(status, frame):
    assert "offline" in frame and "unknown" in status, frame


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=Path("./plume"))
    parser.add_argument("--output", type=Path, default=Path("docs/reviews/evidence/G1b.4/pty"))
    parser.add_argument("--case", help="仅运行指定用例，供复现失败")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    cases = [
        {"name": "default-120x24", "width": 120, "height": 24, "rows": 2, "startup_check": default_startup_check, "check": default_check},
        {"name": "custom-80x24", "width": 80, "height": 24, "rows": 1, "status_line": {"max_rows": 1, "separator": " / ", "cache_format": "both", "context_format": "bar",
         "context_bar": {"width": 5, "style": "ascii"}, "items": [item("cache", "缓存", row=2), item("context", "容量"), item("model", "模型"), item("session_usage", "累计", enabled=True)]}, "check": custom_check},
        {"name": "narrow-20x8", "width": 20, "height": 8, "rows": 1, "status_line": {"items": [item("context")]}, "check": contains("ctx: 20.0% 20/100")},
        {"name": "unknown-40x16", "width": 40, "height": 16, "rows": 1, "scenario": "unknown", "capacity": None,
         "status_line": {"items": [item("context", "c", priority=80), item("cache", "ca", row=2, priority=70)]}, "check": contains("c: capacity unknown", "ca unknown")},
        {"name": "disabled-120x24", "width": 120, "height": 24, "rows": 0, "status_line": {"enabled": False, "items": []}, "check": disabled_check},
        {"name": "cancel-80x24", "width": 80, "height": 24, "rows": 1, "scenario": "cancel",
         "status_line": {"items": [item("context"), item("cache")]}, "check": cancel_check},
        {"name": "disconnect-80x24", "width": 80, "height": 24, "rows": 1, "scenario": "disconnect",
         "status_line": {"items": [item("context"), item("cache")]}, "check": disconnect_check},
        {"name": "offline-invalid-80x24", "width": 80, "height": 24, "rows": 2, "scenario": "offline", "check": offline_check},
        {"name": "truecolor-wide-220x24", "width": 220, "height": 24, "rows": 1, "truecolor": True,
         "startup_check": default_startup_check, "check": default_check},
        {"name": "cache-session-80x24", "width": 80, "height": 24, "rows": 1, "calls": 2,
         "hold_usage": True, "status_line": {"items": [item("context"), item("cache")]}, "check": contains("ctx: 60.0% 60/100", "cache [███████░░░]70.0% 56/80")},
        {"name": "cache-last-call-80x24", "width": 80, "height": 24, "rows": 1, "calls": 2,
         "hold_usage": True, "status_line": {"cache_scope": "last_call", "items": [item("context"), item("cache")]}, "check": contains("ctx: 60.0% 60/100", "cache [████████░░]80.0% 48/60")},
        {"name": "context-1m-80x24", "width": 80, "height": 24, "rows": 1, "capacity": 1000000,
         "hold_usage": True,
         "status_line": {"items": [item("context"), item("cache")]},
         "startup_check": contains("ctx: 0.0% 0/1M", "cache [░░░░░░░░░░]0.0% 0/0"), "check": contains("ctx: 0.0% 20/1M", "cache [████░░░░░░]40.0% 8/20")},
    ]
    selected = [case for case in cases if not args.case or case["name"] == args.case]
    assert selected, "unknown case"
    results = []
    for case in selected:
        results.append(run_case(args.binary.resolve(), args.output, case))
        print("G1B4_PTY_CASE_OK " + case["name"], flush=True)
    report = {"cases": len(results), "results": results, "dependencies": {name: importlib.metadata.version(name) for name in ("pyte", "wcwidth")},
              "limits": ["本地人工 SSE fixture，不代表真实供应商性能或缓存命中率。", "pyte 重建 ANSI 帧，未替代真实终端输入法/kitty/Windows 人工验证。"]}
    filename = "results.json" if not args.case else args.case + "-result.json"
    (args.output / filename).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(f"G1B4_PTY_OK cases={len(results)}")


if __name__ == "__main__":
    main()
