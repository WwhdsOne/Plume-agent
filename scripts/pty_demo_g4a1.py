#!/usr/bin/env python3
"""G4a.1 隔离 PTY 验收：持久历史与 `plume -c` 显式继续。

本地 OpenAI 兼容 fixture 驱动在线路径（不联网、不用真实 Key），临时
PLUME_HOME 隔离配置。场景对齐方案 §7 G4a.1 验收表：
1) 成功轮次落盘 + `-c` 恢复上下文继续对话；
2) default_model 改为其他配置后 `-c` 仍按会话引用恢复原模型；
3) Ctrl+N 新会话与旧会话在持久层分开，`-c` 恢复最新；
4) 运行中 kill -9 后遗留 running 被标 interrupted，无半组 turns、usage 不伪零；
5) 同库第二进程被明确拒绝，持锁进程退出后可取锁；
6) 参数互斥与禁用历史的明确报错。
"""
import json
import os
import sqlite3
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from pty_demo_g1b4 import Terminal, wait_terminal


class Fixture(BaseHTTPRequestHandler):
    """本地 OpenAI 兼容 SSE fixture：记录请求，回复带序号的固定文本。"""

    def log_message(self, *args):
        pass

    def do_POST(self):
        try:
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            self.server.calls += 1
            self.server.requests.append(request)
            self.server.pending.set()
            # gate 默认放行；crash 场景显式 clear 挂起答复，制造运行中崩溃窗口。
            assert self.server.gate.wait(5), "gate timeout"
            index = self.server.calls
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()

            def send(delta=None, finish=None, usage=None):
                event = {"choices": [{"index": 0, "delta": delta or {}, "finish_reason": finish}]}
                if usage is not None:
                    event = {"choices": [], "usage": usage}
                self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
                self.wfile.flush()

            send({"content": f"plume persisted reply {index}"}, "stop")
            send(usage={"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12,
                        "prompt_tokens_details": {"cached_tokens": 4}})
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as error:
            self.server.errors.append(f"{type(error).__name__}: {error}")


def start_fixture():
    server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    server.daemon_threads = True
    server.calls, server.errors, server.requests = 0, [], []
    server.pending = threading.Event()
    server.gate = threading.Event()
    server.gate.set()  # 默认立即放行答复
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def write_config(home, port, default="model-a", extra=None):
    """写指向本地 fixture 的配置；凭据文件为演示占位，不是真实 Key。
    状态栏固定单行（G1b.4 契约的显式配置路径），便于帧断言稳定。"""
    models = [
        {"id": "model-a", "provider": "custom-openai", "protocol": "openai-compatible", "model": "persist-a",
         "base_url": f"http://127.0.0.1:{port}", "api_key_ref": "fixture", "context_window_tokens": 1000000},
    ]
    if extra:
        models.extend(extra)
    (home / "config.json").write_text(json.dumps({
        "schema_version": 1, "default_model": default, "models": models,
        "tui": {"status_line": {"max_rows": 1, "items": [
            {"id": "provider", "enabled": True}, {"id": "model", "enabled": True},
            {"id": "context", "enabled": True}, {"id": "cache", "enabled": True}]}},
    }))
    credentials = home / "credentials"
    credentials.mkdir(mode=0o700, exist_ok=True)
    (credentials / "fixture").write_text("fixture-only-g4a1-not-a-real-key")
    (credentials / "fixture").chmod(0o600)


def db_path(home):
    return home / "sessions" / "history.db"


def query(home, sql, args=()):
    connection = sqlite3.connect(f"file:{db_path(home)}?mode=ro", uri=True)
    try:
        return connection.execute(sql, args).fetchall()
    finally:
        connection.close()


def workspace_env(home, work):
    env = dict(os.environ)
    env.update(PLUME_HOME=str(home), TERM="xterm-256color", COLORTERM="truecolor")
    return env


def open_terminal(binary, home, work, args=None, width=120, height=36):
    return Terminal(binary, work, workspace_env(home, work), width, height, args=args)


