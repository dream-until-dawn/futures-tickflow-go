"""生成 docs_test.go 里的规矩登记表 + 【普查数】。

## 为什么有普查数

评审方 2026-09-08 指出的结构性问题：

    一个「由模式生成」的登记表，它的覆盖面永远只能被【另一个模式】测出来。
    生成器和验证器用同一个模式 ⇒ 它对自己永远自洽。

所以除了按模式登记，再记一个**不用任何模式**的数：
**每份载体里含 `**` 的行数**。这是最宽的网——删掉任何一条规矩它都会掉，
不管那条规矩长什么样、符不符合我挑的格式。

它挡不住「原地改写」（行数不变），而登记表挡得住；
登记表挡不住「没进表的行被删」，而普查数挡得住。**两者因不同原因失效。**

## 模式修过两次，两次都是被外部发现的

v1 只认 `> **…**`（引用块）——漏 11 条独立加粗行。
v2 加了「整行加粗」，仍漏两条，评审方实测找到：

    tools/probe/README.md:81  **…`|| true` 的形状…**   ← 被我的「排除表格行」误伤
                                                          （判据写的是 `"|" in line`，
                                                           而这行的竖线在行内代码里）
    CONTRIBUTING.md:50        **写下这一句是必要的**：不写…  ← 「加粗开头 + 后续文字」

v3（本版）：表格行改判 `startswith("|")`；加粗行改成「行首加粗且有闭合」。
"""
import io, sys, re
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

FILES = ["tools/probe/README.md", "docs/README.md", "CONTRIBUTING.md",
         "tools/audit/README.md", "docs/method-landing.md"]

BQ = chr(96)
BS = chr(92)

# 针 = strip 掉标记之后的【整行】，不是前缀。
#
# 曾经取前 26 字当针，而比对又是双向 HasPrefix ⇒ 一条已登记规矩，
# 前 26 字之后的内容是自由的。评审方 2026-09-08 实证：把一条规矩的后半截
# 改成意思相反的话，两张表都点头。我自己量过面：144 条里 93 条（65%）
# 正文长于 needle，可自由改写的尾部合计 859 字。
#
# 而真正逼着它改的不是这个洞，是守卫【自己的报错文案】写着
# 「改写就把 quotedRules 里那一行一起改」——那句在宣称它管改写，而它不管。
#   ⇒ 要么把检查改成它宣称的样子，要么把宣称改成检查的样子。这里选前者。
#
# 代价：此后改任何一条规矩的措辞，都要重跑 tools/audit/rebuild_docs_test.py。
# 取舍写在 docs/method-landing.md。


def gq(x):
    if BQ not in x and BS not in x:
        return BQ + x + BQ
    return '"' + x.replace(BS, BS + BS).replace('"', BS + '"') + '"'


def is_rule(line):
    """一条【可脱离上下文引用】的规矩。判据必须与 docs_test.go 里的 ruleLines 一致。"""
    if line.startswith("|"):          # 表格行（判据是行首，不是「含竖线」）
        return False
    if line.startswith("> ") and "**" in line:
        return True
    return line.startswith("**") and line.count("**") >= 2 and len(line) > 16


HEAD = re.compile(r'^#{2,3} ')

rows, census = [], []
for f in FILES:
    src = open(f, encoding="utf-8").read()
    n_bold = 0
    # R4（评审方 2026-09-08）：登记表原来只钉「这条规矩在这份文件里」，
    # 没钉「它在哪一节下」——而**一条规矩的适用范围是它所在的小节给的**。
    # 把整行挪到另一个 ## 底下，多重集不变 ⇒ 全绿。所以连小节一起登记。
    section = "(文件开头，尚未进入任何小节)"
    for raw in src.splitlines():
        line = raw.strip()
        if HEAD.match(line):
            section = line
            # ⚠️ 这里【不能】continue：普查数的判据是「含 ** 的行」，标题也算，
            # 而 Go 那边就是这么数的。第一版在这里 continue 了，
            # 于是普查数少了 39 —— 两边的判据当场分岔，是重建时 vet 前的对比发现的。
        if "**" in line:
            n_bold += 1              # 普查：不挑格式，含 ** 就算
        if not is_rule(line):
            continue
        plain = re.sub(r'[*`>]', '', line).strip()
        if len(plain) < 12:
            continue
        rows.append((f, section, plain))   # 整行 + 所属小节；重复行照实重复
    census.append((f, n_bold))

open("quotes.gen", "w", encoding="utf-8", newline="\n").write(
    "\n".join("\t{%s, %s, %s}," % (gq(f), gq(sec), gq(n)) for f, sec, n in rows) + "\n")
# —— 下限跟表一起生成 ——
#
# 评审方 2026-09-08 量出来的：这是这套守卫里【唯一「什么都不做也会变松」】的一处。
#   quotedRules  195 条  下限 60  余量 135   ← 写下 60 那天表里就是六十几条，它没变而表长了
#   ruleAnchors   47 节  下限 20  余量  27
#   carrierCensus  5 份  下限  5  余量   0   ← 只有这个是紧的
# 别的守卫都要有人动手才会弱；下限是【随规模自动变松】的。
#
# 比例本身也生成、不手写在 Go 里——否则它就是「凭记忆递增一个计数」的又一个实例：
# 一个当初对、之后没人再算过的数。
FLOOR_RATIO = 0.9   # 评审方过：挡得住清空与大幅缩水，又容不下正常增减的误报

open("floors.gen", "w", encoding="utf-8", newline="\n").write(
    "%d %d\n" % (int(len(rows) * FLOOR_RATIO), len(census)))
open("census.gen", "w", encoding="utf-8", newline="\n").write(
    "\n".join("\t{%s, %d}," % (gq(f), n) for f, n in census) + "\n")

per = {}
for f, _, _ in rows:
    per[f] = per.get(f, 0) + 1
print("登记 %d 条规矩；普查（含 ** 的行）：" % len(rows))
for f, n in census:
    print("  %-30s 规矩 %2d / 含 ** 的行 %3d" % (f, per.get(f, 0), n))
