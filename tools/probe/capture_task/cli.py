"""独立启动采集监护进程，或读取其可核验状态。"""
import argparse
import json
import io
import os
from pathlib import Path
import subprocess
import sys
import time

from plan import validate, child_environment
from worker import inspect, run, write_status


def launch(root, plan):
    """独占新目录，保留计划；调用方退出后监护继续运行。"""
    # 路径错误必须在创建任何任务目录或启动进程之前拒绝。
    child_environment(plan, root, os.environ)
    data = None
    if "artifact" in plan:
        from artifact import check
        data = check(plan)
    root.mkdir(parents=True, exist_ok=False)
    if data is not None:
        from artifact import stage
        stage(root, plan, data)
    (root / "plan.json").write_text(json.dumps(plan, ensure_ascii=False), encoding="utf-8")
    write_status(root, dict(state="launching", heartbeat=time.time(), child_pid=None,
                           supervisor_pid=None))
    options = {}
    if os.name == "nt":
        options["creationflags"] = (subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP
                                    | subprocess.CREATE_NO_WINDOW | subprocess.CREATE_BREAKAWAY_FROM_JOB)
    else:
        options["start_new_session"] = True
    try:
        with (root / "worker.log").open("xb") as log:
            process = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), "worker", str(root)],
                                       stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                                       close_fds=True, **options)
        return dict(task_dir=str(root), supervisor_pid=process.pid, state="launched")
    except Exception as error:
        write_status(root, dict(state="failed", reason=type(error).__name__))
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["launch", "worker", "status"])
    parser.add_argument("directory", type=Path)
    parser.add_argument("--plan", type=Path)
    parser.add_argument("--stale-seconds", type=float, default=10)
    args = parser.parse_args()
    root = args.directory.resolve()
    if args.action == "status":
        if not 0 < args.stale_seconds < float("inf"):
            parser.error("stale-seconds 必须为有限正数")
        result = inspect(root, args.stale_seconds)
        print(json.dumps(result, ensure_ascii=False))
        return 0 if result["state"] in {"running", "waiting", "completed"} else 1
    if args.action == "worker":
        return run(root, validate(json.loads((root / "plan.json").read_text(encoding="utf-8"))))
    if not args.plan:
        parser.error("launch 必须指定 --plan")
    plan = validate(json.loads(args.plan.read_text(encoding="utf-8")))
    print(json.dumps(launch(root, plan), ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit(main())
