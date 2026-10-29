// api_tree 提取指定源码树的导出声明，并对两份快照做语法树级差异。
// 不执行目标树代码；不把语法相同解释为完整的行为或二进制兼容。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type snapshot struct {
	Schema int               `json:"schema"`
	Scope  string            `json:"scope"`
	API    map[string]string `json:"api"`
}

type change struct {
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type difference struct {
	Added   []change `json:"added"`
	Removed []change `json:"removed"`
	Changed []change `json:"changed"`
}

func rendered(node any) string {
	if node == nil {
		return ""
	}
	var out bytes.Buffer
	if err := printer.Fprint(&out, token.NewFileSet(), node); err != nil {
		panic(err)
	}
	return out.String()
}

func typeParameters(fields *ast.FieldList) string {
	if fields == nil {
		return ""
	}
	var parts []string
	for _, field := range fields.List {
		var names []string
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
		parts = append(parts, strings.Join(names, ",")+" "+rendered(field.Type))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func receiverName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return receiverName(v.X)
	case *ast.IndexExpr:
		return receiverName(v.X)
	case *ast.IndexListExpr:
		return receiverName(v.X)
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

func collect(root string) (snapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return snapshot{}, err
	}
	result := snapshot{Schema: 1, Scope: "AST union of build variants; one module; excludes tests/tools/vendor/hidden directories", API: map[string]string{}}
	put := func(key, value string) error {
		if previous, exists := result.API[key]; exists && previous != value {
			return fmt.Errorf("构建变体声明冲突，需人工选择目标平台：%s", key)
		}
		result.API[key] = value
		return nil
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root {
				if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "tools" || entry.Name() == "vendor" || entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝源码链接：%s", path)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		if file.Name.Name == "main" {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		prefix := filepath.ToSlash(rel) + ":" + file.Name.Name + "."
		for _, declaration := range file.Decls {
			switch d := declaration.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(d.Name.Name) {
					continue
				}
				key, signature := d.Name.Name, rendered(d.Type)
				if d.Recv != nil {
					name := receiverName(d.Recv.List[0].Type)
					if !ast.IsExported(name) {
						continue
					}
					key = name + "." + key
					signature = "recv " + rendered(d.Recv.List[0].Type) + " " + signature
				}
				if err := put(prefix+key, signature); err != nil {
					return err
				}
			case *ast.GenDecl:
				var lastType ast.Expr
				var lastValues []ast.Expr
				for ordinal, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !ast.IsExported(s.Name.Name) {
							continue
						}
						name := prefix + s.Name.Name
						signature := "type " + typeParameters(s.TypeParams) + " " + rendered(s.Type)
						if s.Assign.IsValid() {
							signature = "alias " + rendered(s.Type)
						}
						if shape, ok := s.Type.(*ast.StructType); ok {
							signature = "struct " + typeParameters(s.TypeParams)
							for _, field := range shape.Fields.List {
								fields := field.Names
								if len(fields) == 0 {
									fields = []*ast.Ident{ast.NewIdent(receiverName(field.Type))}
								}
								for _, fieldName := range fields {
									if !ast.IsExported(fieldName.Name) {
										continue
									}
									value := rendered(field.Type)
									if field.Tag != nil {
										value += " " + field.Tag.Value
									}
									if err := put(name+"."+fieldName.Name, value); err != nil {
										return err
									}
								}
							}
						}
						if err := put(name, signature); err != nil {
							return err
						}
					case *ast.ValueSpec:
						if d.Tok == token.CONST && len(s.Values) > 0 {
							lastType, lastValues = s.Type, s.Values
						}
						for index, name := range s.Names {
							if !ast.IsExported(name.Name) {
								continue
							}
							kind, typ, values := d.Tok.String(), s.Type, s.Values
							if d.Tok == token.CONST && len(values) == 0 {
								typ, values = lastType, lastValues
							}
							text := kind + " " + rendered(typ)
							if d.Tok == token.CONST || typ == nil {
								for _, value := range values {
									text += " = " + rendered(value)
								}
								text += " slot=" + strconv.Itoa(index)
								if strings.Contains(text, "iota") {
									text += " iota=" + strconv.Itoa(ordinal)
								}
							}
							if err := put(prefix+name.Name, text); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		return nil
	})
	if err == nil && len(result.API) == 0 {
		err = fmt.Errorf("未提取到任何API，拒绝空转")
	}
	return result, err
}

func compare(before, after snapshot) difference {
	result := difference{Added: []change{}, Removed: []change{}, Changed: []change{}}
	keys := map[string]bool{}
	for key := range before.API {
		keys[key] = true
	}
	for key := range after.API {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		a, old := before.API[key]
		b, current := after.API[key]
		switch {
		case !old:
			result.Added = append(result.Added, change{Name: key, After: b})
		case !current:
			result.Removed = append(result.Removed, change{Name: key, Before: a})
		case a != b:
			result.Changed = append(result.Changed, change{Name: key, Before: a, After: b})
		}
	}
	return result
}

func main() {
	root := flag.String("root", ".", "待提取的模块根目录")
	baseline := flag.String("baseline", "", "已有JSON快照；给定后输出差异")
	output := flag.String("out", "", "独占创建JSON文件；省略则输出stdout")
	flag.Parse()
	current, err := collect(*root)
	var result any = current
	if err == nil && *baseline != "" {
		var data []byte
		data, err = os.ReadFile(*baseline)
		if err == nil {
			var previous snapshot
			err = json.Unmarshal(data, &previous)
			if err == nil && (previous.Schema != 1 || len(previous.API) == 0) {
				err = fmt.Errorf("基线版本错误或为空")
			}
			if err == nil {
				result = compare(previous, current)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	writer := os.Stdout
	if *output != "" {
		writer, err = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer writer.Close()
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
