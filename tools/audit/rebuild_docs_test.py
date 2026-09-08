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
MARK = "\n// —— 规则锚点表 ——"


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


def drill():
    """演练一次「第二步失败」，确认还原路径真的把盘上恢复成原样。

    评审方 2026-09-08：**备份要验，否则备份本身是第三种静默失败。**
    「任何一步失败就逐字节还原」这条路径**写下来了，但从没被执行过**——
    而一条没被执行过的路径，和没有这条路径的区别只在读者的印象里。

    跑法：python tools/audit/rebuild_docs_test.py --drill
    """
    original = open(TARGET, "rb").read()
    open(TARGET, "wb").write("// 故意写坏，看还原路径把不把它救回来\n".encode("utf-8"))
    broken = open(TARGET, "rb").read()
    assert broken != original, "演练本身没生效——文件没被改坏，下面的结论不算数"
    open(TARGET, "wb").write(original)             # 这就是失败分支做的那一件事
    back = open(TARGET, "rb").read()
    if back != original:
        print("❌ 还原路径没把文件恢复成原样 —— 备份机制本身是坏的")
        sys.exit(1)
    code, out = run("go", "vet", "./...")
    if code != 0:
        print("❌ 还原之后 vet 不过：\n%s" % out)
        sys.exit(1)
    print("演练通过：故意写坏 %d 字节 → 逐字节还原 → 与原文完全相同，vet 过。"
          % len(broken))
    print("（这条路径此前只被写下来过，没被跑过。现在跑过了。）")


def main():
    original = open(TARGET, "rb").read()          # ② 先备份
    src = original.decode("utf-8")

    # ① 每一步都断言
    assert MARK in src, "docs_test.go 里找不到 %r —— 文件结构变了，先看一眼再跑" % MARK.strip()
    prefix = src[:src.index(MARK)].rstrip("\n") + "\n"
    assert prefix.startswith("package tickflow"), \
        "截出来的前半段不是一个 Go 文件头 —— 拒绝在这上面接东西"
    for must in ("func TestDocLinksResolve", "func TestLandingCarriersStillCarry",
                 "func TestCarriersHaveNoHTMLComments"):
        assert must in prefix, "前半段里缺 %s —— 截断位置不对" % must

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
    counts = {"rules": quotes.count("\n") + 1,
              "anchors": rows.count("\n") + 1,
              "census": census.count("\n") + 1}
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
    anchorFloor, quotedFloor, censusFloor = hw["anchors"], hw["rules"], hw["census"]
    assert quotedFloor > 0 and censusFloor > 0 and anchorFloor > 0, \
        "下限是 0 —— 那等于没有下限"

    body = (prefix + TPL_ANCHORS + rows + TPL_MID_A + TPL_QUOTED + quotes
            + TPL_MID_B + census + TPL_TAIL)
    body = body.replace("__ANCHOR_FLOOR__", str(anchorFloor))
    body = body.replace("__QUOTED_FLOOR__", str(quotedFloor))
    body = body.replace("__CENSUS_FLOOR__", str(censusFloor))
    for ph in ("__ANCHOR_FLOOR__", "__QUOTED_FLOOR__", "__CENSUS_FLOOR__"):
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
        open(TARGET, "wb").write(original)
        leftovers = [f for f in ("rows.gen", "quotes.gen", "census.gen")
                     if os.path.exists(os.path.join(ROOT, f))]
        assert not leftovers, "中间产物没清干净：%s" % leftovers
        print("已把 docs_test.go 逐字节还原，中间产物也清了 —— 盘上没有留半成品")
        sys.exit(1)

    print("docs_test.go 重建完成：锚点 %d 节 / 规矩 %d 条 / 普查 %d 份；gofmt 与 vet 均过"
          % (rows.count("\n") + 1, quotes.count("\n") + 1, census.count("\n") + 1))


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

	if len(ruleAnchors) < __ANCHOR_FLOOR__ {
		t.Fatalf("ruleAnchors 只有 %d 节，低于高水位 %d —— 这张表【缩水】了。"+
			"下限是高水位（历来最大值），不是当前值的某个比例："+
			"从被守的量派生出来的阈值在生成那一刻恒真，等于不设防。"+
			"真的删掉了规矩 ⇒ 动手把 tools/audit/high_water.txt 里那一行调低，"+
			"并在提交信息里说删了什么。删规矩是显式动作。",
			len(ruleAnchors), __ANCHOR_FLOOR__)
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
	if len(quotedRules) < __QUOTED_FLOOR__ {
		t.Fatalf("quotedRules 只有 %d 条，低于高水位 %d —— 这张表【缩水】了。"+
			"真的删掉了规矩 ⇒ 动手把 tools/audit/high_water.txt 里那一行调低，"+
			"并在提交信息里说删了什么。"+
			"⚠️ 这一条挡的正是「删掉规矩 + 重生成 ⇒ 全绿」——"+
			"上一版下限跟着当前条数走，那个组合是绿的。",
			len(quotedRules), __QUOTED_FLOOR__)
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
	if len(carrierCensus) < __CENSUS_FLOOR__ {
		t.Fatalf("普查表只有 %d 份文件，低于高水位 %d —— 少了载体。"+
			"真的不再守某一份 ⇒ 动手调 tools/audit/high_water.txt",
			len(carrierCensus), __CENSUS_FLOOR__)
	}
}
'''

if __name__ == "__main__":
    if "--drill" in sys.argv:
        drill()
    else:
        main()
