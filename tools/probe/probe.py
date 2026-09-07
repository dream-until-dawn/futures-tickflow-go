#!/usr/bin/env python3
"""重跑 docs/probe.md 里的全部实测结论。

用法：
    python tools/probe/probe.py            # 跑全部
    python tools/probe/probe.py --only sina-frozen

设计意图：**这不是测试，是探针。** 它断言的是【外部世界现在是什么样】，
不是本库的行为。所以：

  - 它会因为上游变化而失败，那正是它存在的理由；
  - 失败不代表本库有 bug，代表 docs/probe.md 该更新了；
  - 因此它【不应该】进 CI 的必过项，否则迟早会被人加 `|| true` 绕过。

依赖：只用标准库。

退出码：0 全部符合记录；1 有偏离；2 网络/解析失败无法判断。
"""

import argparse
import json
import sys
import urllib.request
import urllib.error
from datetime import date

SINA_REF = "https://finance.sina.com.cn"
SINA_K = ("https://stock2.finance.sina.com.cn/futures/api/jsonp.php/"
          "var%20_=/InnerFuturesNewService.")
TQ_SYMBOLS = "https://openmd.shinnytech.com/t/md/symbols/latest.json"

# docs/probe.md 记录的基线。改这里之前先想清楚是上游变了还是记录错了。
BASELINE = {
    "sina_frozen_date": "2024-07-17",
    "sina_minute_cap": 1023,
    "ag0_60m_day_labels": ["09:30", "10:45", "13:45", "14:45", "15:00"],
    "cu0_60m_day_labels": ["10:00", "11:15", "14:15", "15:00"],
    "rb0_first_day": "2009-03-27",
}

results = []


def record(name, ok, detail):
    results.append((name, ok, detail))
    mark = "PASS" if ok else "FAIL"
    print(f"[{mark}] {name}\n       {detail}")


def fetch(url, referer=SINA_REF, timeout=30):
    req = urllib.request.Request(url, headers={
        "User-Agent": "futures-tickflow-go/probe",
        "Referer": referer,
    })
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read()


def sina_json(raw):
    """剥掉 JSONP 外壳与那行防盗链 script。原样返回，不做归一。

    无数据时新浪返回的是 `var _=(null)` —— 是 null，不是空数组。
    **这里刻意不把 null 归一成 []**：docs/probe.md 把「代码写错」与
    「这段确实没数据」渲染成同一个结果列为一种静默失败，
    在解析器里做归一等于亲手制造那个失败。要区分的调用点自己判。
    """
    t = raw.decode("utf-8", "replace")
    return json.loads(t[t.index("=(") + 2: t.rindex(")")])


def bars_or_none(v):
    """把 sina_json 的结果规约成 (根数, 是否为 null)。"""
    if v is None:
        return 0, True
    return len(v), False


def bars(symbol, typ=None):
    if typ is None:
        url = f"{SINA_K}getDailyKLine?symbol={symbol}"
    else:
        url = f"{SINA_K}getFewMinLine?symbol={symbol}&type={typ}"
    v = sina_json(fetch(url))
    return [] if v is None else v


def bars_raw(symbol):
    """不做 None 归一的版本，给需要区分 null 的探针用。"""
    return sina_json(fetch(f"{SINA_K}getDailyKLine?symbol={symbol}"))


def last_full_day(b):
    """挑出最后一个【日盘完整】的自然日。

    不能用「倒数第二个自然日」——夜盘会溢到次日凌晨，于是周六也有 K 线
    （周五夜盘 21:00 → 周六 02:30）。按自然日取，多半会取到一个
    只有夜盘残段、没有日盘的日子。

    判据用 15:00：那是日盘收盘，只有走完整个日盘的自然日才有这一根。
    （这个坑正是本库要解决的问题本身——探针自己也踩了一次。）
    """
    days = sorted({r["d"][:10] for r in b})
    for d in reversed(days):
        times = {r["d"][11:16] for r in b if r["d"].startswith(d)}
        if "15:00" in times:
            return d
    raise RuntimeError("没有找到日盘完整的自然日")


# ---------------------------------------------------------------- 探针

def probe_sina_frozen():
    """新浪期货实时接口是否仍处于冻结状态。

    判据不是「日期字段等于某个值」，而是「日期字段不是最近的交易日」——
    前者在解封后仍会 FAIL 得莫名其妙，后者说的才是我们真正关心的事。
    """
    raw = fetch("https://hq.sinajs.cn/list=RB0")
    txt = raw.decode("gbk", "replace")
    fields = txt.split('"')[1].split(",")
    stamp = fields[17]
    daily = bars("RB0")
    latest = daily[-1]["d"]
    frozen = stamp != latest
    detail = (f"实时接口日期字段={stamp}  日线最新={latest}  "
              f"→ {'仍冻结' if frozen else '已恢复！'}")
    # 记录的状态是「冻结」。恢复了也要提示——那同样是记录该更新了。
    record("sina-frozen", frozen and stamp == BASELINE["sina_frozen_date"], detail)


