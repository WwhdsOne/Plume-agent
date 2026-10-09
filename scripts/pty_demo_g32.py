#!/usr/bin/env python3
"""G3.2 安装版验收：隔离人格文件、本地 SDK 流与真实聊天终端。"""
import argparse
import json
import os
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import tempfile
import threading
import time

from pty_demo_g1b4 import Terminal, events_from, wait_terminal

INITIAL, EDITED = "G32_INITIAL_SOUL_PRIVATE", "G32_EDITED_SOUL_PRIVATE"
PROMPT = "G32_USER_INPUT_PRIVATE"


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        try:
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            self.server.calls += 1
            index, case = self.server.calls, self.server.case
            messages = request["messages"]
            assert request["stream"] is True
            assert sum(m["role"] == "system" for m in messages) == 1
            text = messages[0]["content"]
            if case == "snapshot" and index >= 3:
                # 第三次 run 读取失败不提交历史；第四次请求仅带两轮成功记录。
                assert len(messages) == (6 if index == 3 else 8)
            if case in ("disabled", "missing"):
                assert "Personality (soul.md):" not in text
            else:
                expected = EDITED if case == "snapshot" and index >= 3 else INITIAL
                assert text.count(expected) == 1
                assert "WORKSPACE_SOUL_MUST_NOT_LOAD" not in text
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()

            def send(delta, finish=None):
                event = {"choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
                self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
                self.wfile.flush()

            if case == "snapshot" and index == 1:
                self.server.soul.write_text(EDITED)
                send({"tool_calls": [{"index": 0, "id": "clock", "type": "function", "function": {"name": "current_time", "arguments": "{}"}}]}, "tool_calls")
            elif case == "snapshot" and index == 4:
                send({"content": "cancellation fixture"})
                self.server.waiting.set()
                self.server.release.wait(6)
                return
            else:
                send({"content": "snapshot ready" if index == 2 else "reload ready"}, "stop")
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as error:
            self.server.errors.append(type(error).__name__)


def run_case(binary, output, case):
    server = None
    with tempfile.TemporaryDirectory(prefix="plume-g32-") as temp:
        home, work = Path(temp) / "home", Path(temp) / "work"
        home.mkdir(mode=0o700)
        work.mkdir()
        soul = home / ("custom.md" if case == "custom" else "soul.md")
        (work / "soul.md").write_text("WORKSPACE_SOUL_MUST_NOT_LOAD")
        if case != "missing":
            soul.write_bytes(b"\xff" if case in ("disabled", "offline") else INITIAL.encode())
        if case != "offline":
            server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
            server.daemon_threads = True
            server.calls, server.errors, server.case, server.soul = 0, [], case, soul
            server.waiting, server.release = threading.Event(), threading.Event()
            threading.Thread(target=server.serve_forever, daemon=True).start()
            cfg = {"schema_version": 1, "default_model": "fixture", "models": [{"id": "fixture", "provider": "deepseek", "protocol": "deepseek", "model": "fixture", "base_url": f"http://127.0.0.1:{server.server_port}"}],
                   "agent": {"soul": {"enabled": case != "disabled", "path": soul.name, "max_bytes": 1 if case == "too_large" else 65536}}, "tools": {"enabled": ["current_time"]}}
            config_text = json.dumps(cfg)
        else:
            config_text = "{invalid config - offline must not read it"
        (home / "config.json").write_text(config_text)
        env = dict(os.environ, PLUME_HOME=str(home), TERM="xterm-256color", COLORTERM="truecolor")
        terminal = Terminal(binary, work, env, 120, 36, offline=case == "offline")
        try:
            terminal.drain(.7)
            terminal.send(PROMPT.encode() + b"\r")
            ended = wait_terminal(terminal, home, case)
            if case == "snapshot":
                assert server.calls == 2 and ended[0]["error_code"] == ""
                terminal.send(PROMPT.encode() + b"\r")
                ended = wait_terminal(terminal, home, case, count=2)
                assert server.calls == 3 and ended[1]["error_code"] == ""
                soul.write_bytes(b"\xff")
                terminal.send(PROMPT.encode() + b"\r")
                ended = wait_terminal(terminal, home, case, count=3)
                assert server.calls == 3 and ended[2]["error_code"] == "invalid_config"
                soul.write_text(EDITED)
                terminal.send(PROMPT.encode() + b"\r")
                deadline = time.monotonic() + 6
                while not server.waiting.is_set():
                    terminal.drain(.05)
                    assert time.monotonic() < deadline, "cancellation fixture not reached"
                terminal.send(b"\x1b")
                ended = wait_terminal(terminal, home, case, count=4)
                assert ended[3]["error_code"] == "cancelled" and server.calls == 4
            elif case == "too_large":
                assert server.calls == 0 and ended[0]["error_code"] == "invalid_config"
            else:
                assert ended[0]["error_code"] == ""
                assert server is None or server.calls == 1
            events = events_from(home)
            trace = "".join(json.dumps(e, ensure_ascii=False) + "\n" for e in events)
            assert all(marker not in trace for marker in (INITIAL, EDITED, PROMPT))
            assert len({e["run_id"] for e in ended}) == len(ended)
            assert sum(e["msg"] == "model_start" for e in events) == sum(e["msg"] in ("model_completed", "model_failed") for e in events)
            assert server is None or not server.errors, "SDK fixture request assertion failed"
            if case == "offline":
                assert (home / "config.json").read_text() == config_text and soul.read_bytes() == b"\xff"
            else:
                migrated = json.loads((home / "config.json").read_text())["agent"]["soul"]
                assert migrated == cfg["agent"]["soul"]
            (output / f"{case}.jsonl").write_text(trace)
            result = {"case": case, "status": "passed", "http_calls": server.calls if server else 0,
                      "model_calls": sum(e["msg"] == "model_start" for e in events), "tool_calls": sum(e["msg"] == "tool_started" for e in events),
                      "run_terminals": [e["error_code"] or "completed" for e in ended]}
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
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    cases = [run_case(args.binary.resolve(), args.out, name) for name in ("snapshot", "custom", "disabled", "missing", "too_large", "offline")]
    (args.out / "results.json").write_text(json.dumps(cases, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"passed": len(cases), "cases": cases}, ensure_ascii=False))


if __name__ == "__main__":
    main()
