"""惰性审核已冻结的JYS/PZ/YF常量，不执行JavaScript或发网络请求。"""
import ast
from collections import Counter
import re

STRING = r'''(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')'''


def review(raw):
    text = raw.decode("gb18030", "strict")
    text = re.sub(r"/\*[^*]*(?:\*(?!/)[^*]*)*\*/", "", text)
    markets, products, rows, seen = {}, {}, [], set()
    declared = set()
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("//"):
            continue
        declaration = re.fullmatch(r"var (JYS|PZ|YF) = new Array;", line)
        if declaration:
            name = declaration[1]
            if name in declared:
                raise ValueError("重复声明")
            declared.add(name)
            continue
        match = re.fullmatch(r"(JYS|PZ|YF)((?:\[\d+\])+)(?:\s*)=(?:\s*)(.*);", line)
        if not match or match[1] not in declared:
            raise ValueError("未知语句或未声明目录，不执行")
        name, indices, value = match.groups()
        keys = tuple(map(int, re.findall(r"\d+", indices)))
        identity = (name, keys)
        if identity in seen:
            raise ValueError("重复赋值")
        seen.add(identity)
        if value == "new Array" and ((name == "PZ" and len(keys) == 1) or (name == "YF" and len(keys) in (1, 2))):
            continue
        if re.fullmatch(STRING, value):
            decoded = ast.literal_eval(value)
            if name == "JYS" and len(keys) == 1:
                markets[keys[0]] = decoded
                continue
            if name == "PZ" and len(keys) == 2:
                products[keys] = decoded
                continue
        pair = re.fullmatch(r"new Array\(("+STRING+r"),("+STRING+r")\)", value)
        if name != "YF" or len(keys) != 3 or not pair:
            raise ValueError("未知目录常量")
        label, code = map(ast.literal_eval, pair.groups())
        if keys[0] not in markets or keys[:2] not in products:
            raise ValueError("目录父节点缺失")
        rows.append(dict(market=markets[keys[0]], product=products[keys[:2]], label=label, code=code))
    if declared != {"JYS", "PZ", "YF"} or not rows:
        raise ValueError("不完整目录")
    real = [row for row in rows if re.fullmatch(r"[A-Z]+\d{4}", row["code"])]
    targets = [row for row in real if row["market"] in ("大商所", "广期所")]
    return dict(identity="vendor-static-navigation-only", markets=markets,
                rows=rows, rows_by_market=dict(Counter(row["market"] for row in rows)),
                real_month_code_rows=len(real), target_market_real_codes=targets,
                historical_roster_proved=False, financial_eligible=False,
                javascript_executed=False, network_requests=0)


if __name__ == "__main__":
    import argparse
    import io
    import json
    from pathlib import Path
    import sys
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("original", type=Path, help="已冻结的静态目录原件；只读不联网")
    args = parser.parse_args()
    print(json.dumps(review(args.original.read_bytes()), ensure_ascii=False, indent=2))
