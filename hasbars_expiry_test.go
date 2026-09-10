package tickflow_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/cffexsource"
	"github.com/dream-until-dawn/futures-tickflow-go/source/sinasource"
)

// guard: ㉒ 那条到期条件 —— 到期那天它自己红。
// TestHasBarsExpiryConditionNotYetDue 把 `HasBars` 那条到期条件从
// **只有人能跑**变成**会红的测试**。
//
// 被钉住的那句话（v0.4.0 注解 二②）：
//
//	`HasBars` 是 O(天数 × 根数)
//	到期条件：**出现第一个 `Caps().Periods` 含日内周期的源之前**
//
// ⛔ 它是 v0.4.0 注解里三条到期条件中**唯一没有机器求值法**的那一条
// （原来写的是「**读** source/ 下各源的 Periods 声明」）——
// 而它的失败方向与①同型：新加一个源、`Caps().Periods` 含日内周期
// ⇒ **不会有任何东西响**，要等有人想起来去读。
// （评审方 2026-09-10 在 v0.4 末检里指出；而让这个不齐一眼看得见的，
//
//	是注解把三条**并排**写了出来 —— **并排写本身就是一次检查**。）
//
// —— 这条测试判的是【甲】不是【乙】，而两者的差别要写清 ——
//
//	甲：有源**声明**了含日内周期的 `Caps().Periods`     ← 本测试判这个
//	乙：有源**实际能给**日内数据                        ← 才是那个复杂度真正被引爆的条件
//
// 选甲的理由三条：**可机器判**（②今天的毛病正是只有人能跑）·
// **失败方向是早报**（早报会把人叫来）· **甲是乙的必要条件**。
//
// ⛔ 而第三条**有一个前提，写在这儿**（评审方要求，我同意）：
// **它依赖「本仓只请求 `Caps()` 声明过的周期」。**
// 那个前提哪天不成立（有人绕过 `Caps` 直接取日内数据），
// 甲就不再是乙的必要条件 ⇒ **这条到期条件会【安静地】不响**。
// ⇒ 那一天要改的不是这条测试的阈值，是它判的那个命题。
//
// ⚠️ 而「早报会被当成噪音」那条本仓规矩（**一个长期误报的告警最终会关掉它自己**）
// **在这里不适用**：甲只在「有人新加一个日内源」时才响，
// 那是**罕见且总是值得看一眼**的事件。挑早报的前提是误报率低，这里满足。
//
// —— ⛔ 判据枚举【封闭】的那一侧，而不是「是不是 IntradayPeriod」——
//
// 第一版写的是 `p.(tickflow.IntradayPeriod)` —— 一个**具体类型断言**。
// 而本仓的两侧封闭性不对称（评审方 2026-09-10 指出，我复量）：
//
//	CalendarPeriod  Daily / Weekly / Monthly 一个 const 块 ⇒ **封闭**
//	IntradayPeriod  一个 struct，而「日内」这一侧**不封闭**
//
// ⇒ 第一版枚举的正是**不封闭**的那一侧。
//
// ⚠️ 而风险的射程要收窄：`Period` 是**密封接口**（`source.go`，`isPeriod()` 不导出）
// ⇒ **包外无法实现** ⇒ 不是「任何人都能加一个类型」。
// 🔴 **而收窄之后它更值得改**：在 `tickflow` 包内新增一个周期类型，
// **正是「日内落库」那天要做的事** —— 也就是这条到期条件该响的那一刻。
//
// 两法在今天所有取值上一致（Daily/Weekly/Monthly ⇒ 都判「没到期」；1m ⇒ 都判「到期」），
// **只在那个未来的新类型上分岔**：按具体类型判会**漏掉**它。
// ⇒ 所以判据反过来写：**不在 Daily/Weekly/Monthly 里就红。**
// ⚠️ 代价一并说：`CalendarPeriod` 那个 const 块变长时（例如将来加 `Quarterly`）要跟着改
// —— 而那会**红**，不会静默。**两边都要人动手，差别在哪一边是静默的。**
func TestHasBarsExpiryConditionNotYetDue(t *testing.T) {
	sources := sourcesUnderTest(t)
	products := embedded.Products()
	// 前提自检：尺子不能是空转的 —— 没有品种时下面的循环一格都不跑，而它照样绿。
	if len(products) == 0 {
		t.Fatal("一个品种都没有 —— 这条测试会空转，读数作废")
	}
	if len(sources) == 0 {
		t.Fatal("一个源都没有 —— 同上")
	}

	checked := 0
	for _, s := range sources {
		sawAny := false
		for _, k := range products {
			for _, p := range s.caps(k).Periods {
				sawAny = true
				checked++
				// ⛔ 判据枚举的是**封闭**的那一侧，而不是「是不是 IntradayPeriod」。
				// 理由见函数注释末尾那一段。
				if p != tickflow.Daily && p != tickflow.Weekly && p != tickflow.Monthly {
					t.Fatalf("源 %s 对 %v 声明了一个【不是日历周期】的周期 %v —— "+
						"【㉒ 那条到期条件到期了】。\n"+
						"处置不是把这条测试改掉，是：\n"+
						"  一、去看 HasBars 的 O(天数 × 根数)：日内落库之后它按最坏档约 1.9 小时/次\n"+
						"  二、决定是换算法还是加缓存，并把决定写进 docs/design.md\n"+
						"  三、再回来改这条测试与 v0.4.0 注解里那条到期条件的后继",
						s.name, k, p)
				}
			}
		}
		// 前提自检：一个源若一个周期都不声明，上面的循环什么也没检 —— 那不是「没到期」。
		if !sawAny {
			t.Fatalf("源 %s 对所有品种都没有声明任何周期 —— "+
				"这条测试在它身上是空转的，读数作废", s.name)
		}
	}
	t.Logf("检查了 %d 个（源 × 品种 × 周期）组合，全部落在 Daily/Weekly/Monthly 里", checked)
}

