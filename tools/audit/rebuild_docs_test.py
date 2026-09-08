#!/usr/bin/env python3
"""重建 docs_test.go 里那三张【生成的】表，一步到位。

## 为什么有这个脚本

原来这件事是三个脚本用 `;` 和 `>/dev/null` 手工串起来的。
2026-09-08 它把 docs_test.go 弄坏了：**截断那一步报 `substring not found`，
而后面几步照跑**，于是往一个残缺的文件上又接了两张表，`package` 都没了。

这正是本仓自己那条：

    一条被丢弃了输出的命令，不能拿来当「已完成」的依据。

而更准确的形状是：**用 `;` 串起来的流水线，前一步失败不会拦住后一步**，
于是「后面几步跑完了」被当成了「整条跑成了」。

## 这个脚本做的四件事，缺一不可

1. 每一步都断言，失败就抛，不往下走；
2. 动手前把原文件**整份备份在内存里**；
3. 写完立刻跑 `gofmt` + `go vet`；
4. **只要任何一步失败，就把原文件逐字节写回去**——不留一个半成品在盘上。
"""
import io
import os
import re
import subprocess
import sys

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
TARGET = os.path.join(ROOT, "docs_test.go")
# MARK 常量已删：拆分之后不再有截断点。
# 留着一个没有作用的常量，只会让下一个人以为它还在守什么。


def run(*cmd):
    r = subprocess.run(cmd, cwd=ROOT, capture_output=True)
    return r.returncode, (r.stdout + r.stderr).decode("utf-8", "replace")


HIGH_WATER = os.path.join(HERE, "high_water.txt")


def readHighWater():
    """读高水位。返回 (值字典, 原始行) —— 保留原始行是为了回写时不动注释。"""
    lines = open(HIGH_WATER, encoding="utf-8").read().splitlines()
    hw = {}
    for ln in lines:
        t = ln.strip()
        if not t or t.startswith("#"):
            continue
        parts = t.split()
        assert len(parts) == 2, "high_water.txt 这一行读不懂：%r" % ln
        hw[parts[0]] = int(parts[1])
    assert hw, "high_water.txt 里一个数都没有 —— 那等于没有下限"
    return hw, lines


def writeHighWater(lines, hw):
    """只改数值行，注释与顺序原样保留。"""
    out = []
    for ln in lines:
        t = ln.strip()
        if t and not t.startswith("#"):
            k = t.split()[0]
            if k in hw:
                out.append("%s %d" % (k, hw[k]))
                continue
        out.append(ln)
    open(HIGH_WATER, "w", encoding="utf-8", newline="\n").write("\n".join(out) + "\n")


def restore(original):
    """把盘上恢复成动手之前的样子，返回一句话说明【走的是哪一条路径】。

    ⚠️ 这是【唯一】一份还原实现：main() 的失败路径和 drill() 都调它。

    为什么必须只有一份（评审方 2026-09-08 定的性，我复现过）：
    上一版 drill() 把主流程那一行【抄】了一份，注释还写着「这就是失败分支做的那一件事」——
    **一段注释说「这就是那一件事」，就是在承认这里有第二份拷贝。**
    后果：主流程为了让「删掉能不能造回来」那条判据跑得起来，加了 `original is None` 分支，
    而演练到不了那儿——`rm docs_test.go && rebuild --drill` 直接 FileNotFoundError。
    **演练认证的射程缩了一半，而它的输出一个字没变。**

    ⇒ 处置不是补那一个洞，是让「演练与主流程漂移」在结构上不成立：
      还原只有一份，新分支加在哪儿，演练都在它里面。
      这是本仓杀「第二份拷贝」的第四次（前三次：.gen 删掉、半生成的 docs_test.go 拆开、
      派生出来的下限换成高水位）。

    返回值本身也是那条规矩的一部分：**输出要写明走了哪一条**，
    否则下一次射程变了，还是只能靠人记得去问。
    """
    if original is None:
        # 这次跑之前文件【本来就不存在】。那时「还原」= 把它删掉，
        # 而不是写一个 None 进去——否则失败路径自己会崩，
        # 而崩在还原步骤上，等于盘上留着半成品。
        if os.path.exists(TARGET):
            os.remove(TARGET)
        return "把 docs_test.go 删掉了（它本来就不存在）"
    open(TARGET, "wb").write(original)
    return "把 docs_test.go 逐字节还原了"


