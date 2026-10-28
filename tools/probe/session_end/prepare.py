"""从干净提交准备具体合约末根计划；仅 --launch 才启动正式录制。"""
import argparse
import datetime as dt
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

from clock_probe import combine, measure, trace_writer

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "capture_task"))
from plan import validate


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def validate_spec(spec, now):
    """只接受螺纹钢日盘或夜盘末根；交易日须另按官方日历核实。"""
    if not isinstance(spec, dict) or set(spec) != {"symbol", "trading_day", "start", "close", "end", "deadline"} or any(not isinstance(v, str) for v in spec.values()):
        raise ValueError("执行单字段不完整或含未知字段")
    import re
    if not re.fullmatch(r"SHFE\.rb[0-9]{2}(0[1-9]|1[0-2])", spec["symbol"]):
        raise ValueError("必须显式选择螺纹钢具体合约")
    points = {}
    for key in ("start", "close", "end", "deadline"):
        point = dt.datetime.fromisoformat(spec[key])
        if point.utcoffset() != dt.timedelta(hours=8) or point.second or point.microsecond:
            raise ValueError("窗口须分钟对齐并显式东八区")
        points[key] = point
    start, close, end, deadline = (points[k] for k in ("start", "close", "end", "deadline"))
    day = close.hour == 15
    expected_start_hour = 14 if day else 22
    if not (start.date() == close.date() == end.date() == deadline.date()
            and start.hour == expected_start_hour and close.hour in (15, 23) and close.minute == 0
            and start <= close - dt.timedelta(minutes=5) and close < end <= deadline
            and end - close >= dt.timedelta(minutes=7) and deadline.hour == close.hour
            and deadline.minute <= 10 and now < close.timestamp() - 240):
        raise ValueError("窗口不足、过期或超出已核验的15点/23点收盘段")
    expected_day = close if day else close + dt.timedelta(days=1)
    if close.weekday() >= 5 or (not day and expected_day.weekday() >= 5):
        raise ValueError("周末跨日窗口须另核，不使用自然日推断")
    if spec["trading_day"] != expected_day.strftime("%Y%m%d"):
        raise ValueError("日盘须同日、夜盘须次一自然日；节假日安排须另核")
    return points


