"""contract.md 那条未验：「时段模式一共几种」——现记的 9 种是【5% 前缀的下界】，
而且**已知未覆盖 GFEX**。

而 GFEX 的条目在哪，`gfex_caliber_check.py` 已经找到了：75% 那个绝对偏移的窗口里有 85 条。
⇒ 那就去把它们的 `trading_time` 抠出来，看看 GFEX 会不会多出第 10 种模式。

⚠️ **射程，两条，都要写在前面：**

    一、只看三个 256 KB 的窗口，不是全量 334 MB ⇒ **形态数是下界，不是全集**。
    二、⛔ 2026-09-09 实测：抠到的 133 条 GFEX **全是 `FUTURE_OPTION`，一条期货都没有**。
       —— 和 `probe.md` 早就记过的「CFFEX 714 条里 686 条是期权」是同一件事。
       ⇒ 本脚本回答的是「GFEX **期权**的时段形态」，**不是「GFEX 的」**。

而 GFEX **期货**那一侧由另一条路管：`tools/probe/shinny` 的 `shinny-gfex-no-night`
直接从 K 线量 `KQ.m@GFEX.si` / `.lc` / `.ps`，结论是**没有夜盘**。
两条路各自独立，而它们对「GFEX 无夜盘」这件事**给出同一个答案** ——
**这比任何一条单独说的都硬，但它仍然不是「GFEX 期货的元数据被看过了」。**
"""
import io
import json
import re
import sys
import urllib.request

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

U = "https://openmd.shinnytech.com/t/md/symbols/latest.json"
W = 256 * 1024
OFFSETS = [(175088954, "50%"), (262633431, "75%"), (339672571, "97%")]


def fetch(lo):
    r = urllib.request.Request(U, headers={"Range": "bytes=%d-%d" % (lo, lo + W - 1)})
    with urllib.request.urlopen(r, timeout=300) as f:
        raw = f.read()
    return raw.decode("utf-8", "replace")


def entries(txt):
    """大括号配对抠出完整条目，边缘不完整的丢掉。"""
    for m in re.finditer(r'"([A-Za-z0-9_.@\-]+)":\s*\{', txt):
        i = m.end() - 1
        depth, j = 0, i
        while j < len(txt):
            if txt[j] == "{":
                depth += 1
            elif txt[j] == "}":
                depth -= 1
                if depth == 0:
                    break
            j += 1
        if depth != 0:
            continue
        try:
            v = json.loads(txt[i:j + 1])
        except Exception:
            continue
        if isinstance(v, dict):
            yield m.group(1), v


def norm(tt):
    """把 trading_time 归一成一个可比的形态串。"""
    if not isinstance(tt, dict):
        return None
    parts = []
    for k in ("day", "night"):
        segs = tt.get(k) or []
        if not isinstance(segs, list):
            continue
        s = ";".join("%s-%s" % (a, b) for a, b in
                     (seg for seg in segs if isinstance(seg, list) and len(seg) == 2))
        parts.append("%s[%s]" % (k, s))
    return " ".join(parts)


byex = {}          # 交易所 -> {形态: 条数}
gfex_examples = {}
total = 0
for lo, pct in OFFSETS:
    txt = fetch(lo)
    for name, v in entries(txt):
        ex = v.get("exchange_id")
        tt = norm(v.get("trading_time"))
        if not ex or not tt:
            continue
        total += 1
        byex.setdefault(ex, {}).setdefault(tt, 0)
        byex[ex][tt] += 1
        if ex == "GFEX" and tt not in gfex_examples:
            gfex_examples[tt] = name

print("三个窗口共抠出带 trading_time 的完整条目 %d 条" % total)
print()
allpat = set()
for ex in sorted(byex):
    pats = byex[ex]
    allpat |= set(pats)
    print("%-6s %2d 种形态，%4d 条" % (ex, len(pats), sum(pats.values())))
print()
print("=== 合计不同形态 %d 种 ===" % len(allpat))
print()
print("=== GFEX 的形态（%d 种）===" % len(byex.get("GFEX", {})))
for tt, n in sorted(byex.get("GFEX", {}).items(), key=lambda kv: -kv[1]):
    print("   %4d 条  %s" % (n, tt))
    print("            例：%s" % gfex_examples[tt])
print()
gfex_only = set(byex.get("GFEX", {})) - set().union(
    *[set(v) for k, v in byex.items() if k != "GFEX"]) if len(byex) > 1 else set()
print("=== GFEX 独有、别的交易所没有的形态：%d 种 ===" % len(gfex_only))
for tt in sorted(gfex_only):
    print("   %s" % tt)
