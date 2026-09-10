# -*- coding: utf-8 -*-
"""送审读数：一条命令产出送审信第一段要贴的全部数字。

⛔ 它存在的理由是一次实录（2026-09-10）：
   我在 SHA-A 上取了一批读数，随后提交了 SHA-B，然后把那批读数贴进送审信里
   并标成「当前尖上逐条量」—— **而 SHA-B 正是改掉那个读数的那一颗。**

   🔴 判据（评审方给的）：**一个读数和一颗提交并排出现在同一封信里时，
   先问「这个读数是在这颗提交【之前】还是【之后】取的」** ——
   而「送审信」这个体裁天然把两者并排放，**所以它是这类错误发生率最高的地方。**

⇒ 处置不是「记得在提交之后再量一次」（那又是一条要人想起来的规矩），
  是把那些读数收进一条命令里，**并让它在工作区不干净时直接拒绝出数**：
  工作区脏 ⇒ 你即将送审的那颗，和你正在量的这棵树，不是同一个东西。

⚠️ 而它有一条写下来的射程：**它只保证「这些数取自 HEAD 这一刻」** ——
   而其中【分支尖之外】那一栏漂得更快：**它由别人的动作改变，我什么都不做它也会变**
   ⇒ 所以那一栏单独带一个时刻。
   它保证不了你【贴进信里的时候】HEAD 还是它。⇒ 出数时把 HEAD 全长一并打出来，
   收信方可以用那一行去核。

跑法：python tools/audit/review_readings.py [main]           出送审读数
      python tools/audit/review_readings.py --verify-merge [rev]  核一次真合并干不干净
"""
import datetime
import io
import subprocess
import sys

# 控制台可能是 GBK（本机实测），而本文件的说明是中文。
# ⚠️ 两个流都要包 —— 本仓 TestScriptsWrapBothStreams 立过这条，理由是
# 【失败时】印出来的每个中文字都是乱码，而失败那一支恰恰是本脚本最要紧的输出。
# （我第一版只用 sys.stdout.reconfigure 包了 stdout，被那条守卫当场拦下。）
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")


_warnings = []


class GitFailed(Exception):
    """一次 git 调用非 0 退出。"""

    def __init__(self, args, code, stderr):
        self.args_ = args
        self.code = code
        self.stderr = stderr


