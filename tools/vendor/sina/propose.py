"""只从冻结当前列表提出去重的后续精确清单，不联网、不等于放行。"""
import argparse
import io
import json
from pathlib import Path
import sys
from urllib.parse import urlencode


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("evidence", type=Path)
    p.add_argument("destination", type=Path)
    args = p.parse_args()
    matrix = json.loads((args.evidence / "contract-matrix.json").read_text(encoding="utf-8"))
    plan = json.loads((args.evidence / "plan.json").read_text(encoding="utf-8"))
    done = {(j["exchange"], j["id"][6:]) for j in plan if j["id"].startswith("daily-")}
    remaining = {}
    for row in matrix:
        for c in row.get("current_observed_contracts", []):
            key = c["exchange"], c["code"]
            if key in done:
                continue
            if key in remaining:
                raise ValueError("当前列表有重复代码，不能盲去重")
            remaining[key] = dict(code=c["code"], exchange=c["exchange"], node=row["node"], source=row["list_evidence"], catalog_status="current-observed-only; expired-roster-unknown")
    ordered = sorted(remaining.values(), key=lambda r: (0 if r["exchange"] == "GFEX" else 1 if r["code"].startswith(("LG", "BZ")) else 2, r["exchange"], r["code"]))
    args.destination.mkdir(exist_ok=False)
    batches = []
    for start in range(0, len(ordered), 40):
        part = ordered[start:start + 40]
        jobs = []
        for r in part:
            code = r["code"]
            url = "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_V21052021_4_12=/InnerFuturesNewService.getDailyKLine?" + urlencode(dict(symbol=code, type="2021_04_12"))
            jobs.append(dict(id="daily-" + code, url=url, format="vendor-raw-unqualified", day=0, exchange=r["exchange"], product=code[:-4].lower(), version="sina-vendor-v1"))
        name = f"proposed-batch-{len(batches) + 1:02d}.json"
        (args.destination / name).write_text(json.dumps(jobs, indent=2) + "\n", encoding="utf-8", newline="\n")
        batches.append(dict(file=name, requests=len(jobs), bytes_limit=3 << 20, seconds_per_request=15, minimum_interval_seconds=1, per_response_bytes_limit=1 << 20, approved=False, scope="vendor daily raw only; no calendar or financial admission"))
    report = dict(status="proposal-only-no-network", already_acquired_current_codes=len(done), remaining_current_codes=len(ordered), deduplicated=ordered, batches=batches, total_request_limit=len(ordered), total_bytes_limit=len(batches) * (3 << 20), overflow_probe_allowance_bytes=len(batches), additional_requests_approved=False, historical_roster_route=dict(status="not-certified-no-known-retired-roster-endpoint", clue="frozen AKShare imports zh_subscribe_exchange_symbol_url from akshare.futures.cons", proposed_next_static_source="https://raw.githubusercontent.com/akfamily/akshare/main/akshare/futures/cons.py", extra_static_request_budget=1, maximum_bytes=1 << 20, timeout_seconds=15, retries=0, redirects=0, condition="Only after separate plan review; inspect literal constant. If it resolves to known current-only map, record no new historical route and stop; do not infer retired codes or execute SDK."))
    (args.destination / "proposal.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(f"仅生成建议：{len(ordered)}个剩余当前代码，{len(batches)}批，总上限{report['total_bytes_limit']}字节；没有放行或联网")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