def drill():
    """演练还原路径，**把 restore() 入参的取值空间走遍**，并写明每次走的是哪一条。

    评审方 2026-09-08：**备份要验，否则备份本身是第三种静默失败。**
    「任何一步失败就逐字节还原」这条路径**写下来了，但从没被执行过**——
    而一条没被执行过的路径，和没有这条路径的区别只在读者的印象里。

    ⚠️ 为什么按【入参的取值空间】枚举，而不是按一张「分支清单」：
    手写清单会过期，本仓刚在 must 上验过一次（3 个名字对 5 个守卫）。
    而 `restore(original)` 的入参只有两种取值形状——`None` 与 `bytes`——
    **两种都走，就是走遍了它的定义域**，不是走遍了某人记得的那几条。
    这是本仓那条「先数这个值要区分几种情况」用在【参数】上。

    ⚠️ 而这一格仍然比不了的：如果将来 restore() 的行为不再只由 `original` 决定
    （比如引进第二个参数、或依赖某个全局状态），这个枚举就不再是定义域了。
    **写在这儿，是因为它比不了——不是因为它不重要。**

    跑法：python tools/audit/rebuild_docs_test.py --drill
    """
    # 演练需要一份基线。文件不在【不是崩的理由，是一个前提】——
    # 上一版在这儿直接 FileNotFoundError，而那看起来像工具坏了，不像「你还没造它」。
    if not os.path.exists(TARGET):
        print("❌ 演练需要 docs_test.go 先存在（它是基线）。"
              "先跑一次不带 --drill 的重建，再来演练。")
        sys.exit(1)
    start = open(TARGET, "rb").read()               # 演练结束时必须回到这一份

    # —— 取值一：original 是 bytes（文件在，被写坏）——
    open(TARGET, "wb").write("// 故意写坏，看还原路径把不把它救回来\n".encode("utf-8"))
    broken = open(TARGET, "rb").read()
    assert broken != start, "演练本身没生效——文件没被改坏，下面的结论不算数"
    what1 = restore(start)
    if open(TARGET, "rb").read() != start:
        # ⚠️ 收尾【不走 restore】：它刚被判定是坏的。
        # 一个实验的收尾如果依赖它刚证明为坏的那个东西，收尾本身就不可信。
        # 实测过后果：上一稿在这里直接 sys.exit(1)，盘上留着被截断的 docs_test.go。
        open(TARGET, "wb").write(start)
        print("❌ 还原路径没把文件恢复成原样 —— 备份机制本身是坏的")
        print("   （盘上已由演练【绕过 restore】直接写回，不是靠它自己）")
        sys.exit(1)
    print("  取值一 original=bytes：写坏 %d 字节 → %s → 逐字节相同" % (len(broken), what1))

    # —— 取值二：original 是 None（文件本来就不存在）——
    # 这一支是「删掉它能不能一模一样地造回来」那条判据走的路。
    # 上一版演练【到不了这儿】：它第一行就无条件 open()，文件不在就崩在那儿。
    # ⚠️ 真实场景不是「文件不在、什么也没发生」，而是：
    #   文件本来不存在 → 生成器写了一份出来 → 后面某步失败 → 还原要把这份【新写的】删掉。
    # 所以这里必须先把它造出来，restore(None) 才有活干。
    #
    # 这一格是【对照组抓出来的】：上一稿写成「先 os.remove 再 restore(None)」，
    # 于是 None 分支整个包在 `if os.path.exists` 里根本没执行——
    # 把那一支改成「写个空文件」而不是删掉，演练照样绿、退 0。
    # **一个不执行被测代码的演练，和没有这个演练一样。**
    os.remove(TARGET)
    open(TARGET, "wb").write(b"// pretend the generator just wrote this\n")
    what2 = restore(None)
    if os.path.exists(TARGET):
        # ⚠️ 与取值一同样的收尾：绕过 restore 直接写回。
        # 上一稿只给取值一加了这条，取值二没加 —— 于是它红是红了，
        # 却把 docs_test.go 留成一个空文件，污染了紧跟着跑的下一个实验。
        # **一个洞被修一半，往往不是疏忽，是「修」这个动作本身让人觉得那一格已经处理过了。**
        open(TARGET, "wb").write(start)
        print("❌ original=None 时还原路径没把生成器写出来的那一份删掉")
        print("   （盘上已由演练【绕过 restore】直接写回，不是靠它自己）")
        sys.exit(1)
    print("  取值二 original=None ：生成器写了一份 → %s → 盘上确实没有它" % what2)

    open(TARGET, "wb").write(start)                 # 收掉演练自己的痕迹
    assert open(TARGET, "rb").read() == start, "演练没把盘上还原成开跑前那一份"

    code, out = run("go", "vet", "./...")
    if code != 0:
        print("❌ 演练之后 vet 不过：\n%s" % out)
        sys.exit(1)
    print("演练通过：restore() 的两种入参取值各走了一次，盘上回到开跑前那一份，vet 过。")


