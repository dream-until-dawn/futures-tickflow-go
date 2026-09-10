package segfile

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 这一条守的是「docs/design.md §6.1 那张不变量表」与「本包的测试」之间的对应关系。
//
// 它是那张表下面那条约束③的落地：
//
//	由一条测试用 go/ast 收集编号集合，断言它恰好等于表里的编号、且每个编号两侧俱全。
//
// ⚠️ 编号集合【从 design.md 现读】，不写死一个数字。
// 写死数字的话，这条测试自己就成了那张表的第二份拷贝 ——
// **而这张表的全部用途就是「让下一个人去对，而不是重新数」。**
//
// ⛔ 它的【上界】，和它写在一起（评审方 2026-09-08 要求）：
//
//	它保证这些编号【都有人认领】；**它不保证认领的人真的动手了。**
//	TestInvariantA2_Red 里有没有真的违反 A2，go/ast 读不出来。
//
// 这是「存在 ≠ 一致」在编号上的重演，和 guardNames 那次一模一样：
// 登记的是名字，把函数体掏空照样 PASS。
// ⇒ 真正顶住后一半的不是这条测试，是那些 _Red 各自的对照组
// （tools/audit/invariant_control.py 的定点突变）：
// **每个 _Red 必须在【不违反该不变量】的输入下变绿，
// 而把实现弄坏之后它必须真的红。**

// absent 是【本版合法缺席】的编号，每一条都要写明凭什么。
//
// ⚠️ 这份清单是**有有效期**的：它记的是「今天为什么还没做」，
// 而那个理由一旦不成立，这里就必须跟着改。**别沿用。**
var absent = map[string]string{
	// ✅ **C3b / D2b 已于丙三之三落地，两条一并从这张表里删掉。**
	//
	// ⚠️ 而它们的缺席理由【换过一次，而旧的那句从丙一起就是假的】：
	// 原来写「`SyncReport` 还不存在」，而它丙一就落地了 ——
	// 本清单自己那条规矩（理由一旦不成立就必须跟着改）**兑现过一次**，
	// 而抓到它的是一次顺手的 grep，**不是任何守卫**。
	// ⇒ **一个写成条件的缺席理由，到期时不会有人通知你。**
	"A4": "`.meta` 要存 `recordSize` 并与本版记录长比对 —— 而 `Meta` 结构体里" +
		"**还没有这个字段**（上哪儿看：`Meta`）。" +
		"它和 A3 同一个入口（`DecodeMeta`），补字段那一格一起做，" +
		"**别单独加一个只测一半的测试**。",
	"G1": "`Open` 要把 `.meta` 里的全量参数与请求的参数比对。⛔ **这条 2026-09-10 缩了一格**：" +
		"周期那一维**不再需要比对** —— 它进了文件名（design.md §十八），" +
		"于是「.meta 说的周期」与「调用方要的周期」**不再是两处可以对不上的东西**，" +
		"而是同一处（路径）。⇒ 剩下的只有 A4：`.meta` 要存 `recordSize` 并与本版记录长比对，" +
		"而那是**自描述与常量**的比对，不是调用方参数（上哪儿看：`Meta` 的字段集）。" +
		"⚠️ 所以 G1 现在与 A4 是同一格，**别再把它读成「Open 的签名缺参数」** —— " +
		"签名那一格已经做掉了。",
}

// elsewhere 是【守卫在别的包里】的编号 —— 每一条要写明**在哪**。
//
// ⛔ **它与 absent 必须分开，因为两者的处置【相反】**：
//
//	absent     这一条今天没有守卫 ⇒ 处置是【补一对 _Red/_Green】
//	elsewhere  它有守卫，只是不在本包 ⇒ 处置是【去那儿看】
//
// 合成一格的后果很具体：一个已经被守着的编号会被记成待办，
// 而下一个人「修」它的方式，是在**错的那个包里**再写一份重复的测试。
//
// 🔴 **而这一格是被 C3b / D2b 逼出来的，理由正是本仓记过的那条**：
// **「射程写成位置，就照不到搬走的那份。」**
// 这张覆盖表的射程是【本包】，而 C3b / D2b 这两条不变量**跨了包**：
// 通道在本包（`Store.OpenState`），而**接线与判断在根包**（只有编排知道
// 「源可不可重放」）。⇒ 一条跨包的不变量，本来就不该由一个按包划的表来判缺席。
var elsewhere = map[string]string{
	"C3b": "通道在本包（`Store.OpenState` / `openstate_test.go`），" +
		"而【接线与断言】在根包：`Syncer.disposeOpenState` / `TestC3bTruncatedTailReachesTheReport`",
	"D2b": "同 C3b：本包只交出 `OpenState().LegacyMeta`；" +
		"判定要「源可不可重放」，那是 `Caps` 那一头的信息 ⇒ 处置在根包：" +
		"`TestD2bBothDispositionsReachTheReport`（两支各一格）",
}

