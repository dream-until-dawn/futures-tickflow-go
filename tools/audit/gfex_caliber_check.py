"""50% 那处读回 42，评审当时报 38。差 4。
75% 那处差 1，我解释成「口径不同」（我数到窗口边缘的不完整条目，sample.py 丢掉它们）。
但 4 比边界效应能解释的多，而我【没有验证过】那个解释。

所以：取【同一段字节】一次，两种口径各数一遍。差值若正好是边界条目数，解释成立。
"""
import io, sys, re, json, urllib.request
from collections import Counter
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

U = "https://openmd.shinnytech.com/t/md/symbols/latest.json"
W = 256 * 1024
LO = 175088954          # 旧 total 的 50%，评审当时报 38 条 GFEX 的那一段

r = urllib.request.Request(U, headers={"Range": "bytes=%d-%d" % (LO, LO + W - 1)})
with urllib.request.urlopen(r, timeout=300) as f:
    raw = f.read()
txt = raw.decode("utf-8", "replace")
assert len(raw) == W, "取数失败：%d 字节" % len(raw)

# 口径 A：我的 —— 直接数标记
a = len(re.findall(r'"exchange_id"\s*:\s*"GFEX"', txt))

# 口径 B：sample.py 的 —— 大括号配对抠完整条目，边缘不完整的丢掉
b, edge = 0, 0
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
        b += 1

print("同一段字节（偏移 %d，%d 字节）" % (LO, len(raw)))
print("  口径 A（数标记，我今天用的）      GFEX = %d" % a)
print("  口径 B（配对抠条目，sample.py）   GFEX = %d" % b)
print("  差 = %d ；窗口边缘不完整条目共 %d 个" % (a - b, edge))
print("  评审当时（口径 B，昨天）报的是    GFEX = 38")