def main():
    # ② 先备份。文件可能不存在——**那正是「删掉能不能造回来」那条判据要的情形**，
    #    所以这里不能假设它在。
    original = open(TARGET, "rb").read() if os.path.exists(TARGET) else None

    # ⚠️ 这里【不再】从旧文件里截 prefix。
    #
    # 原来是 prefix = src[:src.index(MARK)] —— 把旧文件的前 9212 字节【照抄】回去，
    # 于是这个文件只有一半是生成的。后果（评审方 2026-09-08 实测，我复现过）：
    # **一个自足的守卫可以被静默删掉，而 rebuild 退 0、gofmt 干净、vet 过、测试全绿。**
    #
    # 那时还有一张 must 清单守着「prefix 里必须有这几个名字」——
    # **而清单已经落后两个**（TestNoOrphanedSentences /
    # TestNoDuplicateHeadingsInCarriers 都不在里面）。
    # 今天顶着的其实是版面：commentOutsideCode 恰好排在守卫之后、被幸存的守卫调用，
    # 于是丢守卫的截断会顺手弄坏编译。**版面撑不到下一个自足的守卫。**
    #
    # ⇒ 手写守卫已挪进 docs_guards_test.go；这个文件从第一行到最后一行都是生成的。
    #   那张 must 清单随 prefix 一起消失——**别把一个已经失效的清单留在那儿。**
    prefix = TPL_HEAD

    for gen in ("gen_rule_anchors.py", "gen_quoted_rules.py"):
        code, out = run(sys.executable, os.path.join("tools", "audit", gen))
        assert code == 0, "%s 失败：\n%s" % (gen, out)
    rows = open(os.path.join(ROOT, "rows.gen"), encoding="utf-8").read().rstrip("\n")
    quotes = open(os.path.join(ROOT, "quotes.gen"), encoding="utf-8").read().rstrip("\n")
    census = open(os.path.join(ROOT, "census.gen"), encoding="utf-8").read().rstrip("\n")
    for name, blob in (("rows", rows), ("quotes", quotes), ("census", census)):
        assert blob.strip(), "%s.gen 是空的 —— 生成器没产出东西，拒绝写空表" % name

    # —— 下限 = 高水位，不是「当前条数 × 比例」——
    #
    # 上一版是 int(当前条数 × 0.9)，而评审方 2026-09-08 收回了他自己那条建议并证明了它更糟：
    # 从【被守的量】派生出来的阈值在生成那一刻恒真，只在两次生成之间有力气，
    # 而正常流程（改 .md → 重生成）从不留下那个窗口。
    # 实测：删掉一整节规矩 + 重生成 → 全绿，下限跟着掉。
    #
    # ⇒ 高水位只涨不落；要合法缩小就得动手改 tools/audit/high_water.txt。
    #   理由与全仓一致：**删一条规矩应该是显式动作。**
    # —— 守卫名字表 —— 登记名字，不登记数字（理由写在 TPL_GUARDS 的注释里）。
    #
    # 名字从【静态模板】+ 手写守卫文件里抽，不从盘上那份 docs_test.go 抽：
    # 从盘上抽会让「上一次生成漏了谁」原样传下去 —— 那是「第二份拷贝」的又一种形态。
    staticTpl = (TPL_HEAD + TPL_ANCHORS + TPL_MID_A + TPL_QUOTED
                 + TPL_MID_B + TPL_TAIL + TPL_GUARDS)
    guardsSrc = staticTpl + open(os.path.join(ROOT, "docs_guards_test.go"),
                                 encoding="utf-8").read()
    names = sorted(set(re.findall(r"(?m)^func (Test[A-Za-z0-9_]*)\(", guardsSrc)))
    assert names, "一个守卫名都没抽到 —— 拒绝写空表"
    guardTable = "\n".join("\t`%s`," % n for n in names)

    counts = {"rules": quotes.count("\n") + 1,
              "anchors": rows.count("\n") + 1,
              "census": census.count("\n") + 1,
              "guards": len(names)}
    hw, hwLines = readHighWater()
    grew = []
    for k, n in counts.items():
        assert k in hw, "high_water.txt 缺一项：%s" % k
        if n > hw[k]:
            grew.append("%s %d→%d" % (k, hw[k], n))
            hw[k] = n
    if grew:
        writeHighWater(hwLines, hw)
        print("高水位抬高：%s（表长大了，这是自动的）" % "，".join(grew))
    # 下限本身不再写进生成物，但生成器仍然要为它把关：
    # 一个 0 或负数的下限等于没有下限，而它会一路安静地生效。
    for k in ("anchors", "rules", "census", "guards"):
        assert hw[k] > 0, "high_water.txt 里 %s 是 %d —— 下限是 0 或负数" % (k, hw[k])

    body = (prefix + TPL_ANCHORS + rows + TPL_MID_A + TPL_QUOTED + quotes
            + TPL_MID_B + census + TPL_TAIL + TPL_GUARDS)
    # ⚠️ 四个下限【不再】烘进生成物 —— 测试自己 go:embed 读 high_water.txt。
    #    生成器仍然要读它（为了抬高水位），于是两边成了「两个读者读一份权威」，
    #    而不是「两份拷贝」。理由与它的代价写在 TPL_HEAD 里。
    body = body.replace("__GUARD_NAMES__", guardTable)
    for ph in ("__ANCHOR_FLOOR__", "__QUOTED_FLOOR__", "__CENSUS_FLOOR__",
               "__GUARDS_FLOOR__", "__GUARD_NAMES__"):
        assert ph not in body, "占位符 %s 没被换掉 —— 那会写出一个编译不过的文件" % ph
    open(TARGET, "w", encoding="utf-8", newline="\n").write(body)

    ok = True
    for cmd in (("gofmt", "-w", "docs_test.go"), ("go", "vet", "./...")):
        code, out = run(*cmd)
        if code != 0:
            print("❌ %s 失败：\n%s" % (" ".join(cmd), out))
            ok = False
            break

    # ⚠️ 清理【中间产物】必须挂在两条路径上，不能只挂在成功那条。
    #
    # 原来 os.remove 在 sys.exit(1) 之后，于是失败时 rows/quotes/census.gen 三个都留在盘上，
    # 而脚本正打印着「盘上没有留半成品」——目标文件还原对了，副作用没还原，
    # 而那句话把两者说成了一件事（评审方 2026-09-08 造了一个 vet 失败量出来的）。
    #
    # 这就是「自述比实现宽」，这次在【失败路径】上，也就是没人会去看的地方。
    for f in ("rows.gen", "quotes.gen", "census.gen"):
        p = os.path.join(ROOT, f)
        if os.path.exists(p):
            os.remove(p)

    if not ok:                                     # ④ 任何一步失败就整份还原
        # 还原只有【一份实现】，drill() 调的是同一个 restore()。
        # 上一版这里是主流程自己写一遍、drill() 抄一遍 —— 于是这里加了
        # `original is None` 分支之后，演练到不了那儿而输出照旧说「演练通过」。
        what = restore(original)
        leftovers = [f for f in ("rows.gen", "quotes.gen", "census.gen")
                     if os.path.exists(os.path.join(ROOT, f))]
        assert not leftovers, "中间产物没清干净：%s" % leftovers
        # ⚠️ 这句必须分两种说：文件本来就不存在时，做的是【删掉】不是【还原】。
        # 上一版两种情况共用「逐字节还原」——而那在第二种情况下是一句假话，
        # 正是本仓一直在拆的「自述比实现宽」，这次在失败路径上（没人会去看的地方）。
        print("已%s，中间产物也清了 —— 盘上没有留半成品" % what)
        sys.exit(1)

    print("docs_test.go 重建完成：锚点 %d 节 / 规矩 %d 条 / 普查 %d 份 / 守卫 %d 个；"
          "gofmt 与 vet 均过"
          % (rows.count("\n") + 1, quotes.count("\n") + 1, census.count("\n") + 1,
             len(names)))