def git(*args):
    """跑一条 git，**非 0 退出就抛**。

    ⛔ 第一版只取 stdout、不看返回码 —— 而评审方 2026-09-10 把它两端都打了：

        base_ref 打错   ⇒ 每一次 git 都失败，而它 exit=0 印出一份【完整的假报告】
        不在 git 仓里   ⇒ 同上，且那份报告是【最令人安心】的那一版：
                          「porcelain 0」「分支尖之外（无）」

    🔴 而承重的那一句正好落在这儿：`dirty = git("status", "--porcelain")`
    失败时也返回空串 ⇒ **「工作区不干净就拒绝出数」这道闸门，在 git 本身失败时判为【干净】**
    ⇒ **失败方向是放行。**

    ⚠️ 而它不是「脚本要健壮」那一类要求：**这份输出会被贴进送审信，
    而送审信是门禁的证据链** —— 一个能静默印出「分支尖之外（无）」的工具，
    恰好在门禁最需要真值的那一栏上失效。

    ⚠️ 单列一条【试过而不成立】的（评审方量的，我收）：
    本以为现实触发是「共用工作树里并发跑 git 撞 index.lock」——
    实测把 index.lock 摆在那儿，`git status --porcelain` **照样 exit 0**（它只是不回写索引）
    ⇒ **不拿它当理由**。可达的是上面那两条（参数打错 / 不在仓里或 git 不在 PATH）。
    """
    p = subprocess.run(("git",) + args, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    if p.returncode != 0:
        raise GitFailed(args, p.returncode, (p.stderr or "").strip())
    # ⛔ 成功时的 stderr 也要收着 —— 实测：一个指向不存在对象的 ref，
    # `for-each-ref` **警告到 stderr 并跳过它**，而退出码是 0
    # ⇒ 那条 ref 既不出现在结果里，也不引起失败 ⇒「分支尖之外」印出「（无）」。
    # 🔴 而那一栏的职责正是**把我不知道的东西报出来**，
    # 一个「被安静跳过的 ref」恰恰是它最该报的那一种。
    warn = (p.stderr or "").strip()
    if warn:
        _warnings.append("git %s: %s" % (" ".join(args), warn.split("\n")[0]))
    return p.stdout.strip()


def verify_merge(rev):
    """核一次【真合并】是不是干净的。

    ⛔ 它存在的理由是一个洞，2026-09-10 评审方在**他自己的门禁检查**上打出来的，
    而我这边也有同一个（我每次并完都打印父数，**却只是打印，没有断言**）：

        `git show --format='' <X> | wc -l` 在一个**单亲提交**上印的是
        **那颗提交自己的 diff**，不是「合并自身的 diff」

    🔴 **两个数长得一模一样（都是「行数」），而含义完全不同** ——
    他那次读到 32 行并按门禁读成「不干净」，而真相是那根本不是一次合并
    （`git merge` 原样回了 "Already up to date."，因为 main 在两批测量之间动了）。

    ⇒ 所以顺序是**先父数、后行数**：**父数 ≠ 2 时，那个行数没有真值。**

    ⚠️ 而本模式**不要求工作区干净** —— 它读的是历史，不是工作树；
    这一条与出数那一模式的射程不同，写在这儿免得被读成疏忽。
    """
    parents = git("rev-list", "--parents", "-n1", rev).split()
    n = len(parents) - 1
    print("提交            %s" % parents[0])
    print("父数            %d" % n)
    if n != 2:
        print("")
        print("REFUSE: 这不是一次【两个父】的合并 —— 那么「合并自身的 diff」这个量没有真值。")
        print("（在单亲提交上，git show 印的是那颗提交自己的改动，"
              "而它与「合并塞进了什么」长得一模一样，都是一个行数。）")
        return 2
    for p in parents[1:]:
        print("父              %s" % p)
    lines = git("show", "--format=", rev).split("\n")
    nl = 0 if lines == [""] else len(lines)
    print("合并自身 diff   %d 行" % nl)
    if nl != 0:
        print("")
        print("REFUSE: 这次合并引入了【两个父都没有的内容】（冲突解决 / evil merge）。")
        print("⇒ 门禁要的是「批的和落地的是同一颗」，而这一行让它不再成立。")
        return 2
    print("")
    print("✅ 干净：父数 2 ＋ 合并自身 0 行 ⇒ 两个父的内容原样落地，没有夹带。")
    return 0


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "--verify-merge":
        rev = sys.argv[2] if len(sys.argv) > 2 else "HEAD"
        try:
            return verify_merge(rev)
        except GitFailed as e:
            print("REFUSE: git %s -> exit %d" % (" ".join(e.args_), e.code))
            return 2
    try:
        return _main()
    except GitFailed as e:
        print("REFUSE: git %s -> exit %d" % (" ".join(e.args_), e.code))
        if e.stderr:
            print("   " + e.stderr.split("\n")[0])
        print("")
        print("一次 git 调用失败了 —— 而这份输出会被贴进送审信。")
        print("宁可不出数，也不出一份【看起来完整】的报告。")
        return 2


def _main():
    base_ref = sys.argv[1] if len(sys.argv) > 1 else "main"

    dirty = git("status", "--porcelain")
    if dirty:
        n = len(dirty.split("\n"))
        print("REFUSE: working tree not clean (%d entries)" % n)
        print("")
        print("你即将送审的那颗，和你正在量的这棵树，不是同一个东西。")
        print("先提交（或还原），再跑这条命令 —— 这正是它存在的理由。")
        for line in dirty.split("\n")[:10]:
            print("   " + line)
        return 1

    tip = git("rev-parse", "HEAD")
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    base = git("rev-parse", base_ref)
    mb = git("merge-base", base_ref, "HEAD")
    ahead = git("rev-list", "--count", "%s..HEAD" % base_ref)
    behind = git("rev-list", "--count", "HEAD..%s" % base_ref)

    # ⛔ **所有 git 调用都收在第一个 print 之前**（评审方 2026-09-10 建议甲，我采纳）。
    #
    # 原来的顺序是：先打印头部六行 → 再去跑 for-each-ref 与循环里的 rev-list。
    # 于是那句「宁可不出数，也不出一份【看起来完整】的报告」
    # **只对第一批 git 调用成立** —— 后面任何一次失败，头部六行都已经落地了，
    # 而 REFUSE 会印在「分支尖之外」那个标题【下面】
    # ⇒ 那份输出是「一份看起来完整的报告，而恰好缺了那一栏」，
    # **而那一栏正是这条命令最值钱的那一栏。**
    # 🔴 ⇒ 那句承诺原来靠「第一批调用恰好都在打印之前」成立，**而不是靠结构**。
    #
    # ⚠️ 单列一条【我复现不出来的】：评审方用一条指向不存在对象的 ref 触发了它；
    # 我照同一构造跑，`for-each-ref` 警告到 stderr 并**跳过**那条 ref、退出码 0
    # ⇒ 循环根本没调到 rev-list ⇒ 没有失败。
    # **我没能复现那个触发** —— 上面这个顺序问题是【读调用次序】确认的，不是量出来的。
    others = []
    for r in git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes").split("\n"):
        if not r:
            continue
        n = git("rev-list", "--count", "%s..%s" % (base_ref, r))
        if n and n != "0":
            others.append((r, n))
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    # ——— 到这里为止，一次 git 都不会再跑；下面只打印 ———

    print("分支            %s" % branch)
    print("分支尖(全长)    %s" % tip)
    print("%-14s  %s%s" % (base_ref, base, "" if base != mb else "   （＝ merge-base，可快进）"))
    print("merge-base      %s" % mb)
    print("%s..尖  %s   ·   尖..%s  %s   ⇒  %s"
          % (base_ref, ahead, base_ref, behind,
             "快进" if behind == "0" else "分岔 ⇒ 并时是真合并，记得核那次合并自身的 diff 是 0 行"))
    # ⚠️ 印【量到的那个值】，不印一个打上去的 0（评审方提，我收）——
    # 这个脚本的题目正是「读数要来自它自己的定义式」，而这一行原来是个字面量。
    # ⇒ 它与上面那道闸门用的是**同一次读数**，所以两者不可能互相矛盾。
    print("porcelain       %d" % (0 if not dirty else len(dirty.split("\n"))))
    print("")

    # ⛔ 这一栏带时刻，而上面那几栏不带 —— 因为它们的【成因】不同（评审方提，我收）：
    #
    #   分支尖 / merge-base / porcelain   由**我自己**的动作改变 ⇒ 我不动它就不变
    #   分支尖之外                        由**别人**的动作改变 ⇒ **我什么都不做它也会变**
    #
    # ⇒ 同本仓那条：**当前值由对方的动作改变，而任何触发条件都必然迟对方一步。**
    # 📎 实录：这一栏第一次真用就报出一条我不知道的 ref（评审方 worktree 的本地分支），
    #    而我发信时它已经被删了 —— **报告为真，只是发信时已不是当前值。**
    print("分支尖之外（本地 heads ＋ 远端跟踪，逐条 rev-list --count %s..它）"
          "  —— 这一栏由【别人】的动作改变，读于 %s：" % (base_ref, now))
    if not others:
        print("   （无）")
    for r, n in others:
        mark = "  ← 本分支" if r.endswith("/" + branch) or r.endswith(branch) else ""
        print("   %-46s +%s%s" % (r, n, mark))

    if _warnings:
        print("")
        print("⚠️ git 自己的告警（退出码是 0，而它说了话 —— 通常意味着某个 ref 被跳过了）：")
        for w in _warnings:
            print("   " + w)
    return 0


if __name__ == "__main__":
    sys.exit(main())
