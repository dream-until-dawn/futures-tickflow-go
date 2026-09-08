"""验评审方的 A2（围栏）/ A3（四空格缩进）：不含 <!--，但同样让规矩渲染成代码块。

三条纪律，都是今天踩出来的：
  · 变异要断言【真的发生了】——找不到目标就抛，别让「没变异」读成「全绿」；
  · 变异对象要在输出里露出来；
  · 还原写回原始 bytes，不做反向替换。
"""
import io
import subprocess
import sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

TARGET = "tools/probe/README.md"
RUN = ["go", "test", ".", "-count=1", "-run",
       "TestEveryQuotedRuleIsRegistered|TestCarrierCensus|"
       "TestEveryRuleSectionHasAnAnchor|TestCarriersHaveNoHTMLComments"]

raw = open(TARGET, "rb").read()
lines = raw.decode("utf-8").split("\n")

cand = [i for i, l in enumerate(lines)
        if l.startswith("> ") and "**" in l and len(l) > 30]
assert cand, "找不到可变异的目标 —— 变异【没发生】，别把结果当数据"
i = cand[0]
print("变异对象  %s:%d" % (TARGET, i + 1))
print("          %s" % lines[i].strip()[:70])
print()


def trial(name, mutated):
    assert mutated != lines, "%s：变异没有改变文件内容" % name
    open(TARGET, "wb").write("\n".join(mutated).encode("utf-8"))
    body = open(TARGET, "rb").read().decode("utf-8")
    assert body != raw.decode("utf-8"), "%s：写盘之后文件仍与原文相同" % name
    code = subprocess.run(RUN, capture_output=True).returncode
    open(TARGET, "wb").write(raw)                      # 逐字节还原
    assert open(TARGET, "rb").read() == raw, "%s：还原没做干净" % name
    print("%-28s → %s" % (name, "🔴 有守卫响" if code != 0 else "🟢 全绿（守不住）"))


trial("A2 用 ``` 围栏包起来", lines[:i] + ["```"] + [lines[i]] + ["```"] + lines[i + 1:])
trial("A3 行首加四个空格", lines[:i] + ["    " + lines[i]] + lines[i + 1:])
trial("对照：真的删掉这一行", lines[:i] + lines[i + 1:])

code = subprocess.run(RUN, capture_output=True).returncode
print("\n还原后自检：%s" % ("绿" if code == 0 else "❌ 红 —— 还原没做干净"))