// guard: 那张手写源表的完整性 —— 新加源忘了进表就红。
// TestSourceTableCoversEveryCapsImplementor 关的是上面那条测试**自己写下的射程边界**：
// 那张源表是**手写**的 ⇒ 新加一个源而忘了加进表，上面那条不会响。
//
// ⛔ 而显然的办法（数 `source/` 下的目录）是错的，评审方量过、我复量：
//
//	source/ 下目录 = **3**（cffexsource · sinasource · **pacing**）
//	其中实现 Caps 的 = **2**
//	⇒ 目录数 ≠ 源数；而「排除 pacing」这件事本身又要手维护 ⇒ 问题只是换了个地方
//
// 🔴 本仓那条：**范围按性质划，别按目录划。**
// ⇒ 判据换成「**声明了 `Caps(tickflow.ProductKey) tickflow.Capabilities` 的类型有几个**」，
// 用 `go/ast` 扫，而不是数目录。
//
// ⚠️ 而它的盲点**本仓已经写过**，就在 `source.go` 那段「不要靠嵌入获得 isPeriod」旁边：
// **AST 扫的是「谁【声明】了它」，而契约要的是「谁【满足】它」** ——
// 一个靠**内嵌**拿到 `Caps`（提升方法）的类型没有这一行字面，**这条断言看不见它**。
// 今天本仓没有这种写法，而这句话要跟着判据走。
func TestSourceTableCoversEveryCapsImplementor(t *testing.T) {
	declared := countCapsDeclarations(t)
	if declared == 0 {
		t.Fatal("一处 Caps 声明都没扫到 —— 尺子坏了，读数作废")
	}
	if declared != len(sourcesUnderTest(t)) {
		t.Fatalf("本仓声明了 %d 处 `Caps(...)`，而 %s 里那张手写源表只有 %d 条。"+
			"  ⇒ 新加了源而没加进表的话，那条到期条件在它身上【不会响】。"+
			"  ⇒ 处置：把新源加进 sourcesUnderTest，别调这里的数。",
			declared, "hasbars_expiry_test.go", len(sourcesUnderTest(t)))
	}
}

// countCapsDeclarations 用 go/ast 数「声明了 Caps 那个签名」的方法有几个。
func countCapsDeclarations(t *testing.T) int {
	t.Helper()
	n := 0
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "tools", "__pycache__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if perr != nil {
			return perr
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "Caps" {
				continue
			}
			// 签名要对得上：一个入参、一个返回，且都是 tickflow.* 那两个类型
			if fd.Type.Params == nil || len(fd.Type.Params.List) != 1 ||
				fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
				continue
			}
			if exprName(fd.Type.Params.List[0].Type) == "ProductKey" &&
				exprName(fd.Type.Results.List[0].Type) == "Capabilities" {
				n++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫源码失败：%v", err)
	}
	return n
}

// exprName 取 `pkg.Name` 或 `Name` 的那个 Name。
func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// capsSource 是一个源在这两条测试里用得到的那一面。
type capsSource struct {
	name string
	caps func(tickflow.ProductKey) tickflow.Capabilities
}

// sourcesUnderTest 是**手写**的源表 —— 本仓今天没有「所有源」的注册表。
//
// ⚠️ 它的失败方向：**新加一个源而忘了加进来** ⇒ 上面那条到期条件在它身上不会响。
// ⇒ 而这一格**已经被 `TestSourceTableCoversEveryCapsImplementor` 关上了**：
//
//	它数「声明了 Caps 那个签名的类型有几个」，与这张表的长度对不上就红。
func sourcesUnderTest(t *testing.T) []capsSource {
	t.Helper()
	cal := testCalendarForCaps(t)
	return []capsSource{
		{"sinasource", newSinaForCaps(t, cal).Caps},
		{"cffexsource", newCffexForCaps(t, cal).Caps},
	}
}

func testCalendarForCaps(t *testing.T) tickflow.Calendar {
	t.Helper()
	// 两天足够 —— 这条测试只问 Caps()，不走日历上的任何一天。
	// ⚠️ 手列，不从别处抽 —— 同本仓 cffexsource 那条：拿数据造日历再用它验那份数据是循环论证。
	days := []tickflow.TradingDay{20260909, 20260910}
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatalf("构造日历失败：%v", err)
	}
	return cal
}

func newSinaForCaps(t *testing.T, cal tickflow.Calendar) *sinasource.Client {
	t.Helper()
	c, err := sinasource.New(cal)
	if err != nil {
		t.Fatalf("构造 sinasource 失败：%v", err)
	}
	return c
}

func newCffexForCaps(t *testing.T, cal tickflow.Calendar) *cffexsource.Client {
	t.Helper()
	c, err := cffexsource.New(cal)
	if err != nil {
		t.Fatalf("构造 cffexsource 失败：%v", err)
	}
	return c
}
