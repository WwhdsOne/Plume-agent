#!/usr/bin/env python3
"""G4a.2.1 隔离 PTY 验收：对话检查点与中断恢复。

本地 OpenAI 兼容 fixture（分步流式 + gate 挂起）驱动在线路径，临时
PLUME_HOME 隔离配置。场景对齐方案 §5 验收矩阵核心行：
1) 流式中取消后退出：退出摘要打印真实保存结论；`-c` 合并检查点展示半截
   正文与终态标记，恢复零模型请求，↑ 回填原输入但不自动发送；
2) 运行中 kill -9：run_checkpoints 保留最后确认快照（已完成工具 + 半截
   正文），`-c` 打开标记 interrupted 并展示进度，无半组 turns；
3) 同进程 `//` 转义回填：字面正文回填后再 Enter 仍是普通消息（第二次
   请求），数据库 input 保存解码正文；
4) 空会话退出不打印恢复摘要；
5) 模型断流失败后退出重开：保留失败正文，↑ 回填但不自动发送；
6) 运行中 `/exit`：在同一收尾期限内取消并保存，不等待远端放行；
7) 可处理 SIGTERM 后重开：按实际落盘终态展示，不预设一定 cancelled。
"""
import json
import os
import signal
import sqlite3
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from pty_demo_g1b4 import Terminal, events_from, wait_terminal


class StagedFixture(BaseHTTPRequestHandler):
    """脚本化 SSE fixture：每个请求从 server.scripts 弹出一组步骤。

    step 形如 ("text", str) / ("tool", id, name, args) / ("hold",) / ("disconnect",) /
    ("finish", "stop") / ("usage", prompt, completion)。hold 等待 server.gate，用于制造"已发部分
    增量、终态未落盘"的运行中窗口。
    """

    def log_message(self, *args):
        pass

    def do_POST(self):
        try:
            self.rfile.read(int(self.headers["Content-Length"]))
            self.server.calls += 1
            self.server.requests_seen += 1
            self.server.pending.set()
            steps = self.server.scripts.pop(0) if self.server.scripts else None
            # None 表示正常的默认回复，必须发送 stop 终态，避免失败 run 误充成功回填场景。
            if steps is None:
                steps = [("text", "plume reply"), ("finish", "stop")]
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()

            def send(delta=None, finish=None, usage=None):
                event = {"choices": [{"index": 0, "delta": delta or {}, "finish_reason": finish}]}
                if usage is not None:
                    event = {"choices": [], "usage": usage}
                self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
                self.wfile.flush()

            for step in steps:
                if step[0] == "text":
                    send({"content": step[1]})
                elif step[0] == "reasoning":
                    send({"reasoning_content": step[1]})
                elif step[0] == "tool":
                    send({"tool_calls": [{"index": 0, "id": step[1], "type": "function",
                                          "function": {"name": step[2], "arguments": step[3]}}]})
                elif step[0] == "finish":
                    send({}, step[1])
                elif step[0] == "usage":
                    send(usage={"prompt_tokens": step[1], "completion_tokens": step[2], "total_tokens": step[1] + step[2]})
                elif step[0] == "hold":
                    assert self.server.gate.wait(20), "gate timeout"
                elif step[0] == "sleep":
                    time.sleep(step[1])
                elif step[0] == "disconnect":
                    # 故意在 finish/[DONE] 前断开，区分模型断流与用户取消。
                    self.close_connection = True
                    return
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as error:  # noqa: BLE001
            self.server.errors.append(f"{type(error).__name__}: {error}")


