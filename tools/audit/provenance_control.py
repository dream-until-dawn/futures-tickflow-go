"""`TestHighWaterProvenance` 的对照组 —— 该红的红没红，该绿的有没有被误伤。

## 为什么这份要进仓库

`docs/README.md` §6：**不是探针输出的数字，产生它的脚本要落进 `tools/audit/`。**

而这一条是被真事逼出来的（2026-09-08）：上一版这个对照组只存在于一次性脚本里，
表格贴进了 commit message。**其中一行的标签和它实际跑的变异不是一回事**——
标签写「改来历行去迁就当前值 ⇒ FAIL」，代码干的是「只改来历、不动当前值」。
两件事，两个结果，而记录里只留下了那个绿灯。

    一行标签与实际跑的不符的对照组，比缺这一行更糟：
    缺了是一处空白，标签错了是【一个为错误结论作证的绿】。

它是评审方独立重跑才发现的。**所以这份脚本进仓库，不是为了留个数，
是为了让那一行标签随时能被任何人重新跑一遍。**

## 它自己造一份副本，不碰工作区

`CONTRIBUTING.md`：**会变异工作区的测量，去扔掉的克隆里跑。**
本脚本把仓库复制到系统临时目录（不带 `.git`、不带 `.env`），在那儿动手，跑完整目录删掉。
⇒ 跑它【不需要】先清工作区，也不会弄脏别人的 `git status`。

## 四态，不是两态

    BUILD  go vet 就没过     ⇒ 这一格什么都没测到，不算变红
    TIME   超时              ⇒ 同上
    FAIL   编译过了，测试红了
    PASS   编译过了，测试绿了

跑法：python tools/audit/provenance_control.py
退出码：全部与期望相符 ⇒ 0；有任何一格不符 ⇒ 1
"""
import io
import os
import shutil
import subprocess
import sys
import tempfile

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
# stderr 也包 —— 断言消息与 SystemExit 走的是 stderr，
# 而**一条读不懂的失败信息，和没有失败信息差不多**（2026-09-09 实测两次）。
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
SKIP = {".git", ".env", "__pycache__", "node_modules"}

PASS, FAIL, BUILD, TIME = "PASS", "FAIL", "BUILD", "TIME"


def ignore(_dir, names):
    return [n for n in names if n in SKIP or n.endswith(".exe")]


def read(p):
    return open(p, encoding="utf-8").read()


def write(p, s):
    open(p, "w", encoding="utf-8", newline="\n").write(s)


def guard(work, name="TestHighWaterProvenance"):
    """跑那条守卫，返回四态之一。

    ⚠️ 收 name 是因为 high_water.txt 上现在有【两条】守卫：
    `TestHighWaterProvenance`（当前值 vs 最后一行来历）与 `TestHighWaterChain`（链）。
    一格变异只该由它针对的那一条来判 —— 让两条一起跑，
    **一格会因为「另一条红了」而显示为红，而那不是这一格测到的东西。**
    """
    v = subprocess.run(["go", "vet", "./..."], cwd=work, capture_output=True, timeout=300)
    if v.returncode != 0:
        return BUILD
    try:
        p = subprocess.run(
            ["go", "test", "-run", name, "-count=1", "."],
            cwd=work, capture_output=True, timeout=300)
    except subprocess.TimeoutExpired:
        return TIME
    if "[build failed]" in (p.stdout + p.stderr).decode("utf-8", "replace"):
        return BUILD
    return PASS if p.returncode == 0 else FAIL


# ── 各格的变异。每个函数拿到 high_water.txt 的原文，返回改过的原文。──
#
# ⚠️ 变异写成【对原文的替换】而不是写死一份文件，这样高水位涨了它们仍然成立；
#    而每个替换都断言命中，命中不了就当场报错 —— 不会静静地测了个寂寞。

def _sub(s, old, new):
    assert s.count(old) == 1, "变异锚点不唯一（%d 次）：%r" % (s.count(old), old)
    return s.replace(old, new, 1)


def _cur(s, key):
    """当前值那一行。"""
    for ln in s.splitlines():
        f = ln.split()
        if len(f) == 2 and f[0] == key:
            return ln, int(f[1])
    raise AssertionError("high_water.txt 里找不到 %s 的当前值行" % key)


def _last_prov(s, key):
    """该名字最后一行来历。"""
    hit = None
    for ln in s.splitlines():
        f = ln.strip().split()
        if len(f) >= 5 and f[0] == "#" and f[1] == "来历" and f[3] == key:
            hit = ln
    assert hit, "high_water.txt 里找不到 %s 的来历行" % key
    return hit


