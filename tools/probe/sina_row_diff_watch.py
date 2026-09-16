#!/usr/bin/env python3
"""(x) 探针：新浪日线收盘后的响应在两次取数之间怎么变（v0.6 晚到行片，评审方 2026-09-15 要求）。

起因：2026-09-15 读到 RB2610 当日那一行出现之后又消失、来回翻（sina_settle_watch.py 与一个 10 分钟探针的读数）。
晚到行的修法依赖一个前提，它其实是两问：
    一、两次响应的行集合，较短那份是不是较长那份按日期对齐的【严格前缀】（只少末尾几行）
    二、共同日期上的行，逐字段（d/o/h/l/c/v/p/s）是否相同 —— 不同就是「源给了、后来改了」
这个探针每次取数都回答这两问，并把原始响应体落盘，留作离线再分析。

⛔ 不按「连续几次相同」判停：「几分钟内没变」是缺席证据，证明不了「是最终值」。只在 --hard-stop 停。
   明早开盘前用 --once 再单取一次。

每次每个合约印：
    行数 · 响应哈希（前 12 位）· 哈希变没变
    当日那一行（整行全部字段），以及与【上一次有这一行时】相比哪些字段变了
    与上一次响应比：前缀关系 · 共同日期上字段不同的行（逐字段）
    当日行出现 / 消失的时刻
原始响应体在哈希与该合约上一次不同时落盘：<raw-dir>/<YYYYMMDD-HHMMSS>_<合约>_<哈希前 12 位>.jsonp

用法：python tools/probe/sina_row_diff_watch.py --out <文件> --raw-dir <目录> [--every 5] [--hard-stop 20:30] [--once]
只用标准库；不连快期，不需要凭证。
"""

import argparse
import hashlib
import io
import json
import os
import sys
import time
import urllib.request
from datetime import datetime, timedelta, timezone

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

CST = timezone(timedelta(hours=8))
URL = ("https://stock2.finance.sina.com.cn/futures/api/jsonp.php/"
       "var%20_=/InnerFuturesNewService.getDailyKLine?symbol=")
FIELDS = ("d", "o", "h", "l", "c", "v", "p", "s")


def now():
    return datetime.now(CST)


def fetch(sym):
    rq = urllib.request.Request(URL + sym, headers={"Referer": "https://finance.sina.com.cn"})
    body = urllib.request.urlopen(rq, timeout=30).read()
    raw = body.decode("utf-8", "replace")
    i, j = raw.find("("), raw.rfind(")")
    rows = json.loads(raw[i + 1:j])
    return body, rows


def compare(prev_rows, rows):
    """返回（前缀关系一句话, 共同日期上字段不同的行列表）。"""
    pd = [r.get("d") for r in prev_rows]
    cd = [r.get("d") for r in rows]
    short, long_ = (pd, cd) if len(pd) <= len(cd) else (cd, pd)
    if pd == cd:
        rel = "日期序列相同"
    elif long_[:len(short)] == short:
        rel = "日期上是严格前缀（%s 少末尾 %d 行）" % ("上一次" if len(pd) < len(cd) else "这一次", len(long_) - len(short))
    else:
        only_prev = sorted(set(pd) - set(cd))
        only_cur = sorted(set(cd) - set(pd))
        rel = "⛔ 不是前缀：只在上一次 %s · 只在这一次 %s" % (only_prev[:10], only_cur[:10])
    pm = {r.get("d"): r for r in prev_rows}
    diffs = []
    for r in rows:
        p = pm.get(r.get("d"))
        if p is None:
            continue
        ch = {k: (p.get(k), r.get(k)) for k in FIELDS if str(p.get(k)) != str(r.get(k))}
        if ch:
            diffs.append((r.get("d"), ch))
    return rel, diffs


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbols", default="RB0,RB2610,RB2701")
    ap.add_argument("--every", type=float, default=5.0, help="分钟")
    ap.add_argument("--hard-stop", default="20:30")
    ap.add_argument("--day", default=None, help="当日那一行的日期，默认今天 YYYY-MM-DD")
    ap.add_argument("--once", action="store_true")
    ap.add_argument("--out", required=True)
    ap.add_argument("--raw-dir", required=True)
    a = ap.parse_args()
    syms = [s for s in a.symbols.split(",") if s]
    day = a.day or now().strftime("%Y-%m-%d")
    h, m = map(int, a.hard_stop.split(":"))
    hard = now().replace(hour=h, minute=m, second=0, microsecond=0)
    os.makedirs(a.raw_dir, exist_ok=True)
    out = io.open(a.out, "a", encoding="utf-8")

    def log(line):
        out.write(line + "\n")
        out.flush()
        print(line, flush=True)

    log("# sina_row_diff_watch 启动 %s · 合约 %s · 当日行 %s · 每 %.1f 分钟 · 硬停 %s · once=%s"
        % (now().strftime("%Y-%m-%d %H:%M:%S %z"), syms, day, a.every, a.hard_stop, a.once))
    prev = {s: None for s in syms}          # (hash, rows)
    prev_today = {s: None for s in syms}    # 上一次有当日行时的那一行
    present = {s: None for s in syms}       # 上一次当日行在不在
    while True:
        t = now()
        ts = t.strftime("%H:%M:%S")
        for s in syms:
            try:
                body, rows = fetch(s)
            except Exception as e:
                log("%s %s 取数失败：%r（不当成「没变」）" % (ts, s, e))
                continue
            hx = hashlib.sha256(body).hexdigest()[:12]
            changed = prev[s] is None or prev[s][0] != hx
            if changed:
                fn = os.path.join(a.raw_dir, "%s_%s_%s.jsonp" % (t.strftime("%Y%m%d-%H%M%S"), s, hx))
                with open(fn, "wb") as f:
                    f.write(body)
            today = next((r for r in rows if r.get("d") == day), None)
            log("%s %s 行数 %d · 哈希 %s（%s）· 末行 %s"
                % (ts, s, len(rows), hx, "首次" if prev[s] is None else ("变了" if changed else "没变"),
                   rows[-1].get("d") if rows else None))
            if today is not None:
                tv = {k: today.get(k) for k in FIELDS}
                if prev_today[s] is None:
                    log("    当日行 %s（首次看到）" % json.dumps(tv, ensure_ascii=False))
                else:
                    ch = [k for k in FIELDS if str(prev_today[s].get(k)) != str(today.get(k))]
                    log("    当日行 %s · 与上一次有它时相比变了 %s" % (json.dumps(tv, ensure_ascii=False), ch))
                prev_today[s] = today
            if present[s] is not None and present[s] != (today is not None):
                log("    ↳ 当日行%s" % ("出现" if today is not None else "⛔ 消失"))
            present[s] = today is not None
            if prev[s] is not None and changed:
                rel, diffs = compare(prev[s][1], rows)
                log("    与上一次响应：%s · 共同日期上字段不同的行 %d 行" % (rel, len(diffs)))
                for d, ch in diffs[:20]:
                    log("      %s %s" % (d, json.dumps(ch, ensure_ascii=False)))
            prev[s] = (hx, rows)
        if a.once or now() >= hard:
            log("# 停 %s（%s）" % (now().strftime("%H:%M:%S"), "单取一次" if a.once else "到硬停"))
            break
        time.sleep(a.every * 60)
    out.close()


if __name__ == "__main__":
    main()
