#!/usr/bin/env python3
"""验证安装版默认 ctx 进度条/数量与 cache 纯百分比，所有数据来自本地 fixture。"""
import argparse
import json
import os
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import tempfile
import threading

from pty_demo_g1b4 import Terminal, events_from, wait_terminal


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        chunks = [
            {"choices": [{"index": 0, "delta": {"content": "status format fixture"}, "finish_reason": "stop"}]},
            {"choices": [], "usage": {"prompt_tokens": 200000, "completion_tokens": 1, "total_tokens": 200001, "prompt_cache_hit_tokens": 50000}},
        ]
        for chunk in chunks:
            self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="plume-status-format-") as temp:
        home, work = Path(temp) / "home", Path(temp) / "work"
        home.mkdir(mode=0o700)
        work.mkdir()
        server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        config = {"schema_version": 1, "default_model": "fixture", "models": [{"id": "fixture", "provider": "deepseek", "protocol": "deepseek", "model": "deepseek-flash", "base_url": f"http://127.0.0.1:{server.server_port}", "context_window_tokens": 1000000}],
                  "tui": {"status_line": {"max_rows": 1, "items": [{"id": "context"}, {"id": "cache"}]}}}
        (home / "config.json").write_text(json.dumps(config))
        terminal = Terminal(args.binary.resolve(), work, dict(os.environ, PLUME_HOME=str(home), TERM="xterm-256color", COLORTERM="truecolor"), 80, 24)
        try:
            terminal.drain(.7)
            initial = "\n".join(terminal.capture(args.out, "status", "initial", 1))
            assert "ctx: [░░░░░░░░░░] 0/1M" in initial and "cache: 0%" in initial
            terminal.send(b"format fixture\r")
            ended = wait_terminal(terminal, home, "format")
            assert len(ended) == 1 and ended[0]["error_code"] == ""
            frame = "\n".join(terminal.capture(args.out, "status", "reported", 1))
            expected = "ctx: [██░░░░░░░░] 200k/1M │ cache: 25%"
            assert expected in frame and "50k/200k" not in frame and "cache [" not in frame
            migrated = json.loads((home / "config.json").read_text())["tui"]["status_line"]
            assert migrated["context_format"] == "bar" and migrated["cache_format"] == "ratio"
            assert migrated["context_bar"]["show_percent"] is False and migrated["items"][0]["enabled"] is True
            trace = events_from(home)
            (args.out / "trace.jsonl").write_text("".join(json.dumps(e, ensure_ascii=False) + "\n" for e in trace))
            result = {"status": "passed", "initial": "ctx: [░░░░░░░░░░] 0/1M │ cache: 0%", "reported": expected, "rows": 1, "width": 80, "height": 24}
            (args.out / "results.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
            print(json.dumps(result, ensure_ascii=False))
        finally:
            terminal.close()
            server.shutdown()
            server.server_close()
        assert terminal.process.returncode == 0


if __name__ == "__main__":
    main()
