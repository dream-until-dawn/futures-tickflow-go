#!/usr/bin/env python3
"""合并门禁：**一颗合并提交的自带 diff，只许落在一张明示的白名单里。**

## 为什么有这道门禁

合并提交是**唯一可以塞进「两个父都没有的内容」的地方** ——
所以本仓原来的规矩是「自带 diff 必须 **0 行**」。

⛔ 而 2026-09-11 那次合并把它顶翻了：`tools/audit/high_water.txt` 自己的体例写着
**「合并时必须写一条合流记录」**，而合流记录按定义就是两个父都没有的内容。
⇒ 两条规矩指向同一颗提交，一条要求 0 行，另一条要求非 0。

⚠️ 我当时提的处置是「0 行 **或** 逐行摊在送审信里」。**它被否掉了，理由我认**：
🔴 **「摊开」不是闸门，是礼节** —— 它核不了，且强度随读信人的注意力变化。
📎 而更锋利的那半句（送审方自省）：**一道闸门的强度不在【通过它要付多少】，
在【绕过它要付多少】** —— 摊开的绕过成本是**零**：少贴几行没人知道。
⇒ **问「绕过它要付多少」时，答案若是「没人会知道」，那它就不是闸门。**

⇒ 所以换成一条**可机器核**的：**自带 diff 涉及的【文件集合】⊆ 白名单。**
📎 一般化：**给一条规矩开例外时，例外的条件必须比规矩本身更容易核** —— 否则例外会吃掉规矩。

## 白名单为什么只有一项

实测 main 上**全部 41 颗合并**（两边各跑一次，逐颗对上）：

    自带 diff 0 行                          35 颗
    只有 tools/audit/high_water.txt         4 颗   ← 账本，合规
    越界                                    2 颗   ← f9278df · 1f62620，**都含 docs_test.go**

🔴 ⇒ **这张白名单不是新规矩，是把一条已经实行过 4 次的惯例写下来。**
⇒ 而 `docs_test.go` **不进**白名单，理由不是「它不常冲突」，是它**已有更强的处置**：
`CONTRIBUTING.md` 逐字写着「`docs_test.go` | **生成物** | **别手合**：删掉重跑生成器」。
🔴 **一个文件若已有「别手合」的处置，它就不该有合并例外** —— 两条规矩指向同一个文件时，
**强的那条吃掉弱的那条。**

## 判据（就是下面这条命令，写死连开关）

    git show --format='' --cc <merge> | grep '^diff --cc' | sed 's|^diff --cc ||'

⚠️ **开关不能换**，两个实测过的陷阱：

    --name-only   在合并上会把「与【某一个】父不同」的文件也列出来 ⇒ 集合更大
    grep '^++'    会把 `+++ b/…` 这一行**文件头**当成内容行 ⇒ 计数偏大

📎 ⇒ **一个读数的射程，取决于那条命令的【全部字符】，不取决于它的名字。**

## 射程（写明它不比什么）

- 它断言的是**文件集合 ⊆ 白名单**，**不是**「非 0 的恰好 N 颗」——
  后者会在下一次合法的账本合并时红，**而那是误报，误报的守卫最终被关掉**。
- 历史豁免名单**只增不改，且只能收历史提交**；新合并不许进。
- 它**不看**那些行写了什么 —— 内容对不对由账本自己那三条守卫（链 / 来历 / 合流位置）管。
"""
import io
import subprocess
import sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

# 允许出现在合并自带 diff 里的文件。**只增要有理由，而理由是「这个文件的体例
# 明文要求合并时写一条记录」** —— 不是「它老是冲突」。
WHITELIST = {"tools/audit/high_water.txt"}

# 历史豁免：**只增不改，且只收历史提交。** 每一颗都写清越界在哪、那个文件今天的处置是什么。
HISTORIC = {
    "f9278df": "越界 docs_test.go —— 它是生成物，今天的处置是"
               "「别手合：删掉重跑 rebuild_docs_test.py」（CONTRIBUTING.md 那张表）",
    "1f62620": "越界 docs_test.go 与 tools/audit/README.md —— 前者同上；"
               "后者是手写散文，正常合即可，不该出现在自带 diff 里",
}


