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
      ⚠️ 任何以 `-` 开头而不是 `--verify-merge` 的参数（**在任何位置**）一律拒绝；
         多余的参数也拒绝 —— 静默忽略它和静默忽略一个打错的旗标是同一件事。
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

    ⚠️ 而「什么时候才假红」是**量出来的**（两人各扫一遍，逐格一致）：
    20 行纯文本，A 改第 10 行，B 改第 (10+d) 行，d 从 0 扫到 8，每格独立建仓：

        d ≤ 1   ⇒ **冲突**（merge exit=1）—— 根本合不出来
        d = 2   ⇒ 全自动合成 · 老判据 **16 行** ⇐ 假红
        d = 3   ⇒ 全自动合成 · 老判据 **17 行** ⇐ 假红
        d ≥ 4   ⇒ 全自动合成 · 老判据 **0 行**

    ⇒ **假红的窗口是「近到落进同一个 combined hunk，而远到不冲突」的那一条缝** ——
    它的宽度由 `diff.context`（默认 3）决定 ⇒ **它是 hunk 粒度的产物**。
    ⚠️ 射程：这是一个构造（纯文本 · 单行替换 · 默认上下文行数）——
    换文件类型或 `-U` 会让窗口移动。**而「窗口存在且很窄」这件事是量出来的。**
    ⇒ 于是这句话有真值了：**老判据只在「两处改动落进同一个 hunk 而不重叠同一行」时假红；
    而新判据不依赖 hunk 粒度 —— 那正是它更硬的地方。**

    ✅ **重命名 × 改同一个文件**这一格也验过（A 把 f 改名成 g，B 改 f 的第 2 行）：
    合并后 `g.txt` 第 2 行是 B 的值，两棵树**相同** ⇒ 在默认配置下，
    `merge-tree` 与 `git merge` 对重命名检测是**同调**的。
    ⚠️ 而有人改了 `merge.renameLimit` / `diff.renames` 的情形**两人都没造** ——
    它的失败方向是**假红**（判成有人动过手）⇒ 会把人叫来，而那是安全的一边。
    🔴 **一个判据要挑失败方向的话，就该挑【会把人叫来】的那一边：假红叫人，假绿不叫。**

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
        print("")
        # ⛔ 「要人读」必须配一条【读什么】，否则它退化成「以后有人会看」。
        # 评审方 2026-09-10 提，我收：**没有那一句，exit=3 与 exit=0 在评审信里长得一样。**
        print("⇒ 要读的是这两份 diff（**两份都要**，它们回答的是相反的问题）：")
        print("     git diff %s...%s" % (p1[:12], parents[0][:12]))
        print("       ↑ 相对【第一个父】这一侧：这次合并**带来了什么**")
        print("     git diff %s...%s" % (p2[:12], parents[0][:12]))
        print("       ↑ 相对【第二个父】这一侧：**有什么没落地**")
        print("⚠️ 而其中一份为空是一个**重要读数**，实测四格：")
        print("     -X ours 第一份 0 行 · -X theirs 第二份 0 行 ·"
              " 人手解出第三个值 两份都 13 行")
        print("     ⛔ **而人手解冲突【选了某一个父的值】时，读数与 -X 完全一样**"
              "（第一份 0 行）——")
        print("       两者的树逐字节相同 ⇒ **树上分不出「机器自动丢的」和"
              "「人读过之后决定丢的」**。")
        print("     ⇒ 所以一份为空说明的是**事实**（那一侧整个没落地），"
              "不是**成因** ——")
        print("       而它恰恰是最该被人读的那一格：**只有人分得出这两者。**")
        print("⛔ ⇒ 所以**空的那一份不是「没事」，恰恰是「去读另一条」的信号** ——")
        print("     实测两格：`-s ours` 丢掉父2新增的整个文件 ⇒ 第一份 **0 行**，"
              "而 `newfile.txt | 1 -` 只出现在第二份里；")
        print("     重命名 × 同一行冲突用 `-X ours` 解 ⇒ 第一份 **0 行**，"
              "而重命名标记与被换掉的那一行**全在第二份**。")
        print("     ⇒ **只读第一条的人，看到的是「什么都没发生」。**")
        print("⇒ 而放行时要在信里【明写一句】：「我读了那次冲突解决；它丢掉了 X、保留了 Y」。")
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


# 本脚本认得的全部旗标。**只有这一份名单**，别在别处再抄一份。
_FLAGS = ("--verify-merge",)