// ⚠️ 上面几条为什么都写成【条件】而不是【版本标签】。
//
// ⛔ **这一句原本只写「评审方 2026-09-09 撤回了他自己『写有效期』的要求」——
// 而他后来说【他核不了这一句】**：他的信不在一个能 grep 的地方。
// ⇒ 判据（他给的，我认）：**注释里引用「代码里有什么」可核，
// 引用「某人说过什么」只有那个人能核** —— 所以这类归属**要带上原话**，
// 否则它是一条永远不会被验的断言，**而它看起来像有出处**。
// ⇒ 原话补在这儿（我从本会话记录里取回的，两处，一字不改）：
//
//	「而我要撤回我自己那条『写有效期』的要求：你的写法比我要的好」
//	「我此前要求 A4/G1 各写『有效期』（做 Store 那一版 / 做 Source 那一版）」
//
// ⚠️ 而这一句我原来写成「它没有变成【可核】，它变成了【可被否认】」——
// **那说轻了**（评审方 2026-09-09 纠，他当场把两句原话都核了）：
// 会话转录是在的、能 grep 的。准确的说法是：
// **仓外可核、仓内无守卫** —— 转录不在这个仓里，所以写不出那个守卫；
// 而「核不了」与「核得了但没有守卫」是两回事，前者是死路，后者只是没人守。
//
//	版本标签   「做 Store 落盘那一版」—— 散文里的版本号是活指针，改别处不会让它变红
//	条件式     「`Meta` 里还没有这个字段」—— 任何人**当场能查**
//
//	⇒ **条件式的缺席理由，自己就是自己的有效期 —— 条件不成立那天，它自动到期。**
//
// 而每条都带一个「上哪儿看」，**给名字不给行号**：
// 指向名字的行号是冗余的（名字自己就能被 grep），而它会随无关的编辑漂掉。

