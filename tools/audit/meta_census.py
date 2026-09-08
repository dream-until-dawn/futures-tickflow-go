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

    数的     **全仓所有 .md** 里【出现 `.meta` 字样】的行
    不数的   没写 `.meta` 但确实在讲这个格式的行（例如只说「元数据文件」）
    不数的   代码与测试里的断言 —— 那一侧由 doccheck 与 §6.1 的收集测试管

⛔ **这里原来写的是「两份文档」，而那是【范围写成位置】** —— 和它自己要治的那个毛病同形。
2026-09-09 改成按性质划（全仓 `.md`）。**换范围之后一个数都没变**：
在 `1f62620` / `e9c4885` / `c79cb59` / `2bd29be` 四个点上各数了一遍，
`.meta` 都只出现在 `docs/design.md` 与 `docs/contract.md` 里。

    **一次不改变读数的换范围，是最便宜的一次 —— 等到它开始改变读数，说明已经漏过了。**

⇒ **这个数是一个下界，不是全集。** 它的用途是「改格式之前逐行过一遍」，
不是「证明没有别的地方在讲这件事」。
"""

import io
import os
import re
import subprocess
import sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
# stderr 也要包 —— SystemExit 的话走 stderr，
# 而一条【读不懂的失败信息】和没有失败信息差不多（实测：不包时是一屏乱码）。
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

NEEDLE = ".meta"
DESIGN = "docs/design.md"   # §6.1 在这一份里，所以它单独拆「之内 / 之外」
SKIPDIRS = {".git", "__pycache__", "node_modules"}


def read(path, ref=None):
    """ref 为 None 时读工作区，否则读那个 SHA —— 两条路都回到同一个 list[str]。"""
    if ref is None:
        return open(path, encoding="utf-8").read().split("\n")
    out = subprocess.run(["git", "show", "%s:%s" % (ref, path)],
                         capture_output=True)
    if out.returncode != 0:
        raise SystemExit("读不到 %s:%s —— %s" % (ref, path, out.stderr.decode("utf-8", "replace").strip()))
    return out.stdout.decode("utf-8").split("\n")


def markdown_files(ref=None):
    """全仓 .md 的路径。工作区走 os.walk，历史点走 git ls-tree ——
    **两条路都必须给出同一套判据**，否则「按 SHA 数」和「按工作区数」会悄悄不是一回事。
    """
    if ref is None:
        out = []
        for root, dirs, files in os.walk("."):
            dirs[:] = [d for d in dirs if d not in SKIPDIRS]
            for fn in files:
                if fn.endswith(".md"):
                    out.append(os.path.relpath(os.path.join(root, fn), ".").replace("\\", "/"))
        return sorted(out)
    r = subprocess.run(["git", "ls-tree", "-r", "--name-only", ref],
                       capture_output=True)
    if r.returncode != 0:
        raise SystemExit("列不出 %s 的文件 —— %s"
                         % (ref, r.stderr.decode("utf-8", "replace").strip()))
    return sorted(l for l in r.stdout.decode("utf-8").split(chr(10))
                  if l.endswith(".md"))


def section_bounds(lines, pattern):
    """找 §6.1 的行区间 [start, end)，并把【两端那两行原文】一起返回。

    ⛔ 为什么要把终止行也返回（评审方 2026-09-09 的 M2，我复现了）：

        把 §6.1 【内部】一个 `####` 提成 `###`
        ⇒ 之内 14→1、之外 4→17、**合计不变**、承重的「旧范围盖不到 39.1%」→ 95.7%
        ⇒ 而自检**照常打绿**

    成因是结构性的，不是没测到：`inside` 与 `outside` 对**任何** (s, e) 都是
    design.md 命中行的一次不重不漏的划分 ⇒ 它们的和恒等于 design 的命中数
    ⇒ **那条「分项之和 == 全文行数」的断言，不可能因为边界错而红。**

        **一条恒真的断言，和一条不存在的断言，区别只在它让人以为有东西在守。**

    ⇒ 所以边界必须自己变成一个【读数】，并且被断言。
    """
    starts = [i for i, l in enumerate(lines) if re.match(pattern, l)]
    if not starts:
        raise SystemExit("找不到 §6.1 的标题（正则 %s）—— 标题改过就要改这里" % pattern)
    s = starts[0]
    level = len(lines[s]) - len(lines[s].lstrip("#"))
    for i in range(s + 1, len(lines)):
        if re.match(r"^#{1,%d} " % level, lines[i]):
            return s, i, lines[s], lines[i]
    return s, len(lines), lines[s], ""


def census(ref=None):
    design = read(DESIGN, ref)
    s, e, head, tail = section_bounds(design, r"^#{2,4} .*6\.1")

    inside = [i + 1 for i in range(s, e) if NEEDLE in design[i]]
    outside = [i + 1 for i in range(len(design))
               if NEEDLE in design[i] and not (s <= i < e)]
    others = {}
    for path in markdown_files(ref):
        if path == DESIGN:
            continue
        hits = [i + 1 for i, ln in enumerate(read(path, ref)) if NEEDLE in ln]
        if hits:
            others[path] = hits
    return {"section": (s + 1, e), "head": head, "tail": tail,
            "inside": inside, "outside": outside, "others": others}


def main():
    ref = sys.argv[1] if len(sys.argv) > 1 else None
    c = census(ref)
    ins, out, others = c["inside"], c["outside"], c["others"]
    total = len(ins) + len(out) + sum(len(v) for v in others.values())

    where = ref if ref else "工作区"
    print("关于 `%s` 的断言 —— %s" % (NEEDLE, where))
    print("  design.md §6.1 = 行 %d..%d" % c["section"])
    print("      起于  %s" % c["head"].strip()[:64])
    print("      止于  %s" % (c["tail"].strip()[:64] or "（文件末尾）"))
    print("    §6.1 之内   %3d 行" % len(ins))
    print("    §6.1 之外   %3d 行   %s" % (len(out), out))
    for path, hits in sorted(others.items()):
        # ⛔ 打全路径，不打 basename：本仓有【四个】README.md
        # （根 / docs / tools/audit / tools/probe）⇒ basename 会打印出两行一模一样的东西，
        # 而读的人照它打不开正确的文件。**范围按性质写对了，显示又把它塌回一个位置名。**
        print("  %-24s %3d 行   %s" % (path, len(hits), hits))
    if not others:
        print("  其余 .md        0 行")
    print("  ── 合计       %3d 行" % total)

    # 旧范围（「本节」）盖不到的那部分 —— 这是当初改范围的【理由】，
    # 所以它也必须出自同一次遍历，不能另外手算。
    missed = len(out) + sum(len(v) for v in others.values())
    if total:
        print("  旧范围（只看 §6.1）盖不到 %d 行，占 %.1f%%" % (missed, 100.0 * missed / total))

    # ⛔ 边界断言 —— 这一条才是承重的那条。
    #
    # 「旧范围盖不到 X%」是当初改范围的【理由】，而它完全由 §6.1 的两端决定。
    # 把终止标题钉死：§6.1 之内哪天加了一个 `###`，这里当场红，
    # 而红的人必须回来看一眼那个百分比还对不对。
    #
    # ⚠️ 这是一条「必须有人写点东西」的规矩：改了章节结构就得改这两行常量。
    #    它**会**因为合法的重构而红 —— 那正是要它红的地方。
    WANT_HEAD = "### 6.1 "
    WANT_TAIL = "## 七、同步"
    assert c["head"].startswith(WANT_HEAD), (
        "§6.1 的标题变了：%r（期望以 %r 开头）—— 改了就把这里的常量一起改"
        % (c["head"][:60], WANT_HEAD))
    assert c["tail"].strip() == WANT_TAIL, (
        "§6.1 的【终止标题】是 %r，而期望 %r。\n"
        "  ⇒ 多半是 §6.1 之内新加了一个 `###`，于是这一节被截短了。\n"
        "  ⇒ 而那会让「旧范围盖不到 X%%」这个【承重的数】悄悄改掉，合计却纹丝不动。\n"
        "  ⇒ 确认新结构没问题之后，把 WANT_TAIL 改成新的终止标题。"
        % (c["tail"].strip()[:60], WANT_TAIL))

    # 分项之和 == 全文命中数。
    # ⚠️ 措辞订正：原来写「出自同一次遍历」，而这里其实遍历了**两次**
    # （census 一次、这里再一次）。真是同一次遍历的话，这条断言就是纯废话。
    # 它的实际作用是：**两条各自独立的路数出同一个数。**
    allmd = []
    for path in markdown_files(ref):
        allmd += read(path, ref)
    plain = sum(1 for l in allmd if NEEDLE in l)
    assert plain == total, ("分项加起来 %d，而全文数出 %d —— "
                            "两条路数出来不一样，就是这里错了" % (total, plain))
    print("  ✅ 分项之和 == 全文行数（%d）—— 两条【各自独立】的路数出同一个数" % plain)
    print("  ✅ §6.1 的两端与记录一致（起 %r / 止 %r）" % (WANT_HEAD, WANT_TAIL))


if __name__ == "__main__":
    main()
