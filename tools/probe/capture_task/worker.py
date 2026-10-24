"""监护前台采集子进程，分别记录进程心跳与数据活性。"""
import json
import io
import math
import os
from pathlib import Path
import subprocess
import sys
import time
from plan import child_environment, quiet_files

TERMINAL = {"completed", "failed", "timed_out"}


def write_status(root, status):
    """原子替换快照，读取方不会读到半份 JSON。"""
    tmp = root / "status.tmp"
    tmp.write_text(json.dumps(status, ensure_ascii=False), encoding="utf-8")
    # Windows 读取方短暂占用目标时，有限重试；最终仍失败则保留错误。
    for attempt in range(20):
        try:
            os.replace(tmp, root / "status.json")
            break
        except PermissionError:
            if attempt == 19:
                raise
            time.sleep(0.01)


def signatures(root, names):
    """只统计显式数据文件；拒绝符号链接逃逸，不以空文件创建代表数据。"""
    found = {}
    for name in names:
        path = root / name
        if not path.resolve().is_relative_to(root):
            raise ValueError("监测路径越过任务目录")
        try:
            stat = path.stat()
            if path.is_file() and stat.st_size:
                found[name] = [stat.st_size, stat.st_mtime_ns]
        except FileNotFoundError:
            pass
    return found


def run(root, plan):
    """监护一个直接子进程；业务验收由外部复核器另行完成。"""
    started = time.monotonic()
    status = dict(state="waiting", supervisor_pid=os.getpid(), child_pid=None,
                  heartbeat=time.time(), started_at=time.time(), last_data_at=None,
                  returncode=None, stalls=0, files={})
    child = None
    previous = {}
    last_change = None
    changed_at = {}
    state_before = None
    stage = "initial_status"

    def publish():
        nonlocal state_before
        status["heartbeat"] = time.time()
        event_key = (status["state"], tuple(status.get("quiet_files", [])), tuple(status.get("stale_files", [])))
        if event_key != state_before:
            with (root / "events.jsonl").open("a", encoding="utf-8") as output:
                output.write(json.dumps(dict(at=status["heartbeat"], state=status["state"],
                                             quiet_files=status.get("quiet_files", []),
                                             stale_files=status.get("stale_files", [])), ensure_ascii=False) + "\n")
            state_before = event_key
        write_status(root, status)

    try:
        publish()
        command = [arg.replace("{task_dir}", str(root)) for arg in plan["command"]]
        stage = "environment"
        environment = child_environment(plan, root, os.environ)
        stage = "capture_log"
        with (root / plan.get("stdout_name", "capture.log")).open("xb") as log:
            stage = "spawn"
            child = subprocess.Popen(command, cwd=plan["cwd"], stdin=subprocess.DEVNULL,
                                     stdout=log, stderr=subprocess.STDOUT, env=environment)
            status["child_pid"] = child.pid
            while True:
                stage = "observe"
                now = time.monotonic()
                current = signatures(root, plan["watch"])
                for name, signature in current.items():
                    if previous.get(name) != signature:
                        changed_at[name] = now
                        last_change = now
                        status["last_data_at"] = time.time()
                previous = current
                status["files"] = current
                status["quiet_files"] = quiet_files(plan, time.time())
                # 每个必需通道独立判断，避免一个活跃文件掩盖另一个已停写的通道。
                stale_files = [name for name in plan["watch"]
                               if ((name not in current or name not in changed_at)
                                   and now - started >= plan["startup_seconds"])
                               or (name in changed_at and now - changed_at[name] >= plan["idle_seconds"])]
                stale_files = [name for name in stale_files if name not in status["quiet_files"]]
                status["stale_files"] = stale_files
                code = child.poll()
                if code is not None:
                    status["returncode"] = code
                    complete = code == 0 and all(name in current for name in plan["watch"])
                    status["state"] = "completed" if complete else "failed"
                    status["reason"] = "进程退出；仍须独立行情验收" if complete else "非零退出或必需数据缺失"
                    break
                if now - started >= plan["max_seconds"]:
                    status.update(state="timed_out", reason="超过总时限")
                    break
                stalled = bool(stale_files)
                next_state = "stalled" if stalled else ("waiting" if last_change is None else "running")
                if next_state == "stalled" and status["state"] != "stalled":
                    status["stalls"] += 1
                status["state"] = next_state
                stage = "publish"
                publish()
                time.sleep(plan["poll_seconds"])
    except Exception as error:
        # 只保存错误类别，不复制可能含凭证的命令或系统错误正文。
        status.update(state="failed", reason=type(error).__name__, error_stage=stage,
                      winerror=getattr(error, "winerror", None))
    finally:
        if child is not None and child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=3)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=3)
        if child is not None:
            status["returncode"] = child.returncode
        publish()
    return 0 if status["state"] == "completed" else 1


def inspect(root, stale_seconds=10):
    """终态保留；非终态心跳超期则报告未知，不把 PID 当作存活证据。"""
    try:
        status = json.loads((root / "status.json").read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return dict(state="supervisor_unknown", reason="状态缺失、损坏或暂不可读")
    known = TERMINAL | {"waiting", "running", "launching", "stalled"}
    if not isinstance(status, dict) or not isinstance(status.get("state"), str) or status["state"] not in known:
        return dict(state="supervisor_unknown", reason="状态结构无效")
    if status["state"] not in TERMINAL:
        heartbeat = status.get("heartbeat")
        if (type(heartbeat) not in (float, int) or not math.isfinite(heartbeat)
                or not -1 <= time.time() - heartbeat <= stale_seconds):
            status = dict(status, state="supervisor_unknown", reason="心跳过期、异常或时钟回退")
    return status


if __name__ == "__main__":
    # 监护入口在 cli；直接执行本模块也保持异常可读，导入时不包流。
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