def probe_sina_minute_cap():
    """分钟线是否仍是每周期硬顶 1023 根。"""
    counts = {t: len(bars("RB0", t)) for t in (5, 15, 30, 60)}
    ok = all(n == BASELINE["sina_minute_cap"] for n in counts.values())
    record("sina-minute-cap", ok, f"各周期根数={counts} 期望全部={BASELINE['sina_minute_cap']}")


def probe_close_labelled():
    """K 线是否按收盘时刻标注（首根应为 09:05 而非 09:00）。"""
    b = bars("RB0", 5)
    d = last_full_day(b)
    times = [r["d"][11:16] for r in b if r["d"].startswith(d)]
    first = times[0] if times else "?"
    ok = first == "09:05"
    record("close-labelled", ok, f"{d} 首根={first} 期望=09:05（收盘时刻标注）")


def probe_session_break_absent():
    """休市段是否不占位（10:15 之后应直接跳到 10:35）。"""
    b = bars("RB0", 5)
    d = last_full_day(b)
    times = [r["d"][11:16] for r in b if r["d"].startswith(d)]
    ok = "10:15" in times and "10:35" in times and not (
        {"10:20", "10:25", "10:30"} & set(times))
    record("session-break-absent", ok,
           f"{d} 10:15→10:35 之间的根: "
           f"{sorted({'10:20','10:25','10:30'} & set(times)) or '无（正确）'}")


def probe_grid_differs_by_night():
    """决定性的一条：AG0 与 CU0 日盘时段相同，60m 网格必须不同。

    这条 FAIL 就说明上游把网格改成了「日盘重新对齐」，
    design.md 第二节的时间模型要重写。
    """
    def day_labels(sym):
        b = bars(sym, 60)
        d = last_full_day(b)
        return [r["d"][11:16] for r in b if r["d"].startswith(d)
                and "09:00" <= r["d"][11:16] <= "15:00"]

    ag, cu = day_labels("AG0"), day_labels("CU0")
    ok = (ag == BASELINE["ag0_60m_day_labels"]
          and cu == BASELINE["cu0_60m_day_labels"] and ag != cu)
    record("grid-differs-by-night", ok,
           f"AG0={ag}\n       CU0={cu}\n       "
           f"两者必须不同（沪银夜盘 330 分钟对 60 余 30，跨隔夜缺口）")


def probe_grid_phase_is_constant():
    """网格相位是【品种的固定属性】，与当天是否真的有夜盘无关。

    长假前交易所停夜盘。对 au/ag/sc（标称夜盘 330 分，对 60 余 30）来说，
    「按当天实际时段累计」会预测停夜盘日的日盘网格退化成 CU0 那样的
    10:00/11:15/14:15/15:00；而实测【仍是】 09:30/10:45/13:45/14:45/15:00。

    这条钉住的是：相位按【标称模板】算，不按当天实际交易的时段算。
    实现里若照着「遍历当天 sessions」写，就会在每个长假前后错一整天。
    """
    def split(sym):
        b = bars(sym, 60)
        by = {}
        for r in b:
            by.setdefault(r["d"][:10], []).append(r["d"][11:16])
        out = {}
        for d, ts in by.items():
            ts = sorted(ts)
            out[d] = {
                "day": [t for t in ts if "09:00" <= t <= "15:00"],
                "early": [t for t in ts if t < "09:00"],
                "full": "15:00" in ts,
            }
        return out

    ag = split("AG0")
    # 普通日：有凌晨根（说明前夜有夜盘）且日盘完整
    normal = next((v["day"] for d, v in sorted(ag.items()) if v["full"] and v["early"]), None)
    # 停夜盘日：日盘完整但【没有】凌晨根
    susp = [(d, v["day"]) for d, v in sorted(ag.items()) if v["full"] and not v["early"]]
    if normal is None or not susp:
        record("grid-phase-constant", False,
               f"窗口内找不到对照日（普通日={normal is not None} 停夜盘日={len(susp)}）")
        return
    bad = [(d, g) for d, g in susp if g != normal]
    record("grid-phase-constant", not bad,
           f"AG0 普通日日盘网格={normal}\n       "
           f"停夜盘日 {len(susp)} 天（{', '.join(d for d, _ in susp)}）"
           f"{'，全部与普通日相同（相位是常量）' if not bad else f'，其中 {len(bad)} 天不同: {bad}'}")


