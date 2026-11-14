"""惰性读取新浪已归档的产品节点常量；只生成精确清单，绝不联网或执行JS。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from urllib.parse import urlencode

TOKEN = re.compile(r"\s*(?:(//[^\n]*(?:\n|$))|('(?:[^'\\]|\\['\\])*')|([A-Za-z_][A-Za-z0-9_]*)|([{}\[\]:,]))")


def product_map(raw):
    text = raw.decode("gb18030", "strict")
    prefix = "ARRFUTURESNODES ="
    if not text.startswith(prefix):
        raise ValueError("不是已审阅产品常量")
    pos = len(prefix)

    def token():
        nonlocal pos
        while True:
            match = TOKEN.match(text, pos)
            if not match:
                raise ValueError("常量含非批准语法")
            pos = match.end()
            if match[1]:
                continue
            if match[2]:
                return "string", match[2][1:-1].replace("\\'", "'").replace("\\\\", "\\")
            if match[3]:
                return "key", match[3]
            return match[4], match[4]

    current = token()

    def consume(kind):
        nonlocal current
        if current[0] != kind:
            raise ValueError("常量结构错误")
        value = current[1]
        current = token()
        return value

    def array():
        consume("[")
        result = []
        while current[0] != "]":
            result.append(array() if current[0] == "[" else consume("string"))
            if current[0] != ",":
                break
            consume(",")
        consume("]")
        return result

    consume("{")
    result = {}
    while current[0] != "}":
        key = consume("key")
        if key in result or key not in {"dce", "gfex", "shfe", "czce", "cffex"}:
            raise ValueError("重复/未知交易所常量")
        consume(":")
        result[key] = array()
        if current[0] != ",":
            break
        consume(",")
    # 不consume末尾，否则会把后续真实页面脚本当常量继续解析。
    if current[0] != "}" or not text[pos:].lstrip().startswith(";"):
        raise ValueError("常量未准确结束")
    return result


def list_jobs(raw):
    mapping = product_map(raw)
    jobs, rows, seen = [], [], set()
    for exchange in ("dce", "gfex"):
        market = mapping[exchange]
        if not market or not isinstance(market[0], str):
            raise ValueError("市场常量缺失")
        for entry in market[1:]:
            if not isinstance(entry, list) or not 3 <= len(entry) <= 4:
                raise ValueError("产品节点形状未知")
            name, node = entry[:2]
            if not re.fullmatch(r"[a-z][a-z0-9_]{0,47}", node) or node in seen:
                raise ValueError("节点错误/重复")
            seen.add(node)
            url = "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHQFuturesData?" + urlencode(dict(page="1", sort="position", asc="0", node=node, base="futures"))
            jobs.append(dict(id="list-" + node, url=url, format="vendor-raw-unqualified", day=0, exchange=exchange.upper(), version="sina-vendor-v1"))
            rows.append(dict(exchange=exchange.upper(), name=name, node=node, source_sha256=hashlib.sha256(raw).hexdigest(), historical_roster_complete=False, calendar_certified=False, status="observed_product_node_only"))
    return jobs, rows


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("body", type=Path)
    p.add_argument("prior_plan", type=Path)
    p.add_argument("next_plan", type=Path)
    p.add_argument("matrix", type=Path)
    args = p.parse_args()
    jobs, rows = list_jobs(args.body.read_bytes())
    prior = json.loads(args.prior_plan.read_text(encoding="utf-8"))
    if len(prior) + len(jobs) > 40 or len({j["url"] for j in prior + jobs}) != len(prior) + len(jobs):
        raise ValueError("总批次清单超限/重复")
    for path, value in ((args.next_plan, prior + jobs), (args.matrix, rows)):
        with path.open("x", encoding="utf-8", newline="\n") as f:
            json.dump(value, f, ensure_ascii=False, indent=2)
            f.write("\n")
    print(f"冻结产品节点{len(rows)}个；累计精确请求清单{len(prior) + len(jobs)}项；尚未发请求")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
