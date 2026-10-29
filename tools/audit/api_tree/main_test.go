package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, root, name, source string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

const example = `package example
type Record[T any] struct { Value T; hidden int }
type Alias = int
type RemoveMe string
type Service interface { Run(int) error }
func Fetch(value int) string { return "" }
func (r *Record[T]) Reset() {}
var Global int
const (First = iota; Second)
func private() {}
`

func TestTreeControlsAndMutation(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "api.go", example)
	for _, name := range []string{"tools/bad.go", "vendor/bad.go", "testdata/bad.go", ".hidden/bad.go", "api_test.go"} {
		fixture(t, root, name, "not Go syntax")
	}
	fixture(t, root, "nested/go.mod", "module nested")
	fixture(t, root, "nested/bad.go", "not Go syntax")
	before, err := collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if before.API[".:example.Record.Value"] != "T" || !strings.Contains(before.API[".:example.Record.Reset"], "recv *Record[T]") {
		t.Fatal("漏掉字段或泛型方法")
	}
	if _, ok := before.API[".:example.Record.hidden"]; ok {
		t.Fatal("把私有字段当API")
	}
	zero := compare(before, before)
	if len(zero.Added)+len(zero.Removed)+len(zero.Changed) != 0 {
		t.Fatal("同树非零")
	}
	fixture(t, root, "api.go", strings.ReplaceAll(example, "hidden int", "renamedPrivate bool"))
	private, err := collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if diff := compare(before, private); len(diff.Added)+len(diff.Removed)+len(diff.Changed) != 0 {
		t.Fatal("私有字段变化污染API")
	}
	changed := strings.ReplaceAll(example, "Fetch(value int)", "Fetch(value string)")
	changed = strings.ReplaceAll(changed, "type RemoveMe string", "func Added() {}")
	fixture(t, root, "api.go", changed)
	after, err := collect(root)
	if err != nil {
		t.Fatal(err)
	}
	diff := compare(before, after)
	if len(diff.Added) != 1 || len(diff.Removed) != 1 || len(diff.Changed) != 1 {
		t.Fatalf("增删改未分别检出：%+v", diff)
	}
	if diff.Changed[0].Name != ".:example.Fetch" {
		t.Fatal("改签名定位错误")
	}
}

func TestEmptyInvalidAndVariantConflict(t *testing.T) {
	root := t.TempDir()
	if _, err := collect(root); err == nil {
		t.Fatal("空树放行")
	}
	fixture(t, root, "broken.go", "broken")
	if _, err := collect(root); err == nil {
		t.Fatal("坏语法放行")
	}
	fixture(t, root, "broken.go", "package p; func Exported(int) {}")
	fixture(t, root, "other.go", "package p; func Exported(string) {}")
	if _, err := collect(root); err == nil {
		t.Fatal("构建变体冲突被覆盖")
	}
}