def check_argv(argv):
    """把每一个参数都过一遍：**认得的旗标只许出现在第 1 位，其余位置不许以 `-` 开头**。

    返回 None 表示放行；否则返回一句拒绝理由。

    ⛔ 它存在的理由是同一条规矩今天露出的**第三个面**（评审方 2026-09-10 量出来的）：

        我上一颗给 `argv[1]` 装了「不认识的旗标就拒」，理由是
        `-x` 会被 git 自己兜住，**而报文指向 `git merge-base`，把人引向错误的地方**。
        ⇒ 而 `--verify-merge **-x**` 原样存在：
          `REFUSE: git rev-list --parents -n1 -x -> exit 129` —— **同一个形态，换了个位置**。

    🔴 **一条规矩只覆盖【它被写下的那个位置】** ——
    它不向后覆盖旧的（已记）、不向前继承新写的（已记）、
    **也不横向覆盖同一支程序里的另一个参数**（这一面是今天新的）。

    ⇒ 所以处置**不是再抄一份判据到 `argv[2]` 上** —— 那样第四个位置出现时还会漏。
    ⇒ 是把它提到**一个覆盖全部参数的地方**，并让名单只有一份（`_FLAGS`）。
    ⚠️ 而「多余的参数」也一并拒：**静默忽略一个我没打算给的参数，
    和静默忽略一个我打错的旗标，是同一件事。**
    """
    for i, a in enumerate(argv[1:], start=1):
        if a in _FLAGS:
            if i != 1:
                return "旗标 %s 只能出现在第一个参数的位置（这里是第 %d 个）" % (a, i)
            continue
        if a.startswith("-"):
            return ("不认识的旗标 %s —— 本脚本只认 %s。\n"
                    "（若你确信它存在，那么手上这份【不是】带它的那个版本：\n"
                    "  用 `grep -c 'def verify_merge' <本文件>` 当场问一次。）"
                    % (a, " ".join(_FLAGS)))
    if len(argv) > 3 or (len(argv) == 3 and argv[1] not in _FLAGS):
        return ("多了用不上的参数：%s —— 本脚本最多接『一个旗标 ＋ 一个 rev』"
                "或『一个 base_ref』。\n"
                "（静默忽略一个多余的参数，和静默忽略一个打错的旗标，是同一件事。）"
                % " ".join(argv[1:]))
    return None


