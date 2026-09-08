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
    # 没钉「它在哪一节下」——而一条规矩的适用范围是它所在的小节给的。
    # 把整行挪到另一个 ## 底下，多重集不变 ⇒ 全绿。所以连小节一起登记。
    #
    # ⚠️ 而「小节」在本仓的粒度【只到 ###】——HEAD 的模式是 ^#{2,3} 。
    # 这是一个【被声明的边界】，不是疏忽：
    #
    #   一条规矩的适用范围，由【表里记得下的那一级】给定 —— 也就是 ## 与 ###。
    #   它底下的 #### 子节【不是】边界：同一个 ### 下的所有 #### 内容，
    #   在表里共用同一个 section 值。
    #
    # ⇒ 所以上面那句「适用范围是它所在的小节给的」要按这个粒度读：
    #   把一条规矩从一个 #### 挪到【同一个 ### 下的另一个 ####】，登记表【不会响】。
    #   （评审方 2026-09-08 实测：插一个 #### 子节 ⇒ 全绿；真删那一行 ⇒ 红。
    #     ⇒ 仪器是好的，这一格没有守卫。）
    #   本仓 #### 现有 40 处，其中受影响（最近标题深于表里记的小节）的规矩 102 条 / 346 条。
    #
    # ⚠️ 为什么不把它扩到 #### ：那会动 102 条的 section 字段，是一次大 diff，
    #   而它换来的是【一个此前从未咬过人的洞】——影响面实测为 0，见 gen_rule_anchors.py。
    #   ⇒ 选择把边界【声明出来】，而不是把它挪一格。
    #   ⚠️ 这个 ^#{2,3} 在本仓有【四个站点】（本文件、gen_rule_anchors.py、
    #     section_move_check.py、以及 rebuild_docs_test.py 里那份生成 docs_test.go 的模板）。
    #     改粒度要四处一起改 —— 而 section_move_check.py 那一处尤其要紧：
    #     **验证这条机制的脚本和这条机制共用同一个定义，所以它永远发现不了这个定义太粗。**
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
open("census.gen", "w", encoding="utf-8", newline="\n").write(
    "\n".join("\t{%s, %d}," % (gq(f), n) for f, n in census) + "\n")

per = {}
for f, _, _ in rows:
    per[f] = per.get(f, 0) + 1
print("登记 %d 条规矩；普查（含 ** 的行）：" % len(rows))
for f, n in census:
    print("  %-30s 规矩 %2d / 含 ** 的行 %3d" % (f, per.get(f, 0), n))