def m_baseline(s):
    return s


def m_lower(s):
    ln, n = _cur(s, "rules")
    return _sub(s, "\n" + ln + "\n", "\nrules %d\n" % (n - 1))


def m_raise(s):
    ln, n = _cur(s, "census")
    return _sub(s, "\n" + ln + "\n", "\ncensus %d\n" % (n + 1))


def m_both(s):
    """E2：当前值和最后一行来历【一起】改成一致 —— 这条守卫的射程边界。"""
    ln, n = _cur(s, "rules")
    s = _sub(s, "\n" + ln + "\n", "\nrules %d\n" % (n - 1))
    p = _last_prov(s, "rules")
    return _sub(s, p, p.replace(" rules %d " % n, " rules %d " % (n - 1), 1))


def m_prov_only(s):
    """E5a：只改来历，不动当前值。"""
    _, n = _cur(s, "rules")
    p = _last_prov(s, "rules")
    return _sub(s, p, p.replace(" rules %d " % n, " rules %d " % (n - 2), 1))


def m_wipe(s):
    keep = [ln for ln in s.splitlines()
            if ln.strip().split()[:2] != ["#", "来历"]]
    return "\n".join(keep) + "\n"


def m_nonnumeric(s):
    _, n = _cur(s, "rules")
    p = _last_prov(s, "rules")
    return _sub(s, p, p.replace(" rules %d " % n, " rules abc ", 1))


def m_lookalike(s):
    return s + "#     # 来历 2026-09-08 rules 999 这一行是讲格式的，不是来历\n"


def m_orphan_key(s):
    return s + "orphan 7\n"


# ── 链检查（TestHighWaterChain）那几格 ──
#
# 现状（2026-09-09）：rules 那一列是 263 266 268 270 275 276 →【合流 276 合流 271】→ 271 → 277。
# 也就是说仓库里**真的有**一条合流记录，所以这几格改的是它。

def _merge_line(s):
    """那一行合流记录。"""
    for ln in s.splitlines():
        f = ln.strip().split()
        if len(f) >= 7 and f[0] == "#" and f[1] == "来历" and f[5] == "合流":
            return ln
    raise AssertionError("high_water.txt 里找不到合流记录 —— 这几格是冲着它去的")


def m_chain_baseline(s):
    return s


def m_chain_kill_marker(s):
    """C1：把「合流」两个字换掉 ⇒ 它退回成一条普通来历，271 那行就没人罩着了。"""
    ln = _merge_line(s)
    return _sub(s, ln, ln.replace(" 合流 ", " 说明 ", 1))


def m_chain_drop(s):
    """C2：整行删掉。"""
    ln = _merge_line(s)
    return _sub(s, ln + "\n", "")


def m_chain_bad_other(s):
    """C3：另一侧最大值写成非数字。"""
    ln = _merge_line(s)
    f = ln.split()
    return _sub(s, ln, ln.replace(" 合流 %s " % f[6], " 合流 abc ", 1))


def m_chain_new_break(s):
    """C4：末尾追加一行【比 running max 低、也低不过合流罩着的范围】的来历。

    模拟的是「又合并了一次，而没人写合流记录」。
    值取 275：低于 rules 的 running max，又高于那条合流记录的 271 ⇒ 罩不住。
    """
    return s + "# 来历 2026-09-09 rules 275 自动：假装又合了一次，而没写合流记录\n"


def m_chain_under_window(s):
    """C5：末尾追加一行【落在合流窗口里】的来历（值 ≤ 另一侧最大值）。

    期望【绿】—— 这是这条守卫写明的口子，见 CASES 下面的 NOTE。
    """
    return s + "# 来历 2026-09-09 rules 100 自动：落在合流窗口里的一行\n"


