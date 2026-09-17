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

// —— 本包「只读不写」的两道守卫（design.md §十五「开工条件二的回答」四格之二） ——
//
// 两道各管一层：
//
//	签名层  TestDerivedTakesDataNotProviders  导出函数的参数里不许出现提供者
//	调用层  TestDerivedNeverCallsStoreWriters  非测试源码里不许出现写库的方法名
//
// 每一道的判法都**先在合成源码上出声**（TestReadonlyCheckersThemselves），再拿去判本包 ——
// 判法与对照组共用同一个函数（本仓那条：对照组验的必须是本体）。
//
// ⚠️ 射程（两道加起来仍守不住的，写在这儿免得有人以为「只读」被机械地保证了）：
//
//	判不了  调用方在外面写好库、再把结果递进来（那不是本包写的）
//	判不了  一个具体类型里藏着提供者字段 —— 参数类型是具体的，签名层看不见
//	判不了  经一个名字不同的方法转手去写（调用层按名字判）
//	不认    非导出函数与 _test.go（测试要造输入）

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

// TestReadonlyCheckersThemselves 是两道守卫的对照组 —— 判法先在已知答案上出声，**命中数要精确**，不只是「报没报」。
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
	fset := token.NewFileSet()
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
