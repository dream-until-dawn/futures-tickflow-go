"""片一那 10 条不变量的【对照组】——把实现弄坏，看该红的那一条红不红。

## 为什么需要它

`store/segfile/meta_test.go` 里 20 个测试全绿。**而全绿什么也不证明**：
一个把判据整段删掉的实现，只要测试没在看那一格，照样全绿。

    焊了对照组 ≠ 对照组在承重。**得拆一次，看它塌。**

本脚本对每一条不变量做一次【定点突变】：把实现里守那一条的地方改坏，
然后跑那一条自己的 _Red / _Green，看**预期该失败的那个测试真的失败了没有**。

## 判据

    每一行都写明【预期哪个测试失败】。
    突变后那个测试【必须】失败 —— 没失败，说明它根本没在守这一条。
    这比「测试通过」强，因为它排除了「测试在看别处」。

⚠️ 而它不保证的：突变是我挑的，**它只覆盖我想得到的坏法**。
一条不变量可能有别的坏法而这里没试到 —— **这是它的射程，不是它的缺陷。**

## 它自己造副本，不碰工作区

复制仓库到系统临时目录（不带 `.git`、不带 `.env`），在那儿动手，跑完删。
⇒ 跑它不需要先清工作区，也不会弄脏别人的 `git status`。

## 四态

    BUILD  编译不过 ⇒ 这一格什么都没测到，【不算】变红
    TIME   超时     ⇒ 同上
    FAIL   编译过了，测试红了
    PASS   编译过了，测试绿了

跑法：python tools/audit/invariant_control.py
退出码：每一格都与预期相符 ⇒ 0；有任何一格不符 ⇒ 1
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

META = os.path.join("store", "segfile", "meta.go")
STORE = "store.go"


def ignore(_d, names):
    return [n for n in names if n in SKIP or n.endswith(".exe")]


def sub(work, rel, old, new):
    """定点替换。命中不了就当场报错——不会静静地测了个寂寞。"""
    p = os.path.join(work, rel)
    s = open(p, encoding="utf-8").read()
    if s.count(old) != 1:
        raise AssertionError("突变锚点在 %s 里命中 %d 次：%r" % (rel, s.count(old), old[:48]))
    open(p, "w", encoding="utf-8", newline="\n").write(s.replace(old, new, 1))


# ── 九次定点突变。每一条都写明【预期哪个测试失败】。──
#
# ⚠️ F1 那一格预期失败的是 _Green 而不是 _Red：
#    F1 的绊线【就是】_Green（真实字段集必须等于声明），
#    而 _Red 测的是绊线这件仪器本身好不好使。**突变要打在绊线上，不是打在仪器自检上。**

MUTATIONS = [
    ("A1a", "TestInvariantA1a_Red", "把「升序」那一条判据删掉", lambda w: sub(
        w, META,
        """		if s.From < prev.From {
			return fmt.Errorf("%w: coverage[%d].From=%d 小于 coverage[%d].From=%d",
				errUnordered, i, s.From, i-1, prev.From)
		}
""", "")),

    ("A1b", "TestInvariantA1b_Red", "把「不重叠」那一条判据删掉", lambda w: sub(
        w, META,
        """		if s.From <= prev.To {
			return fmt.Errorf("%w: coverage[%d] 从 %d 起，而 coverage[%d] 到 %d 为止",
				errOverlap, i, s.From, i-1, prev.To)
		}
""", "")),

    ("A1c", "TestInvariantA1c_Red", "让「端点不是交易日」放行", lambda w: sub(
        w, META,
        """				if errors.Is(err, tickflow.ErrNotTradingDay) {
					return fmt.Errorf("%w: coverage[%d].%s = %s 不是交易日",
						errEndpointNotTradingDay, i, ep.name, ep.day)
				}""",
        """				if errors.Is(err, tickflow.ErrNotTradingDay) {
					continue // 人为弄坏：不是交易日也放行
				}""")),

    ("A2", "TestInvariantA2_Red", "把相邻性改成【自然日】：要求 b == a+1", lambda w: sub(
        w, META,
        "	if b <= a {\n		return false, nil\n	}\n	n := 0",
        "	if b <= a {\n		return false, nil\n	}\n"
        "	if true {\n		return b == a+1, nil // 人为弄坏：按自然日判，不问日历\n	}\n	n := 0")),

    ("A3", "TestInvariantA3_Red", "让写下来的 0 也算合法版本", lambda w: sub(
        w, META,
        "		case *m.Format != FormatVersion:", "		case *m.Format != FormatVersion && *m.Format != 0:")),

    ("D1", "TestInvariantD1_Red", "让两个哨兵变成同一个值", lambda w: sub(
        w, STORE,
        '	ErrLegacyMeta = errors.New("tickflow/store: meta 版本未知且源不可重放——需要一个显式决定")',
        '	ErrLegacyMeta = ErrSpanUnverified // 人为弄坏：两个「答不了」合并成一个')),

    ("D2a", "TestInvariantD2a_Red", "把判定写成常数：一律作废重拉", lambda w: sub(
        w, META,
        "	if replayable {\n		return LegacyDiscard, nil\n	}\n	return LegacyUnverified, tickflow.ErrLegacyMeta",
        "	return LegacyDiscard, nil // ← 人为弄坏：常数判定")),

    ("E1a", "TestInvariantE1a_Red", "解析失败时返回一个空 Meta 顶上", lambda w: sub(
        w, META,
        '		return nil, fmt.Errorf("%w: %v", errMetaUnreadable, err)',
        "		return &Meta{}, nil // ← 人为弄坏：拿默认值顶上")),

    ("E1b", "TestInvariantE1b_Red", "把「超前版本」那一条判据删掉", lambda w: sub(
        w, META,
        """		case *m.Format > FormatVersion:
			return nil, fmt.Errorf("%w: format=%d，本版只认到 %d", errFutureFormat, *m.Format, FormatVersion)