TPL_HEAD = '''package tickflow

// 这一份【整份都是生成的】：tools/audit/rebuild_docs_test.py 造它。
//
// 别手改这里的任何一行 —— 下一次重建会把改动冲掉，而且不会有任何提示。
// 手写的守卫在 docs_guards_test.go。
//
// 「整份都是生成的」是可验的，不靠人读代码确认：
//
//	rm docs_test.go && python tools/audit/rebuild_docs_test.py
//	⇒ 它应当【逐字节一模一样地】回来
//
// 拆分之前这条判据是不成立的：那时删掉它 → FileNotFoundError，造不回来，
// 因为重建脚本要读旧文件去截前半段。**一个需要自己才能造出自己的文件，
// 只有一半是生成的。**

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// —— 下限从【文件】读，不再烘进这个文件 ——
//
// 起因（评审方 2026-09-08 实测，我复现过）：下限原来是生成器烘进来的字面量，
// 于是手改这里的 49 / 228 而不动 tools/audit/high_water.txt ⇒ **全绿**。
// **那是一份没有任何检查的第二拷贝**，而本仓杀过四次同形的东西。
//
// ⚠️ 我一度反对这个改法，理由是「Go 侧的解析会成为 readHighWater 的第二份实现」。
// 评审方驳掉了，而他是对的——**要分清被复制的是什么**：
//
//	两份【拷贝】   两个【值】静默分叉，两边各自都自洽 —— 没有共同真源
//	两个【读者】   读同一份权威，失效方式是对同一份字节【解释】不同 —— 有共同真源
//
// 本仓 carrierCensus 就是后者（Python 数 `**` 行写进表，Go 再数一遍来比），
// 而它抓到过一次真 bug：阈值 16，Python 数字符、Go 数字节 ——
// 见下面 ruleLines 那处 len([]rune(line)) > 16 的注释。
//
// ⚠️ **而他给的第一条代价，我不照抄。** 他写：
//
//	「解析歧义仍然可能，但它会表现为两边算出不同的数 ⇒ 能被发现」
//
// **那在 carrierCensus 那里成立，因为两边的产物被【摆在一起比过】。
// 这里没有任何东西比它们**——Python 用自己的解析去抬高水位，Go 用自己的解析当下限，
// 两个结果从不相遇。所以真实的形态是**不对称的**：
//
//	Go 读出一个【更大】的数 → 生成之后那次 gofmt/vet 立刻红 → 会被发现
//	Go 读出一个【更小】的数 → 下限变松，全绿 → **不会被发现**
//
// ⇒ 所以这里的解析写成**严格**的：**任何读不懂的东西都是 Fatal，不是「跳过」。**
// 把「悄悄读出一个更小的数」压到只剩「两边都成功、但对同一行解释不同」，
// 而格式是「名字 数值」，那个缝已经窄到没有实际写法能落进去。
// **这不是把洞堵上了，是把它缩到可以写下来的大小。**

//go:embed tools/audit/high_water.txt
var highWaterRaw string

// highWater 解析高水位文件。判据必须与 tools/audit/rebuild_docs_test.py 的
// readHighWater 一致，否则两个读者各说各的。
//
// **严格**：读不懂就 Fatal。理由见上面那段——宽松解析的失效方向是「下限变松」，
// 而那一侧没有任何东西会发现。
func highWater(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for i, raw := range strings.Split(highWaterRaw, "\\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Fatalf("high_water.txt:%d 这一行读不懂：%q —— 格式是「名字 数值」", i+1, raw)
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			t.Fatalf("high_water.txt:%d 的数值读不懂：%q", i+1, f[1])
		}
		if _, dup := out[f[0]]; dup {
			t.Fatalf("high_water.txt:%d 重复的键 %q —— 两个值哪个算？", i+1, f[0])
		}
		out[f[0]] = n
	}
	if len(out) == 0 {
		t.Fatal("high_water.txt 里一个数都没有 —— 那等于没有下限")
	}
	return out
}

// floorOf 取一个下限。缺键是 Fatal，不是 0 ——
// **缺了下限就不该继续跑；把缺失读成 0，等于把「不知道该有多大」当成「它够大」。**
func floorOf(t *testing.T, key string) int {
	t.Helper()
	n, ok := highWater(t)[key]
	if !ok {
		t.Fatalf("high_water.txt 里没有 %q 这一行 —— 缺了下限就不该继续跑", key)
	}
	if n <= 0 {
		t.Fatalf("high_water.txt 里 %q 是 %d —— 下限是 0 或负数，那等于没有下限", key, n)
	}
	return n
}
'''

