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
   它保证不了你【贴进信里的时候】HEAD 还是它。⇒ 出数时把 HEAD 全长一并打出来，
   收信方可以用那一行去核。

跑法：python tools/audit/review_readings.py [main]
"""
import io
import subprocess
import sys

# 控制台可能是 GBK（本机实测），而本文件的说明是中文。
# ⚠️ 两个流都要包 —— 本仓 TestScriptsWrapBothStreams 立过这条，理由是
# 【失败时】印出来的每个中文字都是乱码，而失败那一支恰恰是本脚本最要紧的输出。
# （我第一版只用 sys.stdout.reconfigure 包了 stdout，被那条守卫当场拦下。）
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")


def git(*args):
    return subprocess.run(("git",) + args, capture_output=True, text=True,
                          encoding="utf-8", errors="replace").stdout.strip()


def main():
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
    print("porcelain       0")
    print("")

    # 分支尖之外还有没有别的
    print("分支尖之外（本地 heads ＋ 远端跟踪，逐条 rev-list --count %s..它）：" % base_ref)
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
