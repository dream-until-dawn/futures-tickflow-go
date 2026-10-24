"""编译、独立启动、假服务录制、独立复核、封存迁移的一键离线演练。"""
import argparse
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import time

from artifact import synthetic_spec
from prepare import ROOT, prepare
from worker import inspect


def drill(directory, process_scope="independent"):
    directory = directory.resolve()
    if directory.exists() or directory.is_relative_to(ROOT):
        raise ValueError("演练目录必须是仓库外新的目录")
    plan = prepare(directory / "prepared", "synthetic", synthetic_spec(), process_scope)
    task = directory / "task"
    result = subprocess.run([sys.executable, str(Path(__file__).with_name("cli.py")), "launch", str(task), "--plan", str(plan)],
                            capture_output=True, timeout=15)
    if result.returncode:
        raise ValueError(result.stderr.decode("utf-8", "replace"))
    deadline = time.monotonic() + 75
    while time.monotonic() < deadline:
        status = inspect(task)
        if status["state"] in {"completed", "failed", "timed_out"}:
            break
        time.sleep(0.1)
    if status["state"] != "completed":
        raise ValueError("离线采集未完成：" + json.dumps(status))
    sys.path.insert(0, str(ROOT / "tools/audit"))
    from evidence_bundle import analyze, seal, verify
    report = analyze(task, True)
    if report["provenance"] != "synthetic" or report["rows"] != 48 or report["complete_gap_rows"] != 31:
        raise ValueError("合成基线数量或分类错误")
    rejected = False
    try:
        analyze(task, False)
    except ValueError:
        rejected = True
    if not rejected:
        raise ValueError("合成材料被错误接受为现场证据")
    negatives = {}
    for kind in ("missing-row", "changed-price", "unfinished-log"):
        target = directory / kind
        shutil.copytree(task, target)
        if kind == "unfinished-log":
            (target / "run.log").write_text("=== RUN TestGapOfflineBundle\n")
        else:
            path = target / "capture/live.jsonl"
            rows = [json.loads(line) for line in path.read_text().splitlines()]
            if kind == "missing-row":
                rows.pop(15)
            else:
                rows[15]["c"] += 123
            path.write_text("\n".join(json.dumps(row) for row in rows))
        try:
            analyze(target, True)
        except (ValueError, KeyError):
            negatives[kind] = "rejected"
        else:
            raise ValueError("反例未检出：" + kind)
    seal(task, True)
    moved = directory / "migrated"
    shutil.copytree(task, moved)
    migrated = verify(moved, True)
    summary = dict(source_sha=report["source_sha"], provenance="synthetic", process_scope=process_scope, rows=48, complete_gap_rows=31,
                   negatives=negatives, migrated=migrated)
    (directory / "report.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    return summary


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--process-scope", choices=["independent", "job"], default="independent")
    args = parser.parse_args()
    print(json.dumps(drill(args.directory, args.process_scope), ensure_ascii=False, indent=2))
