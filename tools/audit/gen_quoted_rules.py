"""把五份载体里【每一条引用块规矩】抠出来，生成登记表。

为什么是 `> ` 开头且含 `**` 的行：这是本仓写「一条可脱离上下文引用的规矩」时
一直在用的格式。它不是我挑的，是数出来的——所以这张表的覆盖面有个
【机械定义】，而不是「我记得锚住了哪些」。
"""
import io, sys, re
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

FILES = ["tools/probe/README.md", "docs/README.md", "CONTRIBUTING.md",
         "tools/audit/README.md", "docs/method-landing.md"]

BQ = chr(96)
BS = chr(92)


def gq(x):
    if BQ not in x and BS not in x:
        return BQ + x + BQ
    return '"' + x.replace(BS, BS + BS).replace('"', BS + '"') + '"'


# 取每行的前 N 个字符当针：删掉整行 → 针没了；改写开头 → 针没了（该改登记表）。
# 用前缀而不是整行，是为了让登记表读得下去；用【可读前缀】而不是哈希，
# 是因为断言失败时要能看出它当时在守什么。
N = 26

rows, seen = [], set()
for f in FILES:
    src = open(f, encoding="utf-8").read()
    for raw in src.splitlines():
        line = raw.strip()
        # 两种格式都算「一条可脱离上下文引用的规矩」——这是数出来的，不是挑的：
        #   ① `> ` 引用块里的加粗行   ② 独立成行的加粗句
        quoted = line.startswith("> ") and "**" in line
        standalone = (line.startswith("**") and line.endswith("**")
                      and line.count("**") == 2 and "|" not in line and len(line) > 16)
        if not (quoted or standalone):
            continue
        # 去掉 markdown 记号，留下人读的那句
        plain = re.sub(r'[*`>]', '', line).strip()
        if len(plain) < 12:
            continue
        needle = plain[:N]
        key = (f, needle)
        if key in seen:            # 同一文件里前缀撞车：加长到能区分为止
            for k in range(N + 4, len(plain) + 1, 4):
                needle = plain[:k]
                if (f, needle) not in seen:
                    break
            key = (f, needle)
        seen.add(key)
        rows.append((f, needle))

out = ["\t{%s, %s}," % (gq(f), gq(n)) for f, n in rows]
open("quotes.gen", "w", encoding="utf-8", newline="\n").write("\n".join(out) + "\n")

per = {}
for f, _ in rows:
    per[f] = per.get(f, 0) + 1
print("共 %d 条引用块规矩：" % len(rows))
for f in FILES:
    print("  %-30s %d" % (f, per.get(f, 0)))