TPL_ANCHORS = '''
// —— 规则锚点表 ——
//
// 评审方拆出来的洞：landingCarriers 每份文件只点名【一句】，
// 于是同一份文件里另外七八条规矩可以被静默删掉，两道守卫都不响。
//
// 而正确的形状两个提交之前就在仓库里了：tools/probe/shinny 的
// TestGuardListCoversEveryProbeFile —— 新探针文件必须显式登记或豁免。
// 我发明了那个模式，却没有把它用在自己刚建的这套东西上。
//
// 所以往下套一层：每一节规矩都要有一个锚点，或者一条写明理由的豁免。
//
// 表是生成的（tools/audit/rebuild_docs_test.py），不手敲。
var ruleAnchors = []struct {
	file    string
	heading string // 一字不差的标题行
	needle  string // 必须出现在【这一节正文】里；豁免时为空
	exempt  string // 这一节为什么不承载规矩；有锚点时为空
}{
'''

TPL_MID_A = '''
}

var mdHeading = regexp.MustCompile(`(?m)^#{2,3} .+$`)

// sections 把一份 markdown 切成「标题 -> 该节正文」。
func sections(src string) ([]string, map[string]string) {
	locs := mdHeading.FindAllStringIndex(src, -1)
	order := make([]string, 0, len(locs))
	body := make(map[string]string, len(locs))
	for i, loc := range locs {
		title := src[loc[0]:loc[1]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		order = append(order, title)
		body[title] = src[loc[1]:end]
	}
	return order, body
}

// TestEveryRuleSectionHasAnAnchor 断言载体里的每一节规矩都被锚住。
func TestEveryRuleSectionHasAnAnchor(t *testing.T) {
	type entry struct{ heading, needle, exempt string }
	byFile := map[string][]entry{}
	for _, a := range ruleAnchors {
		if (a.needle == "") == (a.exempt == "") {
			t.Errorf("%s / %s：needle 与 exempt 必须【恰好】设一个"+
				"（都空 = 登记了却什么都没守；都填 = 说不清它到底算不算规矩）",
				a.file, a.heading)
			continue
		}
		byFile[a.file] = append(byFile[a.file], entry{a.heading, a.needle, a.exempt})
	}

	for file, entries := range byFile {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s 读不到：%v", file, err)
			continue
		}
		order, body := sections(string(b))
		registered := make(map[string]entry, len(entries))
		for _, e := range entries {
			registered[e.heading] = e
		}
		for _, title := range order {
			e, ok := registered[title]
			if !ok {
				t.Errorf("%s 新增了一节而没登记锚点：\\n  %s\\n"+
					"  在 ruleAnchors 里加一行（跑 tools/audit/rebuild_docs_test.py）。\\n"+
					"  不登记的后果是：这一节以后被人删掉，没有任何东西会响", file, title)
				continue
			}
			if e.needle != "" && !strings.Contains(body[title], e.needle) {
				t.Errorf("%s 这一节还在，但它的锚点句没了：\\n  节：%s\\n  锚：%q\\n"+
					"  有意改写就重跑生成器；不是的话，这条规矩刚被静默删掉了",
					file, title, e.needle)
			}
		}
		have := make(map[string]bool, len(order))
		for _, title := range order {
			have[title] = true
		}
		for _, e := range entries {
			if !have[e.heading] {
				t.Errorf("%s 的 ruleAnchors 里有一条【幽灵项】，文件里已经没有这一节了：\\n  %s\\n"+
					"  留着它会让下一个人以为那条规矩还在", file, e.heading)
			}
		}
	}

	if floor := floorOf(t, "anchors"); len(ruleAnchors) < floor {
		t.Fatalf("ruleAnchors 只有 %d 节，低于高水位 %d —— 这张表【缩水】了。"+
			"下限是高水位（历来最大值），不是当前值的某个比例："+
			"从被守的量派生出来的阈值在生成那一刻恒真，等于不设防。"+
			"真的删掉了规矩 ⇒ 动手把 tools/audit/high_water.txt 里那一行调低，"+
			"并在提交信息里说删了什么。删规矩是显式动作。"+
			"⚠️ 还有第二种成因：**高水位可能是在一次【不干净的重造】里被抬高的**"+
			"（例如载体还带着合并冲突标记就重造，两侧内容一起进表）。"+
			"先确认这个数【是怎么涨上去的】再决定调不调——"+
			"如果你什么都没删，那就【不要】写删除说明，去查那次抬高。",
			len(ruleAnchors), floor)
	}
	t.Logf("锚住 %d 节，其中 %d 节写明豁免", len(ruleAnchors), countExempt())
}

func countExempt() int {
	n := 0
	for _, a := range ruleAnchors {
		if a.exempt != "" {
			n++
		}
	}
	return n
}
'''