def verify_contract(path, symbol, now):
    """短连真实响应必须包含指定原生合约的最近五根完整分钟。"""
    from math import isfinite
    snap = {}

    def merge(dst, patch):
        for key, value in patch.items():
            if value is None:
                dst.pop(key, None)
            elif isinstance(value, dict):
                if not isinstance(dst.get(key), dict): dst[key] = {}
                merge(dst[key], value)
            else: dst[key] = value

    for line in path.read_text(encoding="utf-8").splitlines():
        for patch in json.loads(line)["raw"].get("data", []):
            merge(snap, patch)
    rows = snap.get("klines", {}).get(symbol, {}).get("60000000000", {}).get("data", {})
    complete = sorted((row for row in rows.values() if isinstance(row, dict)
                       and type(row.get("datetime")) in (int, float)
                       and row["datetime"] / 1e9 < int(now // 60) * 60), key=lambda r: r["datetime"])[-5:]
    if len(complete) != 5:
        raise ValueError("具体合约响应缺少五根完整分钟")
    if any(not isfinite(row["datetime"]) or row["datetime"] % 60000000000 for row in complete):
        raise ValueError("具体合约标签须为完整分钟")
    times = [int(row["datetime"]) // 1000000 for row in complete]
    if times[-1] < (now - 86400) * 1000 or any(b-a != 60000 for a, b in zip(times, times[1:])):
        raise ValueError("具体合约分钟不连续或历史已过旧")
    for row in complete:
        for key in ("open", "high", "low", "close", "volume", "close_oi", "open_oi"):
            if type(row.get(key)) not in (float, int) or not isfinite(row[key]):
                raise ValueError("具体合约字段不完整")
    return dict(symbol=symbol, matched_rows=5, first_ms=times[0], last_ms=times[-1])


def prepare(destination, spec):
    points = validate_spec(spec, time.time())
    if destination.exists() or destination.is_relative_to(ROOT):
        raise ValueError("准备目录必须在仓库外且不存在")
    def git(*args):
        return subprocess.check_output(["git", *args], cwd=ROOT, timeout=30).decode().strip()
    source = git("rev-parse", "HEAD")
    if git("status", "--porcelain"):
        raise ValueError("必须从干净提交准备")
    destination.mkdir(parents=True)
    binary = destination / ("probe.exe" if os.name == "nt" else "probe")
    build = subprocess.run(["go", "build", "-mod=readonly", "-o", str(binary), "."],
                           cwd=ROOT / "tools/probe/shinny", env=dict(os.environ, GOWORK="off", GOFLAGS=""),
                           capture_output=True, timeout=240)
    (destination / "build.log").write_bytes(build.stdout + build.stderr)
    if build.returncode or git("rev-parse", "HEAD") != source or git("status", "--porcelain"):
        raise ValueError("编译失败或源码变化")
    with (destination / "clock-preflight-attempts.jsonl").open("x", encoding="utf-8") as diagnostics:
        clock = measure(trace_writer(diagnostics))
    (destination / "clock-preflight.json").write_text(json.dumps(clock, indent=2), encoding="utf-8")
    # 单次六秒录制只作连接与原生合约预检，不等到夜间窗口。
    short_end = dt.datetime.now(dt.timezone(dt.timedelta(hours=8))) + dt.timedelta(seconds=6)
    raw = destination / "contract-preflight.jsonl"
    check = subprocess.run([str(binary), "-only", "shinny-live-tail", "-tail-symbol", spec["symbol"],
                            "-tail-end", short_end.isoformat(), "-tail-out", str(raw)],
                           cwd=ROOT / "tools/probe/shinny", capture_output=True, timeout=50)
    (destination / "contract-preflight.log").write_bytes(check.stdout + check.stderr)
    if check.returncode or b"[PASS] shinny-live-tail" not in check.stdout or b"[SKIP]" in check.stdout:
        raise ValueError("真实连接预检未实际通过")
    identity = verify_contract(raw, spec["symbol"], time.time())
    clock_script = destination / "clock_probe.py"
    shutil.copy2(Path(__file__).with_name("clock_probe.py"), clock_script)
    now = time.time()
    common = dict(cwd=str(ROOT / "tools/probe/shinny"), env={}, stdout_name="run.log",
                  max_seconds=points["deadline"].timestamp()-now, startup_seconds=60,
                  idle_seconds=90, poll_seconds=1, process_scope="independent")
    capture = validate(dict(common, command=[str(binary), "-only", "shinny-live-tail", "-tail-symbol", spec["symbol"],
                          "-tail-end", spec["end"], "-tail-out", "{task_dir}/raw.jsonl"], watch=["raw.jsonl"],
                          quiet_windows={"raw.jsonl": [{"start": spec["close"], "end": spec["deadline"]}]}))
    clock_end = points["deadline"]-dt.timedelta(minutes=1)
    calibration = validate(dict(common, command=[sys.executable, str(clock_script), "{task_dir}/clock.jsonl",
                               "--until", clock_end.isoformat()], watch=["clock.jsonl"]))
    for name, plan in (("capture-plan.json", capture), ("clock-plan.json", calibration)):
        (destination / name).write_text(json.dumps(plan, ensure_ascii=False, indent=2), encoding="utf-8")
    manifest = dict(source_sha=source, spec=spec, contract=identity, prepared_at=time.time(),
                    provenance="real-preflight-not-night-acceptance", files={name: digest(destination/name) for name in
                    (binary.name, "clock_probe.py", "capture-plan.json", "clock-plan.json")})
    (destination / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
    return manifest


def launch(destination):
    """仅使用一分钟内预检的新单；先确认校准在写，再启动录制。"""
    from cli import launch as start
    from worker import inspect
    manifest = json.loads((destination/"manifest.json").read_text(encoding="utf-8"))
    points = validate_spec(manifest["spec"], time.time())
    if time.time() < points["start"].timestamp()-60:
        raise ValueError("仅在计划起点前一分钟内允许正式启动")
    if not 0 <= time.time()-manifest["prepared_at"] <= 60:
        raise ValueError("须重新生成一分钟内的新执行单")
    for name, expected in manifest["files"].items():
        if Path(name).name != name or digest(destination/name) != expected:
            raise ValueError("执行单或产物字节改变")
    clock_root, capture_root = destination/"clock-task", destination/"capture-task"
    if clock_root.exists() or capture_root.exists():
        raise ValueError("任务目录已存在，先核验旧任务，禁止重复启动")
    plans = [validate(json.loads((destination/name).read_text(encoding="utf-8"))) for name in
             ("clock-plan.json", "capture-plan.json")]
    plans[0]["max_seconds"] = points["deadline"].timestamp()-time.time()
    start(clock_root, plans[0])
    limit = time.monotonic()+35
    while time.monotonic() < limit:
        status = inspect(clock_root)
        if status["state"] == "running":
            proof = json.loads((clock_root/"clock.jsonl").read_text(encoding="utf-8").splitlines()[-1])
            combine(proof["samples"])
            plans[1]["max_seconds"] = points["deadline"].timestamp()-time.time()
            return start(capture_root, plans[1])
        if status["state"] in {"failed", "timed_out", "completed"}:
            raise ValueError("校准任务未能持续记录；不启动录制")
        time.sleep(0.2)
    raise ValueError("未取得活跃校准；不启动录制，保留任务目录供核验")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--spec", type=Path)
    parser.add_argument("--launch", action="store_true")
    args = parser.parse_args()
    destination = args.directory.resolve()
    if args.launch:
        result = launch(destination)
    else:
        if not args.spec: parser.error("必须给 --spec")
        result = prepare(destination, json.loads(args.spec.read_text(encoding="utf-8")))
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
