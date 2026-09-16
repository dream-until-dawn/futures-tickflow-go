package continuous

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— 比「包里不出现 Calendar」更接近本质的那一条：**本包只收数据，不收提供者** ——
//
// ⛔ 由来（评审方 2026-09-16 打的 C4 格）：按名字判的守卫，被「本包自己重抄一个同形接口」绕过去 ——
// 名字不带 Calendar、不 import，守卫全绿。⇒ 那一格说明**按名字判到不了本质**。
//
// 本质是：**本包不许拿到一个「能去取数」的东西**。拿到了，它就能绕过调用方自己去问日历 / 库 / 源，
// 而 derived 要拿本包的输出去判夜盘 —— 一旦本包自己能取数，自指环就可能从那条路回来。
//
// 判据（评审方给的形状，我落地）：**导出函数 / 方法的参数类型里不许出现接口类型**（`any` 也不行）。
// 只收具体类型（切片 · 结构体 · 基本类型）与**函数值**（`func(...)` 是回调，不是提供者）。
//
// ⛔ 例外要带理由，而理由要说清**它为什么不是提供者**：
//
//	RollRule / CountingRule / AxisAware  是**策略**：它们不提供数据，只对【已经递到手里的】数据做判定
//	                                     ⇒ 判据不是「不许接口」，是「不许【能去取数】的接口」
//
// ⚠️ 射程（写清楚，别让它看起来比判据宽）：
//
//	判不了  具体类型自己藏着一个提供者字段（例如某个 struct 里放了 Store）—— 参数类型是具体的，这条守卫看不见
//	判不了  包级变量里放一个提供者（不经过参数）
//	不认    非导出函数与 _test.go（测试要造输入）
func TestContinuousTakesDataNotProviders(t *testing.T) {
	// 例外表：**每一条都要写为什么它不是提供者**。
	allow := map[string]string{
		"RollRule":     "策略，不是提供者：它不取数，只在【已经递到手里的】候选里做判定",
		"CountingRule": "同上，外加一层自述（它数到了哪一段）",
		"AxisAware":    "同上：Build 把已经在手的天轴递给它，不是给它一个能去取数的东西",
	}

	ifaces, err := declaredInterfaces(".")
	if err != nil {
		t.Fatal(err)
	}
	// 前提自检：本包确实声明了接口，否则下面整圈在问一张空表。
	if len(ifaces) == 0 {
		t.Fatal("本包一个接口都没扫到 —— 捞法多半坏了，读数作废")
	}

	fset := token.NewFileSet()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if perr != nil {
			t.Fatalf("%s 解析失败：%v —— 解析不了就不许判「没有提供者」", name, perr)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Type.Params == nil {
				continue
			}
			checked++
			for _, p := range fn.Type.Params.List {
				for _, bad := range providerish(p.Type, ifaces) {
					if why := allow[bad]; why != "" {
						continue
					}
					t.Errorf("%s 的导出函数 %s 收了一个接口类型的参数 %s —— 本包只收【数据】，不收【提供者】。\n"+
						"  ⇒ 拿到能取数的东西，本包就能绕过调用方自己去问日历/库/源，而那条路把自指环放回来了。\n"+
						"  ⇒ 真的是策略（只判定、不取数）⇒ 加进本测试的例外表，并写清它为什么不是提供者。",
						name, fn.Name.Name, bad)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个带参数的导出函数都没查到 —— 守卫在空转，读数作废")
	}
}

// declaredInterfaces 收本包**自己声明的接口名**（非测试文件）。
func declaredInterfaces(dir string) (map[string]bool, error) {
	out := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			return nil, perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if _, isIface := ts.Type.(*ast.InterfaceType); isIface {
				out[ts.Name.Name] = true
			}
			return true
		})
	}
	return out, nil
}

// providerish 交出这个参数类型里「像提供者」的名字：本包声明的接口，或写成字面量的 interface{} / any。
//
// ⚠️ 它**共用**给守卫与对照组（本仓那条：对照组验的必须是本体）。
func providerish(t ast.Expr, ifaces map[string]bool) []string {
	var out []string
	ast.Inspect(t, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.InterfaceType:
			out = append(out, "interface{…}")
		case *ast.Ident:
			if ifaces[v.Name] || v.Name == "any" {
				out = append(out, v.Name)
			}
		}
		return true
	})
	return out
}

// TestNoProviderCheckerItself 是上面那条的对照组 —— 判法先在已知答案上出声。
func TestNoProviderCheckerItself(t *testing.T) {
	ifaces := map[string]bool{"RollRule": true, "Store": true}
	cases := []struct {
		name string
		src  string
		want bool // 期望「像提供者」
	}{
		{"收切片", "package p\nfunc F(xs []int) {}\n", false},
		{"收结构体", "package p\ntype S struct{}\nfunc F(s S) {}\n", false},
		{"收回调", "package p\nfunc F(cb func(int) bool) {}\n", false},
		{"收本包接口 Store", "package p\nfunc F(st Store) {}\n", true},
		{"收 any", "package p\nfunc F(x any) {}\n", true},
		{"收匿名接口", "package p\nfunc F(x interface{ Bars() []int }) {}\n", true},
		{"收策略 RollRule", "package p\nfunc F(r RollRule) {}\n", true}, // ⚠️ 判法报它；放行由例外表做，不在判法里
	}
	fset := token.NewFileSet()
	for _, c := range cases {
		f, err := parser.ParseFile(fset, "x.go", c.src, 0)
		if err != nil {
			t.Fatalf("%s：解析失败 %v", c.name, err)
		}
		bad := false
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Type.Params == nil {
				continue
			}
			for _, p := range fn.Type.Params.List {
				if len(providerish(p.Type, ifaces)) > 0 {
					bad = true
				}
			}
		}
		if bad != c.want {
			t.Errorf("%s：判定 %v，期望 %v", c.name, bad, c.want)
		}
	}
}
