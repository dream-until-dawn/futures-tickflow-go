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

## 用法

⚙ 这一节归**读者要能做的四件事**，多一句都不加（评审方 2026-09-12 给的停止规则）：

    一 扫全仓（默认起点 main）   二 只看点名的那几颗   三 换一个起点   四 读它的结论

⛔ 它**拒过东西**（不然这条规则等于没有）：本节原稿有两句被它拒掉 ——
「本仓那条在消费侧的形态：工具把读数印全了而调用方只取退出码」是一句**归类**，
答不出第几件；它归 memory（那边登记在案），不归这个文件头。
📎 本仓那条（**标签的载体排序**）：通则归方法论，**判据**才归工具自己。

### 三种形态，而它们**问的不是同一个问题**

    python tools/audit/merge_gate.py               # 扫 <rev> 上全部合并（默认 main）
    python tools/audit/merge_gate.py <merge>…      # 只看点名的那几颗（**位置参数**）
    python tools/audit/merge_gate.py --rev=<ref>   # 换一个【起点】，仍然扫它上面全部合并

⛔ **`--rev=` 换的是【起点】，不是「只看这一颗」** —— 喂单颗要用**位置参数**。
🔴 这一条是实测出来的（评审方 2026-09-11 撞的，2026-09-12 我在 main 上复量）：

    $ SHA=$(git log --no-merges --format=%h -1 main)   # 2026-09-12 取到 5eaaa02
    $ python tools/audit/merge_gate.py $SHA            ⇒ rc=3  拒绝出读数，报文点名「有 1 个父」
    $ python tools/audit/merge_gate.py --rev=$SHA      ⇒ rc=0  「扫了 **54** 颗合并」
    $ python tools/audit/merge_gate.py                 ⇒ rc=0  「扫了 **55** 颗合并」   ← 默认起点 main

⇒ `--rev=` 那一路扫的是**那颗提交祖先里的全部合并**，它们都合规 ⇒ 退出码当然是 0。
📎 而 **54 与 55 这一对差，本身就是这条的证据**：换起点 ⇒ 颗数变；「只看这一颗」则应当是 **1**。
⚠️ 那三个数是 2026-09-12 的**当场读数**，会随 main 增长 —— 要的是**那条命令**，不是那个数。
🔴 **用错了输入的形状，而它的退出码长得和「这一档不存在」一模一样** ——
差一步就写成「第 3 档走不到」。

⚠️ **而读它的结论，不能只读退出码** —— 要连那行计数一起读：

    ✅ 扫了 N 颗合并：自带 diff 为空 … · 只有账本 … · 历史豁免 …

🔴 想喂单颗时，那个 `N` **必须是 1** —— 不是 1，就说明命令的形状错了（别记具体是几）。
⚠️ 所以别把它的 stdout 扔掉：**`>/dev/null` 之前问一句
「我扔掉的那几行里，有没有能推翻我这个结论的」**（这一句归第四件）。

## 退出码（三档，而每一档都配了一个真能走到它的输入）

    0   扫到 ≥1 颗合并，且全部合规
    1   **有违例** —— 报文里点了名
    2   **扫到 0 颗** —— 空转；那不是「合规」，是【量不了】
    3   **跑不起来／前提不成立** —— 没有 git、不在仓里、没有那个 ref、喂了非合并提交

⛔ 2026-09-11 之前**第 3 档根本不存在**：`raise SystemExit("refuse: …")` 传的是字符串，
退出码是 **1** —— 与「有违例」**同一个字节**。而调用方（`merge_gate_test.go`）的图例
逐字写着「其它 ＝ 跑不起来」。

🔴 那句图例**不是假的，是【为真而有害】**：读到 1 的人被它送去查「哪颗合并越界」，
而真相是**这道门禁根本没跑起来**。⇒ 本仓收的那条：
**一句处置有三种坏法 —— 为假 · 为真而有害 · 循环。**

⚠️ 而它是怎么过了评审的，评审方自己给的答案（原话）：
**「评审读的是断言，而一份【图例】不长得像断言 —— 它长得像帮助文本。」**
⇒ 所以这三档不留在图例里：**每一档在 `merge_gate_test.go` 里都有一个走得到它的用例**，
而**调用方一律断言 `rc == 0`，不许写 `rc != 1`**。
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


# 退出码三档。**别把它们内联成字面量** —— 上面那段记着为什么：
# 「跑不起来」曾经和「有违例」共用 1，而图例照旧写着它们不同。
EXIT_VIOLATION = 1
EXIT_NOTHING = 2
EXIT_BROKEN = 3


def refuse(msg):
    """前提不成立 ⇒ 拒绝出读数，**用它自己那一档退出码**。

    ⛔ 不许写回 `raise SystemExit(msg)`：那个的退出码是 **1**，而 1 是「有违例」。
    两种处置完全相反（一个去查哪颗合并越界，一个去查环境），
    **而它们曾经共用一个字节。**
    """
    print(msg, file=sys.stderr)
    raise SystemExit(EXIT_BROKEN)


def git(*args):
    """跑 git，非 0 就拒。

    ⛔ **不给它加 check=False** —— 本仓那条：一道闸门只要有开关，最终就会被打开。
    要用非 0 表达正常结论的地方，另写一个函数。
    """
    p = subprocess.run(["git"] + list(args), capture_output=True)
    if p.returncode != 0:
        refuse("refuse: git %s ⇒ exit=%d\n%s"
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
        refuse("refuse: %s 有 %d 个父 —— 它不是合并提交。\n"
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
        return EXIT_NOTHING

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
        return EXIT_VIOLATION

    print("✅ 扫了 %d 颗合并：自带 diff 为空 %d · 只有账本 %d · 历史豁免 %d"
          % (len(shas), ok, ledger, exempt))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
