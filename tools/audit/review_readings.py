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
        退出码：0 干净 · **3 有冲突⇒必须有人读一遍** · 2 拒绝出结论
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


def git_raw(*args):
    """跑一条 git，**把 (退出码, stdout, stderr) 原样交回来，不抛**。

    ⛔ 它和上面那个 `git()` 是**故意分开的两个**，而分开的理由是一句判据：
    **`git()` 的契约是「非 0 就抛」，因为它的每一个调用者都把非 0 当成故障。**
    而 `merge-tree` 不是 —— 它用**非 0 退出表达一个正常的结论**（这次有冲突）。
    ⇒ 拿 `git()` 去调它，会把一个结论读成一次故障；
      而给 `git()` 加一个「这次不要抛」的开关，等于让每个调用点都能关掉那道闸门。
    🔴 **一道闸门只要有开关，最终就会被打开。** ⇒ 宁可两个函数。
    """
    p = subprocess.run(("git",) + args, capture_output=True)
    return (p.returncode,
            p.stdout.decode("utf-8", "replace").strip(),
            p.stderr.decode("utf-8", "replace").strip())


def _is_oid(s):
    """40 位十六进制（本仓是 sha1 仓；sha256 仓会是 64 位，那时这里要放宽）。"""
    return len(s) == 40 and all(c in "0123456789abcdef" for c in s)


