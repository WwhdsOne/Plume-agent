#!/usr/bin/env python3
"""G4a.2 隔离 PTY：命令、建议、历史选择/分页与运行中取消。

只用本地 SSE fixture 和临时 PLUME_HOME；截图证据保存为终端帧与 ANSI，
不读取用户凭据、不访问付费模型。数据库断言和请求计数共同验证命令不进模型。
"""
import argparse
from datetime import datetime, timezone
import hashlib
import importlib.metadata
import json
import platform
import subprocess
import tempfile
import time
from pathlib import Path

from wcwidth import wcswidth
from pty_demo_g1b4 import Terminal, wait_terminal, events_from
from pty_demo_g4a1 import start_fixture, write_config, open_terminal, query, workspace_env


def frame(terminal, output, name):
    """在 alt-screen 内记录帧，检查中文可见宽度与终端未意外退出。"""
    terminal.drain(0.2)
    assert terminal.process.poll() is None, name
    assert b"\x1b[?1049h" in terminal.raw and b"\x1b[?1049l" not in terminal.raw, (name, "alt-screen")
    assert b"fixture-only-g4a1-not-a-real-key" not in terminal.raw, (name, "credential leaked")
    lines = terminal.screen.display
    assert all(wcswidth(line) == terminal.width for line in lines), (name, "frame width")
    text = "\n".join(lines)
    (output / f"{name}.txt").write_text(text + "\n")
    (output / f"{name}.ansi").write_bytes(terminal.raw)
    return text


def enter(terminal, text):
    # Ctrl+U 使用 textarea 的行编辑操作，避免测试借助空闲双 Ctrl+C 误退出。
    terminal.send(b"\x15" + text.encode() + b"\r")
    terminal.drain(0.25)


def dismiss(terminal):
    terminal.send(b"\x1b")
    terminal.drain(0.15)


def stop(terminal):
    terminal.send(b"\x04")
    terminal.drain(0.3)
    terminal.process.wait(timeout=6)
    assert terminal.process.returncode == 0, terminal.process.returncode


def cleanup_terminal(terminal):
    """失败清理先关面板、清草稿，避免 Ctrl+D 被输入状态吞掉后掩盖原始断言。"""
    if terminal.process.poll() is None:
        dismiss(terminal)
        terminal.send(b"\x15")
        terminal.drain(0.1)
    try:
        terminal.close()
    except subprocess.TimeoutExpired:
        # Terminal.close 的 finally 已 kill/wait；保留现场主错误而非替换为清理超时。
        print("PTY_CLEANUP_TIMEOUT killed=True", flush=True)


def configure(home, server):
    write_config(home, server.server_port)
    path = home / "config.json"
    cfg = json.loads(path.read_text())
    cfg["sessions"] = {"page_size": 2}
    path.write_text(json.dumps(cfg))


def capture_trace(home, output, name):
    """仅保留生命周期、身份和数字字段，正文/凭据不进入审核 trace 摘录。"""
    fields = {"msg", "session_id", "run_id", "span_id", "parent_span_id", "call_id",
              "model_call_id", "parent_model_call_id", "error_code", "save_error_code", "save_error_stage",
              "status", "run_status", "save_status", "error_class", "duration_ms",
              "model_calls", "tool_calls", "usage_known", "first_answer_ms", "prompt_tokens", "completion_tokens", "total_tokens"}
    # 模型使用 start/completed/failed 命名，不能仅按 started/ended 后缀漏掉取消分类。
    lifecycle = {"model_start", "model_completed", "model_failed", "tool_completed", "tool_failed"}
    events = [{key: value for key, value in event.items() if key in fields}
              for event in events_from(home)
              if event.get("msg", "").endswith(("started", "ended")) or event.get("msg") in lifecycle]
    (output / f"{name}-trace.json").write_text(json.dumps(events, ensure_ascii=False, indent=2)+"\n")


def configure_models(home, server, default="model-a", second_model="persist-b"):
    """两种模型共用隔离 fixture，仅通过请求的 model 字段区分恢复与新建基准。"""
    write_config(home, server.server_port, default=default, extra=[
        {"id": "model-b", "provider": "custom-openai", "protocol": "openai-compatible",
         "model": second_model, "base_url": f"http://127.0.0.1:{server.server_port}", "api_key_ref": "fixture"}])


