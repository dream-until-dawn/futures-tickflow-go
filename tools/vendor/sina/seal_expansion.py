"""封存有界扩样的真实收据/失败/当前码矩阵；不联网、不把计划数当完成数。"""
import argparse
import ast
import hashlib
import io
import json
from pathlib import Path
import sys
from seal import quality


def digest(b):
    return hashlib.sha256(b).hexdigest()


def write(path, value):
    with path.open("x", encoding="utf-8", newline="\n") as f:
        json.dump(value, f, ensure_ascii=False, indent=2)
        f.write("\n")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("execution", type=Path)
    p.add_argument("initial", type=Path)
    p.add_argument("destination", type=Path)
    args = p.parse_args()
    state = json.loads((args.execution / "state.json").read_text(encoding="utf-8"))
    if state["state"] not in {"completed-raw-only", "blocked-with-diagnostics"}:
        raise ValueError("执行仍在运行或未知，不能伪装终止封存")
    args.destination.mkdir(exist_ok=False)
    (args.destination / "raw").mkdir()
    (args.destination / "receipts").mkdir()
    for name in ("state.json", "proposal.json", "build-info.txt", "static-cons-plan.json"):
        (args.destination / name).write_bytes((args.execution / name).read_bytes())
    ledger, qualities = [], []
    static_result = dict(status="not-obtained")
    plans = [(name, False) for name in sorted(args.execution.glob("proposed-batch-*.json"))] + [(args.execution / "static-cons-plan.json", True)]
    for path, static in plans:
        plan = json.loads(path.read_text(encoding="utf-8"))
        folder = args.execution / ("static-cons" if static else path.stem)
        for j in plan:
            receipt_path = folder / "archive" / (j["id"] + ".json")
            item = dict(id=j["id"], url=j["url"], kind="static-source" if static else "vendor/sina/request-bound", state="not-started-after-stop", attempted=False)
            if receipt_path.exists():
                receipt_raw = receipt_path.read_bytes()
                r = json.loads(receipt_raw)
                item.update(state=r["state"], attempted=r.get("http", {}).get("attempted", False), receipt=r)
                (args.destination / "receipts" / (j["id"] + ".json")).write_bytes(receipt_raw)
                if r["body_state"] != "none":
                    raw = (folder / "archive" / (j["id"] + ".body")).read_bytes()
                    if len(raw) != r["bytes"] or digest(raw) != r["evidence"]["sha256"] or r["evidence"]["url"] != j["url"]:
                        raise ValueError("原件与真实收据不符")
                    (args.destination / "raw" / (j["id"] + ".jsonp")).write_bytes(raw)
                    item.update(bytes=len(raw), sha256=digest(raw), raw="raw/" + j["id"] + ".jsonp")
                    if static:
                        static_result = dict(status=r["state"], evidence=r["evidence"], executed=False)
                        if r["state"] == "received":
                            tree = ast.parse(raw.decode("utf-8", "strict"))
                            found = []
                            for node in tree.body:
                                if isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == "zh_subscribe_exchange_symbol_url" for t in node.targets):
                                    found.append(node.value.value if isinstance(node.value, ast.Constant) and isinstance(node.value.value, str) else None)
                            static_result["constant_values"] = found
                            static_result["new_retired_roster_proved"] = False
                            static_result["route"] = "known-current-map-only" if found == ["https://vip.stock.finance.sina.com.cn/quotes_service/view/js/qihuohangqing.js"] else "literal-only-not-followed; no-retired-roster-certification"
                    else:
                        record = dict(code=j["id"][6:], exchange=j["exchange"], evidence=r["evidence"], state=r["state"], calendar_certified=False, historical_roster_complete=False, financial_eligible=False)
                        try:
                            record.update(quality(raw))
                        except (ValueError, UnicodeError, KeyError) as error:
                            record["parse_failure"] = str(error)
                        qualities.append(record)
            ledger.append(item)
    market = [x for x in ledger if x["kind"].startswith("vendor/")]
    if sum(x["attempted"] for x in market) != state["market_requests"] or sum(x["attempted"] for x in ledger if x["kind"] == "static-source") != state["static_requests"]:
        raise ValueError("计划/实际台账计数不符")
    if sum(x.get("bytes", 0) for x in ledger) != state["bytes"]:
        raise ValueError("实际字节总数不符")
    # 联合旧11个与本轮结果，但不把当前目录扩样提升成历史退市全集。
    index = {(x["exchange"], x["code"]): x for x in qualities}
    old_quality = json.loads((args.initial / "daily-quality.json").read_text(encoding="utf-8"))
    for x in old_quality:
        if x["scope"] == "current_observed_contract_sample":
            index[(x["exchange"], x["code"])] = x
    matrix = []
    for row in json.loads((args.initial / "contract-matrix.json").read_text(encoding="utf-8")):
        for c in row.get("current_observed_contracts", []):
            observed = index.get((c["exchange"], c["code"]))
            matrix.append(dict(**c, list_evidence=row["list_evidence"], daily=observed, state="raw-quality-observed" if observed and "parse_failure" not in observed else "not-proved", historical_roster_complete=False, independent_calendar=False, financial_eligible=False))
    write(args.destination / "request-ledger.json", ledger)
    write(args.destination / "daily-quality.json", qualities)
    write(args.destination / "current-coverage.json", matrix)
    write(args.destination / "static-route.json", static_result)
    write(args.destination / "scope.json", dict(provider="sina", class_identity="vendor/request-bound", observed_current_codes=len(matrix), current_raw_quality_available=sum(x["state"] == "raw-quality-observed" for x in matrix), new_market_requests=state["market_requests"], new_static_requests=state["static_requests"], actual_bytes=state["bytes"], official_dce_gfex_requests=0, production_changes=0, complete_2021_expired_roster=False, official_calendar_certified=False, financial_eligible=False, timestamp_semantics="request-start UTC only; neither response completion nor exchange modification; no PIT qualification"))
    files = []
    for f in sorted(args.destination.rglob("*")):
        if f.is_file():
            raw = f.read_bytes()
            files.append(dict(file=f.relative_to(args.destination).as_posix(), bytes=len(raw), sha256=digest(raw)))
    write(args.destination / "files.json", files)
    print(f"封存{len(files)}件；实际市场{state['market_requests']}/静态{state['static_requests']}次，共{state['bytes']}字节；当前码矩阵{len(matrix)}行")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