def run_turn(terminal, home, name, text, wait_reply=True):
    terminal.drain(0.7)
    terminal.send(text.encode() + b"\r")
    if wait_reply:
        wait_terminal(terminal, home, name)
        terminal.drain(0.2)


def exit_tui(terminal):
    terminal.send(b"\x04")  # Ctrl+D：空输入退出
    terminal.process.wait(timeout=10)
    terminal.drain(0.2)


def case_persist_and_resume(binary, output, home, work):
    """场景 1：成功轮次先落盘再进内存；`-c` 预灌上下文，第二轮请求携带旧轮次。"""
    server = start_fixture()
    write_config(home, server.server_port)
    try:
        terminal = open_terminal(binary, home, work)
        run_turn(terminal, home, "persist-first", "first persisted message")
        frame = "\n".join(terminal.capture(output, "persist-first", "reply", 1))
        assert "plume persisted reply 1" in frame, frame
        exit_tui(terminal)
        assert terminal.process.returncode == 0

        rows = query(home, "SELECT status, input, reply FROM runs")
        assert len(rows) == 1 and rows[0][0] == "completed" and rows[0][1] == "first persisted message", rows
        assert len(query(home, "SELECT seq, messages FROM turns")) == 1, "one committed turn"
        first_session = query(home, "SELECT id FROM sessions")[0][0]

        # 第二进程 `-c`：恢复后新请求必须携带旧轮次（fixture 记录 messages）。
        server.pending.clear()
        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.7)
        frame = "\n".join(resume.capture(output, "persist-resume", "transcript", 1))
        assert "Resumed session" in frame and "first persisted message" in frame, frame
        # -c 的首次画面必须恢复用量，不靠发送新请求来刷新状态栏。
        assert "ctx:" in frame and "] 10/1M" in frame and "ctx: usage unknown" not in frame, frame
        assert "cache: 40%" in frame, frame
        assert server.calls == 1, "resuming the status bar must not request the model"
        run_turn(resume, home, "persist-second", "second message")
        exit_tui(resume)
        assert resume.process.returncode == 0

        second_request = server.requests[-1]
        contents = [m["content"] for m in second_request["messages"]]
        assert "first persisted message" in contents and "plume persisted reply 1" in contents, contents
        assert len(query(home, "SELECT seq FROM turns")) == 2, "second turn appended"
        sessions = query(home, "SELECT id FROM sessions")
        assert len(sessions) == 1 and sessions[0][0] == first_session, sessions
        result = {"case": "persist-and-resume", "status": "passed", "runs": 2, "turns": 2, "sessions": 1,
                  "restored_prompt_tokens": 10, "restored_cache_percent": 40}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()
    return result


def case_model_ref_match(binary, output, home, work):
    """场景 2：default_model 改为其他配置后，`-c` 仍按会话引用恢复原模型。"""
    primary, fallback = start_fixture(), start_fixture()
    try:
        write_config(home, primary.server_port)
        terminal = open_terminal(binary, home, work)
        run_turn(terminal, home, "refmatch-first", "model ref message")
        exit_tui(terminal)

        # default_model 切到 B；`-c` 仍恢复 A 的会话（引用匹配优先于默认值）。
        write_config(home, primary.server_port, default="model-b", extra=[
            {"id": "model-b", "provider": "custom-openai", "protocol": "openai-compatible", "model": "persist-b",
             "base_url": f"http://127.0.0.1:{fallback.server_port}", "api_key_ref": "fixture"}])
        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.7)
        frame = "\n".join(resume.capture(output, "refmatch-resume", "transcript", 1))
        assert "Resumed session" in frame and "model ref message" in frame, frame
        run_turn(resume, home, "refmatch-second", "still model a")
        exit_tui(resume)

        assert primary.calls == 2, f"resumed session must keep model-a endpoint ({primary.calls} calls)"
        assert fallback.calls == 0, "model-b must not receive the resumed conversation"
        model_ids = [request["model"] for request in primary.requests]
        assert model_ids == ["persist-a", "persist-a"], model_ids
        return {"case": "model-ref-match", "status": "passed", "primary_calls": 2, "fallback_calls": 0}
    finally:
        for server in (primary, fallback):
            server.gate.set()
            server.shutdown()
            server.server_close()


