package tickflow_test

import (
	"os/exec"
	"strings"
	"testing"
)

// 一条到期条件写在注释里，就要有人**记得去求值**。这条测试把那一步收回来：
// **到期的那一天，它自己红。**
//
// 被钉住的那句话（出处：`refdata/shinnyref` 的包注释）：
//
//	本包的失败哨兵一个都不导出 ⇒ 包外调用方没法 `errors.Is` 分类
//	⇒ 到期条件：**在出现第一个包外调用方之前**
//
// ⛔ 这条到期条件的求值法今天已经栽过**两次**，两次都不是「结论错」而是「方法错」：
//
//	一、裸文本 grep ⇒ 命中一句**注释**（symbol.go），而写那句注释的
//	   正是写下这条求值法的同一颗提交 ⇒ **它生下来就是坏的，而它从来没红过**
//	二、改成 grep 带引号的 import 路径 ⇒ **写下这条方法的那一行自己含着那个路径**
//	   ⇒ 它命中自己
//
// 🔴 ⇒ **任何「数一数某个字符串出现几次」的求值法，都会把写下它的那一行算进去** ——
// 除非换成一个**不读源文本**的问法。⇒ 所以这里问 `go list`：它输出 import 图，注释进不去。
const shinnyrefPkg = "github.com/dream-until-dawn/futures-tickflow-go/refdata/shinnyref"

// goListFields 取一个包的三份 import 列表。
//
// ⚠️ **三份，而不是两份** —— `.XTestImports`（外部测试包 `package X_test` 的 import）
// 是第一版漏掉的那一份，而**它恰恰是这条到期条件最该抓的那一种**：
// 外部测试包里的代码在**包外**，它要分类错误就得 `errors.Is`。
// ⛔ 而漏掉它的失败方向是**静默**的：不误报，**该报的时候不报**。
const goListFields = `{{range .Imports}}{{.}}
{{end}}{{range .TestImports}}{{.}}
{{end}}{{range .XTestImports}}{{.}}
{{end}}`

// importCounts 数一个模块里的 import。
//
// ⚠️ `dir` 是模块根 —— **本仓有两个 go.mod**（根目录与 `tools/`），
// 而 `go list ./...` 只看得见【当前模块】。
// ⛔ 只跑根模块的话，这条守卫的射程就比它声称守的那句话窄一格：
// 那句话说的是「有没有包外调用方」，**而 tools 模块里的包也在包外**。
// （本仓已记过同型的一次：`go test ./...` 被当成「七包全绿」，而规定的自检是 module_sweep。）
func importCounts(t *testing.T, dir string) map[string]int {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", goListFields, "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("在 %s 里 go list 失败：%v —— 而本条不接受「跑不起来」当通过", dir, err)
	}
	n := map[string]int{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			n[l]++
		}
	}
	return n
}

func countField(t *testing.T, field, want string) int {
	t.Helper()
	out, err := exec.Command("go", "list", "-f",
		"{{range ."+field+"}}{{.}}\n{{end}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list %s 失败：%v", field, err)
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == want {
			n++
		}
	}
	return n
}

// TestRefdataExpiryConditionNotYetDue —— 到期的那天它自己红。
func TestRefdataExpiryConditionNotYetDue(t *testing.T) {
	// **两个模块各问一次** —— 见 importCounts 上面那段。
	for _, dir := range []string{".", "tools"} {
		if got := importCounts(t, dir)[shinnyrefPkg]; got != 0 {
			t.Fatalf("模块 %s 里，`%s` 现在有 %d 个包外调用方 —— "+
				"【这条到期条件到期了】。\n"+
				"处置不是把这条测试改掉，是：\n"+
				"  一、把本包对外的失败分类写进 docs/contract.md（本仓文档先行）\n"+
				"  二、然后导出那些哨兵（errNotFuture / errNotReadToEOF / …）\n"+
				"  三、再回来改这条测试与 contract.go 的包注释",
				dir, shinnyrefPkg, got)
		}
	}
}

// TestGoListImportRulerWorks —— **尺子自己也要验**，而它钉的是 `goListFields` 这个常量。
//
// ⛔ **第一版这条测试是空的**，而它长得完全像一条好测试：
// 它拿【自己内联的】逐字段命令去数，证明「三个字段在本仓都是活的」——
// 🔴 **而那与 `goListFields` 里到底写了几个字段【毫无关系】**。
// 实测：把 `goListFields` 退回两字段 ＋ 造一个外部测试包消费者 ⇒ **两条测试都绿**，
// 也就是说那个静默洞原样存在，而守卫一声没吭。
//
// ⇒ 同本仓那条：**一条测试的「构造」和它的「断言」可以分属两个命题** ——
// 构造在讲求值法的事，断言落在「这个仓里有没有外部测试包」上，两半各自都好，合起来是空的。
//
// ⇒ 改法：**让断言直接落在那个常量上** ——
// 用 `goListFields` 数一次，再逐字段各数一次，**两个数必须相等**。
// 少写一个字段 ⇒ 左边小于右边 ⇒ 红。
func TestGoListImportRulerWorks(t *testing.T) {
	const root = "github.com/dream-until-dawn/futures-tickflow-go"

	// 甲｜整条尺子不是常量 0：根包确实被 import 着。
	viaConst := importCounts(t, ".")[root]
	if viaConst == 0 {
		t.Fatalf("用 goListFields 数根包，得 0 —— 那不是读数，那是尺子坏了")
	}

	// 乙｜逐字段各数一次。**每一个字段都必须有活的命中**，
	// 否则「把它写进求值法」这件事今天没有被验证过。
	sum := 0
	for _, f := range []string{"Imports", "TestImports", "XTestImports"} {
		n := countField(t, f, root)
		if n == 0 {
			t.Fatalf("`.%s` 在本仓里一次命中都没有 —— "+
				"那么把它写进求值法这件事【今天没有被验证过】", f)
		}
		sum += n
	}

	// 丙｜**承重的是这一格**：常量数出来的，必须等于三个字段之和。
	//
	// ⚠️ 断言的是两个当场求出来的数**相等**，不是它们等于当时那几个值 ——
	// 本仓那条：**抄「当前值」的断言在它变化时会指向被测方，而过期的是它自己。**
	if viaConst != sum {
		t.Fatalf("goListFields 数到 %d，而三个字段之和是 %d —— "+
			"【求值法里少写了字段】。\n"+
			"（少掉 .XTestImports 是【静默】的：不误报，"+
			"而一个 `package X_test` 的包外调用方就此数不到 —— "+
			"那正是这条到期条件最该抓的那一种。）", viaConst, sum)
	}
}
