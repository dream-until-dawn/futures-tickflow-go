package derived

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

// —— 本包「只读不写」的三道守卫（design.md §十五「开工条件二的回答」四格之二） ——
//
// 三道各管一层：
//
//	签名层  TestDerivedTakesDataNotProviders  导出函数的参数里不许出现提供者
//	调用层  TestDerivedNeverCallsStoreWriters  非测试源码里不许出现写库的方法名
//	导入层  TestDerivedImportsOnlyAllowed      非测试源码只许 import 白名单里的包（本仓根包 · continuous · 标准库纯计算包）
//
// ⛔ 导入层是评审方 2026-09-17 打 D2/D3 打出来的：签名层只认 root 的提供者名、store/ source/ 下的类型、接口与 any；
// 调用层只认三个写库方法名 ⇒ 一个 `*http.Client` 参数（具体类型、不在 store/ source/ 下）或一次 `http.Get`，
// **前两道都看不见** —— 而输入约定写的是「只收数据、不自己去取」，`*http.Client` 恰恰是最直接的「自己去取」。
//
// ⛔ 它第一版是**禁表**（禁 net · net/… · source/ store/ refdata/），评审方随即打了 R1–R3，全绿：
// import github.com/coder/websocket（**本仓自己就在用它连天勤**）· crypto/tls · os/exec ⇒ 禁表的形状决定了它永远在追。
// ⇒ 翻成**白名单**：失败方向由「漏报 ⇒ 静默」换成「误伤 ⇒ 当场红、加一条带理由的白名单项」—— 本仓一贯取吵。
//
// 每一道的判法都**先在合成源码上出声**（TestReadonlyCheckersThemselves），再拿去判本包 ——
// 判法与对照组共用同一个函数（本仓那条：对照组验的必须是本体）。
//
// ⚠️ 射程（三道加起来仍守不住的，写在这儿免得有人以为「只读」被机械地保证了）：
//
//	判不了  调用方在外面写好库、再把结果递进来（那不是本包写的）
//	判不了  调用方递进来一个 func() []Bar 的闭包、闭包里联网 —— 那是回调，按设计放行
//	判不了  一个具体类型里藏着提供者字段 —— 参数类型是具体的，签名层看不见
//	判不了  经一个名字不同的方法转手去写（调用层按名字判）
//	判不了  按名字反射调用写方法（reflect…MethodByName("CommitSpan")）—— 评审方 D5；不为它加守卫：按字符串猜方法名，误伤面大
//	（原先那条「判不了：经 os 直接读写库目录」—— 白名单里没有 os ⇒ 现在挡住了）
//	不认    非导出函数（签名层）与 _test.go（三道都不认：测试要造输入）

// providerNames 是 tickflow 根包里「能去取数 / 能去写」的那几个类型名。
var providerNames = map[string]bool{
	"Store":         true,
	"Calendar":      true,
	"Source":        true,
	"SourceFactory": true,
}

// storeWriters 是写库的方法名。
var storeWriters = map[string]bool{
	"AppendBars":      true,
	"CommitSpan":      true,
	"DiscardCoverage": true,
}

const modulePath = "github.com/dream-until-dawn/futures-tickflow-go"

// allowedImports 是 derived 非测试源码**只许** import 的包。⛔ 每加一条都要写理由：它为什么不能取数、不能写。
var allowedImports = map[string]string{
	modulePath:                 "本仓根包：类型层（Bar · TradingDay · Symbol …）；它也声明 Store / Calendar 接口，而【收】它们由签名层挡",
	modulePath + "/continuous": "主连拼接：只收数据不收提供者（它自己有同形守卫）；derived 要吃它的 Days / NoPick / Rolls",
	"errors":                   "标准库纯计算：哨兵",
	"fmt":                      "标准库纯计算：报文",
	"sort":                     "标准库纯计算：排序",
	"strings":                  "标准库纯计算：拼报文",
	"time":                     "标准库：墙钟换算（判夜盘时刻）；⚠️ 它也能 time.Now() —— 取「现在」不是取数，放行",
	"math":                     "标准库纯计算",
}

// disallowedImports 交出这个文件里**不在白名单里**的 import 路径。它**共用**给守卫与对照组。
func disallowedImports(f *ast.File) []string {
	var out []string
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			out = append(out, im.Path.Value+"（解析不了的 import 路径 ⇒ 按不许算）")
			continue
		}
		if _, ok := allowedImports[p]; !ok {
			out = append(out, p)
		}
	}
	return out
}

