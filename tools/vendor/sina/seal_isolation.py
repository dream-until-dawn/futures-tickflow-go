"""离线封存显式隔离批次，合并真实当前码观察；null不生成行情行或覆盖。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import sys
from seal import quality


def digest(b):
    return hashlib.sha256(b).hexdigest()


def write(path, obj):
    with path.open("x", encoding="utf-8", newline="\n") as f:
        json.dump(obj, f, ensure_ascii=False, indent=2)
        f.write("\n")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("execution", type=Path)
    p.add_argument("prior", type=Path)
    p.add_argument("destination", type=Path)
    args = p.parse_args()
    state = json.loads((args.execution / "state.json").read_text(encoding="utf-8"))
    if state["state"] not in {"completed-with-unavailable-contracts", "completed-raw-only", "blocked-with-diagnostics"}:
        raise ValueError("执行未知/未终止，不能伪装封存")

    if state.get("covered") is not False or state.get("financial_eligible") is not False or state.get("old_stop_modified") is not False:
        raise ValueError("输入状态资格或原stop声明不符")
    profile_raw = (args.execution / "profile.json").read_bytes()
    if digest(profile_raw) != state["profile_sha256"]:
        raise ValueError("实际配置SHA不符")
    profile = json.loads(profile_raw)
    if digest((args.prior / "files.json").read_bytes()) != profile["prior_manifest_sha256"]:
        raise ValueError("原封存清单与执行配置不符")
    # 重新核原严格批次；不可为新政策修改原stop或失败原件。
    prior_files = json.loads((args.prior / "files.json").read_text(encoding="utf-8"))
    for f in prior_files:
        raw = (args.prior / f["file"]).read_bytes()
        if len(raw) != f["bytes"] or digest(raw) != f["sha256"]:
            raise ValueError("原封存被改变")
    args.destination.mkdir(exist_ok=False)
    for name in ("raw", "receipts", "started", "reports"):
        (args.destination / name).mkdir()
    for name in ("state.json", "profile.json", "build-info.txt"):
        (args.destination / name).write_bytes((args.execution / name).read_bytes())
    ledger, qualities = [], []
    for path in sorted(args.execution.glob("batch-*.json")):
        if "-stdout" in path.name:
            continue
        jobs = json.loads(path.read_text(encoding="utf-8"))
        (args.destination / path.name).write_bytes(path.read_bytes())
        folder = args.execution / path.stem
        report = None
        if (folder / "report.json").exists():
            raw_report = (folder / "report.json").read_bytes()
            report = json.loads(raw_report)
            (args.destination / "reports" / path.name).write_bytes(raw_report)
            if report != json.loads((args.execution / (path.stem + "-stdout.json")).read_text(encoding="utf-8")):
                raise ValueError("CLI与归档报告不一致")
        index = {r["id"]: r for r in report["results"]} if report else {}
        for job in jobs:
            r = index.get(job["id"], dict(id=job["id"], state="not-attempted-after-global-stop", covered=False, financial_eligible=False, bytes=0))
            if r["covered"] or r["financial_eligible"]:
                raise ValueError("资格被提升")
            ledger.append(dict(job=job, **r))
            for kind in ("receipts", "started"):
                src = folder / kind / (job["id"] + ".json")
                if src.exists():
                    (args.destination / kind / src.name).write_bytes(src.read_bytes())
            if r.get("http", {}).get("response_received"):
                raw = (folder / "raw" / (job["id"] + ".jsonp")).read_bytes()
                if len(raw) != r["bytes"] or digest(raw) != r["evidence"]["sha256"] or r["evidence"]["url"] != job["url"]:
                    raise ValueError("新原件身份不符")
                (args.destination / "raw" / (job["id"] + ".jsonp")).write_bytes(raw)
                q = dict(code=job["id"][6:], exchange=job["exchange"], evidence=r["evidence"], state=r["state"], covered=False, financial_eligible=False)
                if r["state"] == "received":
                    q.update(quality(raw))
                    if q["raw_rows"] != r["records"]:
                        raise ValueError("独立行数不符")
                elif r["state"] == "failed-quarantined":
                    b = raw.strip()
                    prefix = b"/*<script>location.href='//sina.com';</script>*/"
                    if b.startswith(prefix):
                        b = b[len(prefix):].strip()
                    if b != b"var _V21052021_4_12=(null);" or r["records"] != 0 or r["http"]["status"] != 200 or not r["http"]["complete"]:
                        raise ValueError("隔离不是完整200精确null")
                    q.update(target_rows=0, failure_class="exact-null-unavailable", error=r["error"], calendar_certified=False, historical_roster_complete=False)
                else:
                    q.update(target_rows=0, failure_class=r.get("failure_class", "unknown"), error=r.get("error", ""))
                qualities.append(q)
    attempts = sum(bool(r.get("http", {}).get("attempted")) for r in ledger)
    if attempts != state["requests"] or sum(r["bytes"] for r in ledger) != state["bytes"]:
        raise ValueError("真实请求/字节汇总不符")
    matrix = json.loads((args.prior / "current-coverage.json").read_text(encoding="utf-8"))
    index = {(q["exchange"], q["code"]): q for q in qualities}
    for row in matrix:
        q = index.get((row["exchange"], row["code"]))
        if q is not None:
            row["daily"] = q
            row["state"] = "raw-array-observed" if q["state"] == "received" else q["state"]
        elif row.get("daily") and row["daily"].get("parse_failure"):
            row["state"] = "prior-strict-failure-null-not-retried"
        elif row["state"] == "raw-quality-observed":
            row["state"] = "raw-array-observed"
    normal = [r for r in ledger if r["state"] == "received"]
    isolated = [r for r in ledger if r["state"] == "failed-quarantined"]
    write(args.destination / "request-ledger.json", ledger)
    write(args.destination / "daily-quality.json", qualities)
    write(args.destination / "current-coverage.json", matrix)
    write(args.destination / "scope.json", dict(identity="vendor/sina/request-bound", source_commit=state["source_commit"], actual_requests=attempts, actual_bytes=state["bytes"], normal_arrays=len(normal), failed_quarantined=len(isolated), normal_target_rows=sum(q["target_rows"] for q in qualities if q["state"] == "received"), observed_current_codes=len(matrix), current_array_codes=sum(r["state"] == "raw-array-observed" for r in matrix), old_stop_preserved=True, old_a2711_retried=False, unattempted_current_codes=sum(r["state"].startswith("not-attempted") or r["state"] == "not-proved" for r in matrix), full_2021_expired_roster=False, independent_calendar=False, financial_eligible=False, production_changed=False, timestamp_semantics="request-start UTC; no response-completion or exchange-modification guarantee"))
    files = []
    for path in sorted(args.destination.rglob("*")):
        if path.is_file():
            raw = path.read_bytes()
            files.append(dict(file=path.relative_to(args.destination).as_posix(), bytes=len(raw), sha256=digest(raw)))
    write(args.destination / "files.json", files)
    print(f"封存{len(files)}件，实际{attempts}请求/{state['bytes']}字节；正常{len(normal)}，失败隔离{len(isolated)}，原stop逐件SHA不变")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