def start_fixture(scripts):
    server = ThreadingHTTPServer(("127.0.0.1", 0), StagedFixture)
    server.daemon_threads = True
    server.calls, server.errors, server.requests_seen = 0, [], 0
    server.scripts = list(scripts)
    server.pending = threading.Event()
    server.gate = threading.Event()
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def write_config(home, port, shutdown_timeout_ms=5000):
    # 状态栏固定单行（同 g4a1 演示），便于帧断言稳定。
    (home / "config.json").write_text(json.dumps({
        "schema_version": 1, "default_model": "model-a",
        "models": [{"id": "model-a", "provider": "custom-openai", "protocol": "openai-compatible",
                    "model": "ckpt-a", "base_url": f"http://127.0.0.1:{port}", "api_key_ref": "fixture",
                    "context_window_tokens": 1000000}],
        "tools": {"enabled": ["calculate"]},
        "sessions": {"shutdown_timeout_ms": shutdown_timeout_ms},
        "tui": {"status_line": {"max_rows": 1, "items": [
            {"id": "provider", "enabled": True}, {"id": "model", "enabled": True}]}},
    }))
    credentials = home / "credentials"
    credentials.mkdir(mode=0o700, exist_ok=True)
    (credentials / "fixture").write_text("fixture-only-g4a21-not-a-real-key")
    (credentials / "fixture").chmod(0o600)


def db_path(home):
    return home / "sessions" / "history.db"


def query(home, sql, args=()):
    connection = sqlite3.connect(f"file:{db_path(home)}?mode=ro", uri=True)
    try:
        return connection.execute(sql, args).fetchall()
    finally:
        connection.close()


def open_terminal(binary, home, work, args=None):
    env = dict(os.environ)
    env.update(PLUME_HOME=str(home), TERM="xterm-256color", COLORTERM="truecolor")
    return Terminal(binary, work, env, 120, 36, args=args)


def drain_for(terminal, seconds):
    """在等待窗口内持续读取 PTY 输出：缓冲满会背压 TUI 渲染与事件消费，
    进而卡住 app 层的检查点写入，必须在时间窗内保持读取。"""
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        terminal.drain(0.05)


def exit_tui(terminal):
    # 退出摘要打印在进程退出前（alt screen 关闭后写 stdout）；PTY master
    # 必须持续读取，等待进程退出期间无人读会导致 slave 关闭后数据不可读。
    terminal.send(b"\x04")  # Ctrl+D：空输入退出
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline and terminal.process.poll() is None:
        terminal.drain(0.05)
    terminal.process.wait(timeout=5)
    terminal.drain(0.3)


def wait_process_exit(terminal, timeout):
    """读取 PTY 直到进程退出，记录实际耗时，避免输出背压伪装成收尾超时。"""
    started = time.monotonic()
    deadline = started + timeout
    while terminal.process.poll() is None and time.monotonic() < deadline:
        terminal.drain(0.05)
    terminal.process.wait(timeout=max(0.05, deadline - time.monotonic()))
    elapsed = time.monotonic() - started
    terminal.drain(0.3)
    return elapsed


def wait_checkpoint(terminal, home, text):
    """确认指定正文已经落入检查点再触发退出，不以固定等待假定保存完成。"""
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        terminal.drain(0.05)
        rows = query(home, "SELECT sequence, payload FROM run_checkpoints")
        if rows and text in json.loads(rows[0][1])["reply"]:
            return rows[0][0]
        assert terminal.process.poll() is None, "process exited before checkpoint"
    raise AssertionError("checkpoint was not confirmed before exit")


def cleanup_terminals(*terminals):
    """失败清理先清草稿，避免 Ctrl+D 被输入吞掉后掩盖原始场景断言。"""
    for terminal in terminals:
        if terminal is None:
            continue
        if terminal.process.poll() is None:
            terminal.send(b"\x15")
            terminal.drain(0.1)
        try:
            terminal.close()
        except subprocess.TimeoutExpired:
            # Terminal.close 已在 finally 强杀并回收；保留原场景的错误。
            print("G4A21_PTY_CLEANUP_TIMEOUT killed=True", flush=True)


