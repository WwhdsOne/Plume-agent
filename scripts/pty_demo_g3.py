#!/usr/bin/env python3
"""G3 隔离 PTY 验收：复用终端捕获器；离线脚本与本地 SDK 工具 fixture 分开。"""
import argparse
import json
import os
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import tempfile
import threading
import time

from pty_demo_g1b4 import Terminal, events_from, wait_terminal


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        try:
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            assert request["stream"] is True
            assert [tool["function"]["name"] for tool in request["tools"]] == ["calculate", "current_time"]
            self.server.calls += 1
            index = self.server.calls
            if index == 2:
                assert request["messages"][2]["reasoning_content"] == "G3 retained thought"
                assert request["messages"][3]["tool_call_id"] == "c"
                assert request["messages"][3]["content"] == '{"ok":true,"result":42}'
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()

            def send(delta=None, finish=None, usage=None):
                event = {"choices": [{"index": 0, "delta": delta or {}, "finish_reason": finish}]}
                if usage is not None:
                    event = {"choices": [], "usage": usage}
                self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
                self.wfile.flush()

            if index == 1:
                send({"reasoning_content": "G3 retained thought", "tool_calls": [{"index": 0, "id": "c", "type": "function", "function": {"name": "calculate", "arguments": '{"operation":"multiply",'}}]})
                send({"tool_calls": [{"index": 0, "function": {"arguments": '"a":6,"b":7}'}}]}, "tool_calls")
            else:
                self.server.pending.set()
                assert self.server.release.wait(5), "usage gate timeout"
                send({"content": "6 × 7 = **42**"}, "stop")
            send(usage={"prompt_tokens": 10 if index == 1 else 20, "completion_tokens": 1, "total_tokens": 11 if index == 1 else 21, "prompt_cache_hit_tokens": 4 if index == 1 else 16})
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as error:
            self.server.errors.append(type(error).__name__)


def run_case(binary, output, name, prompt, expected, error="", fixture=False, width=120):
    server = None
    with tempfile.TemporaryDirectory(prefix="plume-g3-") as temp:
        home, work = Path(temp) / "home", Path(temp) / "work"
        home.mkdir(mode=0o700)
        work.mkdir()
        initial = "{invalid config - offline must not read it"
        rows = 2 if width >= 60 else 1
        if fixture:
            rows = 1
            server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
            server.daemon_threads = True
            server.calls, server.errors = 0, []
            server.pending, server.release = threading.Event(), threading.Event()
            threading.Thread(target=server.serve_forever, daemon=True).start()
            (home / "credentials").mkdir(mode=0o700)
            (home / "credentials" / "fixture").write_text("fixture-only-g3-not-a-real-key")
            (home / "credentials" / "fixture").chmod(0o600)
            initial = json.dumps({"schema_version": 1, "default_model": "test", "tools": {"enabled": ["calculate", "current_time"]}, "models": [{"id": "test", "provider": "deepseek", "protocol": "deepseek", "model": "deepseek-flash", "base_url": f"http://127.0.0.1:{server.server_port}", "api_key_ref": "fixture", "context_window_tokens": 1000000}], "tui": {"status_line": {"max_rows": 1, "items": [{"id": "provider", "enabled": True}, {"id": "context", "enabled": True}, {"id": "cache", "enabled": True}]}}})
        (home / "config.json").write_text(initial)
        env = dict(os.environ)
        env.update(PLUME_HOME=str(home), TERM="xterm-256color", COLORTERM="truecolor")
        terminal = Terminal(binary, work, env, width, 36, offline=not fixture)
        try:
            terminal.drain(.7)
            terminal.send(prompt.encode() + b"\r")
            cancelled = None
            if fixture:
                deadline = time.monotonic() + 5
                while not server.pending.is_set():
                    terminal.drain(.05)
                    assert time.monotonic() < deadline
                terminal.drain(.1)
                frame = "\n".join(terminal.capture(output, name, "before-second-usage", rows))
                assert "ctx: 0.0% 10/1M" in frame and "40.0% 4/10" in frame and "partial" not in frame
                assert "calculate · completed · 42" in frame
                server.release.set()
            if error == "cancelled":
                deadline = time.monotonic() + 5
                while not any(event["msg"] == "tool_completed" for event in events_from(home)):
                    terminal.drain(.03)
                    assert time.monotonic() < deadline
                cancelled = time.monotonic()
                terminal.send(b"\x1b")
            ended = wait_terminal(terminal, home, name)
            assert len(ended) == 1 and ended[0]["error_code"] == error
            latency = (time.monotonic() - cancelled) * 1000 if cancelled is not None else None
            frame = "\n".join(terminal.capture(output, name, "final", rows))
            assert expected in frame, (name, "expected visible content missing", frame)
            events = events_from(home)
            models = [event for event in events if event["msg"] == "model_start"]
            tools = [event for event in events if event["msg"] == "tool_started"]
            assert len({event["model_call_id"] for event in models}) == len(models)
            assert sum(event["msg"] in ("model_completed", "model_failed") for event in events) == len(models)
            assert sum(event["msg"] in ("tool_completed", "tool_failed") for event in events) == len(tools)
            trace = "".join(json.dumps(event, ensure_ascii=False) + "\n" for event in events)
            assert prompt not in trace and "fixture-only-g3" not in trace and "G3 retained thought" not in trace
            (output / f"{name}.jsonl").write_text(trace)
            if not fixture:
                assert (home / "config.json").read_text() == initial
                if name == "workspace":
                    assert (work / "plume-demo.txt").read_text() == "goodbye plume\n"
                    assert [event["tool"] for event in tools] == ["write", "glob", "grep", "read", "edit", "bash"]
            else:
                assert server.calls == 2 and not server.errors
                assert "ctx: 0.0% 20/1M" in frame and "66.7% 20/30" in frame
            result = {"case": name, "status": "passed", "error_code": error, "model_calls": len(models), "tool_attempts": len(tools), "width": width, "height": 36, "cancel_to_capture_ms": latency, "run_metrics": ended[0]}
        finally:
            if server:
                server.release.set()
                server.shutdown()
                server.server_close()
            terminal.close()
        assert terminal.process.returncode == 0
        return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=Path("./plume"))
    parser.add_argument("--output", type=Path, default=Path("docs/reviews/evidence/G3.1/pty"))
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    cases = [
        ("calculate", "/demo calculate", "6 × 7 = 42", "", False, 120),
        ("time", "/demo time", "current_time · completed", "", False, 120),
        ("correct-error", "/demo error", "Corrected denominator", "", False, 120),
        ("budget", "/demo budget", "12 tools completed", "", False, 120),
        ("workspace", "/demo workspace", "Workspace verified", "", False, 120),
        ("cancel", "/demo cancel", "cancelled", "cancelled", False, 120),
        ("narrow", "/demo calculate", "calculate · completed · 42", "", False, 50),
        ("sdk-fixture", "G3 local SDK fixture", "6 × 7 = 42", "", True, 120),
    ]
    results = []
    for case in cases:
        results.append(run_case(args.binary.resolve(), args.output, *case))
        print("G3_PTY_CASE_OK " + case[0], flush=True)
    (args.output / "results.json").write_text(json.dumps({"cases": len(results), "results": results, "limits": ["本地脚本与 SDK fixture，不代表真实模型性能。", "cancel_to_capture_ms 包含 150ms 捕获等待，不等于请求退出延迟。"]}, ensure_ascii=False, indent=2) + "\n")
    print(f"G3_PTY_OK cases={len(results)}")


if __name__ == "__main__":
    main()
