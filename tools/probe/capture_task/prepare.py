"""从干净Git提交编译采集产物并生成可独立启动的执行单。"""
import argparse
import datetime as dt
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import time

from artifact import SPEC_KEYS, command, parameters, preflight, synthetic_spec, validate_artifact
from plan import stamp, validate

ROOT = Path(__file__).resolve().parents[3]


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, timeout=30).decode().strip()


def make_plan(binary, source_sha, mode, spec, now, process_scope="independent"):
    """生成参数、监护窗口和产物绑定；真实窗口由Go校验器裁定。"""
    artifact = validate_artifact(dict(path=str(binary.resolve()), sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),
                                      source_sha=source_sha, mode=mode, spec=spec))
    value = dict(command=command(artifact), cwd=str(ROOT / "source/shinnysource"),
                 watch=["capture/reference.jsonl", "capture/live.jsonl"], env=parameters(artifact),
                 stdout_name="run.log", artifact=artifact, max_seconds=60, startup_seconds=10,
                 idle_seconds=5, poll_seconds=0.1, process_scope=process_scope)
    if mode == "real":
        if stamp(spec["BEGIN"]) <= now or stamp(spec["END"]) + 10 > stamp(spec["DEADLINE"]):
            raise ValueError("窗口过期或超过明确授权截止")
        value.update(max_seconds=stamp(spec["DEADLINE"]) - now, startup_seconds=stamp(spec["BEGIN"]) - now + 120,
                     idle_seconds=180, poll_seconds=1,
                     quiet_windows={"capture/live.jsonl": [{"start": spec["DROP"],
                                       "end": dt.datetime.fromtimestamp(stamp(spec["RESUME"]) + 120,
                                               dt.timezone.utc).isoformat()}]})
    return validate(value)


def prepare(destination, mode, spec, process_scope="independent"):
    """先拒绝脏树/旧目录，编译前后核对源树；不启动采集。"""
    destination = destination.resolve()
    if destination.is_relative_to(ROOT) or destination.exists():
        raise ValueError("准备目录必须是仓库外不存在的新目录")
    if not isinstance(spec, dict) or set(spec) != SPEC_KEYS:
        raise ValueError("执行单字段不完整或包含未知字段")
    source_sha = git("rev-parse", "HEAD")
    if git("status", "--porcelain"):
        raise ValueError("只能从已提交干净源码编译")
    destination.mkdir(parents=True, exist_ok=False)
    binary = destination / ("capture.test.exe" if os.name == "nt" else "capture.test")
    result = subprocess.run(["go", "test", "-c", "-o", str(binary), "./source/shinnysource"], cwd=ROOT,
                            capture_output=True, timeout=240)
    (destination / "build.log").write_bytes(result.stdout + result.stderr)
    if result.returncode or git("rev-parse", "HEAD") != source_sha or git("status", "--porcelain"):
        raise ValueError("编译失败或编译期间源码变化，保留目录供排查")
    if mode == "real":
        (destination / "preflight.log").write_text(preflight(binary, spec), encoding="utf-8")
    plan = make_plan(binary, source_sha, mode, spec, time.time(), process_scope)
    (destination / "plan.json").write_text(json.dumps(plan, ensure_ascii=False, indent=2), encoding="utf-8")
    return destination / "plan.json"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--mode", choices=["real", "synthetic"], required=True)
    parser.add_argument("--spec", type=Path)
    parser.add_argument("--process-scope", choices=["independent", "job"], default="independent")
    args = parser.parse_args()
    if args.mode == "real" and not args.spec:
        parser.error("真实模式必须指定完整执行单和DEADLINE")
    if args.mode == "synthetic" and args.spec:
        parser.error("合成模式使用固定虚拟时钟，不接受真实执行单")
    spec = synthetic_spec() if args.mode == "synthetic" else json.loads(args.spec.read_text(encoding="utf-8-sig"))
    print(prepare(args.directory, args.mode, spec, args.process_scope))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