def case_models(binary, output, home, work):
    """裸启动、-c 与显式 --model 的新建基准固定，不随之后的 /resume 改变。"""
    server = start_fixture()
    terminal = None
    try:
        configure_models(home, server)
        terminal = open_terminal(binary, home, work, args=["--model", "model-b"])
        terminal.drain(0.8)
        enter(terminal, "model b historical input")
        wait_terminal(terminal, home, "seed-b", count=1)
        b_id = query(home, "SELECT id FROM sessions WHERE title='model b historical input'")[0][0]
        stop(terminal)
        terminal.close()

        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        enter(terminal, "model a historical input")
        wait_terminal(terminal, home, "seed-a", count=2)
        a_id = query(home, "SELECT id FROM sessions WHERE title='model a historical input'")[0][0]
        enter(terminal, f"/resume {b_id}")
        text = frame(terminal, output, "model-b-resumed-from-bare-a")
        assert "model b historical input" in text and "persist-b" in text, text
        assert server.calls == 2
        enter(terminal, "continued model b input")
        wait_terminal(terminal, home, "continued-b", count=3)
        assert server.requests[-1]["model"] == "persist-b"
        contents = [item.get("content") for item in server.requests[-1]["messages"]]
        assert "model b historical input" in contents and "model a historical input" not in contents
        enter(terminal, "/new bare startup a")
        text = frame(terminal, output, "model-new-keeps-bare-a")
        assert "persist-a" in text and "model b historical input" not in text and server.calls == 3, text
        enter(terminal, "fresh bare baseline a")
        wait_terminal(terminal, home, "fresh-bare-a", count=4)
        assert server.requests[-1]["model"] == "persist-a"
        assert "model b historical input" not in [item.get("content") for item in server.requests[-1]["messages"]]
        stop(terminal)
        terminal.close()

        # 默认配置改 B，-c 仍从最近 A 历史决定本次新建基准；恢复 B 不改这个基准。
        configure_models(home, server, default="model-b")
        terminal = open_terminal(binary, home, work, args=["-c"])
        terminal.drain(0.8)
        text = frame(terminal, output, "continue-a-with-default-b")
        assert "fresh bare baseline a" in text and "persist-a" in text and server.calls == 4, text
        enter(terminal, f"/resume {b_id}")
        enter(terminal, "/new continued startup a")
        text = frame(terminal, output, "model-new-keeps-continued-a")
        assert "persist-a" in text and server.calls == 4, text
        enter(terminal, "fresh continued baseline a")
        wait_terminal(terminal, home, "fresh-continued-a", count=5)
        assert server.requests[-1]["model"] == "persist-a"
        stop(terminal)
        terminal.close()

        # 显式 --model B 优先于默认 A，恢复 A 后再新建仍使用 B。
        configure_models(home, server)
        terminal = open_terminal(binary, home, work, args=["--model", "model-b"])
        terminal.drain(0.8)
        enter(terminal, f"/resume {a_id}")
        enter(terminal, "/new explicit startup b")
        text = frame(terminal, output, "model-new-keeps-explicit-b")
        assert "persist-b" in text and server.calls == 5, text
        enter(terminal, "fresh explicit baseline b")
        wait_terminal(terminal, home, "fresh-explicit-b", count=6)
        assert server.requests[-1]["model"] == "persist-b"
        stop(terminal)
        models = [request["model"] for request in server.requests]
        assert models == ["persist-b", "persist-a", "persist-b", "persist-a", "persist-a", "persist-b"], models
        assert not server.errors, server.errors
        capture_trace(home, output, "model-baselines")
        return {"case": "startup-model-baselines", "status": "passed", "model_calls": 6,
                "request_models": models, "management_model_calls": 0}
    finally:
        if terminal: cleanup_terminal(terminal)
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_incompatible(binary, output, home, work):
    """模型引用失配仍可预览，恢复失败保留原会话、转录、输入与模型调用数。"""
    server = start_fixture()
    terminal = None
    try:
        configure_models(home, server)
        terminal = open_terminal(binary, home, work, args=["--model", "model-b"])
        terminal.drain(0.8)
        enter(terminal, "incompatible b historical input")
        wait_terminal(terminal, home, "incompatible-seed-b", count=1)
        b_id = query(home, "SELECT id FROM sessions")[0][0]
        stop(terminal)
        terminal.close()

        # 同 ConfigID 的模型已改变，不能把旧上下文静默发给替代模型。
        configure_models(home, server, second_model="persist-b-changed")
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        enter(terminal, "retained a transcript")
        wait_terminal(terminal, home, "incompatible-seed-a", count=2)
        a_id = query(home, "SELECT id FROM sessions WHERE title='retained a transcript'")[0][0]
        before = query(home, "SELECT session_id FROM bindings")
        enter(terminal, f"/history --session {b_id}")
        text = frame(terminal, output, "incompatible-history-preview")
        assert "incompatible b historical input" in text and "plume persisted reply 1" in text, text
        assert server.calls == 2 and query(home, "SELECT session_id FROM bindings") == before
        dismiss(terminal)
        draft = f"/resume {b_id}"
        enter(terminal, draft)
        text = frame(terminal, output, "incompatible-resume-preserves-draft")
        assert "resume_incompatible" in text and "retained a transcript" in text and draft in text, text
        assert "incompatible b historical input" not in text and "persist-a" in text, text
        assert query(home, "SELECT session_id FROM bindings") == before and before == [(a_id,)]
        assert server.calls == 2 and len(query(home, "SELECT seq FROM turns")) == 2
        # 失败命令应保留输入；显式清空后才能按空输入的 Ctrl+D 正常退出。
        terminal.send(b"\x15")
        terminal.drain(0.1)
        stop(terminal)
        assert not server.errors, server.errors
        capture_trace(home, output, "incompatible-restore")
        return {"case": "incompatible-preview-preserves-session-and-draft", "status": "passed",
                "model_calls": 2, "management_model_calls": 0, "binding_unchanged": True, "turns": 2}
    finally:
        if terminal: cleanup_terminal(terminal)
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_commands(binary, output, home, work):
    """前缀 Enter 不执行候选，中文命名和 /new 不调用模型。"""
    server = start_fixture()
    terminal = None
    try:
        configure(home, server)
        terminal = open_terminal(binary, home, work, width=100, height=32)
        terminal.drain(0.8)
        terminal.send(b"/re")
        text = frame(terminal, output, "prefix-re")
        assert "/resume" in text and "/rename" in text, text
        terminal.send(b"\r")
        text = frame(terminal, output, "prefix-enter-error")
        assert "unknown_command" in text and server.calls == 0, text
        enter(terminal, "/history 0")
        text = frame(terminal, output, "invalid-page-error")
        assert "invalid_arguments" in text and server.calls == 0, text
        enter(terminal, "/resume deadbeef")
        text = frame(terminal, output, "missing-session-error")
        assert "session_not_found" in text and server.calls == 0, text
        enter(terminal, "/sessions")
        frame(terminal, output, "unnamed-empty-session-hidden")
        assert not query(home, "SELECT id FROM sessions") and server.calls == 0
        dismiss(terminal)
        enter(terminal, "/help")
        text = frame(terminal, output, "help")
        assert "/new" in text and "/history" in text, text
        dismiss(terminal)
        enter(terminal, "第一条羽毛消息")
        wait_terminal(terminal, home, "first", count=1)
        text = frame(terminal, output, "first-reply")
        assert "plume persisted reply 1" in text, text
        first = query(home, "SELECT id,title FROM sessions")[0]
        assert first[1] == "第一条羽毛消息", first
        enter(terminal, "/rename 羽毛测试")
        assert query(home, "SELECT title FROM sessions WHERE id=?", (first[0],))[0][0] == "羽毛测试"
        enter(terminal, "/new 第二个会话")
        text = frame(terminal, output, "new-session")
        assert "plume persisted reply 1" not in text and server.calls == 1, text
        second = query(home, "SELECT id FROM sessions WHERE title='第二个会话'")[0][0]
        enter(terminal, "/sessions")
        text = frame(terminal, output, "session-directory")
        assert "羽毛测试" in text and "第二个会话" in text, text
        terminal.send(b"\t")
        text = frame(terminal, output, "session-preview")
        assert "History" in text and "第一条羽毛消息" in text and server.calls == 1, text
        terminal.send(b"\t")
        terminal.drain(0.1)
        terminal.send(b"\r")
        text = frame(terminal, output, "session-resume")
        assert "第一条羽毛消息" in text and "plume persisted reply 1" in text, text
        assert "cache: 40%" in text and "] 10/1M" in text, text
        enter(terminal, "//tmp/a.txt")
        wait_terminal(terminal, home, "resume", count=2)
        assert "第一条羽毛消息" in [item.get("content") for item in server.requests[-1]["messages"]]
        assert server.requests[-1]["messages"][-1]["content"] == "/tmp/a.txt"
        enter(terminal, "/status")
        text = frame(terminal, output, "status")
        assert "Created:" in text and "Completed runs:" in text and first[0] in text, text
        dismiss(terminal)
        enter(terminal, "/exit")
        terminal.process.wait(timeout=6)
        assert terminal.process.returncode == 0
        assert server.calls == 2 and len(query(home, "SELECT id FROM sessions")) == 2
        assert not server.errors, server.errors
        # 历史库当前迁移到 v4；公共指令回归同时守住检查点迁移后的兼容性。
        assert query(home, "PRAGMA user_version")[0][0] == 4
        capture_trace(home, output, "commands-success")
        return {"case": "commands-selector-resume", "status": "passed", "model_calls": 2, "sessions": 2,
                "schema": 4, "empty_named_id": second, "error_commands_model_calls": 0, "escaped_message": "/tmp/a.txt"}
    finally:
        if terminal: cleanup_terminal(terminal)
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_pages(binary, output, home, work):
    """历史最新页/旧页顺序正确，窄窗口翻页不产生额外模型调用。"""
    server = start_fixture()
    terminal = None
    try:
        configure(home, server)
        terminal = open_terminal(binary, home, work, width=48, height=20)
        terminal.drain(0.8)
        for index in range(1, 4):
            enter(terminal, f"page-message-{index}")
            wait_terminal(terminal, home, f"page-{index}", count=index)
        enter(terminal, "/history 1")
        text = frame(terminal, output, "history-latest-narrow")
        assert "page-message-2" in text and "page-message-3" in text and "page-message-1" not in text, text
        terminal.send(b"\x1b[6~")
        text = frame(terminal, output, "history-older-narrow")
        # 窄屏允许标题在 page 后折行；按可见词序核对页码，不把合法换行误判为分页失败。
        assert "page-message-1" in text and "page 2/2" in " ".join(text.split()), text
        dismiss(terminal)
        enter(terminal, "/clear")
        frame(terminal, output, "clear-keeps-session")
        assert server.calls == 3 and len(query(home, "SELECT seq FROM turns")) == 3
        stop(terminal)
        capture_trace(home, output, "history-pages")
        return {"case": "history-pagination-narrow", "status": "passed", "runs": 3, "page_size": 2, "model_calls": 3}
    finally:
        if terminal: cleanup_terminal(terminal)
        server.shutdown()
        server.server_close()


