// doccheck 核对【文档里声明的东西】与【源码里真实存在的东西】。
//
// 为什么要它：docs/design.md 里的类型名、字段名、函数签名是**设计稿**。
// 设计稿和代码分头演化时，文档不会报错，它只会慢慢变成一份自信的谎。
// 人眼核对签名一致性，是那种做过一次就不会再做第二次的事。
//
// 两条设计要点（都是评审提的，各自防一类失效）：
//
//  1. **比签名，不比名字。** 只查「标识符在源码里找得到」挡不住最要紧的那类：
//     `IntradayPeriod.Bars` 名字还在，签名从 `(tmpl, td)` 变成了 `(td)`——
//     名字对得上，**契约已经变了**，而守卫全绿。那比没有守卫更坏。
//
//  2. **白名单会自己过期。** 「尚未实现」的条目必须写明预定版本；
//     **一旦那个版本已经打过 tag，守卫直接 FAIL**。不靠人自觉去清理，
//     靠时间推着清——否则白名单迟早变成垃圾桶，和「一个会红的必过项
//     迟早被加 || true」是同一个形状。
//
// 用法：
//
//	go run ./tools/doccheck          # 核对
//	go run ./tools/doccheck -list    # 只列出文档声明了什么
//
// 退出码：0 一致；1 有不一致。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// decl 是一处声明，来自文档或源码。
type decl struct {
	Name string // 类型名，或 "Recv.Method"，或函数名
	Sig  string // 归一化签名；类型声明为空
	Kind string // "type" / "func" / "method"
	Src  string // 出处（文件:行）
}

func main() {
	list := flag.Bool("list", false, "只列出文档里声明了什么")
	root := flag.String("root", ".", "仓库根")
	flag.Parse()

	docDecls, err := fromDocs(filepath.Join(*root, "docs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "读文档失败:", err)
		os.Exit(1)
	}
	if *list {
		for _, d := range sorted(docDecls) {
			fmt.Printf("%-34s %-8s %s\n", d.Name, d.Kind, d.Sig)
		}
		fmt.Printf("\n共 %d 处声明\n", len(docDecls))
		return
	}

	srcDecls, err := fromSource(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读源码失败:", err)
		os.Exit(1)
	}
	pending, err := loadPending(filepath.Join(*root, "tools", "doccheck", "pending.txt"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "读 pending 失败:", err)
		os.Exit(1)
	}

	var missing, mismatch, staleWL []string
	used := map[string]bool{}

	for _, d := range sorted(docDecls) {
		s, ok := srcDecls[d.Name]
		if !ok {
			if v, wl := pending[d.Name]; wl {
				used[d.Name] = true
				if tagExists(*root, v) {
					staleWL = append(staleWL,
						fmt.Sprintf("%s —— 白名单写着 %s，而 %s 已经打过 tag", d.Name, v, v))
				}
				continue
			}
			missing = append(missing, fmt.Sprintf("%s (%s) —— 文档 %s", d.Name, d.Kind, d.Src))
			continue
		}
		if d.Sig != "" && s.Sig != d.Sig {
			mismatch = append(mismatch, fmt.Sprintf(
				"%s\n      文档: %s   (%s)\n      源码: %s   (%s)",
				d.Name, d.Sig, d.Src, s.Sig, s.Src))
		}
	}

	var orphan []string
	for name, v := range pending {
		if !used[name] {
			orphan = append(orphan, fmt.Sprintf("%s (%s) —— 白名单里有，但文档里没有声明它", name, v))
		}
	}
	sort.Strings(orphan)

	fmt.Printf("文档声明 %d 处；源码声明 %d 处；白名单 %d 项\n\n",
		len(docDecls), len(srcDecls), len(pending))

	bad := 0
	report := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		bad += len(items)
		fmt.Printf("== %s（%d）==\n", title, len(items))
		for _, s := range items {
			fmt.Println("  " + s)
		}
		fmt.Println()
	}
	report("源码里找不到，且不在白名单", missing)
	report("名字在、【签名不同】", mismatch)
	report("白名单已过期（预定版本已打 tag）", staleWL)
	report("白名单里的孤儿项", orphan)

	if bad == 0 {
		fmt.Printf("一致。（其中 %d 项由白名单显式记为「尚未实现」）\n", len(used))
		return
	}
	fmt.Printf("%d 处不一致。\n", bad)
	fmt.Println("要么让源码与文档一致，要么改文档，要么把它写进 tools/doccheck/pending.txt 并注明预定版本。")
	os.Exit(1)
}

