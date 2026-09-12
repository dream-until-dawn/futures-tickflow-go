package tickflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// —— 这一条守的是【`SyncRequest` 上不许有没人读的字段】——
//
// ⛔ 由来是一个真的：`Force bool`（「忽略 coverage 强制重拉该段」）在结构体上待了几个版本，
// **而没有任何代码读它** ⇒ 设成 `true` 什么都不会发生，**而且不报错**。
// 用户 2026-09-12 的处置是**删掉它**，理由是「设不了，就不会被静默忽略」。
//
// 🔴 而删掉之后有一格空着：**没有任何机器拦得住它被加回来。**
// 实测（2026-09-12，我在这一片里量的）：把 `Force bool` 只加回 `syncer.go`、文档里不写，
//
//	go run ./tools/doccheck ⇒ rc=0，仍然印「一致」
//
// ⇒ 因为那道检查守的是**「文档里声明过的，源码里要有」**，
// **反方向它不看** —— 源码上多一个没人读、也没写进文档的字段，它一声不响。
// 📎 本仓那条（**一个不会红的删除，是最难被拦住的那一种删除**）在这里是它的**镜像**：
// 🔴 **一个不会红的【添加】，是最难被拦住的那一种添加** ——
// 而它比删除更容易发生：加一个字段是「先把口子留着」，看起来什么都没破坏。
//
// —— 判据（写明它比什么，以及它【不】比什么）——
//
// 它只看**形参名为 `req`、类型是 `SyncRequest`** 的那些函数的函数体里的 `req.<字段>`。
// ⛔ **不**用 `grep "\.From"` 那种写法：`Span`、`SyncReport` 上都有 `From`/`To`，
// 实测那把尺子给 `From` 数出 **58** 处、给 `To` 数出 **47** 处，而它们横跨十个文件、
// 大多与 `SyncRequest` 无关 —— **一把会把别人的字段数进来的尺子，在【某字段无人读】这件事上恒绿。**
// ⚠️ 射程：它不认 `r := req` 之后的 `r.X`，也不认 `req` 被整个传走之后在别处的读取。
// ⇒ 那些写法会让这条守卫**误报**（说「没人读」而其实读了）——
// 误报的方向是**吵**，不是哑，所以可以接受；真出现时改这把尺子，别改被测方。
const syncReqParamName = "req"

// syncRequestFields 回 `SyncRequest` 的字段名（按字母序）。
func syncRequestFields(files []*ast.File) []string {
	var out []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "SyncRequest" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fl := range st.Fields.List {
				for _, id := range fl.Names {
					out = append(out, id.Name)
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// isSyncRequestType 判一个形参的类型写法是不是 SyncRequest（值、指针、带包名三种都认）。
func isSyncRequestType(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == "SyncRequest"
	case *ast.StarExpr:
		return isSyncRequestType(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name == "SyncRequest"
	}
	return false
}

// syncRequestFieldReads 数每个字段的读取点，并回「检查了几个函数」——
// 后者是**前提自检**：一个函数都没找到时，所有计数都是 0，而那不是「没人读」，是**尺子没对准**。
func syncRequestFieldReads(files []*ast.File) (map[string]int, int) {
	hits := map[string]int{}
	funcs := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Type.Params == nil {
				return true
			}
			carries := false
			for _, p := range fn.Type.Params.List {
				if !isSyncRequestType(p.Type) {
					continue
				}
				for _, id := range p.Names {
					if id.Name == syncReqParamName {
						carries = true
					}
				}
			}
			if !carries {
				return true
			}
			funcs++
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				se, ok := m.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := se.X.(*ast.Ident); ok && x.Name == syncReqParamName {
					hits[se.Sel.Name]++
				}
				return true
			})
			return true
		})
	}
	return hits, funcs
}

// parseRepoNonTest 解析全仓的非测试 .go。
func parseRepoNonTest(t *testing.T) ([]*ast.File, int) {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "__pycache__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return perr
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatalf("走一遍仓失败：%v", err)
	}
	return files, len(files)
}

