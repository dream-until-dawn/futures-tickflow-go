package main

import (
	// 起别名：main.go 里有个函数就叫 token，同包内会撞
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这些文件【不许】import 本库。
//
// # 为什么要一条机械检查
//
// 「探针不拿本库的日历去验本库」是一条承重的独立性约定：日历错了，
// 被测的一方和用来判定的一方会**因为同一个原因同时错**，那就不是两条路。
//
// 在 template.go 之前，这条约定是被**模块边界强制**的——探针模块根本
// 没有 require 本库，写错了编译不过。template.go 为了审内置表必须把它
// require 进来（那条探针的被测对象就是内置表，不读进来没法审），
// 于是**整个模块的那道结构保障没了，只剩注释**。
//
// 评审的原话：它「从『编译不过』降级成了『记得别这么写』」。
// 这个测试把它接回机械层——**一条刚刚失去结构保障的约定，
// 要么补一道机械检查，要么承认它已经不是约定了。**
//
// # 白名单只有 template.go，而且理由要写在这里
//
// template.go 的方向是【本库是被测对象、真值来自天勤】，与上面那条不冲突：
// 那条禁的是拿本库当**判定依据**。方向反了，就不是同一件事。
var mustNotImportLibrary = []string{
	"main.go",
	"tradingday.go", // 里面的 rbSession 是照交易所公布写死的，不许改成读日历
}

func TestProbesDoNotImportLibrary(t *testing.T) {
	const lib = "github.com/dream-until-dawn/futures-tickflow-go"
	for _, name := range mustNotImportLibrary {
		fset := gotoken.NewFileSet()
		f, err := goparser.ParseFile(fset, name, nil, goparser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, im := range f.Imports {
			p := strings.Trim(im.Path.Value, `"`)
			if p == lib || strings.HasPrefix(p, lib+"/") {
				t.Errorf("%s import 了本库 (%s)。\n"+
					"探针要断言【外部世界】长什么样；拿本库去判定本库，"+
					"本库错了两边会一起错，那就不是两条独立的路。\n"+
					"若这一条确实需要本库当【被测对象】（像 template.go 那样），"+
					"把文件名从 mustNotImportLibrary 移出去，并在那里写清方向。",
					name, p)
			}
		}
	}
}

// TestGuardListCoversEveryProbeFile 白名单要么覆盖到，要么显式豁免——
// **不能靠「新加的文件恰好没人想起来加进列表」**。
//
// 上面那个检查只查列表里的文件。新写一条探针、忘了加进列表，
// 它就完全不受管——而那正好是这道保障最可能失效的方式：
// 不是有人违反它，是有人**绕过了它而不自知**。
func TestGuardListCoversEveryProbeFile(t *testing.T) {
	// 明确豁免，附理由。加一项之前先问：它的方向真的是「本库被测」吗？
	exempt := map[string]string{
		"template.go":          "被测对象就是内置表，方向是【本库被测、真值来自天勤】",
		"independence_test.go": "就是这条检查本身",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, n := range mustNotImportLibrary {
		listed[n] = true
	}
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") && base != "independence_test.go" {
			continue
		}
		if listed[base] {
			continue
		}
		if _, ok := exempt[base]; ok {
			continue
		}
		t.Errorf("%s 既不在 mustNotImportLibrary 里，也不在豁免表里。\n"+
			"新探针默认应当【不】import 本库；确实需要的话，"+
			"把它加进 exempt 并写明方向。", base)
	}
	// 豁免表里的文件必须真的存在——否则它会变成一张只增不减的垃圾表
	for name := range exempt {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("豁免表里有 %s，但文件不存在——豁免表该跟着代码走", name)
		}
	}
}