def case_cancel_exit_resume(binary, output, home, work):
    """场景 1：流式中 Esc 取消 → 退出打印保存结论 → `-c` 合并检查点展示，
    ↑ 回填不自动发送，恢复阶段零模型请求。"""
    server = start_fixture([[
        ("text", "partial checkpoint answer "),
        ("hold",),
        ("text", " never confirmed"),
        ("usage", 12, 3),
        ("finish", "stop"),
    ]])
    write_config(home, server.server_port)
    try:
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        terminal.send(b"cancel me midstream\r")
        assert server.pending.wait(5), "request never arrived"
        # 等待期间持续 drain：PTY 缓冲满会背压事件消费，进而卡住检查点写入。
        drain_for(terminal, 0.6)
        terminal.send(b"\x1b")  # Esc：取消当前 run
        wait_terminal(terminal, home, "cancel")
        frame = "\n".join(terminal.capture(output, "cancel", "after-cancel", 1))
        assert "partial checkpoint answer" in frame, frame
        exit_tui(terminal)
        assert terminal.process.returncode == 0

        # 退出摘要：绑定本轮 run 的真实保存结论与恢复方法。
        tail = terminal.raw.decode("utf-8", "replace")
        assert "Session: " in tail and "History: saved" in tail, tail[-400:]
        assert "plume -c" in tail and "/resume " in tail, tail[-400:]
        rows = query(home, "SELECT status, reply FROM runs")
        assert len(rows) == 1 and rows[0][0] == "cancelled", rows
        session_id = query(home, "SELECT id FROM sessions")[0][0]
        assert session_id in tail, "summary must print the full session id"

        # `-c` 重开：合并检查点展示半截正文与终态标记，恢复零请求。
        server.pending.clear()
        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.8)
        frame = "\n".join(resume.capture(output, "cancel-resume", "restored", 1))
        assert "partial checkpoint answer" in frame and "cancelled" in frame, frame
        assert "Session restored" in frame and "Last task: cancelled" in frame, frame
        assert "Press ↑" in frame, frame
        assert server.calls == 1, "restoring must not call the model"

        # ↑ 回填原输入：输入框出现候选，未按 Enter 前不发送。
        resume.send(b"\x1b[A")  # ↑
        resume.drain(0.3)
        frame = "\n".join(resume.capture(output, "cancel-resume", "backfill", 1))
        assert "cancel me midstream" in frame, frame
        assert server.calls == 1, "backfill must not send a request"
        resume.send(b"\x15")  # Ctrl+U 清行
        resume.drain(0.2)
        exit_tui(resume)
        assert server.calls == 1, "no automatic resend at exit"
        result = {"case": "cancel-exit-resume", "status": "passed", "model_calls": server.calls,
                  "runs": 1, "checkpoint_shown": True}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()
    return result