// ⛔ 字母那一位写 `[A-Z]`，不写 `[A-F]`。
//
// 2026-09-09 实测（把本分支与那条改表的分支合起来跑）：表里新增了 `A4` 与 `G1`，
// 而这两条正则当时写的是 `[A-F]` ——
//
//	A4  落在 A–F 里 ⇒ 被抓住，报「表里有、没人认领」（对）
//	G1  落在 A–F 外 ⇒ **一声不响**：既不算表里的编号，也就永远不会被报
//
// > **一条【只抓得住一部分新编号】的收集测试，比完全抓不住更危险：
// > 它当场给了一个红，让人以为这一轮已经查干净了。**
//
// ⚠️ 这和评审方那次 `[ab]?` 漏掉 `A1c` 是同一形状，只是换到了字母那一位。
// ⇒ 所以这里不是「加一个 G」，是**别再写死字母表**。
//
// ⛔ **而上一版只走了一位** —— 字母放宽了，**数字那一位仍是 `[0-9]`（单个数字）**，
// 失效形状一模一样，只是往右挪了一个字符（评审方 2026-09-09 指出，我复现了）：
//
//	表行侧   `| A4 |` → A4      `| A10 |`  → **不认得**
//	测试侧   TestInvariantA4_Red → A4   TestInvariantA10_Red → **不认得**
//	⇒ 两侧都不认得 ⇒ 两侧都【一声不响】，而 `absent` 那条双向查也救不了
//	  （`absent` 是拿 key 去比 `want`，而 `want` 里根本没有 A10）
//
// 今天不会错（本仓最大编号是个位数），改它的理由是这一格自己的论点：
// **A 组两天里从 A3 长到 A4，表从 6 行长到 20 行；`[0-9]` 就是下一个 `[A-F]`。**
// ⇒ `[0-9]` → `[0-9]+`。验过不过收：A1a→A1a，A4→A4，G1→G1，A10→A10，B12b→B12b。
//
// ⚠️ 而 `A4` / `G1` 要进 absent 的那一半**不在这条分支上**：
// 那两个编号是另一条分支往 `design.md` 里加的，本分支的表里还没有它们，
// **现在就登记进 absent，这条测试会立刻红**（它双向查：absent 里的编号必须在表里）。
//
//	⇒ 这两件事是【一格】，被切到了两条分支上。而它不需要靠人记得：
//	  合并的那一刻，A4 会让这条测试红，红的信息里就写着要补什么。
//	  **一条跨分支的约束，分支上的绿灯看不见它 —— 看得见的是合并。**
// ⛔ 本文件里的**三条**守卫【都没有被登记】—— 删掉它们不会有任何东西响。
//
//	TestInvariantCoverageMatchesTable            经 tableIDs 读 design.md
//	TestFifthColumnNamesExist                    直接读 design.md
//	TestIDPatternAcceptsShapesTheTableDoesNotYetHave  **不读任何文档**（合成用例）
//
// ⚠️ 第三条是 2026-09-09 后加的，而它当场推翻了下面那个数法：
// 我数「哪些是没登记的守卫」时用的代理判据是**「函数体里真去读仓内文档」**，
// 而这一条**一个文件都不读** —— 它守的是那两条守卫赖以工作的【正则】。
// ⇒ **一条按我自己的判据数不出来的守卫，我自己刚刚写了一条。**
//   这是「是不是一条守卫没有可机器判的判据」最直接的一个证据。
//
// 实测（2026-09-09，整个删掉 TestFifthColumnNamesExist 跟着跑一遍，不是推理）：
// go vet 过、四个包全绿、生成器重跑后仍然打印「守卫 12 个」—— **一个都没响。**
//
// 原因：登记表抽名字的来源是【两个位置】（静态模板 + docs_guards_test.go），
// 见 tools/audit/rebuild_docs_test.py。**射程写成了位置，不是性质** —— 本仓反复撞的那个形状。
//
// ⚠️ 而这一格的讽刺：**这是一条专门用来抓「声称有守卫」的守卫，而它自己没被登记。**
//
// ⇒ 不搬家的理由：它们要用本包的 go/ast 收集结果，搬去 docs_guards_test.go 就得重造那套收集。
//
// ⛔ 那为什么不干脆把射程改成按性质？**因为「是不是一条守卫」目前没有可机器判的判据。**
// 2026-09-09 试了两个代理，两个都不对：
//
//	判据①「函数体里出现 .md」         ⇒ 多收 3 个：那三条只是在【失败信息里点了文档的名字】
//	                                    （「请一并更新 contract.md」），并不读它
//	判据②「函数体里有 ReadFile 调用」 ⇒ 漏掉经 helper 去读的那条，又多收 2 个读临时目录的
//
//	⇒ **先按①数出 6 个、再按②数出 3 个，手工核完真数是 2。**
//	  写下这个过程，是因为下一个人多半也会从「grep 一下 .md」开始。
//
// ⛔ **而「没有可机器判的判据」这句话，只对【推断】成立，对【声明】不成立**
// （评审方 2026-09-09 提的，我核过并把它推得更远）：
//
//	推断   从函数体去猜它是不是守卫   ⇒ 我试了两个代理，他试了两个，**四试四败**
//	声明   在函数上方写一行 `// guard:` ⇒ **可机器判，不需要猜意图**
//
// ⇒ 上面那段话如果只写到「没有判据」，读起来像**这件事没救**，
//   而它只是**这条路没救**。
//
// ⛔ **而任何按【行为性质】重抽的方案，第一步是重新定义「什么算守卫」，不是找个更好的正则** ——
// 因为现有登记表里就有成员会被这类性质**踢出去**。我逐个核过（措辞要准，别照抄一句「两条不读文档」）：
//
//	TestGuardsDoNotSkipThemselves   走全仓 `*_test.go`，用 `parser.ParseFile` ⇒ **读文件，不读文档**
//	TestGuardsStillExist            走全仓 `*_test.go`，用 `os.ReadFile`      ⇒ **读文件，不读文档**
//
//	按「读文档」抽       ⇒ **两条都被踢**
//	按「有 ReadFile」抽  ⇒ StillExist 留下；**DoNotSkip 仍被踢**（它用的是 `parser.ParseFile`）
//	⇒ **两个代理判据，没有一个能把这两条【同时】留下。**
//
//	⇒ **同一张表，换一个性质就换一批成员** —— 说明这张表的真实定义从来不是「文档守卫」，
//	  就是「**那个文件里的 `func Test`**」。
//
// ⛔ **上面这两行我第一版写成了「`TestGuardsDoNotSkipThemselves` 一个文件都不读」，那是假的**
// （评审方 2026-09-09 抓到；他原话是「未直接读文档」，**是我把它改强了**）。
// 成因值得留着：我当时判「读不读文件」用的模式是
// `ReadFile|os\.Open|ReadDir|filepath\.Join` —— **一张【当前写法】的清单**，
// 而它捞不到 `parser.ParseFile`，也捞不到 `filepath.Walk`。
// ⇒ **判据第三次写成了「它现在长什么样」。而这一次，它把我引向了一个【更强而更假】的结论 ——
//   那比一个含糊的结论更危险，因为它读起来更像做过功课。**
//
// 🔴 **而最值得记的一条在这儿：全仓收集的机器，本仓【已经有了】，而且天天在跑。**
//
//	遍历      **两台**，都用 `filepath.Walk(".")` 扫全仓 `*_test.go`：
//	          `TestGuardsStillExist`（`os.ReadFile`）／`TestGuardsDoNotSkipThemselves`（`parser.ParseFile`）
//	          前者的失败信息里还写着「只是挪了个文件 ⇒ 那【不会】红」
//	登记      `guardNames` 只从 `staticTpl` ＋ `docs_guards_test.go` 抽
//
//	⚠️ 说准一点（评审方的更正）：**那两台都是【消费】`guardNames` 的，不【产生】它。**
//	  ⇒ 现成的是**遍历**，不是**产生登记表**那一步 —— 别让下一个人以为后者也现成。
//
//	⇒ **同一张表，存在性按【性质】查（全仓），而登记按【位置】抽（一个文件）。**
//	  ⇒ 缺的从来不是收集的机器，是「哪些 `func Test` 算守卫」这个**定义**。
//	  而 `// guard:` 正好补的就是这个定义 —— 它对「一个文件都不读的守卫」同样有效，
//	  **而任何基于行为的代理都不行。**
//
// ⛔ **而实现那天会撞上的一个坑，现在写下来是一行**（评审方 2026-09-09 读到的）：
//
//	`TestGuardsDoNotSkipThemselves` 里是 `parser.ParseFile(fset, p, nil, 0)`，
//	**最后那个 `0` 意思是「不解析注释」**（它自己的注释写着「判据只看真的调用」）。
//	⇒ **要按 `// guard:` 这种注释标记收集，那一处必须换成 `parser.ParseComments`。**
//	  而它正好埋在**你要复用的那台机器里**。
//
// ⚠️ 那是一次跨文件的机制改动（要动生成器与两条守卫）⇒ **单独一格、单独评审，不在这里。**
//
// ⚠️ 还有半句：上面说的【两个位置】里，`staticTpl` 那一半是**写死在生成器源码里的**，
// 它连「放错文件就不登记」这个失败模式都没有 —— **因为它根本不来自任何文件。**
// 两个位置不是同一种东西，别让读者以为是。
//
// ⇒ 所以现在的处置是【写下来它守不住】，不是假装守住了：
// 这两条守卫要靠人记得它们在这儿。**它们的缺席，机器看不见。**