// providerParams 交出这个文件里【导出函数 / 方法】参数中像提供者的类型，形如「F 收了 tickflow.Store」。
//
// 像提供者 ＝ 以下任一：
//
//	tickflow 根包的 Store / Calendar / Source / SourceFactory
//	本模块 store/ 或 source/ 下任何包里的任何类型（它们要么是提供者本身，要么带写方法）
//	本文件声明的接口 · 字面量接口 · any
//
// 它**共用**给守卫与对照组。
func providerParams(f *ast.File) []string {
	// 本文件里 import 的别名 → 路径
	alias := map[string]string{}
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if im.Name != nil {
			name = im.Name.Name
		}
		alias[name] = p
	}
	ifaces := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok {
			if _, isIface := ts.Type.(*ast.InterfaceType); isIface {
				ifaces[ts.Name.Name] = true
			}
		}
		return true
	})

	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || !fn.Name.IsExported() || fn.Type.Params == nil {
			continue
		}
		for _, p := range fn.Type.Params.List {
			ast.Inspect(p.Type, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.InterfaceType:
					out = append(out, fn.Name.Name+" 收了 interface{…}")
				case *ast.SelectorExpr:
					x, ok := v.X.(*ast.Ident)
					if !ok {
						return true
					}
					path := alias[x.Name]
					switch {
					case path == modulePath && providerNames[v.Sel.Name]:
						out = append(out, fn.Name.Name+" 收了 "+x.Name+"."+v.Sel.Name)
					case strings.HasPrefix(path, modulePath+"/store/") || strings.HasPrefix(path, modulePath+"/source/"):
						out = append(out, fn.Name.Name+" 收了 "+x.Name+"."+v.Sel.Name+"（"+path+"）")
					}
					return false // 选择子里的 Sel 不再当成本地标识符去比
				case *ast.Ident:
					if ifaces[v.Name] || v.Name == "any" {
						out = append(out, fn.Name.Name+" 收了 "+v.Name)
					}
				}
				return true
			})
		}
	}
	return out
}

// writerCalls 交出这个文件里出现的写库方法名（选择子或裸标识符都算）。它**共用**给守卫与对照组。
func writerCalls(f *ast.File) []string {
	// ⚠️ 选择子 s.AppendBars 里的 AppendBars 会被 Inspect 访问两次（一次作为 SelectorExpr、一次作为它的 Sel 标识符）⇒
	// 每一处只数一次：凡是出现过的标识符节点按指针去重。
	seen := map[*ast.Ident]bool{}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		var id *ast.Ident
		switch v := n.(type) {
		case *ast.SelectorExpr:
			id = v.Sel
		case *ast.Ident:
			id = v
		}
		if id != nil && storeWriters[id.Name] && !seen[id] {
			seen[id] = true
			out = append(out, id.Name)
		}
		return true
	})
	return out
}

// sourceFiles 解析本包的非测试源码。解析不了就不许判「没有」。
func sourceFiles(t *testing.T) []*ast.File {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []*ast.File
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if perr != nil {
			t.Fatalf("%s 解析失败：%v —— 解析不了就不许判「没有」", name, perr)
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		t.Fatal("本包一个非测试源文件都没扫到 —— 守卫在空转，读数作废")
	}
	return out
}

func TestDerivedTakesDataNotProviders(t *testing.T) {
	checked, hits := 0, 0
	for _, f := range sourceFiles(t) {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.IsExported() {
				checked++
			}
		}
		for _, bad := range providerParams(f) {
			hits++
			t.Errorf("%s —— 本包只收【数据】，不收【提供者】。\n"+
				"  ⇒ 拿到能取数 / 能写的东西，本包就能绕过调用方自己去问库、甚至去改库；\n"+
				"    而让判据去写它判的那份数据，正是「自指环」最怕的形状（design.md §十五「开工条件二的回答」）。",
				bad)
		}
	}
	// 中间量印出来：突变表里「施加前 → 施加后」那一列读的就是它
	t.Logf("签名层：查了 %d 个导出函数，命中 %d 处", checked, hits)
	if checked == 0 {
		t.Fatal("一个导出函数都没查到 —— 守卫在空转，读数作废")
	}
}