TPL_QUOTED = '''
// —— 规矩登记表（逐条，不是逐节） ——
//
// 评审方的 X2 打穿的是上一层：按【小节】锚，一节只锚一句，
// 于是同一节里另外几条可以被静默删掉。实测过：按小节锚仍然全绿。
//
// 覆盖面的模式化定义：
//
//	载体里，① `> ` 引用块中的加粗行  ② 行首加粗且有闭合的行（表格行按行首是 | 排除）
//
// ⚠️ 这个模式改过两次，两次都是【外部】发现的，不是我读出来的：
//
//	v1 只认 ①          → 漏 11 条；一次打不中目标的变异暴露的
//	v2 加「整行加粗」   → 仍漏 2 条，评审方实测找到（一条被「排除表格行」误伤，
//	                      判据写成了「含竖线」而竖线在行内代码里；
//	                      一条是「加粗开头 + 后续文字」）
//
// 「什么算一条规矩」现已在 docs/README.md 开头声明，不再由这个模式默认决定。
//
// ⚠️ 登记项带【所属小节】（R4，评审方 2026-09-08）：原来只钉「这条规矩在这份文件里」，
// 于是把整行挪到另一个 ## 底下，多重集不变 ⇒ 全绿。
// 而**一条规矩的适用范围是它所在的小节给的**——挪节就是改适用范围。
var quotedRules = []struct {
	file    string
	section string // 一字不差的标题行；文件开头那段用一个固定占位串
	needle  string
}{
'''

TPL_MID_B = '''
}

// —— 普查表：一个【不用任何模式】的数 ——
//
// 评审方指出的结构性问题：
//
//	一个「由模式生成」的登记表，它的覆盖面永远只能被【另一个模式】测出来。
//	生成器和验证器用同一个模式 ⇒ 它对自己永远自洽。
//
// 所以这里记一个最宽的数：每份载体里含 `**` 的行数。删掉任何一条规矩它都会掉，
// 不管那条规矩长什么样。两张表因【不同原因】失效：
//
//	普查数    挡不住「原地改写」（行数不变）    挡得住「没进登记表的行被删」
//	登记表    挡得住改写（针对不上）            挡不住没进表的行被删
//
// 代价：改这五份文件时，加/删任何一个加粗短语都会让它变红，得重跑生成器。
// 有意的摩擦，同 pending.txt —— 不这样，覆盖面就只能靠回想。
var carrierCensus = []struct {
	file      string
	boldLines int
}{
'''

