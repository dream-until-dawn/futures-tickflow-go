"""验 R4：把一条【已登记】规矩整行挪到另一个 ## 底下（同文件、字节总量不变）。

三条纪律照旧：变异必须断言真的发生、变异对象打进输出、还原写回原始字节。
外加一个对照组：真删那一行必须红——否则「全绿」可能只是仪器坏了。
"""
import io
import re
import subprocess
import sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

TARGET = "docs/README.md"
RUN = ["go", "test", ".", "-count=1", "-run",
       "TestEveryQuotedRuleIsRegistered|TestCarrierCensus|"
       "TestEveryRuleSectionHasAnAnchor|TestCarriersHaveNoHTMLComments|"
       "TestLandingCarriersStillCarry"]

raw = open(TARGET, "rb").read()
lines = raw.decode("utf-8").split("\n")

heads = [i for i, l in enumerate(lines) if re.match(r"^#{2,3} ", l)]
assert len(heads) >= 3, "小节不够，换个文件"

# 找一条已登记规矩：`> ` 引用块里的加粗行
cand = [i for i, l in enumerate(lines)
        if l.startswith("> ") and "**" in l and len(l) > 30]
assert cand, "找不到可挪动的规矩 —— 变异【没发生】，别把结果当数据"
src_i = cand[0]

# 目标：一个【不同】小节的开头之后
src_head = max(h for h in heads if h < src_i)
dst_head = next(h for h in heads if h > src_i)
print("挪动对象  %s:%d" % (TARGET, src_i + 1))
print("          %s" % lines[src_i].strip()[:70])
print("从        %s" % lines[src_head].strip()[:60])
print("到        %s 之下" % lines[dst_head].strip()[:60])
print()


def trial(name, mutated):
    assert mutated != lines, "%s：变异没有改变文件内容" % name
    open(TARGET, "wb").write("\n".join(mutated).encode("utf-8"))
    assert open(TARGET, "rb").read() != raw, "%s：写盘之后文件仍与原文相同" % name
    code = subprocess.run(RUN, capture_output=True).returncode
    open(TARGET, "wb").write(raw)
    assert open(TARGET, "rb").read() == raw, "%s：还原没做干净" % name
    print("%-34s → %s" % (name, "🔴 有守卫响" if code != 0 else "🟢 全绿（守不住）"))


moved = [l for k, l in enumerate(lines) if k != src_i]
ins = dst_head - 1 if dst_head > src_i else dst_head
moved = moved[:ins + 1] + [lines[src_i]] + moved[ins + 1:]
trial("R4 整行挪到另一个小节底下", moved)
trial("对照：真的删掉这一行", [l for k, l in enumerate(lines) if k != src_i])

code = subprocess.run(RUN, capture_output=True).returncode
print("\n还原后自检：%s" % ("绿" if code == 0 else "❌ 红 —— 还原没做干净"))
