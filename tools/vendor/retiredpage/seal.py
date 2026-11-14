"""只离线封存已取得的control.js与无新目录结论，不执行JS或网络请求。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("execution", type=Path)
    p.add_argument("stdout", type=Path)
    p.add_argument("destination", type=Path)
    args = p.parse_args()
    args.destination.mkdir(exist_ok=False)
    for source, name in ((args.execution / "archive/sina-control-static.body", "control.jsonp"), (args.execution / "archive/sina-control-static.json", "receipt.json"), (args.execution / "plan.json", "plan.json"), (args.stdout, "result.json")):
        (args.destination / name).write_bytes(source.read_bytes())
    raw = (args.destination / "control.jsonp").read_bytes()
    receipt = json.loads((args.destination / "receipt.json").read_text(encoding="utf-8"))
    if len(raw) != receipt["bytes"] or hashlib.sha256(raw).hexdigest() != receipt["evidence"]["sha256"]:
        raise ValueError("control原件不符")
    text = raw.decode("gb18030", "strict")
    analysis = dict(identity="static-js-unqualified", bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest(), encoding="gb18030 strict; parent HTML declares gb2312", lines=len(text.splitlines()), explicit_urls=re.findall(r"https?://[^\s]+", text), historical_roster_proved=False, javascript_executed=False, followup_requests=0, price_requests=0, financial_eligible=False, observed_calls=["view.init", "modular.init", "modular.select.change", "modular.select.query", "modular.select.query_history"])
    (args.destination / "analysis.json").write_text(json.dumps(analysis, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")
    files = [dict(file=f.name, bytes=len(f.read_bytes()), sha256=hashlib.sha256(f.read_bytes()).hexdigest()) for f in sorted(args.destination.iterdir())]
    (args.destination / "files.json").write_text(json.dumps(files, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(f"封存{len(files)}件，未追加请求")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
