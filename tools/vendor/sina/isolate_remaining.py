"""显式工具模式的175未尝试清单协调器；旧stop只读，任何其他失败停依赖后批。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import time
from expand import write


def sha(b):
    return hashlib.sha256(b).hexdigest()


def run_batch(binary, output, profile, plan):
    folder = output / plan.stem
    jobs = json.loads(plan.read_text(encoding="utf-8"))
    args = [str(binary.resolve()), "-root", str(folder.resolve()), "-plan", str(plan.resolve()), "-requests", str(len(jobs)), "-bytes", str(3 << 20), "-isolate-exact-null", "-null-profile", str(profile.resolve())]
    with (output / (plan.stem + "-stdout.json")).open("xb") as out, (output / (plan.stem + "-stderr.log")).open("xb") as err:
        result = subprocess.run(args, stdout=out, stderr=err, timeout=1200)
    report = json.loads((output / (plan.stem + "-stdout.json")).read_text(encoding="utf-8"))
    if report["profile_sha256"] != sha(profile.read_bytes()) or report["covered"] or report["financial_eligible"] or [r["id"] for r in report["results"]] != [j["id"] for j in jobs]:
        raise ValueError("工具报告身份/资格/清单不符；停止后续")
    attempts, received, quarantined, total_bytes = 0, 0, 0, 0
    for r, job in zip(report["results"], jobs):
        meta = r.get("http", {})
        attempts += int(bool(meta.get("attempted")))
        total_bytes += r["bytes"]
        if meta.get("response_received"):
            raw = (folder / "raw" / (r["id"] + ".jsonp")).read_bytes()
            if len(raw) != r["bytes"] or sha(raw) != r["evidence"]["sha256"] or r["evidence"]["url"] != job["url"]:
                raise ValueError("工具原件身份不符")
        if r["covered"] or r["financial_eligible"]:
            raise ValueError("单合约资格被提升")
        if r["state"] == "failed-quarantined":
            quarantined += 1
            raw = (folder / "raw" / (r["id"] + ".jsonp")).read_bytes().strip()
            comment = b"/*<script>location.href='//sina.com';</script>*/"
            if raw.startswith(comment):
                raw = raw[len(comment):].strip()
            if raw != b"var _V21052021_4_12=(null);" or meta.get("status") != 200 or not meta.get("complete") or meta.get("final_url") != job["url"] or r["records"] != 0:
                raise ValueError("隔离项并非完整精确null")
        elif r["state"] == "received":
            received += 1
    if (attempts, received, quarantined, total_bytes) != (report["requests"], report["received"], report["quarantined"], report["bytes"]):
        raise ValueError("实际请求/结果/字节台账不符")
    allowed_states = {"completed-raw-only", "completed-with-quarantined-unavailable"}
    success = result.returncode == 0 and not report["host_stopped"] and report["state"] in allowed_states and received + quarantined == len(jobs)
    return dict(state=report["state"], success=success, requests=attempts, bytes=total_bytes, received=received, quarantined=quarantined, returncode=result.returncode)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("profile", type=Path)
    p.add_argument("binary", type=Path)
    p.add_argument("commit")
    p.add_argument("output", type=Path)
    args = p.parse_args()

    if len(args.commit) != 40 or any(c not in "0123456789abcdef" for c in args.commit):
        raise ValueError("必须固定完整提交SHA")
    config = json.loads(args.profile.read_text(encoding="utf-8"))
    pending_path = args.profile.parent / config["unattempted"]
    pending_raw = pending_path.read_bytes()
    if sha(pending_raw) != config["unattempted_sha256"]:
        raise ValueError("旧未尝试清单被改变")
    pending = json.loads(pending_raw)
    allowed = {r["url"] for r in pending if not r["attempted"]}
    if len(allowed) != len(pending) or len(pending) != 175:
        raise ValueError("不是既有175未尝试范围")
    plans = sorted(args.profile.parent.glob("batch-*.json"))
    jobs = [j for f in plans for j in json.loads(f.read_text(encoding="utf-8"))]
    if [len(json.loads(f.read_text(encoding="utf-8"))) for f in plans] != [40, 40, 40, 40, 15] or {j["url"] for j in jobs} != allowed or len(jobs) != len(allowed):
        raise ValueError("精确计划变更/重复/旧请求")
    info = subprocess.check_output(["go", "version", "-m", str(args.binary.resolve())], text=True, encoding="utf-8")
    if "vcs.revision=" + args.commit not in info or "vcs.modified=false" not in info:
        raise ValueError("执行文件不是准确干净提交")
    args.output.mkdir(exist_ok=False)
    for f in plans + [args.profile]:
        (args.output / f.name).write_bytes(f.read_bytes())
    (args.output / "build-info.txt").write_text(info, encoding="utf-8", newline="\n")
    state = dict(state="running", policy="sina-tool-exact-null-v1", source_commit=args.commit, executable_sha256=sha(args.binary.read_bytes()), profile_sha256=sha(args.profile.read_bytes()), batches=[], requests=0, bytes=0, received=0, quarantined=0, old_stop_modified=False, covered=False, financial_eligible=False)
    write(args.output / "state.json", state)
    for plan in plans:
        outcome = run_batch(args.binary, args.output, args.profile, plan)
        state["batches"].append(dict(plan=plan.name, **outcome))
        for k in ("requests", "bytes", "received", "quarantined"):
            state[k] += outcome[k]
        if not outcome["success"]:
            state["state"] = "blocked-with-diagnostics"
        write(args.output / "state.json", state)
        print(f"批次{len(state['batches'])}：{outcome['state']}；实际{state['requests']}请求，正常{state['received']}，失败隔离{state['quarantined']}", flush=True)
        if not outcome["success"]:
            break
        time.sleep(1)
    if state["state"] == "running":
        state["state"] = "completed-with-unavailable-contracts" if state["quarantined"] else "completed-raw-only"
    if state["requests"] > 175 or state["bytes"] > (15 << 20) + 5:
        raise ValueError("工具显式模式总量越界")
    write(args.output / "state.json", state)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