func TestDerivedNeverCallsStoreWriters(t *testing.T) {
	files, hits := sourceFiles(t), 0
	defer func() { t.Logf("调用层：查了 %d 个源文件，命中 %d 处", len(files), hits) }()
	for _, f := range files {
		for _, w := range writerCalls(f) {
			hits++
			t.Errorf("本包非测试源码里出现了写库方法 %s —— derived 只读不写（design.md §十五「开工条件二的回答」）。\n"+
				"  ⇒ 洞是库的缺陷，另立登记；不借 derived 去补。", w)
		}
	}
}

func TestDerivedImportsOnlyAllowed(t *testing.T) {
	files, hits := sourceFiles(t), 0
	defer func() { t.Logf("导入层：查了 %d 个源文件，白名单外命中 %d 处", len(files), hits) }()
	for _, f := range files {
		for _, p := range disallowedImports(f) {
			hits++
			t.Errorf("本包非测试源码 import 了白名单外的 %q —— derived 只收数据、不自己去取（design.md §十五「derived 的输入约定」与「开工条件二的回答」）。\n"+
				"  ⇒ 要数据，由调用方取好、摊平成快照递进来。\n"+
				"  ⇒ 真的只是纯计算 ⇒ 加进 allowedImports，并写清它为什么不能取数、不能写。", p)
		}
	}
}