func sorted(m map[string]decl) []decl {
	out := make([]decl, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------------------------------------------------------------- 文档侧

var fenceGo = "```go"

func fromDocs(dir string) (map[string]decl, error) {
	out := map[string]decl{}
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(b), "\n")
		in, start := false, 0
		var buf []string
		for i, ln := range lines {
			t := strings.TrimSpace(ln)
			if !in && strings.HasPrefix(t, fenceGo) {
				in, start, buf = true, i+2, nil
				continue
			}
			if in && strings.HasPrefix(t, "```") {
				in = false
				scanBlock(buf, filepath.Base(f), start, out)
				continue
			}
			if in {
				buf = append(buf, ln)
			}
		}
	}
	return out, nil
}

// scanBlock 从一个 go 代码块里抽声明。
//
// 文档里的代码块常常是【片段】：函数只有签名没有函数体，结构体里带省略号。
// 所以不整块解析，而是逐条尝试——给无体函数补一个体再解析，
// 这样参数分组（`exch, instID string`）能由 go/parser 正确展开，
// 而不是靠正则去猜。
func scanBlock(lines []string, file string, base int, out map[string]decl) {
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		t := strings.TrimSpace(ln)
		where := fmt.Sprintf("%s:%d", file, base+i)

		switch {
		case strings.HasPrefix(t, "func "):
			if d, ok := parseFunc(t, where); ok {
				out[d.Name] = d
			}
		case strings.HasPrefix(t, "type "):
			name, kind := typeName(t)
			if name == "" {
				continue
			}
			out[name] = decl{Name: name, Kind: "type", Src: where}
			if kind == "interface" {
				// 接口里的方法也算契约
				for j := i + 1; j < len(lines); j++ {
					mt := strings.TrimSpace(lines[j])
					if mt == "}" {
						i = j
						break
					}
					if mt == "" || strings.HasPrefix(mt, "//") {
						continue
					}
					if d, ok := parseIfaceMethod(name, mt,
						fmt.Sprintf("%s:%d", file, base+j)); ok {
						out[d.Name] = d
					}
				}
			}
		}
	}
}

func typeName(t string) (string, string) {
	f := strings.Fields(t)
	if len(f) < 2 {
		return "", ""
	}
	name := f[1]
	if !isExported(name) {
		return "", ""
	}
	kind := ""
	if len(f) > 2 {
		kind = strings.TrimSuffix(f[2], "{")
	}
	return name, kind
}

// parseFunc 解析文档里的一行函数签名（可能没有函数体）。
func parseFunc(line, where string) (decl, bool) {
	line = strings.TrimSuffix(strings.TrimSpace(line), "{")
	line = strings.TrimSpace(line)
	src := "package d\n" + line + " { panic(0) }\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil || len(f.Decls) == 0 {
		return decl{}, false
	}
	fd, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok {
		return decl{}, false
	}
	name := fd.Name.Name
	kind := "func"
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		name = recvType(fd.Recv.List[0].Type) + "." + name
		kind = "method"
	}
	if !isExported(lastSeg(name)) {
		return decl{}, false
	}
	return decl{Name: name, Sig: sigOf(fset, fd.Type), Kind: kind, Src: where}, true
}

// parseIfaceMethod 解析接口里的一行方法声明，如 `DayAt(k ProductKey, ts int64) (TradingDay, bool)`。
func parseIfaceMethod(iface, line, where string) (decl, bool) {
	src := "package d\ntype I interface {\n" + line + "\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return decl{}, false
	}
	var got *ast.FuncType
	var name string
	ast.Inspect(f, func(n ast.Node) bool {
		if it, ok := n.(*ast.InterfaceType); ok && it.Methods != nil {
			for _, m := range it.Methods.List {
				if len(m.Names) == 1 {
					if ft, ok := m.Type.(*ast.FuncType); ok {
						name, got = m.Names[0].Name, ft
					}
				}
			}
		}
		return true
	})
	if got == nil || !isExported(name) {
		return decl{}, false
	}
	return decl{Name: iface + "." + name, Sig: sigOf(fset, got), Kind: "method", Src: where}, true
}