// guard: SyncRequest 上每个字段都得有代码读它 —— 一个没人读的字段是一句假承诺。
func TestEverySyncRequestFieldIsRead(t *testing.T) {
	// 标定源码：`Force` 无人读，`From` 有人读。两格标定共用它。
	const calib = `package p
type SyncRequest struct {
	From  int
	Force bool
}
func run(req SyncRequest) int { return req.From }
`

	t.Run("标定 一个确实没人读的字段_报得出", func(t *testing.T) {
		// ⛔ 这一格是这把尺子唯一能被证伪的时刻：
		// 少了它，「全仓每个字段都有人读」与「尺子一个函数都没看」在结果上是同一个字节。
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "zz_calib.go", calib, 0)
		if err != nil {
			t.Fatalf("标定源码解析失败：%v", err)
		}
		files := []*ast.File{f}
		if got := syncRequestFields(files); len(got) != 2 {
			t.Fatalf("标定源码里 SyncRequest 应有 2 个字段，尺子数出 %v", got)
		}
		hits, funcs := syncRequestFieldReads(files)
		if funcs != 1 {
			t.Fatalf("标定源码里带 req SyncRequest 的函数应有 1 个，尺子数出 %d", funcs)
		}
		if hits["Force"] != 0 {
			t.Errorf("Force 在标定源码里没人读，而尺子给了 %d 处 ⇒ 它会漏掉真的假承诺", hits["Force"])
		}
	})

	t.Run("标定 被读到的字段_不算没人读", func(t *testing.T) {
		// ⇒ 这一格与上一格是一对：**同一把尺子，对两种输入给出不同的答案**。
		// 少了它，一把「永远回 0」的尺子会让上一格绿。
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "zz_calib.go", calib, 0)
		if err != nil {
			t.Fatalf("标定源码解析失败：%v", err)
		}
		hits, _ := syncRequestFieldReads([]*ast.File{f})
		if hits["From"] != 1 {
			t.Errorf("From 在标定源码里被读 1 处，而尺子给了 %d 处 ⇒ 尺子照不到读取点，"+
				"下面那一格的「都有人读」没有真值", hits["From"])
		}
	})

	t.Run("被测 全仓每个字段都有人读", func(t *testing.T) {
		files, n := parseRepoNonTest(t)
		// ⛔ 前提自检一：真的扫到了东西。
		if n < 10 {
			t.Fatalf("只扫到 %d 个非测试 .go ⇒ 走法不对（跑的目录？）⇒ 读数作废", n)
		}
		fields := syncRequestFields(files)
		// ⛔ 前提自检二：`SyncRequest` 得找得到，而且字段不为空。
		// 🔴 「结构体找不到」与「每个字段都有人读」在循环上同样是【一次都不失败】。
		if len(fields) == 0 {
			t.Fatalf("在全仓里一个 SyncRequest 字段都没找着 ⇒ 尺子没对准，读数作废")
		}
		hits, funcs := syncRequestFieldReads(files)
		// ⛔ 前提自检三：真的有函数收 `req SyncRequest`。
		if funcs == 0 {
			t.Fatalf("一个「形参 req 类型 SyncRequest」的函数都没找着 ⇒ 是不是改了形参名？"+
				"（这把尺子按 %q 认）⇒ 读数作废", syncReqParamName)
		}
		var dead []string
		for _, f := range fields {
			if hits[f] == 0 {
				dead = append(dead, f)
			}
		}
		if len(dead) != 0 {
			t.Errorf("SyncRequest 有 %d 个字段没有任何代码读它：%s\n"+
				"  ⇒ 调用方设得了它，而设了什么都不会发生，【并且不报错】——那是一句假承诺。\n"+
				"  ⇒ 这正是 Force 的那一格（2026-09-12 删除）：它在结构体上待了几个版本，\n"+
				"     而 doccheck 看不见 —— 那道检查守的是「文档里声明过的源码里要有」，反方向它不看。\n"+
				"  ⇒ 两条出路，选一条：①【同一颗提交里】就写上读它的那一行；②别加这个字段。",
				len(dead), strings.Join(dead, " "))
		}
		var line []string
		for _, f := range fields {
			line = append(line, f+"="+strconv.Itoa(hits[f]))
		}
		t.Logf("扫了 %d 个非测试 .go · %d 个收 req SyncRequest 的函数 · 读取点 %s",
			n, funcs, strings.Join(line, " "))
	})
}
