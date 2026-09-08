"""数「关于 `.meta` 的断言」有多少行，并且**分项与合计出自同一次测量**。

## 为什么要有这个脚本

`docs/design.md` 那张普查表的范围从「本节」改成「按主题、全仓」时，
表里写的是：

    2026-09-09 / 1f62620 实测   20 行
      design.md §6.1 之内   11 行
      design.md §6.1 之外    4 行
      contract.md            5 行

**四个数里错了三个。** 在 `1f62620` 上真实的读数是 **14 行 = 9 + 4 + 1**；
`11 / 5 / 20` 是**改了一半的工作区**上的数，却被标上了改动前那个 SHA。

而它一直没被发现，理由值得单写一行：

    ⛔ **11 + 4 + 5 = 20 —— 它加得起来。**
    一组【加得起来】的数看起来像被核过的，
    **而加法核的是这几个数彼此自洽，不是它们和盘上的文件一致。**

成因是分工：**合计来自一条命令，分项来自眼睛。**
⇒ 这个脚本的全部意义就是把这个分工取消掉 ——
**分项与合计必须出自同一次遍历，否则「加得起来」就还是一句安慰。**

## 怎么用

    python tools/audit/meta_census.py            # 数工作区
    python tools/audit/meta_census.py 1f62620    # 数某个 SHA（走 git show）

⚠️ **射程**（照例写明它不比什么）：

    数的     两份文档里【出现 `.meta` 字样】的行
    不数的   没写 `.meta` 但确实在讲这个格式的行（例如只说「元数据文件」）
    不数的   代码与测试里的断言 —— 那一侧由 doccheck 与 §6.1 的收集测试管

⇒ **这个数是一个下界，不是全集。** 它的用途是「改格式之前逐行过一遍」，
不是「证明没有别的地方在讲这件事」。
"""

import io
import re
import subprocess
import sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
# stderr 也要包 —— SystemExit 的话走 stderr，
# 而一条【读不懂的失败信息】和没有失败信息差不多（实测：不包时是一屏乱码）。
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

NEEDLE = ".meta"
DESIGN = "docs/design.md"
CONTRACT = "docs/contract.md"


def read(path, ref=None):
    """ref 为 None 时读工作区，否则读那个 SHA —— 两条路都回到同一个 list[str]。"""
    if ref is None:
        return open(path, encoding="utf-8").read().split("\n")
    out = subprocess.run(["git", "show", "%s:%s" % (ref, path)],
                         capture_output=True)
    if out.returncode != 0:
        raise SystemExit("读不到 %s:%s —— %s" % (ref, path, out.stderr.decode("utf-8", "replace").strip()))
    return out.stdout.decode("utf-8").split("\n")


def section_bounds(lines, pattern):
    """找 §6.1 的行区间 [start, end)。end 取【同级或更高级】的下一个标题。"""
    starts = [i for i, l in enumerate(lines) if re.match(pattern, l)]
    if not starts:
        raise SystemExit("找不到 §6.1 的标题（正则 %s）—— 标题改过就要改这里" % pattern)
    s = starts[0]
    level = len(lines[s]) - len(lines[s].lstrip("#"))
    for i in range(s + 1, len(lines)):
        if re.match(r"^#{1,%d} " % level, lines[i]):
            return s, i
    return s, len(lines)


def census(ref=None):
    design = read(DESIGN, ref)
    contract = read(CONTRACT, ref)
    s, e = section_bounds(design, r"^#{2,4} .*6\.1")

    inside = [i + 1 for i in range(s, e) if NEEDLE in design[i]]
    outside = [i + 1 for i in range(len(design))
               if NEEDLE in design[i] and not (s <= i < e)]
    other = [i + 1 for i in range(len(contract)) if NEEDLE in contract[i]]
    return {"section": (s + 1, e), "inside": inside,
            "outside": outside, "contract": other}


def main():
    ref = sys.argv[1] if len(sys.argv) > 1 else None
    c = census(ref)
    ins, out, con = c["inside"], c["outside"], c["contract"]
    total = len(ins) + len(out) + len(con)

    where = ref if ref else "工作区"
    print("关于 `%s` 的断言 —— %s" % (NEEDLE, where))
    print("  design.md §6.1 = 行 %d..%d" % c["section"])
    print("    §6.1 之内   %3d 行" % len(ins))
    print("    §6.1 之外   %3d 行   %s" % (len(out), out))
    print("  contract.md   %3d 行   %s" % (len(con), con))
    print("  ── 合计       %3d 行" % total)

    # 旧范围（「本节」）盖不到的那部分 —— 这是当初改范围的【理由】，
    # 所以它也必须出自同一次遍历，不能另外手算。
    missed = len(out) + len(con)
    if total:
        print("  旧范围（只看 §6.1）盖不到 %d 行，占 %.1f%%" % (missed, 100.0 * missed / total))

    # 合计必须等于那条【文档里写着的】命令的输出 —— 两条路数出来不一样就是这里错了
    plain = sum(1 for l in read(DESIGN, ref) + read(CONTRACT, ref) if NEEDLE in l)
    assert plain == total, ("分项加起来 %d，而 grep 全文数出 %d —— "
                            "分项与合计出自同一次遍历，它们不该不等" % (total, plain))
    print("  ✅ 分项之和 == 全文行数（%d）—— 两个数出自同一次遍历" % plain)


if __name__ == "__main__":
    main()