// ---------------------------------------------------------------- 源码侧

func fromSource(root string) (map[string]decl, error) {
	out := map[string]decl{}
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			b := filepath.Base(p)
			// 探针与工具不是本库的公开契约
			if b == ".git" || b == "tools" || b == "testdata" || strings.HasPrefix(b, ".") && len(b) > 1 {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, d := range f.Decls {
			switch n := d.(type) {
			case *ast.FuncDecl:
				name, kind := n.Name.Name, "func"
				if n.Recv != nil && len(n.Recv.List) > 0 {
					name = recvType(n.Recv.List[0].Type) + "." + name
					kind = "method"
				}
				if !isExported(lastSeg(name)) {
					continue
				}
				where := fmt.Sprintf("%s:%d", rel, fset.Position(n.Pos()).Line)
				out[name] = decl{Name: name, Sig: sigOf(fset, n.Type), Kind: kind, Src: where}
			case *ast.GenDecl:
				if n.Tok != token.TYPE {
					continue
				}
				for _, sp := range n.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok || !isExported(ts.Name.Name) {
						continue
					}
					where := fmt.Sprintf("%s:%d", rel, fset.Position(ts.Pos()).Line)
					out[ts.Name.Name] = decl{Name: ts.Name.Name, Kind: "type", Src: where}
					// 接口的方法一并登记
					if it, ok := ts.Type.(*ast.InterfaceType); ok && it.Methods != nil {
						for _, m := range it.Methods.List {
							if len(m.Names) != 1 || !isExported(m.Names[0].Name) {
								continue
							}
							ft, ok := m.Type.(*ast.FuncType)
							if !ok {
								continue
							}
							mn := ts.Name.Name + "." + m.Names[0].Name
							out[mn] = decl{Name: mn, Sig: sigOf(fset, ft), Kind: "method",
								Src: fmt.Sprintf("%s:%d", rel, fset.Position(m.Pos()).Line)}
						}
					}
				}
			}
		}
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------- 归一化

// sigOf 把函数类型渲染成 `(T1,T2) (R1,R2)`，**丢掉参数名**。
//
// 丢参数名是刻意的：文档里叫 `tmpl`、代码里叫 `t`，不该算不一致；
// 而参数【类型】少一个、多一个、换一个，就是契约变了。
func sigOf(fset *token.FileSet, ft *ast.FuncType) string {
	return "(" + strings.Join(typeList(fset, ft.Params), ",") + ") (" +
		strings.Join(typeList(fset, ft.Results), ",") + ")"
}

func typeList(fset *token.FileSet, fl *ast.FieldList) []string {
	var out []string
	if fl == nil {
		return out
	}
	for _, f := range fl.List {
		t := render(fset, f.Type)
		n := len(f.Names)
		if n == 0 {
			n = 1 // 无名参数/返回值
		}
		for i := 0; i < n; i++ {
			out = append(out, t)
		}
	}
	return out
}

func render(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "?"
	}
	return strings.Join(strings.Fields(b.String()), "")
}

func recvType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvType(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvType(t.X)
	}
	return "?"
}

func isExported(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

func lastSeg(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ---------------------------------------------------------------- 白名单

// loadPending 读「尚未实现」白名单：每行 `标识符<空白>预定版本<空白>说明`。
func loadPending(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return nil, fmt.Errorf("白名单格式错误（缺预定版本）: %q", line)
		}
		out[f[0]] = f[1]
	}
	return out, sc.Err()
}

// tagExists 报告某个版本是否已经打过 tag。
//
// 白名单项的预定版本一旦打了 tag，这一项就【过期】——守卫直接 FAIL。
// 这是让白名单自己清理的机制：不靠人自觉，靠时间推着清。
func tagExists(root, version string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "-q", "--verify",
		"refs/tags/"+version)
	return cmd.Run() == nil
}
