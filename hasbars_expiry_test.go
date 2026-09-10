package tickflow_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
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
// 一个靠**内嵌**拿到 `Caps`（提升方法）的类型没有这一行字面。
//
// ⛔ **而这条盲点的【方向】要写出来，光指一个位置不够**（评审方 2026-09-10 量的，我复现）：
// 造 `type Wrapper struct{ *sinasource.Client }`（本包一处 Caps 声明都没有，`go vet` 过）：
//
//	覆盖守卫 ⇒ **顶层红 0**，而它的日志是「Caps 实现 2 个，**与表逐个对上**」
//
// 🔴 **失败方向是【静默放行】，而且它出的是一份说「全对上了」的证据。**
// ⇒ 本仓那条再进一格：「一个产出证据的工具，失败方向必须是【不出证据】」——
// 这里它不是不出证据，**是出了一份令人安心的假证据**。
// ⇒ 所以下一个人读到这行绿时该知道：**它只覆盖【声明了 Caps】的那些**。
//
// ⚠️ 同一个盲点的另一面，**评审方推而未量、我也没量**（只报不断言）：
// `runtime.FuncForPC` 取到的是**声明方**的名字 ⇒ **两个源内嵌同一个 Caps 提供者时，
// 两条表项会报同一个名字 ⇒ 集合塌**。今天本仓没有这种写法。
func TestSourceTableCoversEveryCapsImplementor(t *testing.T) {
	declared := capsImplementors(t)
	if len(declared) == 0 {
		t.Fatal("一处 Caps 声明都没扫到 —— 尺子坏了，读数作废")
	}
	inTable := map[string]bool{}
	for _, s := range sourcesUnderTest(t) {
		inTable[implOf(t, s.caps)] = true
	}
	for impl := range declared {
		if !inTable[impl] {
			t.Fatalf("本仓声明了 %s 的 Caps，而那张手写源表里没有它。"+
				"⇒ 新加了源而没加进表的话，那条到期条件在它身上【不会响】。"+
				"⇒ 处置：把它加进 sourcesUnderTest，别动这里的判据。", impl)
		}
	}
	for impl := range inTable {
		if !declared[impl] {
			t.Fatalf("表里有 %s，而本仓没有它的 Caps 声明 —— "+
				"表里那一行指向的不是一个源（或者它靠内嵌拿到 Caps，见上面那段盲点）。", impl)
		}
	}
	t.Logf("Caps 实现 %d 个，与表逐个对上：%v", len(declared), keysOf(declared))
}

// implOf 从一个**方法值**里取出它属于哪个实现，形如 `sinasource.Client`。
//
// ⛔ 它存在的理由是【必改】：上一版断言的是 `len(表) == 声明数` —— **一个计数** ——
// 而守卫自己的报文说的是**覆盖**。评审方四格实测，我复现：
//
//	① 表里把 cffexsource 换成第二个 sinasource ⇒ 计数仍 2 ⇒ **两条守卫都绿**
//	② ① ＋ 让 cffexsource 真的声明日内周期     ⇒ **㉒ 真的到期了，而全仓一条不红**
//	③ 对照：只让 cffexsource 到期、表不动       ⇒ 到期条件红 ✅（尺子是好的）
//
// 🔴 **守卫声称的是【覆盖】，断言的却是【个数】** —— 与上一颗那个标记同型：
// **招牌好处在它写成的样子上不成立。**
// ⚠️ 而①不是刁钻构造：**照着表里已有那行复制一份当新源的模板、忘了换构造函数**，产出的正是①。
//
// ⇒ 而修法比「表里再加一个手写字段」硬一格：**实现名从函数值本身取** ——
// `runtime.FuncForPC` 给出 `pkg.(*Type).Method-fm` ⇒ **那张表说不了谎**。
func implOf(t *testing.T, f func(tickflow.ProductKey) tickflow.Capabilities) string {
	t.Helper()
	full := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	// 形如 …/source/sinasource.(*Client).Caps-fm
	name := strings.TrimSuffix(full, "-fm")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		t.Fatalf("取不出实现名：%q —— 这条判据依赖方法值的形状，而它变了", full)
	}
	recv := strings.Trim(parts[len(parts)-2], "(*)")
	return parts[0] + "." + recv
}

// capsImplementors 用 go/ast 收「声明了 Caps 那个签名」的 `包名.接收者类型`。
//
// ⚠️ 收的是**集合**不是个数 —— 见 implOf 上面那段。
func capsImplementors(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
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
			if !ok || fd.Recv == nil || fd.Name.Name != "Caps" || len(fd.Recv.List) != 1 {
				continue
			}
			if fd.Type.Params == nil || len(fd.Type.Params.List) != 1 ||
				fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
				continue
			}
			if exprName(fd.Type.Params.List[0].Type) != "ProductKey" ||
				exprName(fd.Type.Results.List[0].Type) != "Capabilities" {
				continue
			}
			out[f.Name.Name+"."+exprName(fd.Recv.List[0].Type)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫源码失败：%v", err)
	}
	return out
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// exprName 取 `pkg.Name` 或 `Name` 的那个 Name。
func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.StarExpr:
		return exprName(v.X)
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
