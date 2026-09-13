"""把【本仓所有 Go 模块】都跑一遍 —— 而不是只跑根模块。

## 为什么有它

`go test ./...` 的射程是**当前模块**。本仓有两个：

    ./go.mod                        根模块
    ./tools/probe/shinny/go.mod     天勤探针（要 coder/websocket，故意独立出去）

⇒ 而 `CONTRIBUTING.md` 的自检链写的是 `go test ./...` —— **它够不到第二个**。
实测（2026-09-09）：那个模块里有两条守卫，**从来没有被自检链跑过**：

    TestProbesDoNotImportLibrary     探针不许 import 本库
                                     （拿本库去判定本库，本库错了两边会一起错）
    TestGuardListCoversEveryProbeFile 豁免清单要覆盖每个探针文件

它们今天是绿的 —— **而「它们是绿的」和「它们在跑」是两件事**。
一条没有被任何门禁跑到的守卫，坏了不会有人知道，
**它和不存在的区别只在读者的印象里。**

## ⚠️ 它为什么枚举而不是写死两个模块

写死的话，第三个模块进来时**没有任何机械途径**提醒人把它加进来 ——
那就成了又一条「靠人记得」的规矩，而本仓这一类已经杀过十次。
⇒ **枚举 go.mod，并把找到的模块清单【打印出来】** ：
范围写在输出里，而不是写在某个人的记忆里。

## 另外一格：生成器的还原演练（`rebuild_docs_test.py --drill`）

2026-09-13 接进来（评审方判）。理由：CONTRIBUTING 说「演练的取值三就是这条路径的**常驻形态**」——
而此前**没有任何东西自动跑它**，那个「常驻」比实现宽。接进来之后那句话才成立。

⛔ 它会**临时改写工作区**（写坏 docs_test.go、塞探针文件）⇒ 这里在跑它**前后各取一次**
`git status --porcelain` 并比较，**把结果印出来** —— 核的是状态，不是「调过 drill」这个动作。
一次 Ctrl-C 留下的残留会当场出声，不会被下一次提交顺手带走。
⚠️ 演练的结论**单独一行**，不混进「模块 gofmt / vet / test」那几行。

跑法：python tools/audit/module_sweep.py
退出码：全部模块 gofmt / vet / test 都过、演练过、演练前后 porcelain 相同 ⇒ 0；任一不过 ⇒ 1
"""
import io
import os
import subprocess
import sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
SKIP_DIRS = {".git", "__pycache__", "node_modules"}


def modules():
    """枚举本仓所有 go.mod 所在目录（升序，根在最前）。"""
    out = []
    for dirpath, dirnames, filenames in os.walk(ROOT):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        if "go.mod" in filenames:
            out.append(dirpath)
    out.sort(key=lambda p: (p.count(os.sep), p))
    return out


def run(cwd, *args):
    p = subprocess.run(args, cwd=cwd, capture_output=True, timeout=600)
    return p.returncode, (p.stdout + p.stderr).decode("utf-8", "replace")


def drill():
    """跑生成器的还原演练，前后比 porcelain。回 True ＝ 演练过 且 前后相同。"""
    c0, before = run(ROOT, "git", "status", "--porcelain")
    if c0 != 0:
        print("   ❌ 演练前取不到 git status --porcelain（rc=%d）⇒ 演练不跑，读数作废" % c0)
        print(before.strip()[:400])
        return False
    code, out = run(ROOT, sys.executable, os.path.join(HERE, "rebuild_docs_test.py"), "--drill")
    c1, after = run(ROOT, "git", "status", "--porcelain")
    same = (c1 == 0 and before == after)
    print("   %-34s %s" % ("演练 rebuild_docs_test.py --drill", "ok" if code == 0 else "❌ rc=%d" % code))
    print("   %-34s %s" % ("演练前后 git status --porcelain", "相同" if same else "❌ 不同"))
    if code != 0:
        print(out.strip()[-1200:])
    if not same:
        print("   演练前：\n%s" % (before.rstrip() or "（空）"))
        print("   演练后：\n%s" % (after.rstrip() or "（空）"))
        print("   ⇒ 演练在工作区里留下了东西（探针文件？写坏的 docs_test.go？）—— 先清掉再提交。")
    return code == 0 and same


def main():
    mods = modules()
    print("本仓 Go 模块（枚举出来的，不是写死的）：")
    for m in mods:
        rel = os.path.relpath(m, ROOT).replace("\\", "/")
        print("   %s" % ("." if rel == "." else rel))
    print()

    bad = 0
    for m in mods:
        rel = os.path.relpath(m, ROOT).replace("\\", "/")
        rel = "." if rel == "." else rel
        for label, args in (("gofmt", ("gofmt", "-l", ".")),
                            ("vet", ("go", "vet", "./...")),
                            ("test", ("go", "test", "./...", "-count=1"))):
            code, out = run(m, *args)
            # gofmt 的判据是【有没有输出】，不是退出码：它对格式不对的文件也退 0。
            ok = (code == 0) if label != "gofmt" else (code == 0 and out.strip() == "")
            print("   %-28s %-5s %s" % (rel, label, "ok" if ok else "❌"))
            if not ok:
                bad += 1
                print(out.strip()[:800])
    print()
    print("生成器还原演练（单独一格，不算模块）：")
    drillOK = drill()
    print()
    if bad or not drillOK:
        if bad:
            print("❌ 模块：%d 项没过。" % bad)
        if not drillOK:
            print("❌ 演练没过（见上面「生成器还原演练」那一段）。")
        return 1
    print("✅ %d 个模块，gofmt / vet / test 全过；生成器还原演练过，演练前后 porcelain 相同。" % len(mods))
    print("⚠️ 而这只说明【被枚举到的】模块都过了 ——")
    print("   判据是「目录里有 go.mod」；一个不用 go.mod 组织的东西它照样看不见。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