def main():
    bad = check_argv(sys.argv)
    if bad:
        print("REFUSE: " + bad)
        return 2
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
    # ⛔ 不认识的 `--flag` **不许静默落进这一模式**。
    #
    # 这一条是被自己咬出来的（2026-09-10）：我拿一份**旧版本**的本脚本
    # （那条分支上还没有 `--verify-merge`）去跑 `--verify-merge`，
    # 它把那个参数当成 `base_ref` 之外的东西**默默忽略**，落进出数模式，
    # 然后报了一句关于**别的问题**的话（「working tree not clean」）——
    # 🔴 **一个真的读数、一个清楚的报错，而它回答的是我没问的那个问题** ⇒
    # 我据此写下了八格「新判据 exit=1」，**八格全废**。
    #
    # ⇒ 判据：**一个工具收到它不认识的旗标时，必须停，不能改做另一件事。**
    # ⚠️ 而它救不了「旧版本」那一格（旧版本里没有这段代码）——
    # 它防的是**此后**改名/新增旗标时的同一个坑。
    # 救「旧版本」那一格的是另一句：**跨版本取工具时，先问它认不认得那个旗标**
    # （`grep -c 'def verify_merge'`），别假定手上这份就是我改过的那份。
    # ⚠️ 判据是 `-` 不是 `--`，而放宽的理由不是洁癖（评审方 2026-09-10 量出来的）：
    #
    #	`-x` 今天靠 git 自己失败兜住 ⇒ exit=2（方向对）
    #	**而它的报文是 `git merge-base -x HEAD -> exit 129`** ⇒ 读的人会去查 merge-base
    #
    # 🔴 **一个方向正确的拒绝，仍然可以把人指向错误的地方。**
    # ⛔ **【2026-09-10 更正】这里原来有一句全称否定，说「一个能用的 base_ref
    # 【绝无可能】以连字符起头，所以放宽是安全的」—— 而那句话是假的。**
    #
    # ⚠️ 这一段**描述**那句话，不**复制**它 —— 抄一遍的话，
    # 任何「那句假话清干净了没有」的 grep 都会命中这段更正说明本身
    # （本仓记过这一格；而我第一版正是抄了，复扫读到 1、对照组也读到 1，
    #  **两个数一样 ⇒ 那次复扫什么也没证明**）。
    #
    # ⚠️ **而复扫那个数要带上【选择器】** —— 我在提交信息里只写了「复扫 0 · 对照组 1」，
    # 而评审方拿六条选择器各量一遍，发现其中一条在两颗 SHA 上**都是 1**：
    # **那条选择器取的是原句的主语部分，而我只改了谓语，主语原样留着。**
    # 设计仍然成立（以【断言部分】为选择器的 grep 都干净了），
    # 🔴 **而「0」若不写清是哪条选择器下的 0，下一个人换一条读到 1，会以为没清干净。**
    # ⇒ 本仓那条的又一次：**给别人一个数，要把取它的那条命令一起给。**
    #
    # ⛔ **而写这一段时我又踩了上面那一格：第一版把选择器本身原样抄了进来**
    # ⇒ 那条选择器在本文件上的读数当场从 0 变回 1 —— **一句「要带上选择器」的说明，
    # 自己把那条选择器污染了。** ⇒ 所以此后报复扫要写成
    # 「复扫（选择器＝**那句被更正的话的谓语部分**）0 · 对照组 1」——
    # **描述那条选择器，别复制它。**
    #
    # 我当时的证据是 `git branch -f -- "-leading"` ⇒ exit=128 —— 我把它当成了
    # 「这个域不允许」。评审方往下问了一层，我复现，逐格：
    #
    #	git branch / git tag  "-leading"        ⇒ **exit=128**（拒绝创建）
    #	git check-ref-format refs/heads/-leading ⇒ **exit=0**  ← 它说这个名字**合法**
    #	git update-ref refs/heads/-leading <sha> ⇒ **exit=0，建成了**
    #	for-each-ref 列得出来 · `rev-parse refs/heads/-leading` ⇒ exit=0，给出一颗 SHA
    #
    # ⛔ **【同日再更正一格】上面这一行原来还列着 `rev-parse -- "-leading"` ⇒ exit=0，
    # 而我把它当成了「解析成功」的证据 —— 那是假的。**
    # 评审方去看了它的**输出**（我只看了退出码）：
    #
    #	git rev-parse -- "-leading"              ⇒ exit=0，而输出是 `-- -leading` **两个 token**
    #	                                            ⇒ 它根本没解析，只是把 `--` 之后的东西原样回吐
    #	git rev-list --parents -n1 -- "-leading" ⇒ **exit=129**  ← **加 `--` 也不救**
    #	git rev-list --parents -n1 refs/heads/-leading ⇒ exit=0  ← **只有全名可以**
    #
    # 🔴 **一个 exit=0 不等于「它做了我以为的那件事」** —— 而这一格里
    # 退出码和输出**说的是两件事**：码说「命令没出错」，输出说「什么都没解析」。
    # ⇒ 判据：**拿一条命令当证据时，读它的【输出】，不要只读它的退出码。**
    #
    # 🔴 ⇒ **这样一条 ref 能存在，而且能用（只是必须写全名）** ——
    # `git branch` 拒的是**创建它**，
    # 而 `update-ref` 是另一个入口，`check-ref-format`（git 自己的判据函数）根本放行。
    # ⇒ 形状是本仓那条打在我自己身上：**一句「不可能」是一句全称断言，
    # 而我量到的是【一个入口】** —— 而「那个域有几个入口」要先数一遍。
    #
    # ✅ 而**拒绝它仍然完全正确**，理由换成真的、且**比第一版更强**：
    # **即使这样一条 ref 存在，它也不能作为【裸参数】被安全地使用** ——
    # 实测 `git rev-list --parents -n1 "-leading"` ⇒ **exit=129**（被当成选项），
    # **而 `--` 这条常见的救法在 `rev-list` 上不成立**（同样 129，见上）⇒ 只有写全名可以。
    # 而本脚本给 git 的正是裸参数。
    # ⇒ **判据的行为一个字没改，变的只是它站在哪句话上。**
    #
    # ⇒ 而这道判据**已经不在这儿了** —— 它提到了 `check_argv()`，一次覆盖全部参数。
    # 🔴 理由见那个函数的说明：**原来这一份只覆盖 `argv[1]`，而同一个形态在 `argv[2]` 上原样存在**
    # （`--verify-merge -x`）⇒ **再抄一份到 argv[2] 上，第四个位置出现时还会漏。**
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
             "快进" if behind == "0" else
             "分岔 ⇒ 并时是真合并 ⇒ 并完跑 `--verify-merge <那颗合并>`"))
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
