"""按已审阅六批逐一执行，跨批全局停主机；未知启动不重试，不新增推测URL。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import time
from urllib.parse import urlencode

APPROVED_PROPOSAL_SHA = "2615fcce61395a44ccd801cb462cf9ef118f38b21acb1d2b93782537243fce8e"
STATIC = "https://raw.githubusercontent.com/akfamily/akshare/main/akshare/futures/cons.py"


def sha(b):
    return hashlib.sha256(b).hexdigest()


def write(path, value):
    # 单写者；状态.tmp只在同目录原子替换，不改原件或冻结计划。
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")
    temporary.replace(path)


def execute(binary, root, plan_path, count, static=False):
    folder = root / ("static-cons" if static else plan_path.stem)
    folder.mkdir(exist_ok=False)
    args = [str(binary.resolve()), "-root", str((folder / "archive").resolve()), "-plan", str(plan_path.resolve())]
    args += ["-static-cons"] if static else ["-requests", str(count), "-bytes", str(3 << 20)]
    write(folder / "launch.json", dict(state="in_progress", args=args, plan_sha256=sha(plan_path.read_bytes())))
    with (folder / "stdout.json").open("wb") as out, (folder / "stderr.log").open("wb") as err:
        result = subprocess.run(args, stdout=out, stderr=err, timeout=1200)
    report = json.loads((folder / "stdout.json").read_text(encoding="utf-8"))
    failures, usage = [], 0
    for item in report["results"]:
        receipt = item["receipt"]
        metadata = receipt.get("http", {})
        usage += int(bool(metadata.get("attempted")))
        if receipt["body_state"] != "none":
            raw = (folder / "archive" / (item["id"] + ".body")).read_bytes()
            if len(raw) != receipt["bytes"] or sha(raw) != receipt["evidence"]["sha256"]:
                raise ValueError("实际原件与收据不符；停止全部后续")
        if receipt["state"] != "received":
            failures.append(item["id"])
    if usage != report["requests"] or len(report["results"]) != count:
        raise ValueError("执行记录不完整；不能重试")
    success = result.returncode == 0 and not failures
    outcome = dict(state="completed" if success else "failed-stop-host-and-dependent-batches", requests=report["requests"], bytes=report["bytes"], returncode=result.returncode, failures=failures)
    write(folder / "launch.json", outcome)
    return outcome


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("proposal", type=Path)
    p.add_argument("binary", type=Path)
    p.add_argument("commit")
    p.add_argument("output", type=Path)
    args = p.parse_args()
    data = args.proposal.read_bytes()
    if sha(data) != APPROVED_PROPOSAL_SHA:
        raise ValueError("不是已审阅计划")
    proposal = json.loads(data)
    if [b["requests"] for b in proposal["batches"]] != [40, 40, 40, 40, 40, 30] or len(proposal["deduplicated"]) != 230:
        raise ValueError("预算/清单变更")
    plans = []
    for index, batch in enumerate(proposal["batches"]):
        path = args.proposal.parent / batch["file"]
        jobs = json.loads(path.read_text(encoding="utf-8"))
        expected = proposal["deduplicated"][index * 40:index * 40 + 40]
        if len(jobs) != len(expected):
            raise ValueError("批次长度变化")
        for job, contract in zip(jobs, expected):
            code = contract["code"]
            url = "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_V21052021_4_12=/InnerFuturesNewService.getDailyKLine?" + urlencode(dict(symbol=code, type="2021_04_12"))
            if job["url"] != url or job["exchange"] != contract["exchange"] or job["id"] != "daily-" + code or job["version"] != "sina-vendor-v1":
                raise ValueError("批次不是批准的精确合约")
        plans.append(path)
    info = subprocess.check_output(["go", "version", "-m", str(args.binary.resolve())], text=True, encoding="utf-8")
    if "vcs.revision=" + args.commit not in info or "vcs.modified=false" not in info:
        raise ValueError("执行文件不是声明的干净提交")
    args.output.mkdir(exist_ok=False)  # 既有目录/未知启动一律不重复。
    for path in [args.proposal] + plans:
        (args.output / path.name).write_bytes(path.read_bytes())
    (args.output / "build-info.txt").write_text(info, encoding="utf-8", newline="\n")
    state = dict(state="running", source_commit=args.commit, executable_sha256=sha(args.binary.read_bytes()), proposal_sha256=sha(data), batches=[], market_requests=0, static_requests=0, bytes=0, future_market_batches_stopped=False)
    write(args.output / "state.json", state)
    for path in plans:
        outcome = execute(args.binary, args.output, args.output / path.name, len(json.loads(path.read_text(encoding="utf-8"))))
        state["batches"].append(dict(plan=path.name, **outcome))
        state["market_requests"] += outcome["requests"]
        state["bytes"] += outcome["bytes"]
        if outcome["state"] != "completed":
            state["future_market_batches_stopped"] = True
            state["state"] = "market-blocked"
        write(args.output / "state.json", state)
        print(f"批次{len(state['batches'])} {outcome['state']}；累计{state['market_requests']}请求/{state['bytes']}字节", flush=True)
        if state["future_market_batches_stopped"]:
            break
        time.sleep(1)  # 跨新进程边界也保留至少1秒间隔，不借批次重置节流。
    # 静态模块与市场采集分开计账；失败不换URL或重试。
    static_plan = args.output / "static-cons-plan.json"
    write(static_plan, [dict(id="akshare-cons", url=STATIC, format="static-python-raw", day=0, version="akshare-static-source-v1")])
    outcome = execute(args.binary, args.output, static_plan, 1, static=True)
    state["static"] = outcome
    state["static_requests"] = outcome["requests"]
    state["bytes"] += outcome["bytes"]
    state["state"] = "completed-raw-only" if not state["future_market_batches_stopped"] and outcome["state"] == "completed" else "blocked-with-diagnostics"
    write(args.output / "state.json", state)
    if state["market_requests"] > 230 or state["static_requests"] > 1 or state["bytes"] > (19 << 20) + 6:
        raise ValueError("总量越界")
    print(state["state"], flush=True)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
