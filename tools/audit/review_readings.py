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

跑法：python tools/audit/review_readings.py [main]
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
    return p.stdout.strip()


def main():
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

    # 分支尖之外还有没有别的
    # ⛔ 这一栏带时刻，而上面那几栏不带 —— 因为它们的【成因】不同（评审方 2026-09-10 提，我收）：
    #
    #   分支尖 / merge-base / porcelain   由**我自己**的动作改变 ⇒ 我不动它就不变
    #   分支尖之外                        由**别人**的动作改变 ⇒ **我什么都不做它也会变**
    #
    # ⇒ 同本仓索引里那条：**当前值由对方的动作改变，而任何触发条件都必然迟对方一步。**
    # 📎 实录：这一栏第一次真用就报出一条我不知道的 ref（评审方 worktree 的本地分支），
    #    而我发信时它已经被删了 —— **报告为真，只是发信时已不是当前值。**
    #    ⇒ 印一个时刻，让收信方一眼看出「这一栏是那一刻的」。
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    print("分支尖之外（本地 heads ＋ 远端跟踪，逐条 rev-list --count %s..它）"
          "  —— 这一栏由【别人】的动作改变，读于 %s：" % (base_ref, now))
    refs = git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes").split("\n")
    other = []
    for r in refs:
        if not r:
            continue
        n = git("rev-list", "--count", "%s..%s" % (base_ref, r))
        if n and n != "0":
            other.append((r, n))
    if not other:
        print("   （无）")
    for r, n in other:
        mark = "  ← 本分支" if r.endswith("/" + branch) or r.endswith(branch) else ""
        print("   %-46s +%s%s" % (r, n, mark))
    return 0


if __name__ == "__main__":
    sys.exit(main())