def case_kill_midstream(binary, output, home, work):
    """场景 2：工具完成后第二段正文流出中途 kill -9——数据库保留最后确认
    检查点（completed 工具 + 半截正文），`-c` 打开标记 interrupted 并展示。"""
    server = start_fixture([
        [("tool", "call-1", "calculate", '{"operation":"add","a":1,"b":2}'), ("usage", 9, 2), ("finish", "tool_calls")],
        [("text", "step two part"), ("sleep", 1.4), ("text", "ial body "), ("hold",), ("usage", 9, 4), ("finish", "stop")],
    ])
    write_config(home, server.server_port)
    try:
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        terminal.send(b"run tools then die\r")
        assert server.pending.wait(5), "first request never arrived"
        server.pending.clear()
        assert server.pending.wait(5), "second request never arrived"
        # 持续读取（PTY 背压会卡住事件消费与检查点写入），等待覆盖 fixture
        # 的静默 sleep：跨过 1 秒合并间隔后的第二个增量触发周期写，正文
        # 进入检查点——这是"合并窗口内增量不逐条落盘"的正常语义。
        drain_for(terminal, 2.6)
        terminal.process.kill()  # SIGKILL：模拟崩溃
        terminal.process.wait(timeout=10)

        rows = query(home, "SELECT status FROM runs ORDER BY started_at")
        assert rows == [("running",)], rows
        checkpoint = query(home, "SELECT sequence, payload FROM run_checkpoints")
        assert len(checkpoint) == 1 and checkpoint[0][0] >= 1, checkpoint
        payload = json.loads(checkpoint[0][1])
        assert "step two partial body" in payload["reply"], payload
        assert payload["tools"] and payload["tools"][0]["status"] == "completed", payload
        assert len(query(home, "SELECT seq FROM turns")) == 0, "crashed run must not commit a turn"

        # `-c` 重开：遗留 running 标记 interrupted，检查点进度可见。
        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.8)
        frame = "\n".join(resume.capture(output, "kill-resume", "restored", 1))
        assert "interrupted" in frame and "step two partial body" in frame, frame
        assert "calculate" in frame and "Last task: interrupted" in frame, frame
        assert server.calls == 2, "resume must not call the model"
        exit_tui(resume)
        statuses = query(home, "SELECT status FROM runs ORDER BY started_at")
        assert statuses == [("interrupted",)], statuses
        return {"case": "kill-midstream", "status": "passed", "run": "interrupted",
                "checkpoint_sequence": checkpoint[0][0], "checkpoint_reply_chars": len(payload["reply"]),
                "turns": 0}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_slash_literal_backfill(binary, output, home, work):
    """场景 3：同进程 `//` 转义正文回填后仍是普通消息；数据库保存解码正文。"""
    server = start_fixture([None, None])  # None 弹出默认整段回复。
    write_config(home, server.server_port)
    try:
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        terminal.send(b"//exit is literal\r")  # 转义正文，不是退出指令
        wait_terminal(terminal, home, "literal-1")
        assert server.calls == 1, server.calls
        terminal.send(b"\x1b[A")  # ↑ 回填
        terminal.drain(0.3)
        frame = "\n".join(terminal.capture(output, "literal", "backfill", 1))
        assert "//exit is literal" in frame, frame
        terminal.send(b"\r")  # Enter：仍是普通消息，发出第二个请求
        wait_terminal(terminal, home, "literal-2", 2)
        assert server.calls == 2, f"backfilled literal must resend as message: {server.calls}"
        exit_tui(terminal)
        inputs = [row[0] for row in query(home, "SELECT input FROM runs ORDER BY started_at")]
        assert inputs == ["/exit is literal", "/exit is literal"], inputs
        statuses = query(home, "SELECT status FROM runs ORDER BY started_at")
        assert statuses == [("completed",), ("completed",)], statuses
        assert len(query(home, "SELECT seq FROM turns")) == 2, "both literal messages must commit successful turns"
        assert not server.errors, server.errors
        return {"case": "slash-literal-backfill", "status": "passed", "model_calls": server.calls,
                "stored_inputs": len(inputs), "completed_runs": len(statuses), "turns": 2}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_empty_exit_no_summary(binary, output, home, work):
    """场景 4：空会话（无已登记 run）退出不打印恢复摘要。"""
    server = start_fixture([])
    write_config(home, server.server_port)
    try:
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        exit_tui(terminal)
        tail = terminal.raw.decode("utf-8", "replace")
        assert "Session: " not in tail and "plume -c" not in tail, tail[-300:]
        # 历史开启时空会话同样打开数据库（G4a.1 语义），但不登记任何行。
        assert query(home, "SELECT COUNT(*) FROM sessions")[0][0] == 0, "empty session must not persist rows"
        return {"case": "empty-exit-no-summary", "status": "passed", "summary": False}
    finally:
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_stream_failure_resume(binary, output, home, work):
    """场景 5：模型断流保存 failed，重开只展示和回填，不自动重试失败请求。"""
    reply = "partial before broken stream"
    original_input = "recover the broken stream input"
    server = start_fixture([[("text", reply), ("disconnect",)]])
    write_config(home, server.server_port)
    terminal, resume = None, None
    try:
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        terminal.send(original_input.encode() + b"\r")
        wait_terminal(terminal, home, "stream-failure")
        frame = "\n".join(terminal.capture(output, "stream-failure", "failed", 1))
        assert reply in frame, frame
        rows = query(home, "SELECT id, status, reply FROM runs")
        assert len(rows) == 1 and rows[0][1] == "failed" and reply in rows[0][2], rows
        ended = [event for event in events_from(home) if event.get("msg") == "run_ended"]
        assert len(ended) == 1 and ended[0]["error_code"] == "stream_interrupted", ended
        assert not query(home, "SELECT seq FROM turns"), "failed stream must not commit successful context"
        exit_tui(terminal)
        assert terminal.process.returncode == 0
        assert "History: saved" in terminal.raw.decode("utf-8", "replace")

        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.8)
        frame = "\n".join(resume.capture(output, "stream-failure-resume", "restored", 1))
        assert reply in frame and "Last task: failed" in frame and "Press ↑" in frame, frame
        assert server.calls == 1, "restoring a failed run must not retry the model"
        resume.send(b"\x1b[A")
        resume.drain(0.3)
        frame = "\n".join(resume.capture(output, "stream-failure-resume", "backfill", 1))
        assert original_input in frame, frame
        assert server.calls == 1, "backfill must not retry the failed request"
        resume.send(b"\x15")
        resume.drain(0.1)
        exit_tui(resume)
        assert resume.process.returncode == 0 and server.calls == 1
        assert query(home, "SELECT id, status, reply FROM runs") == rows, "recovery must retain the original failed run"
        assert not query(home, "SELECT seq FROM turns")
        assert not any(event.get("msg") == "tool_started" for event in events_from(home))
        assert not server.errors, server.errors
        return {"case": "stream-failure-exit-resume", "status": "passed", "model_calls": server.calls,
                "run": "failed", "error_code": "stream_interrupted", "turns": 0, "backfill_requests": 0}
    finally:
        cleanup_terminals(terminal, resume)
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_active_exit_deadline(binary, output, home, work):
    """场景 6：模型仍挂起时 /exit 共用配置期限收尾，终态与退出摘要一致。"""
    reply = "partial while exiting active task"
    shutdown_timeout_ms = 1000
    server = start_fixture([[("text", reply), ("hold",), ("finish", "stop")]])
    write_config(home, server.server_port, shutdown_timeout_ms)
    terminal, resume = None, None
    try:
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        terminal.send(b"exit while the model is held\r")
        assert server.pending.wait(5), "request never arrived"
        sequence = wait_checkpoint(terminal, home, reply)
        terminal.capture(output, "active-exit", "before-exit", 1)
        terminal.send(b"/exit\r")
        # 给 PTY 调度留少量余量，但不能等待 fixture 的 20 秒 gate 超时。
        elapsed = wait_process_exit(terminal, shutdown_timeout_ms / 1000 + 2)
        assert elapsed <= shutdown_timeout_ms / 1000 + 2, elapsed
        assert terminal.process.returncode == 0, terminal.raw.decode("utf-8", "replace")[-600:]
        tail = terminal.raw.decode("utf-8", "replace")
        assert "History: saved" in tail and "plume -c" in tail, tail[-500:]
        (output / "active-exit-summary.ansi").write_bytes(terminal.raw)
        rows = query(home, "SELECT status, reply FROM runs")
        assert rows == [("cancelled", reply)], rows
        assert not query(home, "SELECT seq FROM turns"), "active exit must not publish an unfinished turn"
        assert not query(home, "SELECT sequence FROM run_checkpoints"), "terminal commit must clear its checkpoint"

        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.8)
        frame = "\n".join(resume.capture(output, "active-exit-resume", "restored", 1))
        assert reply in frame and "Last task: cancelled" in frame, frame
        assert server.calls == 1, "active exit recovery must not retry the model"
        exit_tui(resume)
        assert resume.process.returncode == 0 and server.calls == 1
        assert not server.errors, server.errors
        return {"case": "active-exit-deadline", "status": "passed", "model_calls": server.calls,
                "shutdown_timeout_ms": shutdown_timeout_ms, "exit_elapsed_ms": round(elapsed * 1000, 2),
                "checkpoint_sequence": sequence, "run": "cancelled", "history": "saved", "turns": 0}
    finally:
        cleanup_terminals(terminal, resume)
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_sigterm_resume(binary, output, home, work):
    """场景 7：SIGTERM 尝试收尾，重开按真实终态或遗留 running 的 interrupted 展示。"""
    reply = "partial before handled SIGTERM"
    shutdown_timeout_ms = 1000
    server = start_fixture([[("text", reply), ("hold",), ("finish", "stop")]])
    write_config(home, server.server_port, shutdown_timeout_ms)
    terminal, resume = None, None
    try:
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        terminal.send(b"handle SIGTERM without replay\r")
        assert server.pending.wait(5), "request never arrived"
        sequence = wait_checkpoint(terminal, home, reply)
        terminal.capture(output, "sigterm", "before-signal", 1)
        terminal.process.send_signal(signal.SIGTERM)
        elapsed = wait_process_exit(terminal, shutdown_timeout_ms / 1000 + 2)
        assert terminal.process.returncode >= 0, "SIGTERM must reach the handled exit path"
        (output / "sigterm-exit.ansi").write_bytes(terminal.raw)
        rows = query(home, "SELECT status FROM runs")
        assert len(rows) == 1 and rows[0][0] in {"running", "cancelled", "failed", "interrupted"}, rows
        before_status = rows[0][0]
        expected_status = "interrupted" if before_status == "running" else before_status
        assert not query(home, "SELECT seq FROM turns"), "SIGTERM must not publish the unfinished response"

        resume = open_terminal(binary, home, work, args=["-c"])
        resume.drain(0.8)
        frame = "\n".join(resume.capture(output, "sigterm-resume", "restored", 1))
        assert reply in frame and f"Last task: {expected_status}" in frame, frame
        assert query(home, "SELECT status FROM runs") == [(expected_status,)]
        assert server.calls == 1, "SIGTERM recovery must not retry the model or tools"
        assert not any(event.get("msg") == "tool_started" for event in events_from(home))
        exit_tui(resume)
        assert resume.process.returncode == 0 and server.calls == 1
        assert not server.errors, server.errors
        return {"case": "sigterm-exit-resume", "status": "passed", "model_calls": server.calls,
                "exit_code": terminal.process.returncode, "exit_elapsed_ms": round(elapsed * 1000, 2),
                "checkpoint_sequence": sequence, "saved_run": before_status, "restored_run": expected_status,
                "turns": 0}
    finally:
        cleanup_terminals(terminal, resume)
        server.gate.set()
        server.shutdown()
        server.server_close()


CASES = [case_cancel_exit_resume, case_kill_midstream, case_slash_literal_backfill, case_empty_exit_no_summary,
         case_stream_failure_resume, case_active_exit_deadline, case_sigterm_resume]


def main():
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=Path("./plume"))
    parser.add_argument("--output", type=Path, default=Path("docs/reviews/evidence/G4a.2.1/pty"))
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    results = []
    for case in CASES:
        with tempfile.TemporaryDirectory(prefix="plume-g4a21-") as temp:
            home, work = Path(temp) / "home", Path(temp) / "work"
            home.mkdir(mode=0o700)
            work.mkdir()
            results.append(case(args.binary.resolve(), args.output, home, work))
        print("G4A21_PTY_CASE_OK " + results[-1]["case"], flush=True)
    (args.output / "results.json").write_text(json.dumps({
        "cases": len(results), "results": results,
        "limits": ["本地 fixture，不代表真实模型延迟；演示凭据非真实 Key；SIGTERM 仅覆盖当前终端库可处理路径，未覆盖 SIGHUP。"],
    }, ensure_ascii=False, indent=2) + "\n")
    print(f"G4A21_PTY_OK cases={len(results)}")


if __name__ == "__main__":
    main()
