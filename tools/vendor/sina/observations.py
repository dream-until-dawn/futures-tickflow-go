"""离线核当前合约观察，生成精确日线清单；不推断历史全集、上市日或日历。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from urllib.parse import urlencode


def unique_object(pairs):
    out = {}
    for k, v in pairs:
        if k in out:
            raise ValueError("重复JSON键")
        out[k] = v
    return out


def contract_observations(raw, exchange):
    values = json.loads(raw, object_pairs_hook=unique_object)
    if not isinstance(values, list) or not values:
        raise ValueError("列表空/未知")
    contracts, excluded, seen = [], [], set()
    for row in values:
        if not isinstance(row, dict) or row.get("exchange", "").upper() != exchange:
            raise ValueError("合约列表市场不匹配")
        code = row.get("symbol", "")
        if not isinstance(code, str) or code in seen:
            raise ValueError("重复/非法代码")
        seen.add(code)
        if re.fullmatch(r"[A-Z]{1,8}0", code):
            excluded.append(code)
            continue
        if not re.fullmatch(r"[A-Z]{1,8}[0-9]{2}(?:0[1-9]|1[0-2])", code):
            raise ValueError("非真实年月格式，不能猜补")
        contracts.append(dict(code=code, exchange=exchange, vendor_tradedate=row.get("tradedate"), identity="vendor-observed-not-official-roster"))
    return contracts, excluded


def build(archive, rows, selections):
    jobs, matrix, selected = [], [], set()
    for row in rows:
        node = row["node"]
        receipt = json.loads((archive / ("list-" + node + ".json")).read_text(encoding="utf-8"))
        raw = (archive / ("list-" + node + ".body")).read_bytes()
        if receipt["state"] != "received" or hashlib.sha256(raw).hexdigest() != receipt["evidence"]["sha256"] or len(raw) != receipt["bytes"]:
            raise ValueError("列表收据/原件身份不符")
        contracts, excluded = contract_observations(raw, row["exchange"])
        matrix.append({**row, "list_evidence": receipt["evidence"], "current_observed_contracts": contracts, "excluded_continuous": excluded, "status": "observed-current-list-only; historical-expired-roster-and-schedule-unknown"})
        for contract in contracts:
            code = contract["code"]
            key = row["exchange"] + "." + code
            if key not in selections:
                continue
            if key in selected:
                raise ValueError("跨节点重复所选代码")
            selected.add(key)
            url = "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_V21052021_4_12=/InnerFuturesNewService.getDailyKLine?" + urlencode(dict(symbol=code, type="2021_04_12"))
            jobs.append(dict(id="daily-" + code, url=url, format="vendor-raw-unqualified", day=0, exchange=row["exchange"], product=re.sub(r"[0-9]+$", "", code).lower(), version="sina-vendor-v1"))
    if selected != set(selections):
        raise ValueError("选择不在已取得真实列表，禁止猜测")
    # 三个DCE月均价族没有节点/历史全集依据，必须在矩阵保留unknown。
    matrix.extend(dict(exchange="DCE", product=p, status="unknown-no-observed-vendor-node-or-contract-roster", historical_roster_complete=False, calendar_certified=False) for p in ("l_f", "v_f", "pp_f"))
    return jobs, matrix


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("archive", "product_matrix", "prior_plan", "selection", "next_plan", "matrix"):
        p.add_argument(name, type=Path)
    args = p.parse_args()
    selections = json.loads(args.selection.read_text(encoding="utf-8"))
    if len(selections) != len(set(selections)):
        raise ValueError("选择重复")
    jobs, matrix = build(args.archive, json.loads(args.product_matrix.read_text(encoding="utf-8")), selections)
    prior = json.loads(args.prior_plan.read_text(encoding="utf-8"))
    if len(prior) + len(jobs) > 40:
        raise ValueError("累计请求预算超限")
    for path, value in ((args.next_plan, prior + jobs), (args.matrix, matrix)):
        with path.open("x", encoding="utf-8", newline="\n") as f:
            json.dump(value, f, ensure_ascii=False, indent=2)
            f.write("\n")
    print(f"合约观察矩阵{len(matrix)}行；新增精确日线{len(jobs)}项；累计清单{len(prior) + len(jobs)}，未联网")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