def probe_rb0_unadjusted():
    """主力连续是否仍是未复权拼接。

    判据：找出 RB0 与具体合约的切换点，比较 RB0 的表面涨跌
    与两个合约各自的真实涨跌。差得明显就是未复权。
    """
    rb0 = {r["d"]: r for r in bars("RB0")}
    cands = {}
    for s in ("RB2610", "RB2701", "RB2705"):
        try:
            cands[s] = {r["d"]: r for r in bars(s)}
        except Exception:
            pass
    days = sorted(rb0)[-60:]
    prev, found = None, None
    for d in days:
        who = None
        for s, m in cands.items():
            r = m.get(d)
            if r and r["c"] == rb0[d]["c"] and r["p"] == rb0[d]["p"]:
                who = s
        if prev and who and who != prev:
            i = days.index(d)
            pd_ = days[i - 1]
            surface = float(rb0[d]["c"]) / float(rb0[pd_]["c"]) - 1
            reals = []
            for s in (prev, who):
                a, bb = cands[s].get(pd_), cands[s].get(d)
                if a and bb:
                    reals.append(float(bb["c"]) / float(a["c"]) - 1)
            if reals:
                found = (pd_, d, prev, who, surface, reals)
            break
        prev = who or prev
    if not found:
        record("rb0-unadjusted", False, "最近 60 个交易日里没找到换月点（可能是探测窗口太窄）")
        return
    pd_, d, o, n, surface, reals = found
    gap = surface - max(reals)
    ok = abs(gap) > 0.003          # 虚增超过 0.3pp 就认定未复权
    record("rb0-unadjusted", ok,
           f"{pd_}[{o}] → {d}[{n}]  RB0 表面={surface*100:+.2f}%  "
           f"真实={'/'.join(f'{r*100:+.2f}%' for r in reals)}  "
           f"虚增={gap*100:+.2f}pp")


def probe_czce_four_digit():
    """郑商所 3 位码返回的是 null 还是空数组——这两者必须分得开。

    docs/probe.md 记的是 **null**。若哪天上游改成 `[]`，
    「代码写错」与「确实没数据」就真的合并了，解析层的处置要跟着改。
    """
    n3, is_null3 = bars_or_none(bars_raw("TA701"))
    n4, is_null4 = bars_or_none(bars_raw("TA2701"))
    ok = is_null3 and n3 == 0 and (not is_null4) and n4 > 0
    record("czce-four-digit", ok,
           f"TA701 -> {'null' if is_null3 else f'数组[{n3}]'}（期望 null）  "
           f"TA2701 -> {'null' if is_null4 else f'数组[{n4}]'}（期望 非null且非空）")


def probe_tq_trading_time():
    """天勤 openmd 是否仍免费提供 trading_time。

    12.5 MB 全量太慢，这里只读前若干字节确认结构还在。
    """
    # 这个端点很慢（实测约 1.3 MB / 3 分钟），且服务端会忽略 Range。
    # 所以只读够判断结构的前若干字节就主动断开，不等它传完 12.5 MB。
    req = urllib.request.Request(TQ_SYMBOLS, headers={
        "User-Agent": "futures-tickflow-go/probe", "Range": "bytes=0-262144"})
    with urllib.request.urlopen(req, timeout=180) as r:
        head = r.read(262144).decode("utf-8", "replace")
    ok = '"trading_time"' in head and '"volume_multiple"' in head
    record("tq-trading-time", ok,
           f"前 256KB 内 trading_time={'有' if '\"trading_time\"' in head else '无'}  "
           f"volume_multiple={'有' if '\"volume_multiple\"' in head else '无'}")


def probe_rb0_depth():
    """新浪日线深度是否仍覆盖到 2009 年。"""
    b = bars("RB0")
    ok = b[0]["d"] == BASELINE["rb0_first_day"]
    record("rb0-depth", ok,
           f"{len(b)} 根  {b[0]['d']} → {b[-1]['d']}  期望首根={BASELINE['rb0_first_day']}")


PROBES = {
    "sina-frozen": probe_sina_frozen,
    "sina-minute-cap": probe_sina_minute_cap,
    "close-labelled": probe_close_labelled,
    "session-break-absent": probe_session_break_absent,
    "grid-differs-by-night": probe_grid_differs_by_night,
    "grid-phase-constant": probe_grid_phase_is_constant,
    "rb0-unadjusted": probe_rb0_unadjusted,
    "czce-four-digit": probe_czce_four_digit,
    "tq-trading-time": probe_tq_trading_time,
    "rb0-depth": probe_rb0_depth,
}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", action="append", choices=sorted(PROBES))
    args = ap.parse_args()
    names = args.only or sorted(PROBES)

    print(f"futures-tickflow-go 探针  {date.today()}")
    print(f"基线记录见 docs/probe.md\n")

    errors = 0
    for n in names:
        try:
            PROBES[n]()
        except Exception as e:            # 网络/解析失败与「结论变了」是两回事
            errors += 1
            record(n, False, f"探测失败（网络或解析）: {type(e).__name__}: {e}")
        print()

    bad = [n for n, ok, _ in results if not ok]
    print("-" * 60)
    print(f"{len(results) - len(bad)}/{len(results)} 条与 docs/probe.md 的记录一致")
    if bad:
        print(f"偏离: {', '.join(bad)}")
        print("→ 这不一定是 bug。先判断是上游变了还是记录错了，然后更新 docs/probe.md。")
    return 2 if errors else (1 if bad else 0)


if __name__ == "__main__":
    sys.exit(main())