CASES = [
    ("0    基线：一个字不改",                                    m_baseline,   PASS),
    ("E1   只调低当前值（rules -1），不补来历",                    m_lower,      FAIL),
    ("E1b  只调高当前值（census +1），不补来历",                   m_raise,      FAIL),
    ("E2   当前值与最后一行来历【一起】改成一致",                   m_both,       PASS),
    ("E5a  只改来历、不动当前值",                                m_prov_only,  FAIL),
    ("E3   删光全部来历行",                                     m_wipe,       FAIL),
    ("E4   来历行的值写成非数字",                                m_nonnumeric, FAIL),
    ("E6   子串像来历、字段不像的注释",                            m_lookalike,  PASS),
    ("E7   高水位多一个没有来历的名字",                            m_orphan_key, FAIL),
    ("C0   链-基线：一个字不改",                                 m_chain_baseline,   PASS, "TestHighWaterChain"),
    ("C1   链-把合流记录的「合流」二字换掉",                       m_chain_kill_marker, FAIL, "TestHighWaterChain"),
    ("C2   链-整行删掉合流记录",                                 m_chain_drop,       FAIL, "TestHighWaterChain"),
    ("C3   链-合流记录的另一侧最大值写成非数字",                    m_chain_bad_other,  FAIL, "TestHighWaterChain"),
    ("C4   链-追加一行低于 running max、又罩不住的来历",           m_chain_new_break,  FAIL, "TestHighWaterChain"),
    ("C5   链-追加一行【落在合流窗口里】的来历",                    m_chain_under_window, PASS, "TestHighWaterChain"),
]

NOTE = {
    "E2": "⚠️ 这一格【期望绿】——它是这条守卫的射程边界，不是漏网。\n"
          "     守卫只保证「有人签了字、且签的是当前这个数」，E2 两条都满足。\n"
          "     真正看得见这一步的是【读 diff 的人】：改一行来历必然留下一个减号。\n"
          "     ⇒ 若哪天它变红，说明有人把守卫扩宽了 —— 那是好事，\n"
          "       但要连同 docs_guards_test.go 里那段射程注释一起更新，别只改这里。",
    "E6": "⚠️ 这一格【期望绿】——两边的判据都按空白切开的字段读，不用子串，\n"
          "     所以那行讲格式的注释不会被误当成来历（第二个字段是 #）。",
    "C5": "⚠️ 这一格【期望绿】——它是链检查写明的口子，不是漏网。\n"
          "     一条合流记录声明了「另一侧最高到 271」，于是此后任何 ≤ 271 的行都被放行，\n"
          "     **窗口不会关**。为什么不关：一次合并可能接进来好几行，其中一行若高过本侧，\n"
          "     关窗就会把它后面【合法的】被接行判成断链 —— 那正是「把正确的行也标红」。\n"
          "     ⇒ 代价：一条合流记录会长期许可低值行。补偿是另一条守卫：\n"
          "       TestHighWaterProvenance 仍然钉死「最后一行来历 == 当前值」，\n"
          "       所以一行落在窗口里的低值行【降不低高水位】，它只是没被解释。\n"
          "     ⇒ 若哪天它变红，说明有人把窗口收窄了 —— 连同 Go 侧那段射程注释一起改。",
}


def main():
    work = tempfile.mkdtemp(prefix="prov_ctl_")
    dst = os.path.join(work, "repo")
    try:
        shutil.copytree(ROOT, dst, ignore=ignore)
        assert not os.path.exists(os.path.join(dst, ".env")), "副本里不该有 .env"
        assert not os.path.exists(os.path.join(dst, ".git")), "副本里不该有 .git"
        hw = os.path.join(dst, "tools", "audit", "high_water.txt")
        orig = open(hw, "rb").read()

        print("副本：%s（不带 .git / .env，跑完删）" % dst)
        print()
        print("%-46s %-6s %-6s %s" % ("格", "期望", "实测", "判定"))
        print("-" * 76)
        bad = 0
        for case in CASES:
            # 第 4 个元素是【跑哪条守卫】，缺省是当前值那条。
            label, mutate, want = case[0], case[1], case[2]
            which = case[3] if len(case) > 3 else "TestHighWaterProvenance"
            # 每一格【都】先还原 —— 否则第 2 格量的是第 1 格的残骸。
            open(hw, "wb").write(orig)
            assert open(hw, "rb").read() == orig, "还原失败，停手"
            write(hw, mutate(read(hw)))
            got = guard(dst, which)
            ok = got == want
            bad += 0 if ok else 1
            print("%-46s %-6s %-6s %s" % (label, want, got, "✅" if ok else "❌ 不符"))
            key = label.split()[0]
            if key in NOTE and ok:
                print(NOTE[key])
        open(hw, "wb").write(orig)
        print()
        if bad:
            print("❌ %d 格与期望不符 —— 别急着改期望，先看是守卫变了还是变异写错了。" % bad)
            return 1
        print("✅ %d 格全部与期望相符。" % len(CASES))
        print("⚠️ 而「全对」只说明这条守卫的行为没变，**不说明它守得够宽**——")
        print("   E2 那一格就是它守不到的地方，而它是【期望绿】的。")
        return 0
    finally:
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