def case_ctrl_n_new_session(binary, output, home, work):
    """场景 3：Ctrl+N 建立新会话 ID；新旧会话在持久层分开，`-c` 恢复最新。"""
    server = start_fixture()
    write_config(home, server.server_port)
    try:
        terminal = open_terminal(binary, home, work, args=["-c"])
        terminal.drain(0.7)
        run_turn(terminal, home, "ctrln-turn", "old session turn")
        old_session = query(home, "SELECT id FROM sessions")[0][0]
        terminal.send(b"\x0e")  # Ctrl+N：新会话
        terminal.drain(0.5)
        frame = "\n".join(terminal.capture(output, "ctrln-new", "after-reset", 1))
        assert "New session started" in frame, frame
        run_turn(terminal, home, "ctrln-fresh", "fresh session message")
        exit_tui(terminal)

        sessions = [row[0] for row in query(home, "SELECT id FROM sessions ORDER BY last_run_at")]
        assert len(sessions) == 2 and sessions[0] == old_session, sessions
        new_session = sessions[1]
        turns_old = query(home, "SELECT COUNT(*) FROM turns WHERE session_id = ?", (old_session,))[0][0]
        turns_new = query(home, "SELECT COUNT(*) FROM turns WHERE session_id = ?", (new_session,))[0][0]
        assert turns_old == 1 and turns_new == 1, (turns_old, turns_new)

        # `-c` 恢复最新（Ctrl+N 后的新会话）：转录不含旧会话消息。
        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.7)
        frame = "\n".join(resume.capture(output, "ctrln-resume", "latest", 1))
        assert "fresh session message" in frame and "old session turn" not in frame, frame
        exit_tui(resume)
        return {"case": "ctrl-n-new-session", "status": "passed", "sessions": 2, "turns_per_session": [1, 1]}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_crash_interrupted(binary, output, home, work):
    """场景 4：同会话先成功一轮，再运行中 kill -9；`-c` 打开时遗留
    running 被标记 interrupted，旧 turns 完好，无半组轮次，
    未完成调用的 usage 保持 NULL 不伪零。"""
    server = start_fixture()
    write_config(home, server.server_port)
    try:
        terminal = open_terminal(binary, home, work)
        run_turn(terminal, home, "crash-survive", "surviving turn")
        server.gate.clear()  # 下一轮答复挂起，制造"请求已发出、终态未落盘"
        terminal.send(b"crash me\r")
        assert server.pending.wait(5), "model request never arrived"
        terminal.drain(0.3)
        terminal.process.kill()  # SIGKILL：模拟进程崩溃
        terminal.process.wait(timeout=10)
        terminal.drain(0.1)
        statuses = [row[0] for row in query(home, "SELECT status FROM runs ORDER BY started_at")]
        assert statuses == ["completed", "running"], statuses
        crash_run = query(home, "SELECT id FROM runs WHERE status = 'running'")[0][0]

        # `-c` 恢复同一会话：打开数据库时遗留 running 被标记 interrupted。
        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.7)
        frame = "\n".join(resume.capture(output, "crash-resume", "transcript", 1))
        assert "Resumed session" in frame and "2 runs" in frame, frame
        assert "surviving turn" in frame and "plume persisted reply 1" in frame, frame
        assert "crash me" in frame and "interrupted" in frame, frame
        exit_tui(resume)

        assert query(home, "SELECT status FROM runs WHERE id = ?", (crash_run,))[0][0] == "interrupted", "must be interrupted"
        usage_value = query(home, "SELECT usage FROM model_calls WHERE run_id = ?", (crash_run,))[0][0]
        assert usage_value is None, f"unfinished call usage must stay NULL, got {usage_value!r}"
        # 崩溃 run 不产生 turns：只有幸存那一轮。
        assert len(query(home, "SELECT seq FROM turns")) == 1, "crashed run must not commit a turn"
        return {"case": "crash-interrupted", "status": "passed", "crash_run": "interrupted", "usage": "NULL", "turns": 1}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_second_process_lock(binary, output, home, work):
    """场景 5：整库进程独占——持锁 TUI 运行中，第二进程被明确拒绝；
    持锁进程退出后可重新取锁。"""
    holder = start_fixture()
    try:
        write_config(home, holder.server_port)
        first = open_terminal(binary, home, work)
        first.drain(1.0)
        assert first.process.poll() is None, "holder must stay running"

        # 第二进程也要在 pty 里跑：TTY 判定在取锁之前，管道进程走不到锁检查。
        second = open_terminal(binary, home, work, args=["-c"])
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and second.process.poll() is None:
            second.drain(0.05)
        assert second.process.poll() not in (None, 0), "second process must exit non-zero"
        second.drain(0.1)
        captured = second.raw.decode("utf-8", "replace")
        assert "history database is locked" in captured, captured[-500:]
        second.close()

        exit_tui(first)
        retry = open_terminal(binary, home, work, args=["-c"])
        retry.drain(0.8)
        assert retry.process.poll() is None, "lock must be released after holder exits"
        exit_tui(retry)
        return {"case": "second-process-lock", "status": "passed", "rejected": True}
    finally:
        holder.gate.set()
        holder.shutdown()
        holder.server_close()