""", "")),

    ("F1", "TestInvariantF1_Green", "往 Meta 里加一个日历派生字段", lambda w: sub(
        w, META,
        "	Coverage []tickflow.Span `json:\"coverage\"`",
        "	Coverage []tickflow.Span `json:\"coverage\"`\n\n	IsTradingDay bool `json:\"is_trading_day\"` // ← 人为弄坏：把日历的答案抄进来")),
]


def run_one(work, name):
    v = subprocess.run(["go", "vet", "./..."], cwd=work, capture_output=True, timeout=300)
    if v.returncode != 0:
        return BUILD
    try:
        p = subprocess.run(["go", "test", "-run", "^" + name + "$", "-count=1", "./store/segfile/"],
                           cwd=work, capture_output=True, timeout=300)
    except subprocess.TimeoutExpired:
        return TIME
    out = (p.stdout + p.stderr).decode("utf-8", "replace")
    if "[build failed]" in out or "no tests to run" in out:
        return BUILD
    return PASS if p.returncode == 0 else FAIL


def main():
    tmp = tempfile.mkdtemp(prefix="inv_ctl_")
    dst = os.path.join(tmp, "repo")
    try:
        shutil.copytree(ROOT, dst, ignore=ignore)
        assert not os.path.exists(os.path.join(dst, ".env")), "副本里不该有 .env"
        assert not os.path.exists(os.path.join(dst, ".git")), "副本里不该有 .git"
        keep = {rel: open(os.path.join(dst, rel), "rb").read() for rel in (META, STORE)}

        print("副本：%s（不带 .git / .env，跑完删）" % dst)
        print()
        print("基线：不做任何突变，20 个测试应当全绿")
        base = subprocess.run(["go", "test", "-count=1", "./store/segfile/"],
                              cwd=dst, capture_output=True, timeout=300)
        if base.returncode != 0:
            print((base.stdout + base.stderr).decode("utf-8", "replace")[:800])
            print("❌ 基线就不绿，后面每一格都没有意义 —— 停手。")
            return 1
        print("  ✅ 基线全绿")
        print()
        print("%-5s %-26s %-24s %-6s %s" % ("编号", "突变", "预期失败的测试", "实测", "判定"))
        print("-" * 92)
        bad = 0
        for nid, target, desc, mutate in MUTATIONS:
            for rel, b in keep.items():          # 每一格都先还原
                open(os.path.join(dst, rel), "wb").write(b)
            mutate(dst)
            got = run_one(dst, target)
            ok = got == FAIL                     # 弄坏了，那个测试就该红
            bad += 0 if ok else 1
            print("%-5s %-26s %-24s %-6s %s" % (nid, desc, target, got,
                                                "✅ 塌了" if ok else "❌ 没塌"))
            if not ok:
                print("      ↳ 弄坏了这一条，而 %s 仍然 %s —— 它没有在守这一条。" % (target, got))
        for rel, b in keep.items():
            open(os.path.join(dst, rel), "wb").write(b)
        print()
        if bad:
            print("❌ %d 格没塌。**没塌的那几条，等于没有对照组。**" % bad)
            return 1
        print("✅ %d 格全部塌了：每一条不变量都有一个测试在真的守它。" % len(MUTATIONS))
        print("⚠️ 而这只说明【我想到的那种坏法】会被抓住 ——")
        print("   突变是我挑的，一条不变量可能有别的坏法没试到。**这是射程，不是缺陷。**")
        return 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
