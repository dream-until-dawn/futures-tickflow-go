"""同一段字节取一次，两种口径各数一遍。**判据：差值 == 窗口边缘不完整条目数。**

来历：`probe.md` 一度把「GFEX 85→86 的差 1」解释成「计数口径不同」——
我数 `"exchange_id":"GFEX"` 标记，`sample.py` 靠大括号配对抠完整条目、丢掉边缘那个。
听起来很合理。而 50% 那处的差是 **4**，比边界效应能解释的多，
**那个解释从来没有被验证过**。

⚠️ **这是【检查类】，不是测量类。** 上一版它把判据写在这份 docstring 里、
把判据要的两个量（差值、边界条目数）**都印出来了，却从不比较它们**——缺的是一行 `if`。
补上之后它才是一条检查。

	这是「自述比实现【窄】」的一个实例：docstring 承诺了一个判定，实现少做一步，
	**而输出看起来像一份完整的报告。**

⚠️ **今天它是【红的】，而那是对的**：50% 那一段实测 差 0 / 边界 1 ⇒ 判据不成立，
即「口径不同」解释不了那个差。**一个真实的、未解决的问题应当红着。**
红在哪一格、以及为什么**不**把它接进送审自检，写在 `tools/audit/README.md`。

⚠️ 它**不**回答的：差值到底是什么造成的（那还没有答案），以及
那三个偏移上的内容将来变没变——**那是另一件事，需要把观测值钉住，不在这个脚本里。**

跑法：python tools/audit/gfex_caliber_check.py
"""
import io
import json
import re
import sys
import urllib.request

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

U = "https://openmd.shinnytech.com/t/md/symbols/latest.json"
W = 256 * 1024

# 两个偏移都用【旧 total 的百分比】算出来的绝对字节 —— 和 probe.md 那张表同源。
# 上一版只测 50%，而被解释的那个差 1 在 75% —— 于是判据从来没跑在它要解释的那一格上。
OFFSETS = [
    (175088954, "50%", 38, "评审当时（口径 B）报的那一段"),
    (262633431, "75%", 85, "原记录「85 条」的那一段 —— 「差 1 是口径」说的就是它"),
]


def fetch(lo):
    r = urllib.request.Request(U, headers={"Range": "bytes=%d-%d" % (lo, lo + W - 1)})
    with urllib.request.urlopen(r, timeout=300) as f:
        raw = f.read()
    assert len(raw) == W, "取数失败：偏移 %d 只拿到 %d 字节" % (lo, len(raw))
    return raw.decode("utf-8", "replace")


def count_a(txt):
    """口径 A：直接数标记。"""
    return len(re.findall(r'"exchange_id"\s*:\s*"GFEX"', txt))


def count_b(txt):
    """口径 B：大括号配对抠完整条目，边缘不完整的丢掉。返回 (计数, 边界条目数)。"""
    n, edge = 0, 0
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
            edge += 1
            continue
        try:
            v = json.loads(txt[i:j + 1])
        except Exception:
            continue
        if isinstance(v, dict) and v.get("exchange_id") == "GFEX":
            n += 1
    return n, edge


failed = []
for lo, pct, old, note in OFFSETS:
    txt = fetch(lo)
    a = count_a(txt)
    b, edge = count_b(txt)
    diff = a - b
    ok = diff == edge
    if not ok:
        failed.append((pct, diff, edge))
    print("%s 偏移 %d（%d 字节）—— %s" % (pct, lo, W, note))
    print("  口径 A（数标记）        GFEX = %d" % a)
    print("  口径 B（配对抠条目）    GFEX = %d" % b)
    print("  差 = %d ；窗口边缘不完整条目 = %d  ⇒ 判据 %s"
          % (diff, edge, "成立" if ok else "【不成立】"))
    print("  原记录（口径 B）报的是  GFEX = %d" % old)
    print()

if failed:
    print("❌ 「差 1 / 差 4 是【计数口径】造成的」这个解释 —— **判据不成立**：")
    for pct, diff, edge in failed:
        print("     %s：差 = %d，而窗口边缘不完整条目 = %d —— 两者不等" % (pct, diff, edge))
    print("   ⇒ 那两个差值【仍然未解释】。这个红不是工具坏了，是那件事没解决。")
    print("   ⇒ 别用「口径不同」去解释它 —— 那正是被这条检查否掉的那句。")
    print("   处置与它为什么不进送审自检：见 tools/audit/README.md")
    sys.exit(1)

print("✅ 两个偏移上，差值都正好等于边界条目数 ⇒「口径不同」这个解释成立。")
