"""独立扫荡：把五份载体里【每一行含 `**` 的行】各删一次，看守卫响不响。

为什么这么做（评审方 2026-09-08）：

    一个「由模式生成」的登记表，它的覆盖面永远只能被【另一个模式】测出来。
    用生成器自己的模式测生成器，得到的一定是满分。

所以这里用的网是**最宽的**——「含 `**`」，跟生成器的 is_rule 没有关系。
输出分三类：

    响了            —— 被守住
    没响（是规矩）  —— 【漏洞】，必须处理
    没响（不是规矩）—— 行内加粗的说明文字，本来就不该守；但要**逐条看过**，
                        不能因为「没响的都不是规矩」就假定它们不是

还原用逐字节写回原始 bytes（`wb` + 原始内容），不用 replace ——
评审方在做同类实验时正是栽在这里：文件是 CRLF，replace 匹配不上，
「删掉」什么也没删，而第二次跑还是红，差点被当成「删除也被抓到」的证据。
"""
import io, os, subprocess, sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

FILES = ["tools/probe/README.md", "docs/README.md", "CONTRIBUTING.md",
         "tools/audit/README.md", "docs/method-landing.md"]
RUN = ["go", "test", ".", "-count=1",
       "-run", "TestEveryQuotedRuleIsRegistered|TestCarrierCensus|"
               "TestEveryRuleSectionHasAnAnchor|TestLandingCarriersStillCarry"]

silent = []
total = 0
for f in FILES:
    with open(f, "rb") as fh:
        raw = fh.read()
    lines = raw.decode("utf-8").split("\n")
    idx = [i for i, l in enumerate(lines) if "**" in l]
    for i in idx:
        total += 1
        cut = lines[:i] + lines[i + 1:]
        with open(f, "wb") as fh:
            fh.write("\n".join(cut).encode("utf-8"))
        r = subprocess.run(RUN, capture_output=True)   # bytes：不解码，别让编码错误混进判据
        with open(f, "wb") as fh:          # 逐字节还原，不用 replace
            fh.write(raw)
        if r.returncode == 0:
            silent.append((f, i + 1, lines[i].strip()))

# 还原完再确认一次仓库是干净的（不然后面的结论都建在一个被改坏的树上）
chk = subprocess.run(RUN, capture_output=True)

# —— 对照组 ——
# 「299 行全响」和「这个 harness 永远返回非零」在输出上长得一模一样。
# 所以拿一份【不是载体】的文件（docs/probe.md）删三行含 ** 的：它们必须【全绿】。
# 不做这一步，0 这个数就是一个看起来像证据的东西。
CONTROL = "docs/probe.md"
with open(CONTROL, "rb") as fh:
    craw = fh.read()
clines = craw.decode("utf-8").split("\n")
cgreen = 0
cidx = [i for i, l in enumerate(clines) if "**" in l][:3]
for i in cidx:
    with open(CONTROL, "wb") as fh:
        fh.write("\n".join(clines[:i] + clines[i + 1:]).encode("utf-8"))
    if subprocess.run(RUN, capture_output=True).returncode == 0:
        cgreen += 1
with open(CONTROL, "wb") as fh:
    fh.write(craw)

restored = chk.returncode == 0
control_ok = cgreen == len(cidx)

print("扫了 %d 行（含 `**` 的行，最宽的网）" % total)
print("还原后自检：%s" % ("绿" if restored else "❌ 红 —— 还原没做干净！"))
print("对照组（删 docs/probe.md 三行，非载体，应当全绿）：%d/3 绿" % cgreen)
if not control_ok:
    print("❌ 对照组不成立 —— 这个 harness 可能【根本报不出绿】，下面那个数不算数")
print("没响的：%d 行\n" % len(silent))
for f, n, l in silent:
    print("  %-26s :%-4d %s" % (f.split("/")[-1], n, l[:78]))

# —— 失败要传播 ——
# 这个脚本以前【只 print】：找到漏洞、对照组不成立、还原没做干净，退出码一律 0。
# 而我一直在把它的输出当证据引用（「0 漏、对照组 3/3」）。
# 评审方 2026-09-08 在隔壁那个脚本上指出同一件事：
#   一条规矩在 A 文件立住、在 B 文件失效，比没立更值得一提。
# 「一条被丢弃了输出的命令不能当依据」的镜像是：
#   **一条退出码没有意义的命令，同样不能放进任何链子里。**
if silent or not control_ok or not restored:
    sys.exit(1)
