"""同一个【绝对字节区间】重取一次，判「内容搬了」还是「窗口挪了」。

原表那一行(75% -> GFEX 85)是 sample.py 用 TOTAL=350177909 算的绝对偏移读出来的。
今天 total 变成 350338103，按 75% 算出的偏移比它大 12 万字节 —— 256KB 窗口只重叠一半多。
所以先别问「内容变没变」，先把【同一段字节】重取一遍。

对照组焊在里面：每个窗口必须 ①正好 262144 字节 ②exchange_id 总数 > 0。
任何一条不成立就报「取数失败」，不报计数 —— 否则一次失败的取数会伪装成「这里没有 GFEX」。
"""
import io, sys, re, time, urllib.request
from collections import Counter
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

U = "https://openmd.shinnytech.com/t/md/symbols/latest.json"
W = 256 * 1024
OLD_TOTAL = 350177909          # probe.md 记的，也是 sample.py 硬编码的那个

def rng(a, b, t=300):
    r = urllib.request.Request(U, headers={"Range": "bytes=%d-%d" % (a, b)})
    with urllib.request.urlopen(r, timeout=t) as f:
        return f.read(), f.headers.get("Content-Range")

_, cr = rng(0, 100)
total = int(cr.split("/")[1])
print("今天 total = %d   （旧 %d，差 %+d）" % (total, OLD_TOTAL, total - OLD_TOTAL))

old75 = min(int(OLD_TOTAL * 75 / 100), OLD_TOTAL - W)   # sample.py 当年真正读的那一段
new75 = min(int(total     * 75 / 100), total     - W)   # 今天按 75% 算出来的
print("旧 75%% 绝对偏移 = %d ；今天 75%% = %d ；相差 %d 字节（窗口 %d，重叠 %.0f%%）"
      % (old75, new75, new75 - old75, W, 100 * (W - (new75 - old75)) / W))
print()

failed = 0
for label, lo in (("旧表那一段(绝对偏移)", old75), ("今天的 75%", new75)):
    t0 = time.time()
    raw, _ = rng(lo, lo + W - 1)
    txt = raw.decode("utf-8", "replace")
    allx = re.findall(r'"exchange_id"\s*:\s*"([A-Z]+)"', txt)
    # —— 对照组：两条都成立才允许报计数 ——
    if len(raw) != W or not allx:
        print("%-22s ❌ 取数失败：%d 字节 / exchange_id %d 个 —— 不报计数"
              % (label, len(raw), len(allx)))
        failed += 1          # 而且要传播出去：只 print 不影响退出码，等于没检查
        continue
    g = [m.start() for m in re.finditer(r'"exchange_id"\s*:\s*"GFEX"', txt)]
    cls = Counter()
    for pos in g:
        ms = re.findall(r'"class"\s*:\s*"([A-Z_]+)"', txt[max(0, pos-2500):pos+2500])
        cls[ms[0] if ms else "(未取到)"] += 1
    print("%-22s 偏移 %d  %d 字节  %.0fs  ｜ exchange_id 合计 %d，分布 %s"
          % (label, lo, len(raw), time.time()-t0, len(allx), dict(Counter(allx).most_common())))
    print("%-22s ⇒ GFEX %d 次  class %s" % ("", len(g), dict(cls)))
    print()

if failed:
    print("❌ 有 %d 个窗口取数失败 —— 这次的计数不构成证据" % failed)
    sys.exit(1)
