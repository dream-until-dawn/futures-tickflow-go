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

## ⛔ 2026-09-09：**这条判据自己比错了对象，那个红是工具的，不是问题的**

上一版的判据是 `差值 == 窗口边缘不完整条目数`。而这两个量**数的不是同一个总体**：

	差值   `A - B`，**只数 GFEX**
	边界   `edge`，数【任何交易所】的不完整条目

实测当场就看得见：50% 那一段唯一的边界条目是 `CZCE.PX411P8000`
——**一条郑商所的期权**。它被切断，`edge` 记 1；而它不是 GFEX，`A` `B` 都不数它，
于是差值是 0。**0 ≠ 1，判据「不成立」——而这什么也没说明。**

	**一条把两个不同总体放在等号两边的判据，会给出一个【看起来像结论】的红。**

⇒ 判据改成同一个总体：**差值 == 边缘不完整条目里【可见片段含 GFEX 标记】的条数。**

## ⛔ 而更根本的一层：**「差 1」和「差 4」是两件事，被写进了同一句解释里**

	差 1（75%）  同一段字节、同一时刻，口径 A = 86 / 口径 B = 85   ⇒ 是【口径】
	差 4（50%）  同一个绝对偏移、隔了时间，口径 B 38 → 42          ⇒ 是【内容漂移】

**一条检查同时背两个问题，它红的时候就说不清红的是哪一个。**
这一版把它们拆开：本脚本只回答第一个，第二个由 `gfex_offset_recheck.py` 那条路管。

2026-09-09 实测，两个偏移上新判据**都成立**，而 75% 那一格给出了直接证据：

	被切断的那条边界条目是 `GFEX.lc2606-P-108000` —— 它本身就是一条 GFEX 合约
	⇒ 口径 A 数得到它的标记、口径 B 丢掉它 ⇒ 差正好是 1

⇒ **「85 vs 86 是口径造成的」这句话，从「听起来对」变成了【验过】。**
（`probe.md` 原来记的是「它从未被验证过」，那句现在要改。）

⚠️ **而 50% 那个「42 vs 38」仍然不是口径**，这一点没变，也不该变：
今天 A 与 B 在那一段上给出同一个数（42 = 42），**口径在那儿没有差**。

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
    txt = raw.decode("utf-8", "replace")
    # ⛔ 把【头部残片】切掉 —— 这是上一版那个病的镜像，评审方 2026-09-09 指出，我复现了。
    #
    # 窗口开头必然落在某条条目【中间】：它的 `"name": {` 在窗口之前
    #	⇒ 口径 B 的正则匹配不到它 ⇒ 既不进 n、也不进 edge、更不进 edge_gfex
    #	⇒ 而口径 A 数的是【整个窗口】里的标记，**包括头部残片里的**
    #	⇒ 残片里若含一个 GFEX 标记：差多 1，而 edge_gfex 不变 ⇒ 判据不成立
    #
    # **这和「edge 数了郑商所」是同一个病：等号两边不是同一段字节。**
    # 上一版从「交易所」那一维修好了，**「窗口起点」这一维原样留着。**
    #
    # 实测（2026-09-09）：50% 的残片 1131 字节、75% 的 759 字节，**两个都恰好没有 GFEX 标记**
    # ⇒ 今天两格绿是**运气**，不是判据管住了。而 50% 那一格差一点：
    # 切口正落在一条条目的 `exchange_id` 值中间（`hange_id": "SHFE"`），只是那条不是 GFEX。
    #
    # 切掉之后今天**一个数都不改**（count_a 仍是 42 / 86）——
    #	**一次不改变读数的换范围，是最便宜的一次；等到它开始改变读数，说明已经漏过了。**
    m = re.search(r'"[A-Za-z0-9_.@\-]+":\s*\{', txt)
    if m:
        txt = txt[m.start():]
    return txt


def count_a(txt):
    """口径 A：直接数标记。"""
    return len(re.findall(r'"exchange_id"\s*:\s*"GFEX"', txt))


