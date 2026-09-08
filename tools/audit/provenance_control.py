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


def guard(work):
    """跑那条守卫，返回四态之一。"""
    v = subprocess.run(["go", "vet", "./..."], cwd=work, capture_output=True, timeout=300)
    if v.returncode != 0:
        return BUILD
    try:
        p = subprocess.run(
            ["go", "test", "-run", "TestHighWaterProvenance", "-count=1", "."],
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
]

NOTE = {
    "E2": "⚠️ 这一格【期望绿】——它是这条守卫的射程边界，不是漏网。\n"
          "     守卫只保证「有人签了字、且签的是当前这个数」，E2 两条都满足。\n"
          "     真正看得见这一步的是【读 diff 的人】：改一行来历必然留下一个减号。\n"
          "     ⇒ 若哪天它变红，说明有人把守卫扩宽了 —— 那是好事，\n"
          "       但要连同 docs_guards_test.go 里那段射程注释一起更新，别只改这里。",
    "E6": "⚠️ 这一格【期望绿】——两边的判据都按空白切开的字段读，不用子串，\n"
          "     所以那行讲格式的注释不会被误当成来历（第二个字段是 #）。",
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
        for label, mutate, want in CASES:
            # 每一格【都】先还原 —— 否则第 2 格量的是第 1 格的残骸。
            open(hw, "wb").write(orig)
            assert open(hw, "rb").read() == orig, "还原失败，停手"
            write(hw, mutate(read(hw)))
            got = guard(dst)
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