def git(*args):
    """跑 git，非 0 就抛。

    ⛔ **不给它加 check=False** —— 本仓那条：一道闸门只要有开关，最终就会被打开。
    要用非 0 表达正常结论的地方，另写一个函数。
    """
    p = subprocess.run(["git"] + list(args), capture_output=True)
    if p.returncode != 0:
        raise SystemExit("refuse: git %s ⇒ exit=%d\n%s"
                         % (" ".join(args), p.returncode,
                            p.stderr.decode("utf-8", "replace")))
    return p.stdout.decode("utf-8", "replace")


def parentCount(sha):
    """这一颗有几个父。"""
    return len(git("rev-list", "--parents", "-n1", sha).split()) - 1


def ownDiffFiles(sha):
    """这一颗合并的自带 diff 涉及哪些文件（`--cc` 组合 diff，即「与所有父都不同」）。

    ⛔ **必须先断言父数** —— 本仓那条逐字适用（2026-09-11 我在这支脚本上实测到）：
    喂它一颗**非合并**提交，`git show --cc` 印的是普通 diff（`diff --git` 而不是 `diff --cc`）
    ⇒ 这个函数返回**空集** ⇒ 上层判它「自带 diff 为空」⇒ **静默合规**。

        非合并提交 9e8702f（父数 1）⇒ 「✅ 扫了 1 颗合并：自带 diff 为空 1」exit=0

    🔴 **「量不了」和「量出来是 0」在这里长得一模一样** ——
    而那正是本仓那条：**先断言父数，再读那个量；父数不对时它没有真值。**
    """
    n = parentCount(sha)
    if n < 2:
        raise SystemExit("refuse: %s 有 %d 个父 —— 它不是合并提交。\n"
                         "  ⇒ `git show --cc` 对它印的是普通 diff，本判据在它身上【没有真值】，\n"
                         "     而返回的空集会被读成「自带 diff 为空」＝合规。拒绝出这个读数。"
                         % (sha, n))
    out = git("show", "--format=", "--cc", sha)
    return [ln[len("diff --cc "):].strip()
            for ln in out.split("\n") if ln.startswith("diff --cc ")]


def main():
    argv = sys.argv[1:]
    # `--rev=<ref>` 换一个起点（默认 main）。
    # ⛔ 它存在的理由是**对照组**，不是方便：「一颗合并都没扫到」那一格
    # 原来造不出输入（要伪造浅克隆），而指向根提交就够了 ——
    # 评审方 2026-09-11 想到的，比我原来打算造浅克隆便宜得多。
    rev = "main"
    rest = []
    for a in argv:
        if a.startswith("--rev="):
            rev = a[len("--rev="):]
        else:
            rest.append(a)
    if rest:
        shas = rest
    else:
        shas = git("log", "--merges", "--format=%h", rev).split()

    # ⛔ 前提自检：空输入上不许出结论。空转与「全都合规」同形。
    if not shas:
        print("⛔ 一颗合并都没扫到 —— 这道门禁是空转的，读数作废\n"
              "  ⇒ 浅克隆（--depth）里没有合并历史；那不是「合规」，是【量不了】。",
              file=sys.stderr)
        return 2

    bad = []
    ok, ledger, exempt = 0, 0, 0
    for sha in shas:
        files = ownDiffFiles(sha)
        if not files:
            ok += 1
            continue
        short = sha[:7]
        outside = [f for f in files if f not in WHITELIST]
        if not outside:
            ledger += 1
            continue
        if short in HISTORIC:
            exempt += 1
            print("⚠️ 历史豁免 %s：%s" % (short, HISTORIC[short]))
            continue
        bad.append((short, outside))

    if bad:
        print("⛔ %d 颗合并的自带 diff 越出白名单：" % len(bad), file=sys.stderr)
        for short, outside in bad:
            print("   · %s ⇒ %s" % (short, " ".join(outside)), file=sys.stderr)
        print("  白名单：%s" % " ".join(sorted(WHITELIST)), file=sys.stderr)
        print("  ⇒ 合并提交是唯一能塞进「两个父都没有的内容」的地方；\n"
              "     白名单之外的文件出现在那里，意味着有东西没经过任何一次单独评审。\n"
              "  ⇒ 若那个文件是生成物（如 docs_test.go）：别手合，删掉重跑生成器。",
              file=sys.stderr)
        return 1

    print("✅ 扫了 %d 颗合并：自带 diff 为空 %d · 只有账本 %d · 历史豁免 %d"
          % (len(shas), ok, ledger, exempt))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