def case_flag_errors(binary, output, home, work):
    """场景 6：--offline 与 -c 互斥；禁用历史后 -c 明确报错，不静默开新会话。"""
    env = workspace_env(home, work)
    mixed = subprocess.run([str(binary), "chat", "--offline", "-c"], stdin=subprocess.DEVNULL,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT, cwd=work, env=env, timeout=15)
    assert mixed.returncode != 0 and "mutually exclusive" in mixed.stdout.decode(), mixed.stdout.decode()

    server = start_fixture()
    try:
        write_config(home, server.server_port)
        (home / "config.json").write_text(json.dumps({
            "schema_version": 1, "default_model": "model-a",
            "models": [{"id": "model-a", "provider": "custom-openai", "protocol": "openai-compatible",
                        "model": "persist-a", "base_url": f"http://127.0.0.1:{server.server_port}",
                        "api_key_ref": "fixture"}],
            "sessions": {"history": {"enabled": False}},
        }))
        # 禁用历史后 `-c` 在 pty 里被明确拒绝（TTY 判定在配置读取之前）。
        disabled = open_terminal(binary, home, work, args=["-c"])
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and disabled.process.poll() is None:
            disabled.drain(0.05)
        assert disabled.process.poll() not in (None, 0), "disabled -c must exit non-zero"
        disabled.drain(0.1)
        captured = disabled.raw.decode("utf-8", "replace")
        assert "session history is disabled" in captured, captured[-500:]
        disabled.close()
        # 禁用历史时不创建数据库文件。
        assert not db_path(home).exists(), "disabled history must not create the database"
        return {"case": "flag-errors", "status": "passed", "offline_mix_rejected": True, "disabled_rejected": True}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()


CASES = [case_persist_and_resume, case_model_ref_match, case_ctrl_n_new_session,
         case_crash_interrupted, case_second_process_lock, case_flag_errors]


def main():
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=Path("./plume"))
    parser.add_argument("--output", type=Path, default=Path("docs/reviews/evidence/G4a.1/pty"))
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    results = []
    for case in CASES:
        with tempfile.TemporaryDirectory(prefix="plume-g4a1-") as temp:
            home, work = Path(temp) / "home", Path(temp) / "work"
            home.mkdir(mode=0o700)
            work.mkdir()
            results.append(case(args.binary.resolve(), args.output, home, work))
        print("G4A1_PTY_CASE_OK " + results[-1]["case"], flush=True)
    (args.output / "results.json").write_text(json.dumps({
        "cases": len(results), "results": results,
        "limits": ["本地 fixture，不代表真实模型延迟；演示凭据非真实 Key。"],
    }, ensure_ascii=False, indent=2) + "\n")
    print(f"G4A1_PTY_OK cases={len(results)}")


if __name__ == "__main__":
    main()
