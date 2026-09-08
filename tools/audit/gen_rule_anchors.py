import io, sys, re
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

FILES = ["tools/probe/README.md", "docs/README.md", "CONTRIBUTING.md",
         "tools/audit/README.md", "docs/method-landing.md"]

EXEMPT = {
    ("tools/probe/README.md", "## 跑"): "只是跑法，不是规矩",
    ("tools/probe/README.md", "## 写一条新探针之前，这八条"): "小节标题，规矩在它下面各条里",
    ("docs/README.md", "## 改文档之前，这八条"): "小节标题，规矩在它下面各条里",
    ("tools/audit/README.md", "## 现有脚本"): "目录清单，随脚本增减",
    ("docs/method-landing.md", "## 一、落点表"): "表本身，由 TestDocLinksResolve 顶着",
    ("docs/method-landing.md", "## 二、落不进去的（第③类）——**这是合法结果，不是遗漏**"): "第三类清单，内容随审计变",
    ("docs/method-landing.md", "## 三、这次审计**审到哪一步为止、按什么来源审的**"): "一次性的来源声明",
    ("docs/method-landing.md", "## 四、这张表自己会怎么坏"): "自曝清单，内容随守卫增减",
    ("CONTRIBUTING.md", "## 自检（送审前跑，输出贴进第 3 样材料）"): "命令清单，不是规矩",
}

MANUAL = {
    ("tools/probe/README.md", "### 3. 对照组还要问第三个问题：**它在你最需要它的那一天还在不在**"):
        "GFEX 有日盘，白天跑会走进",
    ("docs/README.md", "### 2. **一条只存在于对话里的规则，等于没有这条规则**"):
        "因为它感觉上最像「已经有了」",
    ("CONTRIBUTING.md", "## 八、没问题的时候要**明说没问题**"):
        "规则就失去信息量",
}

BQ = chr(96)   # 反引号
BS = chr(92)   # 反斜杠


def gq(x):
    """Go 字面量：优先反引号原始串；含反引号或反斜杠时退回双引号。"""
    if BQ not in x and BS not in x:
        return BQ + x + BQ
    return '"' + x.replace(BS, BS + BS).replace('"', BS + '"') + '"'


# ⚠️ 小节粒度只到 ###（与 gen_quoted_rules.py 同一个 ^#{2,3}，那里写了为什么）。
# 对锚点而言这有一个具体后果，写在这儿：
#
#   一个小节的「正文」= 从它的标题到【下一个 ## 或 ###】之间的【全部内容】，
#   其中【包含它底下所有 #### 子节】。⇒ 锚点可能取自一个 #### 子节里的句子。
#
# ⚠️ 实测（2026-09-08，评审方提出，我独立复现）：54 个 ##/### 小节里，
#   锚点【借自 #### 子节】的 = 0 个。
#   **但那是运气，不是设计**——写一个 ### 小节、让它的第一段内容就是一个 ####
#   子节，它的锚点就会被静默地借过去，而本脚本读的这五个载体里【#### 及更深】
#   已有 25 处（#### 22 ／ ##### 2 ／ ###### 1；2026-09-08 实测，
#   量法：五个载体里行首连续 4 个及以上 # 的行数；同样没有守卫）。
#   ⚠️ 这里要数的是【#### 及更深】而不是恰好 ####：##### 底下的第一段内容
#     同样会被借走锚点——**风险的判据是「比表里记得下的那级深」，不是「正好深一级」。**
#   ⚠️ 这里原先写的是「本仓 40 处」—— 那是【全仓九份 .md】的数，
#     而本脚本只读上面 FILES 里这五份。**一个范围比结论宽的数，会把风险报大，
#     而它看起来和量对了的数一模一样。**
head = re.compile(r'^(#{2,3}) (.+)$', re.M)
rows = []
for f in FILES:
    s = open(f, encoding="utf-8").read()
    ms = list(head.finditer(s))
    for i, m in enumerate(ms):
        title = m.group(0)
        body = s[m.end(): ms[i + 1].start() if i + 1 < len(ms) else len(s)]
        key = (f, title)
        if key in EXEMPT:
            rows.append((f, title, "", EXEMPT[key]))
            continue
        if key in MANUAL:
            a = MANUAL[key]
            assert a in body, "手填锚点不在正文里：" + title
            rows.append((f, title, a, ""))
            continue
        cands = [b for b in re.findall(r'\*\*(.+?)\*\*', body, re.S)
                 if 8 <= len(b) <= 70 and "\n" not in b]
        assert cands, "没有锚点候选：" + f + " " + title
        rows.append((f, title, cands[0], ""))

out = []
for f, t, n, e in rows:
    out.append("\t{%s, %s, %s, %s}," % (gq(f), gq(t), gq(n), gq(e)))
open("rows.gen", "w", encoding="utf-8", newline="\n").write("\n".join(out) + "\n")
print("生成 %d 行；其中豁免 %d 条" % (len(rows), sum(1 for r in rows if r[3])))
