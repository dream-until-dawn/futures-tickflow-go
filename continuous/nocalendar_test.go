package continuous

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// —— 本包的硬约束：**不碰交易日历** —— 而这是它的承载体，不是那段包注释 ——
//
// ⛔ 由来（design.md §十五「v0.7 起手」＋「自指环」那两节）：
// derived 拿本包的输出判「那一夜开没开」，结论回头修正日历。本包若吃到 derived 修正过的那份日历，
// **判别符与被判别的状态就是同一个东西 ⇒「证伪」在环里做不到**。
//
// ⇒ 承载体选**守卫**不选注释，理由写死：注释挡不住「后来的人给 `Pick` 加一个 `cal` 参数」。
//
// 判据两条（评审方 2026-09-16 定，缺一条就漏）：
//
//	一  标识符：本包非测试代码里不出现 `Calendar`（类型 / 字段 / 参数 / 方法调用一律不许）
//	    ⛔ 用 go/ast 查，**不用 grep 文本** —— 文本会被注释与字符串骗（本文件自己就含这个词）
//	二  import：本包非测试代码不许 import `calendar/` 下的任何包
//	    ⛔ 只查标识符会漏掉「import 了，而变量名不叫 Calendar」
//
// ⚠️ 射程（写清楚，别让它看起来比判据宽）：
//
//	守不住  调用方在外面用日历筛过 bars 再喂进来 ⇒ 由 `Continuous.Days`（天轴交出去）接住
//	守不住  收进来的接口恰好带 `Calendar()` 方法**而我们不调用** —— 不调用就不构成依赖
//	不认    _test.go（测试要造输入，允许提到它；本文件自己就是）

// calendarMentions 收「这一份源码里对日历的依赖」：标识符命中与 import 命中各一份。
//
// ⛔ 它是**守卫与对照组共用的那一份**（评审方 2026-09-16 那条：对照组验的必须是本体，不是副本）——
// 判法哪天改了（例如加上处理别名 import），对照组跟着变，不会继续验一份过期的副本。
func calendarMentions(src string) (idents []string, imports []string, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}
	for _, im := range f.Imports {
		p, uerr := strconv.Unquote(im.Path.Value)
		if uerr != nil {
			return nil, nil, uerr
		}
		if strings.Contains(p, "/calendar/") || strings.HasSuffix(p, "/calendar") {
			imports = append(imports, p)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && strings.Contains(id.Name, "Calendar") {
			idents = append(idents, id.Name)
		}
		return true
	})
	return idents, imports, nil
}

// guard: continuous 包的非测试代码不碰交易日历（标识符与 import 两条）。
func TestContinuousDoesNotDependOnCalendar(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(".", name))
		if rerr != nil {
			t.Fatal(rerr)
		}
		n++
		ids, imps, perr := calendarMentions(string(b))
		if perr != nil {
			t.Fatalf("%s 解析失败：%v —— 解析不了就不许判「没依赖」", name, perr)
		}
		if len(ids) > 0 {
			t.Errorf("%s 里出现了 %v —— 本包不许碰交易日历。\n"+
				"  ⇒ derived 要拿本包的输出去判夜盘；本包吃到日历就成了自指环，【证伪在环里做不到】。\n"+
				"  ⇒ 真要那个信息，改从调用方传【数据本身】（如天轴 Days），不要传日历。", name, ids)
		}
		if len(imps) > 0 {
			t.Errorf("%s import 了 %v —— 本包不许 import calendar/ 下的包（同上）。", name, imps)
		}
	}
	// 前提自检（防空转）：一个非测试文件都没扫到时，上面整圈不跑而测试照绿。
	if n == 0 {
		t.Fatal("本包一个非测试 .go 都没扫到 —— 守卫在空转，读数作废")
	}
}

// TestCalendarMentionsCheckerItself 是上面那条的对照组：**判法自己先在已知答案上出声**。
//
// ⚠️ 它调的是 `calendarMentions` 本体（不是再抄一份），所以判法改了它会跟着动。
func TestCalendarMentionsCheckerItself(t *testing.T) {
	cases := []struct {
		name            string
		src             string
		wantIds, wantIm bool
	}{
		{"干净", "package p\n\nfunc f() int { return 1 }\n", false, false},
		{"参数里带 Calendar", "package p\n\nimport tickflow \"github.com/dream-until-dawn/futures-tickflow-go\"\n\nfunc f(cal tickflow.Calendar) {}\n", true, false},
		{"import 了而变量名不叫那个", "package p\n\nimport \"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded\"\n\nvar x = embedded.New\n", false, true},
		{"只在注释与字符串里提到", "package p\n\n// 这里写 Calendar 只是说明\nvar s = \"Calendar\"\n", false, false},
	}
	for _, c := range cases {
		ids, imps, err := calendarMentions(c.src)
		if err != nil {
			t.Fatalf("%s：解析失败 %v", c.name, err)
		}
		if got := len(ids) > 0; got != c.wantIds {
			t.Errorf("%s：标识符命中 %v（%v），期望 %v", c.name, got, ids, c.wantIds)
		}
		if got := len(imps) > 0; got != c.wantIm {
			t.Errorf("%s：import 命中 %v（%v），期望 %v", c.name, got, imps, c.wantIm)
		}
	}
}