def verify_merge(rev):
    """核一次【真合并】是不是干净的。

    门禁要的那句话是：**「批过的，和落地的，是同一个东西。」**
    快进时它自动成立；而合并提交是**唯一**能让它不成立的地方。

    ⛔ **【2026-09-10 升级】此前用的判据是「`git show --format='' <合并>` 必须 0 行」，
    而那条判据【两个方向都会错】。** 评审方造对照组打出来，我另造一组独立复现：

        场景                          老判据              新判据
        真·不相交·全自动               0 行  ✅            树相等 ✅
        **两边改同一块·全自动**        **8 行 ⇒ 判成脏**   树相等 ✅
        evil merge（夹带一行）         11 行 ⇒ 拒 ✅        树不等 ⇒ 拒 ✅
        **`-s ours`（丢掉一个父）**    **0 行 ⇒ 判成干净**  树不等 ⇒ 拒 ✅
        **真冲突用 `-X ours` 解**      **0 行 ⇒ 判成干净**  冲突 ⇒ 要人读 ✅
        真冲突·人手解                  14 行               冲突 ⇒ 要人读 ✅

    🔴 **假红**：`--cc` 只显示「与**所有**父都不同」的 hunk，而两边改同一块时，
    交织出来的结果**本来就与两个父都不同** ⇒
    **「引入了两个父都没有的内容」≠「有人夹带」。**

    🔴 **假绿（更重）**：`-s ours` / `-X ours` 会让一个父的内容**整个不落地**，
    而每一处结果都等于某一个父 ⇒ `--cc` 一行都不显示 ⇒ 老门禁判它**干净**。
    ⇒ **那正是门禁存在的理由被完整绕过的那一格。**

    ⇒ 新判据问的是**正确的那个问题**：**「git 自己会不会产出这一棵树？」**

        mt = git merge-tree --write-tree <父1> <父2>      与  <合并>^{tree}  比

    相等 ⇒ 没有人在合并当中动过手；不等 ⇒ 有人动过（**夹带或丢弃**）；
    而 `merge-tree` 报冲突时**两边都不判** —— 它精确地挑出「必须有人读一遍」那一类。

    ⚠️ 三条写下来的射程：
      · 要 **git >= 2.38**（`--write-tree` 是那时加的）——
        而这里**不去解析版本号**，判的是「它出没出树」：**量能力，不量标签**。
      · **章鱼合并（父数 > 2）在本判据下没有定义**（merge-tree 只吃两个父）
        ⇒ 第一段拒绝，而理由写成「本判据只覆盖两父合并」，**不是「它不干净」**。
      · `--write-tree` 会**往对象库里写树对象**（游离对象，`gc` 会收）——
        本模式因此不是纯只读的，写在这儿免得下一个人以为它没有副作用。

    ⚠️ 而本模式**不要求工作区干净** —— 它读的是历史，不是工作树；
    这一条与出数那一模式的射程不同，写在这儿免得被读成疏忽。

    ⚠️ 六次历史合并（42508fa 4aa53c0 55456af 0d0c0b4 7a74083 53e7cb6）新旧判据都过 ——
    🔴 **而没被咬到的原因要写下来：那六次的 hunk 都不相交，也没人用过 `-s ours`。
    不是判据强，是【输入没走到那两格】。**
    """
    parents = git("rev-list", "--parents", "-n1", rev).split()
    n = len(parents) - 1
    print("提交            %s" % parents[0])
    print("父数            %d" % n)

    # ── 第一段：父数 ──
    if n != 2:
        print("")
        print("REFUSE: 本判据只覆盖【两父合并】，而这一颗有 %d 个父。" % n)
        print("（单亲：git show 印的是那颗自己的改动，与「合并塞进了什么」同型而不同义。）")
        print("（章鱼：git merge-tree 只吃两个父 ⇒ 这里【没有定义】，不是「不干净」。）")
        return 2
    p1, p2 = parents[1], parents[2]
    print("父              %s" % p1)
    print("父              %s" % p2)

    # ⚠️ 这一行**只是展示，不再是判据**。它留着的唯一理由是：
    # 旧门禁按它做过六次判断，把它并排印出来，读信的人能看见新旧两条是否同调。
    lines = git("show", "--format=", rev).split("\n")
    nl = 0 if lines == [""] else len(lines)
    print("合并自身 diff   %d 行  ⚠️ 仅供展示，**不是判据**（两个方向都会错，见本函数说明）" % nl)

    # ── 第二段：让 git 自己重做一次机械合并 ──
    code, out, err = git_raw("merge-tree", "--write-tree", p1, p2)
    first = out.split("\n")[0].strip() if out else ""

    # ⛔ 先判「这条命令有没有产出一棵树」，再判它的退出码 —— 顺序不能反。
    # 实测（本机 git 2.45.0.windows.1，2026-09-10）：
    #     真冲突      exit=1   stdout 首行 = 一个 oid
    #     坏 ref      exit=1   stdout **空**
    # 🔴 **两者退出码一模一样** ⇒ 只看退出码会把「工具自己失败」读成「这次是人手解的」。
    # ⇒ 判据是**「它出没出树」**，退出码只用来分「干净 / 冲突」。
    if not _is_oid(first):
        print("")
        print("REFUSE: git merge-tree 没有产出一棵树 ⇒ **本次不出结论**（exit=%d）。" % code)
        if err:
            print("        stderr: %s" % err.split("\n")[0])
        print("⚠️ 若报的是不认识 --write-tree：本判据要 **git >= 2.38**。")
        print("（而这里判的是【它出没出树】不是【版本号是多少】——"
              "量能力比量标签硬：版本对而功能被裁掉的构建也会在这儿被拦住。）")
        return 2

    if code == 1:
        print("机械合并        **有冲突** ⇒ 这一颗必然是【人手或非默认策略】解出来的")
        print("")
        print("需要有人读一遍：本判据到此为止 —— **它不判干净，也不判脏**。")
        print("（`-X ours` / `-X theirs` / 手工编辑都会落在这一格，"
              "而它们的共同点是：那次合并的内容【不是 git 自己算出来的】。）")
        return 3
    if code != 0:
        print("")
        print("REFUSE: merge-tree 出了树却又非 0/1 退出（exit=%d）⇒ 本次不出结论。" % code)
        return 2

    got = git("rev-parse", rev + "^{tree}")
    print("机械合并树      %s" % first)
    print("这颗合并的树    %s" % got)
    if first != got:
        print("")
        print("REFUSE: 两棵树不同 ⇒ **有人在这次合并当中动过手**。")
        print("⇒ 两个方向都落在这儿，而它们看起来相反：")
        print("   夹带（evil merge）—— 塞进了两个父都没有的内容")
        print("   丢弃（-s ours 之类）—— 某个父的内容【整个没落地】")
        print("🔴 而后者正是门禁存在的理由被绕过的那一格：")
        print("   「批过的那颗分支内容一行都没进来」，而它没有冲突、没有报错。")
        return 2

    print("")
    print("✅ 干净：父数 2 ＋ **git 自己会产出同一棵树** ⇒ 没有人在合并当中动过手。")
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