TPL_TAIL = '''
}

// sectionedRule 是一条规矩连同它所在的小节。
// 挪到别的小节底下就是另一件东西——适用范围是小节给的。
type sectionedRule struct{ section, text string }

// ruleLines 把一份 markdown 里所有「独立成句的规矩行」连同所属小节抠出来。
// 判据必须和 tools/audit/gen_quoted_rules.py 的 is_rule 一致，否则两边各说各的。
func ruleLines(src string) []sectionedRule {
	stripMark := strings.NewReplacer("*", "", "`", "", ">", "")
	section := "(文件开头，尚未进入任何小节)"
	var out []sectionedRule
	for _, raw := range strings.Split(src, "\\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\\r"))
		if mdHeading.MatchString(line) {
			section = line
		}
		if strings.HasPrefix(line, "|") { // 表格行：判据是【行首】，不是「含竖线」
			continue
		}
		quoted := strings.HasPrefix(line, "> ") && strings.Contains(line, "**")
		// 用 rune 不用 byte：生成器那边是 Python 的 len()，数的是【字符】。
		// 同一个阈值 16，Go 的 len() 数字节 ⇒ 16 个汉字的一行会落到不同的一侧。
		bold := strings.HasPrefix(line, "**") && strings.Count(line, "**") >= 2 &&
			len([]rune(line)) > 16
		if !quoted && !bold {
			continue
		}
		plain := strings.TrimSpace(stripMark.Replace(line))
		if len([]rune(plain)) < 12 {
			continue
		}
		out = append(out, sectionedRule{section, plain})
	}
	return out
}

// TestEveryQuotedRuleIsRegistered 断言载体里的规矩【逐字相同、一条不多、一条不少】。
//
// ⚠️ 比对是【整行相等】，不是前缀。
//
// 上一版针取前 26 字、比对用双向 HasPrefix ⇒ 一条已登记规矩，前 26 字之后的
// 内容是自由的。评审方 2026-09-08 实证：把一条规矩的后半截改成意思相反的话，
// 两张表都点头。量过面：144 条里 93 条（65%）正文长于 needle，尾部合计 859 字。
//
// 而逼着它改的不是这个洞，是这条测试【自己的报错文案】曾经写着
// 「改写就把 quotedRules 里那一行一起改」——**那句在宣称它管改写，而它不管**。
//
//	要么把检查改成它宣称的样子，要么把宣称改成检查的样子。
//
// 这里选前者。代价是改任何一条规矩的措辞都要重跑生成器——取舍写在
// docs/method-landing.md，那是评审方委派我判、并要求写下来的一格。
func TestEveryQuotedRuleIsRegistered(t *testing.T) {
	byFile := map[string][]sectionedRule{}
	for _, q := range quotedRules {
		byFile[q.file] = append(byFile[q.file], sectionedRule{q.section, q.needle})
	}
	for file, want0 := range byFile {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s 读不到：%v", file, err)
			continue
		}
		// 多重集比对，键是【小节 + 整行】：同一行出现两次也要登记两次。
		want := map[sectionedRule]int{}
		for _, r := range want0 {
			want[r]++
		}
		got := map[sectionedRule]int{}
		for _, r := range ruleLines(string(b)) {
			got[r]++
		}
		for r, c := range want {
			if got[r] < c {
				t.Errorf("%s 少了一条登记过的规矩（登记 %d 次，实际 %d 次）：\\n"+
					"  节：%s\\n  文：%q\\n"+
					"  它被【删掉】、【改过一个字】、或【挪到别的小节】了。\\n"+
					"  有意的话重跑 tools/audit/rebuild_docs_test.py；"+
					"不是的话，这条规矩刚被静默改掉了。\\n"+
					"  注意：这一层比的是【小节 + 整行逐字相同】，"+
					"改标点会红、挪节也会红——都是有意的",
					file, c, got[r], r.section, r.text)
			}
		}
		for r, c := range got {
			if want[r] < c {
				x := []rune(r.text)
				if len(x) > 44 {
					x = x[:44]
				}
				t.Errorf("%s 多了一条没登记的规矩（或某条被改写/挪节之后的新样子）：\\n"+
					"  节：%s\\n  文：%q…\\n"+
					"  跑 tools/audit/rebuild_docs_test.py。\\n"+
					"  不登记的后果不是现在出错，是【以后它被删掉时没有东西会响】",
					file, r.section, string(x))
			}
		}
	}
	if floor := floorOf(t, "rules"); len(quotedRules) < floor {
		t.Fatalf("quotedRules 只有 %d 条，低于高水位 %d —— 这张表【缩水】了。"+
			"真的删掉了规矩 ⇒ 动手把 tools/audit/high_water.txt 里那一行调低，"+
			"并在提交信息里说删了什么。"+
			"⚠️ 这一条挡的正是「删掉规矩 + 重生成 ⇒ 全绿」——"+
			"上一版下限跟着当前条数走，那个组合是绿的。"+
			"⚠️ 还有第二种成因：**高水位可能是在一次【不干净的重造】里被抬高的**"+
			"（例如载体还带着合并冲突标记就重造，两侧内容一起进表）。"+
			"先确认这个数【是怎么涨上去的】再决定调不调——"+
			"如果你什么都没删，那就【不要】写删除说明，去查那次抬高。",
			len(quotedRules), floor)
	}
	t.Logf("登记了 %d 条规矩", len(quotedRules))
}

// TestCarrierCensus 用一个【不带模式】的数守着同一批文件。
func TestCarrierCensus(t *testing.T) {
	for _, c := range carrierCensus {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Errorf("%s 读不到：%v", c.file, err)
			continue
		}
		n := 0
		for _, raw := range strings.Split(string(b), "\\n") {
			if strings.Contains(raw, "**") {
				n++
			}
		}
		if n != c.boldLines {
			t.Errorf("%s 含 `**` 的行数：登记 %d，实际 %d（%+d）\\n"+
				"  · 你刚改过这份文件 ⇒ 跑 tools/audit/rebuild_docs_test.py\\n"+
				"  · 你没改过 ⇒ 有人动了它，而【登记表可能看不见那一处】——"+
				"这一条守的正是登记表的模式盖不住的地方",
				c.file, c.boldLines, n, n-c.boldLines)
		}
	}
	if floor := floorOf(t, "census"); len(carrierCensus) < floor {
		t.Fatalf("普查表只有 %d 份文件，低于高水位 %d —— 少了载体。"+
			"真的不再守某一份 ⇒ 动手调 tools/audit/high_water.txt。"+
			"⚠️ 还有第二种成因：**高水位可能是在一次【不干净的重造】里被抬高的**"+
			"（例如载体还带着合并冲突标记就重造，两侧内容一起进表）。"+
			"先确认这个数【是怎么涨上去的】再决定调不调——"+
			"如果你什么都没删，那就【不要】写删除说明，去查那次抬高。",
			len(carrierCensus), floor)
	}
}
'''