// TestReadonlyCheckersThemselves 是三道守卫的对照组 —— 判法先在已知答案上出声，**命中数要精确**，不只是「报没报」。
func TestReadonlyCheckersThemselves(t *testing.T) {
	const tf = `import tickflow "github.com/dream-until-dawn/futures-tickflow-go"` + "\n"
	sig := []struct {
		name string
		src  string
		want int
	}{
		{"收 tickflow.Store", "package p\n" + tf + "func F(st tickflow.Store) {}\n", 1},
		{"收 tickflow.Calendar", "package p\n" + tf + "func F(c tickflow.Calendar) {}\n", 1},
		{"收 *segfile.Store（store/ 下的包）", "package p\nimport \"github.com/dream-until-dawn/futures-tickflow-go/store/segfile\"\nfunc F(s *segfile.Store) {}\n", 1},
		{"收 shinnysource.Config（source/ 下的包）", "package p\nimport \"github.com/dream-until-dawn/futures-tickflow-go/source/shinnysource\"\nfunc F(c shinnysource.Config) {}\n", 1},
		{"收 any", "package p\nfunc F(x any) {}\n", 1},
		{"收字面量接口", "package p\nfunc F(x interface{ W() }) {}\n", 1},
		{"收本文件接口", "package p\ntype W interface{ W() }\nfunc F(w W) {}\n", 1},
		{"收别名导入的 Store", "package p\nimport tf \"github.com/dream-until-dawn/futures-tickflow-go\"\nfunc F(s tf.Store) {}\n", 1},
		{"两个参数都是提供者 ⇒ 2 处", "package p\n" + tf + "func F(s tickflow.Store, c tickflow.Calendar) {}\n", 2},

		{"收 tickflow.TradingDay（数据）", "package p\n" + tf + "func F(d tickflow.TradingDay) {}\n", 0},
		{"收 []tickflow.Bar（数据）", "package p\n" + tf + "func F(bs []tickflow.Bar) {}\n", 0},
		{"收回调", "package p\nfunc F(cb func(int) bool) {}\n", 0},
		{"非导出函数收 Store（不认）", "package p\n" + tf + "func f(st tickflow.Store) {}\n", 0},
		{"别的模块里同名的 Store（不是本模块）", "package p\nimport x \"example.com/other\"\nfunc F(s x.Store) {}\n", 0},
	}
	call := []struct {
		name string
		src  string
		want int
	}{
		{"调 AppendBars", "package p\nfunc f(s S) { s.AppendBars(nil) }\n", 1},
		{"调 CommitSpan", "package p\nfunc f(s S) { s.CommitSpan() }\n", 1},
		{"取方法值 DiscardCoverage", "package p\nfunc f(s S) { g := s.DiscardCoverage; _ = g }\n", 1},
		{"两处调用 ⇒ 2 处", "package p\nfunc f(s S) { s.AppendBars(nil); s.CommitSpan() }\n", 2},
		{"同一方法连调两次 ⇒ 2 处", "package p\nfunc f(s S) { s.AppendBars(nil); s.AppendBars(nil) }\n", 2},
		{"接口里声明 CommitSpan 方法 ⇒ 1 处（声明它本身就是一条去写的路）", "package p\ntype W interface{ CommitSpan() }\n", 1},
		{"声明加调用 ⇒ 2 处（两处各数一次）", "package p\nfunc f(s interface{ CommitSpan() }) { s.CommitSpan() }\n", 2},
		{"只读方法 Walk", "package p\nfunc f(s S) { s.Walk() }\n", 0},
		{"注释里提到 AppendBars（不算）", "package p\n// AppendBars 是库的写方法\nfunc f() {}\n", 0},
	}
	imp := []struct {
		name string
		src  string
		want int
	}{
		{"import net/http", "package p\nimport \"net/http\"\n", 1},
		{"import github.com/coder/websocket（本仓自己在用的 ws 客户端，R1）", "package p\nimport \"github.com/coder/websocket\"\n", 1},
		{"import crypto/tls（R2）", "package p\nimport \"crypto/tls\"\n", 1},
		{"import os/exec（R3）", "package p\nimport \"os/exec\"\n", 1},
		{"import os（直接读写库目录）", "package p\nimport \"os\"\n", 1},
		{"import 本仓 source/shinnysource", "package p\nimport \"github.com/dream-until-dawn/futures-tickflow-go/source/shinnysource\"\n", 1},
		{"import 本仓 store/segfile", "package p\nimport \"github.com/dream-until-dawn/futures-tickflow-go/store/segfile\"\n", 1},
		{"import 本仓 refdata/shinnyref", "package p\nimport \"github.com/dream-until-dawn/futures-tickflow-go/refdata/shinnyref\"\n", 1},
		{"import 本仓 calendar/embedded（derived 收的是摊平的快照，不是日历实现）", "package p\nimport \"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded\"\n", 1},
		{"分组里白名单外两个 ⇒ 2 处", "package p\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"os/exec\"\n)\n", 2},
		{"别名导入 net/http 也算", "package p\nimport h \"net/http\"\n", 1},
		{"点导入与下划线导入也算 ⇒ 2 处", "package p\nimport (\n\t. \"net/http\"\n\t_ \"crypto/tls\"\n)\n", 2},

		{"import 本仓根包", "package p\n" + tf, 0},
		{"import 本仓 continuous", "package p\nimport \"github.com/dream-until-dawn/futures-tickflow-go/continuous\"\n", 0},
		{"import errors/fmt/math/sort/strings/time", "package p\nimport (\n\t\"errors\"\n\t\"fmt\"\n\t\"math\"\n\t\"sort\"\n\t\"strings\"\n\t\"time\"\n)\n", 0},
		{"注释里写 net/http（不算）", "package p\n// 这里不许 import \"net/http\"\n", 0},
	}
	// 白名单自检：本包当前真实用到的 import 必须全在白名单里（否则上面「不报」那几格证明不了今天的本包能过）
	for _, p := range []string{"errors", "fmt", modulePath} {
		if _, ok := allowedImports[p]; !ok {
			t.Errorf("白名单缺了本包今天就在用的 %q", p)
		}
	}
	fset := token.NewFileSet()
	for _, c := range imp {
		f, err := parser.ParseFile(fset, "x.go", c.src, 0)
		if err != nil {
			t.Fatalf("导入层 %s：解析失败 %v", c.name, err)
		}
		if got := disallowedImports(f); len(got) != c.want {
			t.Errorf("导入层 %s：命中 %d 处 %v，期望 %d", c.name, len(got), got, c.want)
		}
	}
	for _, c := range sig {
		f, err := parser.ParseFile(fset, "x.go", c.src, 0)
		if err != nil {
			t.Fatalf("签名层 %s：解析失败 %v", c.name, err)
		}
		if got := providerParams(f); len(got) != c.want {
			t.Errorf("签名层 %s：命中 %d 处 %v，期望 %d", c.name, len(got), got, c.want)
		}
	}
	for _, c := range call {
		f, err := parser.ParseFile(fset, "x.go", c.src, 0)
		if err != nil {
			t.Fatalf("调用层 %s：解析失败 %v", c.name, err)
		}
		if got := writerCalls(f); len(got) != c.want {
			t.Errorf("调用层 %s：命中 %d 处 %v，期望 %d", c.name, len(got), got, c.want)
		}
	}
}
