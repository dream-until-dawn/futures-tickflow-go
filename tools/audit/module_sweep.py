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

⛔ 它会**临时改写工作区**（写坏 docs_test.go、塞探针文件）⇒ 这里在跑它**前后各取一次
【内容指纹】**并比较，**把结果印出来** —— 核的是状态，不是「调过 drill」这个动作。
一次 Ctrl-C 留下的残留会当场出声，不会被下一次提交顺手带走。
⚠️ 演练的结论**单独一段**，不混进「模块 gofmt / vet / test」那几行。

—— ⛔ 为什么比【内容指纹】，而不是比 `git status --porcelain`（评审方 2026-09-13 的突变 P1 打出来的）——

porcelain 记的是**文件的状态**（` M` / `??`），不是**文件的内容**。一个已经是 ` M` 的文件被演练改了内容，
状态还是 ` M` ⇒ 前后 porcelain **相同**。实测（b5b1612 的扔掉克隆，演练通过之后往 high_water.txt 多追加一行）：

    对照组  干净树                           ⇒ rc=1「演练前后 git status --porcelain ❌ 不同」   看得见
    实验组  high_water.txt 事先已是 M        ⇒ rc=0「演练前后 git status --porcelain 相同」       ⛔ 看不见

🔴 而**演练会写的正好是 docs_test.go 与 high_water.txt** —— 开发者刚改完载体、刚重造、还没提交的那一刻，
这两个文件**正是 M**。⇒ 那个比较**恰好在它最需要看得见的场景里看不见**。

⇒ 指纹 ＝ `git diff HEAD --binary` 的字节 ＋ 每个未跟踪文件的「路径 ＋ 字节」，一起取 sha256。
  已跟踪文件的内容变了 ⇒ diff 变了；多了未跟踪文件 ⇒ 清单变了；**两者都不需要一张手写的文件清单**
  （评审方提醒过：若只哈希「演练会写的那几个文件」，那张清单就得和 restore() 共用，否则演练多写一个就漏）。
📎 porcelain 仍然印出来 —— 给人看「是哪几个文件」；**判据用的是指纹**。

跑法：python tools/audit/module_sweep.py
退出码：全部模块 gofmt / vet / test 都过、演练过、演练前后内容指纹相同 ⇒ 0；任一不过 ⇒ 1
"""
import hashlib
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


def fingerprint():
    """工作区相对 HEAD 的【内容】指纹。回 (成功与否, 指纹十六进制, 给人看的 porcelain)。

    ⛔ 不用 porcelain 当判据：它只记状态，已是 M 的文件内容再变它看不见（见文件头 P1 那两组读数）。
    """
    d = subprocess.run(["git", "diff", "HEAD", "--binary"], cwd=ROOT, capture_output=True, timeout=600)
    u = subprocess.run(["git", "ls-files", "--others", "--exclude-standard", "-z"],
                       cwd=ROOT, capture_output=True, timeout=600)
    p = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, capture_output=True, timeout=600)
    if d.returncode != 0 or u.returncode != 0 or p.returncode != 0:
        return False, "", (d.stderr + u.stderr + p.stderr).decode("utf-8", "replace")
    h = hashlib.sha256()
    h.update(d.stdout)
    for rel in sorted(x for x in u.stdout.decode("utf-8", "replace").split("\0") if x):
        h.update(b"\0untracked\0" + rel.encode("utf-8") + b"\0")
        try:
            h.update(hashlib.sha256(open(os.path.join(ROOT, rel), "rb").read()).digest())
        except OSError as e:
            # 读不到也要进指纹 —— 否则「读不到」和「内容相同」同形
            h.update(("unreadable:%s" % e).encode("utf-8"))
    return True, h.hexdigest(), p.stdout.decode("utf-8", "replace")


def drill():
    """跑生成器的还原演练，前后比【内容指纹】。回 True ＝ 演练过 且 前后指纹相同。"""
    ok0, fp0, before = fingerprint()
    if not ok0:
        print("   ❌ 演练前取不到工作区指纹 ⇒ 演练不跑，读数作废")
        print(before.strip()[:400])
        return False
    code, out = run(ROOT, sys.executable, os.path.join(HERE, "rebuild_docs_test.py"), "--drill")
    ok1, fp1, after = fingerprint()
    same = (ok1 and fp0 == fp1)
    print("   %-34s %s" % ("演练 rebuild_docs_test.py --drill", "ok" if code == 0 else "❌ rc=%d" % code))
    print("   %-34s %s" % ("演练前后工作区内容指纹",
                            ("相同（%s）" % fp0[:12]) if same else
                            ("❌ 不同（%s → %s）" % (fp0[:12], fp1[:12] if ok1 else "取不到"))))
    if code != 0:
        print(out.strip()[-1200:])
    if not same:
        print("   演练前：\n%s" % (before.rstrip() or "（空）"))
        print("   演练后：\n%s" % (after.rstrip() or "（空）"))
        if before == after:
            print("   ⚠️ 前后 porcelain【一字不差】而内容指纹不同 ⇒ 某个【已经是 M】的文件内容被演练改了，")
            print("      状态没变所以 porcelain 看不见 —— 多半是 docs_test.go 或 high_water.txt。")
            print("      跑 `git diff` 看它们，别直接提交。")
        print("   ⇒ 演练在工作区里留下了东西（探针文件？写坏的 docs_test.go？追加过的 high_water.txt？）—— 先清掉再提交。")
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
    print("✅ %d 个模块，gofmt / vet / test 全过；生成器还原演练过，演练前后工作区内容指纹相同。" % len(mods))
    print("⚠️ 而这只说明【被枚举到的】模块都过了 ——")
    print("   判据是「目录里有 go.mod」；一个不用 go.mod 组织的东西它照样看不见。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
