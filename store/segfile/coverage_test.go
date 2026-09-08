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
	"C3b": "截断要进 SyncReport.TruncatedTails —— 而 SyncReport 还不存在（tools/doccheck/pending.txt:59）",
	"D2b": "两种结果都进 SyncReport（LegacyMetaDiscarded / LegacyMetaUnverified）—— 同上",
}

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
// ⚠️ 而 `A4` / `G1` 要进 absent 的那一半**不在这条分支上**：
// 那两个编号是另一条分支往 `design.md` 里加的，本分支的表里还没有它们，
// **现在就登记进 absent，这条测试会立刻红**（它双向查：absent 里的编号必须在表里）。
//
//	⇒ 这两件事是【一格】，被切到了两条分支上。而它不需要靠人记得：
//	  合并的那一刻，A4 会让这条测试红，红的信息里就写着要补什么。
//	  **一条跨分支的约束，分支上的绿灯看不见它 —— 看得见的是合并。**
var (
	tableRow = regexp.MustCompile(`^\| ([A-Z][0-9][a-z]?) \|`)
	testName = regexp.MustCompile(`^TestInvariant([A-Z][0-9][a-z]?)_(Red|Green)$`)
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
	for _, ln := range strings.Split(string(b), "\n") {
		if m := tableRow.FindStringSubmatch(ln); m != nil {
			ids[m[1]] = true
		}
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
