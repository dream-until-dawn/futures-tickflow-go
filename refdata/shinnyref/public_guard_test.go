package shinnyref

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// 旧零消费者到期条件履行后的后继：新增私有errors.New哨兵也必须公开并记契约。
func TestEveryFailureSentinelIsPublicAndDocumented(t *testing.T) {
	pkgs, e := parser.ParseDir(token.NewFileSet(), ".", func(i os.FileInfo) bool { return !strings.HasSuffix(i.Name(), "_test.go") }, 0)
	if e != nil {
		t.Fatal(e)
	}
	pkg := pkgs["shinnyref"]
	if pkg == nil {
		t.Fatal("找不到包")
	}
	private := map[string]bool{}
	aliases := map[string]string{}
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			v, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, value := range v.Values {
				if i >= len(v.Names) {
					continue
				}
				name := v.Names[i].Name
				if call, ok := value.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
						if p, ok := sel.X.(*ast.Ident); ok && p.Name == "errors" && strings.HasPrefix(name, "err") {
							private[name] = true
						}
					}
				}
				if id, ok := value.(*ast.Ident); ok && ast.IsExported(name) {
					aliases[id.Name] = name
				}
			}
			return true
		})
	}
	doc, e := os.ReadFile("../../docs/contract.md")
	if e != nil {
		t.Fatal(e)
	}
	if len(private) == 0 {
		t.Fatal("哨兵尺子空转")
	}
	for name := range private {
		public, ok := aliases[name]
		if !ok || !strings.Contains(string(doc), "`"+public+"`") {
			t.Errorf("私有哨兵%s没有公开别名和契约", name)
		}
	}
}