TPL_GUARDS = '''

// —— 守卫名字表 ——
//
// 起因（评审方 2026-09-08 实测，我复现过）：拆分之后**没有任何东西在数守卫**。
// 整份删掉 docs_guards_test.go ⇒ vet 0 / 三包 ok / doccheck 0，Test 函数 8 变 3，
// 而 go test 说 ok。生成文件不引用手写文件里的任何符号，所以连编译都不断。
//
// ⚠️ 登记的是【名字】，不是【数字】。两个计数方案都被否掉，理由是它们各有偏：
//
//	只数 docs_guards_test.go 的 func Test  → 把守卫【挪到别的文件】读成删除
//	数全仓的 func Test                     → 全仓 51 个（守卫 9 + 业务 42），
//	                                         业务测试一涨，高水位就停在「历史最多测试数」上，
//	                                         而合法删掉一个过时的业务测试会红
//	                                         ⇒「一个会红的必过项迟早被加 || true」的标准候选
//
// **计数是代理量里最弱的一种：它连「少了哪一个」都说不出来。**
// 而本仓第三次得到同一个答案——needle 从截断前缀改成整行、must 清单删掉、
// 序数改成点名——**每次的结论都是「登记名字，别登记一个会漂的代理量」。**
//
// ⚠️ **这条检查是【单向】的，而且是有意的**：
//
//	查      登记过的名字，现在还在不在【任意一份 _test.go】里
//	不查    存在的测试函数有没有被登记
//
// 双向会让每加一个业务测试都要重生成——**那正是「数全仓」那个方案的摩擦，
// 只是换了个位置。**
//
// ⚠️ **代价一（单向带来的）**：一个【新加的】守卫在被生成器收进来之前不受保护。
// 写完新守卫、还没跑 rebuild 的那段时间里，删掉它没有任何东西会响。
//
// ⚠️ **代价二（「登记名字」本身带来的）**：**它看不见「这个守卫还做不做事」。**
// 评审方 2026-09-08 补的，而这正是他三轮前对 doccheck 说过的那句：
//
//	**只登记名字的守卫，挡的是改名，不是改值。存在 ≠ 一致。**
//
// 实测两种，名字都还在，全绿：
//
//	把函数体掏空（只留 `_ = t`）   → PASS
//	函数体开头加 t.Skip()          → PASS，而那个守卫自己是 SKIP
//
// ⇒ **掏空一个守卫和删掉一个守卫，后果一样，而只有后者会响。**
// ⚠️ 而 t.Skip 那一种更隐蔽：自检用的 `go test ./... -count=1` 是**非 -v** 的，
// 它对被跳过的测试只印 `ok` —— **SKIP 在标准自检的输出里根本不出现。**
//
// 为什么不修（评审方判的，我同意）：真正盖住它的只有「每个守卫各有一格
// 能重跑的对照组、证明它会红」，那是比这一版大得多的一块；而退而求其次
// 对函数体做哈希登记，会让**每一次【合法】修改守卫都要重生成**——
// **摩擦落在最该被鼓励的动作上。** ⇒ 这一格的正解是记账，不是加严。
//
// ⚠️ 上一版这里写的是「**这是这个方案唯一的洞**」。代价二被发现之后那句就不成立了，
// 已删。**一个「唯一」是最容易过期的措辞——它把「我现在只想到一个」写成了「只有一个」。**
var guardNames = []string{
__GUARD_NAMES__
}

// funcTestRe 抠出一份 _test.go 里所有顶层测试函数的名字。
var funcTestRe = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\\(`)

// TestGuardsStillExist 检查每一个登记过的守卫【还在仓库里】。
//
// 判据是「名字出现在任意一份 _test.go 里」，不是「在原来那份文件里」——
// **挪个文件不算消失，删掉才算。**
func TestGuardsStillExist(t *testing.T) {
	have := map[string]string{}
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "__pycache__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range funcTestRe.FindAllStringSubmatch(string(b), -1) {
			have[m[1]] = p
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走 _test.go 时出错：%v", err)
	}
	// 前提也要打印出来：不然「一个都没扫到」和「全都在」长得一样。
	if len(have) == 0 {
		t.Fatal("一个测试函数都没扫到 —— 这个守卫没在守任何东西")
	}
	for _, name := range guardNames {
		if _, ok := have[name]; !ok {
			t.Errorf("守卫 %s 【不见了】—— 它登记过，而现在任何一份 _test.go 里都没有。\\n"+
				"  · 真的不再要它 ⇒ 删掉之后跑 tools/audit/rebuild_docs_test.py，"+
				"并在提交信息里说为什么\\n"+
				"  · 只是挪了个文件 ⇒ 那【不会】红，所以红就是真的没了", name)
		}
	}
	if floor := floorOf(t, "guards"); len(guardNames) < floor {
		t.Fatalf("守卫名字表只有 %d 条，低于高水位 %d —— 这张表【缩水】了。"+
			"删守卫是显式动作 ⇒ 动手把 tools/audit/high_water.txt 里那一行调低，"+
			"并在提交信息里说删了哪个。"+
			"⚠️ 还有第二种成因：**高水位可能是在一次【不干净的重造】里被抬高的**"+
			"（例如载体还带着合并冲突标记就重造，两侧内容一起进表）。"+
			"先确认这个数【是怎么涨上去的】再决定调不调——"+
			"如果你什么都没删，那就【不要】写删除说明，去查那次抬高。",
			len(guardNames), floor)
	}
	t.Logf("登记 %d 个守卫名，全仓 %d 个测试函数里都在", len(guardNames), len(have))
}
'''

if __name__ == "__main__":
    if "--drill" in sys.argv:
        drill()
    else:
        main()
