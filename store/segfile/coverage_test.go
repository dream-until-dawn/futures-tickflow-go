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
	"C3b": "截断要进 SyncReport.TruncatedTails —— 而 `SyncReport` 还不存在" +
		"（上哪儿看：`tools/doccheck/pending.txt` 里 SyncReport 那一行）",
	"D2b": "两种结果都进 SyncReport（LegacyMetaDiscarded / LegacyMetaUnverified）—— " +
		"**与 C3b 同因：`SyncReport` 还不存在**" +
		"（上哪儿看：`tools/doccheck/pending.txt` 里 SyncReport 那一行）",
	"A4": "`.meta` 要存 `recordSize` 并与本版记录长比对 —— 而 `Meta` 结构体里" +
		"**还没有这个字段**（上哪儿看：`Meta`）。" +
		"它和 A3 同一个入口（`DecodeMeta`），补字段那一格一起做，" +
		"**别单独加一个只测一半的测试**。",
	"G1": "`Open` 要把 `.meta` 里的全量参数与请求的参数比对 —— 而 `Open` 现在只认目录，" +
		"**参数还没有被传进来**（上哪儿看：`func Open` 的签名）。" +
		"⚠️ 比对这件事**就在这一层**（segfile 的 `Open`），缺的是它的【输入】；" +
		"参数从哪来由「目录命名规则」那一节定，那一格还没做。",
}

// ⚠️ 上面四条为什么都写成【条件】而不是【版本标签】（评审方 2026-09-09 撤回了他自己
// 「写有效期」的要求，理由比原来的写法准）：
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
var (
	tableRow = regexp.MustCompile(`^\| ([A-Z][0-9]+[a-z]?) \|`)
	testName = regexp.MustCompile(`^TestInvariant([A-Z][0-9]+[a-z]?)_(Red|Green)$`)
)

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
					"而本包里**没有** TestInvariant%s_%s。\n"+
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
