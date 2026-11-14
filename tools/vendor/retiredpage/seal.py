"""只离线封存逐项批准取得的静态原件与有限目录结论，不执行JS或网络请求。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from html.parser import HTMLParser


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("execution", type=Path)
    p.add_argument("stdout", type=Path)
    p.add_argument("destination", type=Path)
    p.add_argument("--modular", action="store_true", help="封存已批准modular静态请求，不追加网络")
    p.add_argument("--config", action="store_true", help="封存已批准config静态请求，不追加网络")
    p.add_argument("--history", action="store_true", help="封存唯一无参数历史HTML")
    p.add_argument("--history-script", action="store_true", help="封存唯一双斜线历史脚本")
    args = p.parse_args()
    args.destination.mkdir(exist_ok=False)
    if sum((args.modular, args.config, args.history, args.history_script)) > 1:
        raise ValueError("只能选择一种已执行模式")
    kind = "history-script" if args.history_script else ("history" if args.history else ("config" if args.config else ("modular" if args.modular else "control")))
    stem = "sina-history-script" if args.history_script else ("sina-history-html" if args.history else f"sina-{kind}-static")
    raw_name = f"{kind}.jsonp"
    for source, name in ((args.execution / f"archive/{stem}.body", raw_name), (args.execution / f"archive/{stem}.json", "receipt.json"), (args.execution / "plan.json", "plan.json"), (args.stdout, "result.json")):
        (args.destination / name).write_bytes(source.read_bytes())
    raw = (args.destination / raw_name).read_bytes()
    receipt = json.loads((args.destination / "receipt.json").read_text(encoding="utf-8"))
    if len(raw) != receipt["bytes"] or hashlib.sha256(raw).hexdigest() != receipt["evidence"]["sha256"]:
        raise ValueError("静态原件不符")
    text = raw.decode("utf-8" if args.config else "gb18030", "strict")
    analysis = dict(identity="static-js-unqualified", bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest(), encoding="gb18030 strict; parent HTML declares gb2312", lines=len(text.splitlines()), explicit_urls=re.findall(r"https?://[^\s]+", text), historical_roster_proved=False, javascript_executed=False, followup_requests=0, price_requests=0, financial_eligible=False, observed_calls=["view.init", "modular.init", "modular.select.change", "modular.select.query", "modular.select.query_history"])
    if args.modular:
        analysis.pop("observed_calls")
        analysis["observed_references"] = [dict(line=i, text=line.strip()) for i, line in enumerate(text.splitlines(), 1) if "config.api.get_history" in line or "jys_data.hy[" in line]
        analysis["limit"] = "query_history打开config.api.get_history；下拉框读取jys_data.hy。静态引用不自证历史全集，不执行也不追API。"
    if args.config:
        analysis.pop("observed_calls")
        analysis["encoding"] = "utf-8 strict; original HTML script charset=utf-8"
        analysis["observed_references"] = [dict(line=i, text=line.strip()) for i, line in enumerate(text.splitlines(), 1) if "get_history:" in line]
        analysis["limit"] = "只观察历史查询页路径，未访问该页或其API，不证明目录或历史全集。"
    if args.history:
        class Page(HTMLParser):
            def __init__(self):
                super().__init__()
                self.scripts = []
                self.selects = []
                self.options = 0
            def handle_starttag(self, tag, attrs):
                attrs = dict(attrs)
                if tag == "script" and "src" in attrs:
                    self.scripts.append(attrs["src"])
                if tag == "select":
                    self.selects.append(attrs.get("id"))
                if tag == "option":
                    self.options += 1
        page = Page()
        page.feed(text)
        analysis.pop("observed_calls")
        analysis.update(identity="static-html-unqualified", scripts=page.scripts, selects=page.selects, option_count=page.options, default_price_table_counted=False, limit="静态select占位；inline引用JYS/PZ/YF但无定义；默认A1109表不计行情，不翻页，不取子资源。")
    if args.history_script:
        analysis.pop("observed_calls")
        analysis["observed_references"] = [dict(line=i, text=line.strip()) for i, line in enumerate(text.splitlines(), 1) if any(key in line for key in ("YF[", "PZ[", "JYS"))]
        analysis["declared_catalog_objects"] = re.findall(r"\bvar\s+(JYS|PZ|YF)\s*=", text)
        analysis["limit"] = "仅引用目录对象，未定义JYS/PZ/YF，未证明2021起退市全集。其他HTTP字面量不跟随、不擅自改HTTPS。"
    (args.destination / "analysis.json").write_text(json.dumps(analysis, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")
    files = [dict(file=f.name, bytes=len(f.read_bytes()), sha256=hashlib.sha256(f.read_bytes()).hexdigest()) for f in sorted(args.destination.iterdir())]
    (args.destination / "files.json").write_text(json.dumps(files, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(f"封存{len(files)}件，未追加请求")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
