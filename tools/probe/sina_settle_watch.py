#!/usr/bin/env python3
"""(x) 探针：收盘之后，新浪日线「当日那一行」什么时候定下来（v0.6 片 A 评审 2026-09-14 登记）。

起因：Sync 判「这一天收盘了」只看日历的收盘时刻（日线 15:00）。而新浪日线的 s（结算价）
要等交易所出结算。15:00 到出结算之间 TsEnd ≤ now，那一行照样通过「完结」检查；
如果那时给的是临时值，它会被存下来，挑段跳过已覆盖之后【永远不会再拉】。

做法：从 --start（默认 15:00，北京时间）起，每 --every 分钟取一次各合约的日线，
印出【取数时刻】与当日那一行的 d/o/h/l/c/v/p/s，以及与上一次相比哪些字段变了。

⛔ 停：这支脚本的判停【已作废】（2026-09-15，评审方定，我认）——「连续两次相同」是缺席证据，
证明不了那是终值；2026-09-15 实测当日行出现之后还会再消失（docs/probe.md 6.22）。
⇒ 再量请用 sina_row_diff_watch.py（不按「没变」停，只 --hard-stop；并逐次核前缀与逐字段差异）。
本节以下关于判停的文字保留原样，是为了让 6.22 里那组读数读得回来。

停（作废的那条）：评审方给的判据是「结算价不再变 ＝ 连续两次相同」。
⚠️ 而那个判据有一个会提前停的输入：结算价公布之前，s 可能连续几次都是同一个临时值（例如 0）。
⇒ 这里在它之上加两条，并把原判据在输出里单独标出来（读者能看到两种停法各停在哪）：
    一、s 非 0
    二、不早于 --min-until（默认 17:00）
每个合约各自判；全部停了或到 --hard-stop（默认 20:30，夜盘开始前）就结束。

用法：python tools/probe/sina_settle_watch.py --symbols RB0,RB2610,RB2701 --out <文件>
只用标准库；不连快期，不需要凭证。
"""

import argparse
import io
import json
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


def fetch_last(sym):
    rq = urllib.request.Request(URL + sym, headers={"Referer": "https://finance.sina.com.cn"})
    raw = urllib.request.urlopen(rq, timeout=30).read().decode("utf-8", "replace")
    i, j = raw.find("("), raw.rfind(")")
    rows = json.loads(raw[i + 1:j])
    if not rows:
        return None, 0
    return rows[-1], len(rows)


def at_today(hhmm):
    h, m = map(int, hhmm.split(":"))
    n = now()
    return n.replace(hour=h, minute=m, second=0, microsecond=0)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbols", default="RB0,RB2610,RB2701")
    ap.add_argument("--start", default="15:00")
    ap.add_argument("--every", type=float, default=5.0, help="分钟")
    ap.add_argument("--min-until", default="17:00")
    ap.add_argument("--hard-stop", default="20:30")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    syms = [s for s in a.symbols.split(",") if s]
    start, min_until, hard = at_today(a.start), at_today(a.min_until), at_today(a.hard_stop)
    out = io.open(a.out, "a", encoding="utf-8")

    def log(line):
        out.write(line + "\n")
        out.flush()
        print(line, flush=True)

    log("# sina_settle_watch 启动 %s · 合约 %s · 起 %s · 每 %.1f 分钟 · 不早于 %s · 硬停 %s"
        % (now().strftime("%Y-%m-%d %H:%M:%S %z"), syms, a.start, a.every, a.min_until, a.hard_stop))
    while now() < start:
        time.sleep(min(60, max(1, (start - now()).total_seconds())))

    prev = {s: None for s in syms}
    same = {s: 0 for s in syms}          # 结算价连续相同的次数（评审判据）
    stopped = {s: False for s in syms}
    reviewer_stop = {s: None for s in syms}
    while True:
        t = now()
        for s in syms:
            if stopped[s]:
                continue
            try:
                row, n = fetch_last(s)
            except Exception as e:  # 取数失败照实记，不当成「没变」
                log("%s %s 取数失败：%r" % (t.strftime("%H:%M:%S"), s, e))
                continue
            vals = tuple(str(row.get(k)) for k in FIELDS) if row else None
            changed = []
            if prev[s] is not None and vals is not None:
                changed = [FIELDS[i] for i in range(len(FIELDS)) if vals[i] != prev[s][i]]
            if prev[s] is not None and vals is not None and vals[7] == prev[s][7]:
                same[s] += 1
            else:
                same[s] = 0
            log("%s %s 行数 %d · %s · 相对上次变了 %s · 结算价连续相同 %d 次"
                % (t.strftime("%H:%M:%S"), s, n, dict(zip(FIELDS, vals)) if vals else None,
                   changed if prev[s] is not None else "（首次）", same[s]))
            if same[s] >= 1 and reviewer_stop[s] is None:
                reviewer_stop[s] = t.strftime("%H:%M:%S")
                log("  ↳ %s 按评审判据（连续两次相同）会停在这一次 %s" % (s, reviewer_stop[s]))
            prev[s] = vals
            # ⛔ 只对【今天那一行】判停：当日行没出现时，最后一行是上一个交易日的，它的结算价早就稳了 ——
            # 不加这一条，今天的行一直不来时会在 --min-until 之后停在昨天那行上，量到的是昨天（冒烟时读到过：盘中 RB0 末行是上一个交易日）。
            if (same[s] >= 1 and vals and vals[0] == t.strftime("%Y-%m-%d")
                    and vals[7] not in ("0", "0.000", "None") and t >= min_until):
                stopped[s] = True
                log("  ↳ %s 停：结算价 %s 连续两次相同、非 0、不早于 %s" % (s, vals[7], a.min_until))
        if all(stopped.values()):
            log("# 全部停 %s" % now().strftime("%H:%M:%S"))
            break
        if now() >= hard:
            log("# 到硬停 %s，未停的：%s" % (now().strftime("%H:%M:%S"), [s for s in syms if not stopped[s]]))
            break
        time.sleep(a.every * 60)
    out.close()


if __name__ == "__main__":
    main()