def case_busy(binary, output, home, work):
    """运行中只读命令可用，Esc 关闭面板后 /cancel 经同一路由终止任务。"""
    server = start_fixture()
    terminal = None
    try:
        configure(home, server)
        server.gate.clear()
        terminal = open_terminal(binary, home, work, width=100, height=30)
        terminal.drain(0.8)
        enter(terminal, "wait for cancellation")
        assert server.pending.wait(2)
        enter(terminal, "/status")
        text = frame(terminal, output, "busy-status")
        assert "running" in text, text
        dismiss(terminal)
        enter(terminal, "/new")
        text = frame(terminal, output, "busy-new-rejected")
        assert "busy" in text, text
        enter(terminal, "/cancel")
        wait_terminal(terminal, home, "cancel", count=1)
        text = frame(terminal, output, "busy-cancel")
        assert "cancel" in text.lower(), text
        assert query(home, "SELECT status FROM runs")[0][0] == "cancelled"
        assert len(query(home, "SELECT seq FROM turns")) == 0 and server.calls == 1
        # 第二次取消从仍打开的只读面板发 Ctrl+C，不能被面板导航吞掉。
        server.gate.set()
        terminal.drain(0.2)
        server.gate.clear()
        server.pending.clear()
        enter(terminal, "wait for panel control c")
        assert server.pending.wait(2)
        enter(terminal, "/status")
        frame(terminal, output, "busy-panel-before-control-c")
        terminal.send(b"\x03")
        wait_terminal(terminal, home, "panel-control-c", count=2)
        dismiss(terminal)
        frame(terminal, output, "busy-panel-control-c-cancel")
        assert query(home, "SELECT status FROM runs ORDER BY started_at") == [("cancelled",), ("cancelled",)]
        assert len(query(home, "SELECT seq FROM turns")) == 0 and server.calls == 2
        stop(terminal)
        capture_trace(home, output, "cancel-failure")
        return {"case": "busy-readonly-cancel", "status": "passed", "model_calls": 2, "completed_turns": 0,
                "cancelled_runs": 2, "cancel_inputs": ["/cancel", "Ctrl+C with status panel open"]}
    finally:
        if terminal: cleanup_terminal(terminal)
        server.gate.set()
        server.shutdown()
        server.server_close()