var (
	tableRow = regexp.MustCompile(`^\| ([A-Z][0-9]+[a-z]?) \|`)
	testName = regexp.MustCompile(`^TestInvariant([A-Z][0-9]+[a-z]?)_(Red|Green)$`)
)

// TestIDPatternAcceptsShapesTheTableDoesNotYetHave 守上面那三个正则的**放宽**部分。
//
// # 为什么要一条【合成】的测试
//
// 这三个正则被放宽过两次，每次都是因为一次真实的漏检：
//
//	[A-F] → [A-Z]    G1 原来被**一声不响**地漏掉（cf22a46）
//	[0-9] → [0-9]+   同一个失效方式，在数字那一位上（58b42e1）
//
// 2026-09-09 我把三个成分**分开**改窄回去，一次只动一个，量出来：
//
//	[A-Z]   改窄 ⇒ **红**（`G1` 在 absent 里，`[A-F]` 容不下它）
//	[a-z]?  拿掉 ⇒ **红**（`A1a` / `A1b` 撑着）
//	[0-9]+  改窄 ⇒ **全绿** ← **只有这一个没有对照组**
//
// ⇒ 因为今天表里的编号**全是一位数**。
// **一个没有用例去走的放宽，和没有放宽，机器分不出来** ——
// 而它的失效方式恰恰是「一声不响地漏掉」，也就是本仓在 `G1` 上已经吃过一次的那一种。
//
// ⚠️ 所以这条测试**故意用表里不存在的编号**。它不读 design.md：
// 要等表里真出现 `A10` 才有对照组，就等于要先漏检一次才肯装守卫。
//
// ⚠️ 它的射程：只管**正则收不收**，不管收进来之后那一套对不对。
// 表里真加了两位数编号，`tableIDs` 的连续性断言会不会跟着成立，这条测试不答。
func TestIDPatternAcceptsShapesTheTableDoesNotYetHave(t *testing.T) {
	for _, c := range []struct{ row, want string }{
		{"| A10 | 两位数 |", "A10"},  // ← 今天表里没有：数字那一位的放宽全靠它
		{"| Z1 | 字母尾端 |", "Z1"},   // ← 今天表里没有：字母那一段的上界
		{"| A1a | 真实存在 |", "A1a"}, // ← 表里有，放这儿是为了让三条并排读
	} {
		m := tableRow.FindStringSubmatch(c.row)
		if m == nil {
			t.Errorf("tableRow 认不出 %q —— 这一行会被一声不响地跳过，"+
				"而那正是 G1 当年的漏法", c.row)
			continue
		}
		if m[1] != c.want {
			t.Errorf("tableRow 从 %q 里取到 %q，要 %q", c.row, m[1], c.want)
		}
	}
	for _, c := range []struct{ fn, want string }{
		{"TestInvariantA10_Red", "A10"},
		{"TestInvariantZ1_Green", "Z1"},
		{"TestInvariantA1a_Red", "A1a"},
	} {
		m := testName.FindStringSubmatch(c.fn)
		if m == nil {
			t.Errorf("testName 认不出 %q —— 这个测试会被算成【不是不变量测试】，"+
				"于是那条编号看起来【没人认领】", c.fn)
			continue
		}
		if m[1] != c.want {
			t.Errorf("testName 从 %q 里取到 %q，要 %q", c.fn, m[1], c.want)
		}
	}
	for _, c := range []struct{ col, want string }{
		{"读：`X`→`A10_Red`", "A10"},
		{"读：`X`→`Z1_Green`", "Z1"},
		{"读：`X`→`A1a_Red`", "A1a"},
	} {
		m := fifthColName.FindStringSubmatch(c.col)
		if m == nil {
			t.Errorf("fifthColName 认不出 %q —— 第五列写了个假名字也查不出来", c.col)
			continue
		}
		if m[1] != c.want {
			t.Errorf("fifthColName 从 %q 里取到 %q，要 %q", c.col, m[1], c.want)
		}
	}
}

