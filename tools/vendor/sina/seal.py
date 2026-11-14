"""封存已执行批次的原字节、收据和逐合约质量矩阵；不发请求。"""
import argparse
from collections import Counter
from decimal import Decimal, InvalidOperation
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from coverage import unique_object


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def quality(raw, callback="_V21052021_4_12"):
    text = raw.decode("ascii", "strict").strip()
    comment = "/*<script>location.href='//sina.com';</script>*/"
    if text.startswith(comment):
        text = text[len(comment):].strip()
    prefix = "var " + callback + "=("
    if not text.startswith(prefix) or not text.endswith(");"):
        raise ValueError("惰性包装不符")
    rows = json.loads(text[len(prefix):-2], object_pairs_hook=unique_object)
    if not isinstance(rows, list) or not rows:
        raise ValueError("零行/未知")
    dates = [r["d"] for r in rows]
    if dates != sorted(set(dates)):
        raise ValueError("重复/乱序日期")
    target = [r for r in rows if r["d"] >= "2021-01-01"]
    issues, zero, absent = [], 0, 0
    for r in target:
        try:
            settle = Decimal(r.get("s", "NaN"))
            zero += int(settle == 0)
            absent += int(not settle.is_finite())
            vals = [Decimal(r[k]) for k in ("o", "h", "l", "c", "v", "p")]
            o, h, l, c, v, oi = vals
            if any(not x.is_finite() for x in vals) or o <= 0 or l <= 0 or h < max(o, c) or l > min(o, c) or v < 0 or oi < 0 or v != v.to_integral_value() or oi != oi.to_integral_value():
                issues.append(r["d"])
        except (KeyError, InvalidOperation):
            issues.append(r["d"])
    return dict(raw_rows=len(rows), raw_first=dates[0], raw_last=dates[-1], target_rows=len(target), target_first=target[0]["d"] if target else None, target_last=target[-1]["d"] if target else None, zero_settlement=zero, absent_settlement=absent, invalid_ohlc_or_quantity_days=issues, dates_after_20261114=[d for d in dates if d > "2026-11-14"], calendar_certified=False, historical_roster_complete=False, financial_eligible=False)


def write(path, value):
    with path.open("x", encoding="utf-8", newline="\n") as f:
        json.dump(value, f, ensure_ascii=False, indent=2)
        f.write("\n")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("batch", type=Path)
    p.add_argument("destination", type=Path)
    p.add_argument("fixtures", type=Path)
    args = p.parse_args()
    args.destination.mkdir(exist_ok=False)
    raw_dir = args.destination / "raw"
    raw_dir.mkdir()
    receipts = args.destination / "receipts"
    receipts.mkdir()
    plan = json.loads((args.batch / "plan-03.json").read_text(encoding="utf-8"))
    ledger, daily = [], []
    used_bytes = 0
    for j in plan:
        receipt_raw = (args.batch / "archive" / (j["id"] + ".json")).read_bytes()
        r = json.loads(receipt_raw)
        raw = (args.batch / "archive" / (j["id"] + ".body")).read_bytes()
        if r["state"] != "received" or r["http"]["status"] != 200 or r["evidence"]["url"] != j["url"] or sha(raw) != r["evidence"]["sha256"] or len(raw) != r["bytes"]:
            raise ValueError("原件身份/状态不符")
        (raw_dir / (j["id"] + ".jsonp")).write_bytes(raw)
        (receipts / (j["id"] + ".json")).write_bytes(receipt_raw)
        ledger.append(dict(id=j["id"], url=j["url"], bytes=len(raw), sha256=sha(raw), request_started_at=r["http"]["requested_at"], receipt=r, raw="raw/" + j["id"] + ".jsonp"))
        used_bytes += len(raw)
        if j["id"].startswith("daily-"):
            daily.append(dict(code=j["id"][6:], exchange=j["exchange"], sha256=sha(raw), scope="current_observed_contract_sample", **quality(raw)))
    if len(ledger) != 40 or used_bytes != 359219:
        raise ValueError("批次预算统计不符")
    write(args.destination / "request-ledger.json", ledger)
    write(args.destination / "plan.json", plan)
    write(args.destination / "contract-matrix.json", json.loads((args.batch / "contract-matrix.json").read_text(encoding="utf-8")))
    for r in json.loads((args.fixtures / "receipts.json").read_text(encoding="utf-8")):
        raw = (args.fixtures / (r["name"] + ".jsonp")).read_bytes()
        if sha(raw) != r["sha256"]:
            raise ValueError("复用原件不符")
        daily.append(dict(code=r["name"], sha256=sha(raw), scope="reused_expired_sample_not_historical_roster", **quality(raw)))
    for name in ("offline", "current-offline"):
        write(args.destination / (name + ".json"), json.loads((args.batch / name / "coverage.json").read_text(encoding="utf-8")))
    write(args.destination / "daily-quality.json", daily)
    write(args.destination / "scope.json", dict(schema="sina-vendor-evidence-v1", provider="sina", data_class="vendor/request-bound", requests=40, bytes=used_bytes, retries=0, redirects=0, official_dce_gfex_requests=0, additional_requests_authorized=False, complete_2021_history=False, historical_calendar_certified=False, production_changed=False, tag_created=False, request_timestamp_semantics="New Go batch refs use request-start UTC, not response-completion nor exchange-change time; reused original Python receipts retain their actual original timestamps.", build_provenance="Acquisition executable was built from uncommitted working tree based on ad4956f; original executable retained outside Git. Final candidate is verified separately offline; not claimed byte-identical to acquisition executable.", release="candidate; independent acceptance and exact CI tracked separately"))
    manifest = []
    for path in sorted(args.destination.rglob("*")):
        if path.is_file():
            b = path.read_bytes()
            manifest.append(dict(file=path.relative_to(args.destination).as_posix(), bytes=len(b), sha256=sha(b)))
    write(args.destination / "files.json", manifest)
    print(f"{len(manifest)}文件封存；40请求/{used_bytes}字节；15样本质量矩阵，无新增请求")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