def case_memory(binary, output, home, work):
    """禁用历史仍可命名/恢复本进程历史，offline 不创建配置或数据库。"""
    server = start_fixture()
    terminal = None
    try:
        configure(home, server)
        path = home / "config.json"
        cfg = json.loads(path.read_text())
        cfg["sessions"]["history"] = {"enabled": False}
        path.write_text(json.dumps(cfg))
        terminal = open_terminal(binary, home, work)
        terminal.drain(0.8)
        enter(terminal, "/new memory")
        enter(terminal, "memory question")
        wait_terminal(terminal, home, "memory", count=1)
        enter(terminal, "/new second")
        enter(terminal, "/resume")
        text = frame(terminal, output, "memory-resume-list")
        assert "memory" in text and "second" in text, text
        terminal.send(b"\r")
        text = frame(terminal, output, "memory-resume-transcript")
        assert "memory question" in text, text
        assert not (home / "sessions" / "history.db").exists()
        stop(terminal)
        terminal.close()
        offline_home = home / "offline"
        offline_home.mkdir()
        terminal = Terminal(binary, work, workspace_env(offline_home, work), 100, 30, offline=True)
        terminal.drain(0.8)
        enter(terminal, "/help")
        text = frame(terminal, output, "offline-help")
        assert "/history" in text, text
        dismiss(terminal)
        enter(terminal, "/demo workspace")
        wait_terminal(terminal, offline_home, "demo", count=1)
        frame(terminal, output, "offline-workspace-demo")
        assert (work / "plume-demo.txt").exists()
        assert not (offline_home / "config.json").exists() and not (offline_home / "sessions").exists()
        stop(terminal)
        capture_trace(home, output, "memory")
        capture_trace(offline_home, output, "offline-demo")
        return {"case": "disabled-history-and-offline", "status": "passed", "database_created": False, "offline_config_read": False}
    finally:
        if terminal: cleanup_terminal(terminal)
        server.shutdown()
        server.server_close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=Path("./plume"))
    parser.add_argument("--output", type=Path, default=Path("docs/reviews/evidence/G4a.2/pty"))
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    with tempfile.TemporaryDirectory(prefix="plume-g4a2-version-") as version_home:
        version = subprocess.check_output([str(binary), "version"],
            env=workspace_env(Path(version_home), Path.cwd()), text=True)
    environment = {"recorded_at": datetime.now(timezone.utc).isoformat(),
        "system": platform.system(), "machine": platform.machine(), "python": platform.python_version(),
        "binary_version": version.strip(), "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "pyte": importlib.metadata.version("pyte"), "wcwidth": importlib.metadata.version("wcwidth"),
        "term": "xterm-256color", "colorterm": "truecolor", "terminal_sizes": ["100x32", "48x20", "100x30", "120x36"]}
    results = []
    for case in [case_commands, case_pages, case_busy, case_memory, case_models, case_incompatible]:
        with tempfile.TemporaryDirectory(prefix="plume-g4a2-") as temp:
            home, work = Path(temp)/"home", Path(temp)/"work"
            home.mkdir(mode=0o700)
            work.mkdir()
            start = time.perf_counter()
            result = case(binary, args.output, home, work)
            result["elapsed_ms"] = round((time.perf_counter()-start)*1000, 2)
            results.append(result)
            print("G4A2_PTY_CASE_OK " + result["case"], flush=True)
    (args.output / "results.json").write_text(json.dumps({"cases":len(results),"results":results,"environment":environment,
        "limits":["本地fixture只验证链路与界面，不代表真实模型性能；中文窄窗口为PTY，非人工kitty/Windows验证。"]},ensure_ascii=False,indent=2)+"\n")
    print(f"G4A2_PTY_OK cases={len(results)}")


if __name__ == "__main__":
    main()