// tableIDs 从 design.md 那张表里现读编号集合。
func tableIDs(t *testing.T) map[string]bool {
	t.Helper()
	p := filepath.Join("..", "..", "docs", "design.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读不到 %s：%v —— 这条测试要拿它当权威", p, err)
	}
	ids := map[string]bool{}
	var first, last, n int
	for i, ln := range strings.Split(string(b), "\n") {
		if m := tableRow.FindStringSubmatch(ln); m != nil {
			ids[m[1]] = true
			if n == 0 {
				first = i
			}
			last = i
			n++
		}
	}
	// ⛔ 命中行必须是【一整块连续的】。
	//
	// 这条测试读的是**整份 design.md**，没有节边界；而 design.md 现在已经不止一张
	// 不变量表了（另一条分支加了 `SRC-1..14` / `SYN-1..8` 那 22 行）。
	// 今天不撞车（`SRC-`/`SYN-` 是三字母加连字符，`[A-Z][0-9]+` 认不得，实测误收 0 条），
	// 但状态变了：
	//
	//	**本包的收集测试，把「单个大写字母 + 数字」这个编号空间，
	//	在【全仓 design.md 范围内】占住了。**
	//	哪天同步层那张表改用 `| S1 |` 这种短编号，segfile 会开始要求
	//	`TestInvariantS1_Red/Green` —— 而那条不变量根本不属于这一层。
	//
	// ⇒ 一行连续性断言就挡得住：别处冒出同形状的表行，它当场红。
	// ⚠️ 而**不用「按节切」来解**：`meta_census.py` 那一格刚好给了反例 ——
	// 它按节切了，而节边界错了没人看得见（那条 ✅ 恒真，边界错了不可能红）。
	//
	//	**两条分支在同一个问题上翻了相反的错：一个切了节但没人核边界，一个根本没切。**
	//	⇒ 「这条检查的权威是【文件】还是【文件里的哪一块】」必须被问一次，
	//	  **而两种答案都要配一个能红的东西。**
	if n > 0 && last-first+1 != n {
		t.Fatalf("design.md 里形如 `| A1a |` 的表行【不连续】：%d 行散布在 %d..%d。\n"+
			"⇒ 多半是别处也出现了同形状的表行，而本包会把它们一起当成自己的不变量。\n"+
			"⇒ 确认那些行不该由 store/segfile 认领之后，给它们换一个形状"+
			"（例如加前缀，像 SRC-/SYN- 那样）", n, first+1, last+1)
	}
	if len(ids) == 0 {
		t.Fatal("从 design.md 里一个编号都没读到 —— 要么表没了，要么行的形状变了。" +
			"这时【不能】当成「没有要求」：一个读不到权威的检查，不是一个通过的检查")
	}
	return ids
}