def count_b(txt):
    """口径 B：大括号配对抠完整条目，边缘不完整的丢掉。

    返回 `(完整GFEX数, 未闭合匹配数, 其中含GFEX标记的数, 边界样本)`。

    ⚠️ **第 2 个数的名字要写准：它数的是【未闭合的 `"name": {` 匹配数】，不是【条目数】。**
    `latest.json` 的条目是**嵌套**的 —— 实测（2026-09-09）：50% 窗口匹配 338 个，
    其中 **168 个被别的匹配包住**（是条目内部的子对象）；75% 窗口 336 / 167。
    今天两个偏移的未闭合数都恰好是 1，所以这件事看不出来。

    ⇒ 这个脚本顶上写着上一版的病是「自述比实现【窄】」，
    **而这一处是反过来的一格：自述比实现【宽】。**
    名字说「条目」，实现数的是「匹配」。

    	**两个方向都会骗人，而宽的那一种更不容易被发现 ——
    	因为它读起来更像你想要的东西。**

    ⚠️ 第 3 个才是判据要用的那个。第 2 个留着**是为了让两个数并排出现** ——
    上一版就是拿第 2 个当判据的，而它数的是任何交易所。
    并排印出来，下一个人一眼看得出它们不是一回事。
    """
    n, edge, edge_gfex, samples = 0, 0, 0, []
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
            hit = bool(re.search(r'"exchange_id"\s*:\s*"GFEX"', txt[i:]))
            if hit:
                edge_gfex += 1
            samples.append((m.group(1), hit))
            continue
        try:
            v = json.loads(txt[i:j + 1])
        except Exception:
            continue
        if isinstance(v, dict) and v.get("exchange_id") == "GFEX":
            n += 1
    return n, edge, edge_gfex, samples


failed = []
drift = []
for lo, pct, old, note in OFFSETS:
    txt = fetch(lo)
    a = count_a(txt)
    b, edge, edge_gfex, samples = count_b(txt)
    diff = a - b
    ok = diff == edge_gfex
    if not ok:
        failed.append((pct, diff, edge_gfex, edge))
    if b != old:
        drift.append((pct, old, b))
    print("%s 偏移 %d（%d 字节）—— %s" % (pct, lo, W, note))
    print("  口径 A（数标记）        GFEX = %d" % a)
    print("  口径 B（配对抠条目）    GFEX = %d" % b)
    print("  差 = %d" % diff)
    print("  边缘未闭合匹配：%d 个（**是匹配数不是条目数**，见 count_b 的说明），"
          "其中含 GFEX 标记 = %d" % (edge, edge_gfex))
    for name, hit in samples:
        print("      %-26s 含 GFEX 标记 = %s" % (name, hit))
    print("  ⇒ 判据（差值 == 边缘里含 GFEX 的条数）%s"
          % ("成立" if ok else "【不成立】"))
    print("  原记录（口径 B）报的是  GFEX = %d%s"
          % (old, "" if b == old else "  ⚠️ 与今天不同 ⇒ 那是【内容漂移】，不是口径"))
    print()

if failed:
    print("❌ 判据不成立 —— 「同一窗口内 A 与 B 的差来自被切断的 GFEX 条目」这句话不对：")
    for pct, diff, eg, ea in failed:
        print("     %s：差 = %d，而边缘里含 GFEX 的 = %d（边缘总数 %d）"
              % (pct, diff, eg, ea))
    print("   ⇒ 这个红是【那件事没解决】，不是工具坏了。")
    print("   处置与它为什么不进送审自检：见 tools/audit/README.md")
    sys.exit(1)

print("✅ 两个偏移上，差值都正好等于【边缘里含 GFEX 标记】的条数")
print("   ⇒ 「同一窗口内 A 与 B 的差 = 被窗口切断的 GFEX 条目」，这句话成立。")
if drift:
    print()
    print("⚠️ 而下面这些格的【口径 B 本身】就和原记录不同 —— 那是另一件事：")
    for pct, old, now in drift:
        print("     %s：原记录 %d，今天 %d（差 %+d）" % (pct, old, now, now - old))
    print("   同一个绝对偏移、同一种口径、不同的日子 ⇒ 是【内容漂移】。")
    print("   **一个固定字节偏移，落在一个会长大的文件上，不是一个固定的窗口。**")
    print("   这一层由 gfex_offset_recheck.py 那条路管，不在本脚本的射程里。")
