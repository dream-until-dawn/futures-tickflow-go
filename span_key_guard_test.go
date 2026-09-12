package tickflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— 这一条守的是【`Span` 不再被当 map 键用】——
//
// 丙（2026-09-12）把身份收窄成 `SpanKey{From, To}`，而**类型本身没有被改成不可比较** ——
// 那会连 `==` 一起禁掉，而本仓多处在正当地用它（等价性测试比两个 `Span` 的内容）。
// ⇒ 于是「不许拿 `Span` 当键」这条约束**只靠「没人再那样写」** ——
// 🔴 而本仓那条：**一条靠纪律的约束，若能变成机器检查就该变。**
//
// —— ⛔ 为什么用 AST 而不是 grep ——
//
// grep `map\[Span\]` 会命中**注释里的历史说明** —— 而这一片自己的注释就在讲那段历史
// （`store.go` 的 `SpanKey` 那一段逐字写着「上一版拿 `Span` 本身做 map 键」）。
// ⇒ **会误报的守卫最终被关掉** ⇒ 判据必须只看**真的类型**：`*ast.MapType` 的 `Key`。
// 📎 而这一条同时是本仓「格式伪装成内容」那一族的预防：**注释里的代码长得和代码一样。**
//
// —— ✅ 标定：这把尺子自己先被喂过一个【已知为真】的端 ——
//
// 本仓那条（**任何筛子在用之前先喂一个已知为真的端**）在这里落成了子用例「标定」：
// 它把一段**含 `map[Span]bool` 的源码**喂给同一个探测器，断言它**报得出**。
// 🔴 少了它，「全仓 0 处」与「探测器什么都照不到」在输出上是同一个字节。

// spanMapKeyHits 在一份已解析的源码里找「键是 Span 的 map 类型」。
//
// ⚠️ 判据是**类型节点**，不是文本：`map[Span]X` 与 `map[tickflow.Span]X` 都算，
// 而注释、字符串字面量里的同样几个字**不算** —— 它们不是 `*ast.MapType`。
func spanMapKeyHits(f *ast.File, fset *token.FileSet) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		mt, ok := n.(*ast.MapType)
		if !ok {
			return true
		}
		name := ""
		switch k := mt.Key.(type) {
		case *ast.Ident:
			name = k.Name
		case *ast.SelectorExpr:
			if pkg, ok := k.X.(*ast.Ident); ok {
				name = pkg.Name + "." + k.Sel.Name
			}
		}
		if name == "Span" || name == "tickflow.Span" {
			out = append(out, fset.Position(mt.Pos()).String())
		}
		return true
	})
	return out
}

// guard: 全仓不许有 map[Span] —— 一段的身份是 SpanKey，不是那四个字段。
func TestNoMapKeyedBySpan(t *testing.T) {
	t.Run("标定 探测器喂一个已知为真的端_报得出", func(t *testing.T) {
		// ⛔ 这一格是这把尺子唯一能被证伪的时刻：
		// 少了它，「全仓 0 处」与「探测器什么都照不到」在输出上是同一个字节。
		const src = `package p
type Span struct{ From, To, Bars, Days int }
var zz = map[Span]bool{}
`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "zz_calib.go", src, 0)
		if err != nil {
			t.Fatalf("标定源码解析失败：%v", err)
		}
		if hits := spanMapKeyHits(f, fset); len(hits) != 1 {
			t.Fatalf("探测器对一段【确实含 map[Span]bool】的源码报了 %d 处（要 1）⇒ 尺子坏了，"+
				"下面那一格的「0 处」没有真值", len(hits))
		}
	})

	t.Run("标定 注释与字符串里的同样几个字_不算", func(t *testing.T) {
		// ⇒ 这一格钉的是「用 AST 而不是 grep」这个决定本身：
		// 少了它，一个退回 grep 的实现会在本仓自己的注释上误报，而那种守卫最终被关掉。
		const src = `package p
type Span struct{ From, To int }
// 上一版拿 map[Span]bool 做键，那是错的。
var s = "map[Span]bool"
`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "zz_calib2.go", src, parser.ParseComments)
		if err != nil {
			t.Fatalf("标定源码解析失败：%v", err)
		}
		if hits := spanMapKeyHits(f, fset); len(hits) != 0 {
			t.Errorf("注释/字符串里的 map[Span] 被算进去了：%v\n"+
				"  ⇒ 那是 grep 的毛病，而本仓自己的注释就在讲那段历史 ⇒ 必然误报。", hits)
		}
	})

	t.Run("被测 全仓 0 处", func(t *testing.T) {
		fset := token.NewFileSet()
		var all []string
		scanned := 0
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
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				return perr
			}
			scanned++
			all = append(all, spanMapKeyHits(f, fset)...)
			return nil
		})
		if err != nil {
			t.Fatalf("走一遍仓失败：%v", err)
		}
		// ⛔ 前提自检：真的扫到了东西。
		// 🔴 「一个文件都没扫到」与「全仓干净」在结果上是同一个数（0 处）——
		// 而它们的处置完全不同（一个是尺子没对准，一个是被测物合格）。
		if scanned < 20 {
			t.Fatalf("只扫到 %d 个 .go 文件 ⇒ 走法不对（跑的目录？）⇒ 读数作废", scanned)
		}
		if len(all) != 0 {
			t.Errorf("全仓有 %d 处拿 Span 当 map 键：\n  %s\n"+
				"  ⇒ 一段的身份是 [From, To]（根包的 SpanKey），不是那四个字段。\n"+
				"  ⇒ 拿整个结构体当键 ＝ 宣布 Bars/Days 也是身份的一部分，\n"+
				"     而那句话没有任何人同意过 —— 是 Go 的 == 替我们答的，它答错过两次（(o) 与 (j)）。\n"+
				"  ⇒ 改法：键走 span.Key()。",
				len(all), strings.Join(all, "\n  "))
		}
		// ⛔ 这一行原来把「0 处」**写死在格式串里** —— 2026-09-12 被突变 M6 当场抓出：
		// 仓里真塞了一处 `map[Span]bool`，这一格已经红了，而它还在印「map[Span] 0 处」。
		// 🔴 本仓那条（**结论句要【算出来】，不能【写死】**）逐字适用：
		// 一个写死的读数会**盖住旁边那一行打出的真值**。
		t.Logf("扫了 %d 个 .go 文件，map[Span] %d 处", scanned, len(all))
	})
}