// testIDs 用 go/ast 收集本包测试里出现的编号与它们的两侧。
func testIDs(t *testing.T) map[string]map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("找不到本包的测试文件：%v", err)
	}
	got := map[string]map[string]bool{}
	for _, p := range files {
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", p, err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			m := testName.FindStringSubmatch(fn.Name.Name)
			if m == nil {
				continue
			}
			if got[m[1]] == nil {
				got[m[1]] = map[string]bool{}
			}
			got[m[1]][m[2]] = true
		}
	}
	return got
}

func TestInvariantCoverageMatchesTable(t *testing.T) {
	want := tableIDs(t)
	got := testIDs(t)

	var missing, extra, oneSided []string
	for id := range want {
		if reason, ok := absent[id]; ok {
			if _, has := got[id]; has {
				extra = append(extra, fmt.Sprintf("%s（它登记为【合法缺席】：%s，但测试里出现了）", id, reason))
			}
			continue
		}
		if _, ok := elsewhere[id]; ok {
			// 守卫在别的包 ⇒ 本表不判它缺席。
			//
			// ⚠️ **这一格的价值上界改过一次，因为它被抬高了一半**：
			// 上一版写「本表不去核那个别处是不是真的有…挡不住『说了个假地址』」。
			// 而假地址是**零成本的散文** ⇒ 值得抬。
			// ⇒ `TestElsewhereNamesExist` 现在核**那些名字还在不在**
			//（整词匹配，全仓 .go）。
			//
			// ⛔ **而它仍然只核名字，不核语义** —— 名字在，不等于那儿真的守着这一条。
			// ⇒ 所以上界现在是：**挡得住「对面改名或删掉」，挡不住「那儿其实没在守」。**
			continue
		}
		sides, has := got[id]
		if !has {
			missing = append(missing, id)
			continue
		}
		if !sides["Red"] || !sides["Green"] {
			oneSided = append(oneSided, id)
		}
	}
	for id := range got {
		if !want[id] {
			extra = append(extra, id+"（测试里有，而 design.md 那张表里没有）")
		}
	}
	// ⛔ 两张表不许重叠：一个编号要么「今天没有守卫」，要么「守卫在别处」，
	// 不可能两者都是。同时登记 ⇒ 其中一条一定是陈的。
	for id := range elsewhere {
		if _, dup := absent[id]; dup {
			t.Errorf("%s 同时登记在 absent 与 elsewhere 里 —— 两者处置相反，"+
				"同时为真说不通；其中一条是陈的", id)
		}
		if !want[id] {
			t.Errorf("elsewhere 里的 %s 在 design.md 那张表里没有 —— 幽灵项，删掉它", id)
		}
	}

	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(oneSided)

	if len(missing) > 0 {
		t.Errorf("这些编号在 design.md 的表里，而本包没有对应的测试：%v\n"+
			"⇒ 要么补上 _Red / _Green 一对，要么把它登记进 absent 并写明凭什么缺席。\n"+
			"⚠️ 「登记进 absent」不是绕过：那份清单有有效期，它记的是【今天为什么还没做】。", missing)
	}
	if len(oneSided) > 0 {
		t.Errorf("这些编号只有一侧：%v\n"+
			"⚠️ 只有 _Red 证明的是「这个测试会红」，不是「它因为那条不变量而红」。\n"+
			"每一对必须是【同一条判据、两个只差这一条的输入】。", oneSided)
	}
	if len(extra) > 0 {
		t.Errorf("这些编号对不上那张表：%v", extra)
	}

	// 缺席清单本身也要对得上表 —— 登记一个表里没有的编号，说明清单过期了。
	for id, reason := range absent {
		if !want[id] {
			t.Errorf("absent 里登记了 %s，而 design.md 的表里没有这个编号（理由写的是：%s）\n"+
				"⇒ 多半是那张表改了而这份清单没跟着改。", id, reason)
		}
	}
}

// fifthColName 从「入口与守卫」那一列里抓形如 `A1a_Red` / `B2_Green` 的守卫名。
var fifthColName = regexp.MustCompile(`([A-Z][0-9]+[a-z]?)_(Red|Green)`)

// TestFifthColumnNamesExist 断言：表第五列里提到的每个 `XX_Red` / `XX_Green`，
// **在本包里真的是一个测试函数**。
//
// ⛔ 为什么必须有它（评审方 2026-09-09 抓到的一处，我核过）：
//
//	design.md 的 A4 第五列  「读：`DecodeMeta`→**`A4_Red`**／写：…」 ⇒ 读起来像【已经守住了】
//	coverage_test.go        `A4` 在 `absent` 里                    ⇒ 【今天还没做】
//	包里实际                 `TestInvariantA4_Red` **不存在**        ⇒ 事实
//	（全树 grep `A4_Red` 唯一命中是上面那段讲正则的注释）
//
// 而另外三条是对的：`C3b` / `D2b` / `G1` 的第五列都写着「**空 —— …尚不存在**」。
// **只有 A4 填了一个不存在的名字。**
//
// ⇒ 这正是本仓自己写过两遍的那条判据：
//
//	**第五列写着一个守卫的名字，而那个守卫没被登记、或者从来没红过 ——
//	那一格填了，比空着更糟：空着说「还没做」，填错了说「已经守住了」。**
//
// ⚠️ 而【整个第五列原来是一列「只写不读」的字段】：
// `TestInvariantCoverageMatchesTable` 只比编号三方，从不读第五列；
// `guardNames` 与 `doccheck` 也都不管它 ⇒ **写什么都没有任何东西会红。**
// 这和 `high_water.txt` 里 `自` 那一格同形，**而这一列更贵**：
// `自` 写错只是记错一个前值，**第五列写错是在说「已经守住了」。**
//
// ⚠️ 射程：
//
//	查的   第五列里形如 `XX_Red` / `XX_Green` 的名字，在本包里是不是真的函数
//	不查的 第五列里的**散文**对不对（例如「写：`EncodeMeta` 一律写本版 `format`」）
//	不查的 那个函数是不是真的守着那条不变量 —— 那是对照组的活（invariant_control）
func TestFifthColumnNamesExist(t *testing.T) {
	p := filepath.Join("..", "..", "docs", "design.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读不到 %s：%v", p, err)
	}
	have := testIDs(t)
	var checked int
	for _, ln := range strings.Split(string(b), "\n") {
		if !tableRow.MatchString(ln) {
			continue
		}
		cols := strings.Split(ln, "|")
		if len(cols) < 6 {
			continue
		}
		id := strings.TrimSpace(cols[1])
		for _, m := range fifthColName.FindAllStringSubmatch(cols[5], -1) {
			checked++
			if !have[m[1]][m[2]] {
				t.Errorf("design.md 的 %s 第五列写着 %s_%s，"+
					"而本包里【没有】 TestInvariant%s_%s。\n"+
					"  ⇒ 第五列填了一个不存在的守卫名，读起来像【已经守住了】。\n"+
					"  ⇒ 还没做就写成「**空 —— <凭什么还没做>**」，和 C3b / D2b / G1 同形。",
					id, m[1], m[2], m[1], m[2])
			}
		}
	}
	if checked == 0 {
		t.Fatal("第五列里一个守卫名都没抓到 —— 要么列没了，要么正则不认得它的形状。" +
			"这时【不能】当成通过")
	}
	t.Logf("查了第五列里的 %d 个守卫名", checked)
}
