package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这套测试断言的是【报告的措辞】，不是退出码。
//
// 理由是本工具至今出过的三个 bug 全是同一形态：**退出码正确、文本错误**。
//
//	一行多名的字段只登记第一个        → 误报另外三个「找不到」
//	单行结构体扫过头                  → 把下一个类型的字段登记到前者名下
//	「欠条已兑现」被说成「文档里没声明」→ 一句字面上的假话
//
// 第三个尤其说明问题：旧版遇到它 exit 1，新版也 exit 1，
// **两版退出码一模一样，差别只在那行字**。
// 只看退出码的自动化在这一格是零防护——所以要把期望文本写进断言。
//
// 之前这些是我手工跑的一次性脚本（D1–D7）。按本仓自己的话：
// **手验不会跟着代码走。**

// fixture 造一个最小仓库：docs/a.md + a.go + pending 表。
type fixture struct {
	name    string
	doc     string            // markdown，含 ```go 块
	src     string            // 一个 .go 文件的内容（package p）
	pending map[string]string // 标识符 → 预定版本
	tagged  []string          // 这些版本算「已经打过 tag」
	want    []string          // 报告里【必须】出现的片段
	notWant []string          // 报告里【不该】出现的片段
}

func (f fixture) run(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("docs", "a.md"), f.doc)
	write("a.go", "package p\n\n"+f.src)

	docDecls, err := fromDocs(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	srcDecls, err := fromSource(root)
	if err != nil {
		t.Fatal(err)
	}
	tagged := map[string]bool{}
	for _, v := range f.tagged {
		tagged[v] = true
	}
	res := compare(docDecls, srcDecls, f.pending, docConflicts,
		func(v string) bool { return tagged[v] })
	return res.render()
}

func md(body string) string { return "# x\n\n```go\n" + body + "\n```\n" }

// okMark 是「通过」那一行的标记。
//
// 不能用「一致。」——它是「2 处不一致。」的子串，
// **那个断言会在一份失败的报告上通过**。写这套测试时它第一个抓到的就是它自己：
// 一个只在「看起来对」的层面上成立的断言。
//
// 同一形状在别处出现过：`Full` 曾同时意味着两件事、`PASS` 曾承载两个相反结论。
// **子串匹配天生会把「否定」读成「肯定」**，标记要选一个不可能被包含的。
const okMark = "一致。（"

func TestReportWording(t *testing.T) {
	for _, f := range []fixture{
		{
			name:    "一致时不该报任何问题",
			doc:     md("type Bar struct {\n    Ts int64\n}"),
			src:     "type Bar struct {\n\tTs int64\n}",
			want:    []string{okMark},
			notWant: []string{"找不到", "签名不同", "孤儿", "欠条", "矛盾"},
		},
		{
			// 曾经的真 bug①：源码侧只登记 Names[0]，
			// 于是 High/Low/Close 被报成「源码里找不到」——**假警报**。
			// 一条假警报比没有警报更糟：它教人忽略这个工具。
			name:    "一行多名的字段，四个都要认得（曾误报三个）",
			doc:     md("type Bar struct {\n    Open, High, Low, Close float64\n}"),
			src:     "type Bar struct {\n\tOpen, High, Low, Close float64\n}",
			want:    []string{okMark},
			notWant: []string{"Bar.High", "Bar.Low", "Bar.Close", "找不到"},
		},
		{
			// 曾经的真 bug②：文档里一行写完的结构体没有独立的 `}`，
			// 往下扫会一路跑进【下一个】类型的 body，
			// 把 Day 的字段登记成 Session.Num / Session.Sessions。
			//
			// 断言故意让文档与源码的字段类型【不同】：只断言「Session.Num 没出现」
			// 是不够的——扫飞时解析会失败、什么都不登记，而**没登记是不报的**，
			// 于是一份「什么都没说」的报告照样满足那个否定断言。
			// 让它必须报出一处 mismatch，「什么都没说」才会被抓住。
			name: "一行写完的结构体，不能扫进下一个类型",
			doc: md("type Session struct{ Start, End int32 }\n\n" +
				"type Day struct {\n    Num int32\n}"),
			src: "type Session struct {\n\tStart, End int64\n}\n\n" +
				"type Day struct {\n\tNum int32\n}",
			want:    []string{"Session.Start", "文档: int32", "源码: int64"},
			notWant: []string{"Session.Num", "Session.Sessions", okMark},
		},
		{
			// 上一条的姊妹。两者的存在理由是：**那个 bug 有两道刹车挡着，
			// 而它们对同一份输入是【冗余】的**——变异验证显示，
			// 只拆掉任意一道，上一条仍然全绿；两道同时拆掉才变红。
			// 那意味着「少了一层防护」会静默发生。
			//
			// 这一条专挑只有 `startsNewDecl` 挡得住的输入：结构体在文档片段里
			// **根本没有闭合**就撞上了下一个声明（文档写残是常态）。
			// 头一行没有 `}`，所以那道刹车用不上。
			//
			// 断言用「类型不一致」而不是「字段没登记」：跑飞时解析会失败、
			// 什么都不登记，而**没登记是不报的**——一份「什么都没说」的报告
			// 看起来和「一致」一模一样。
			name: "结构体没闭合就撞上下一个声明，只有 startsNewDecl 挡得住",
			doc: md("type Session struct {\n    Start int32\n\n" +
				"type Day struct {\n    Num int32\n}"),
			src: "type Session struct {\n\tStart int64\n}\n\n" +
				"type Day struct {\n\tNum int32\n}",
			want:    []string{"名字在、【签名不同】", "Session.Start", "文档: int32", "源码: int64"},
			notWant: []string{"Session.Num", okMark},
		},
		{
			// 曾经的真 bug③：这一句是【字面上的假话】——
			// 文档声明了、源码也实现了，说的却是「文档里没有声明它」。
			// 而它落在最常发生的那一种情况上：版本收尾忘了清白名单。
			name:    "欠条已兑现：说的是「删掉这一行」，不是「文档里没声明」",
			doc:     md("type Source interface {\n    Bars() error\n}"),
			src:     "type Source interface {\n\tBars() error\n}",
			pending: map[string]string{"Source": "v0.2.0"},
			want: []string{
				"欠条已兑现，但白名单没清",
				"源码里已经有了，欠条该销：把这一行从 pending.txt 删掉",
			},
			notWant: []string{"白名单里有，但【文档里】没有声明它"},
		},
		{
			name:    "真孤儿：白名单写了个文档里根本没有的名字",
			doc:     md("type Bar struct {\n    Ts int64\n}"),
			src:     "type Bar struct {\n\tTs int64\n}",
			pending: map[string]string{"ZzzTypo": "v0.9.0"},
			want: []string{
				"白名单里的孤儿项",
				"ZzzTypo (v0.9.0)",
				"写错名字？还是文档删了没同步？",
			},
			notWant: []string{"欠条该销"},
		},
		{
			// 「存在 ≠ 一致」在字段上的形状。design.md 曾一边论证
			// 「具名类型而非 int32 别名」，一边把 Bar.TradingDay 写成 int32。
			name: "字段类型不一致，要指名道姓并给出两边",
			doc:  md("type Bar struct {\n    TradingDay int32\n}"),
			src:  "type TradingDay int32\n\ntype Bar struct {\n\tTradingDay TradingDay\n}",
			want: []string{"名字在、【签名不同】", "Bar.TradingDay", "文档: int32", "源码: TradingDay"},
		},
		{
			name: "函数签名不一致，参数名不同【不算】不一致",
			doc:  md("func (p IntradayPeriod) Bars(tmpl SessionTemplate, d Day) []BarBound"),
			src: "type IntradayPeriod int\ntype SessionTemplate int\ntype Day int\ntype BarBound int\n\n" +
				"func (p IntradayPeriod) Bars(t SessionTemplate, dd Day) []BarBound { return nil }",
			want:    []string{okMark},
			notWant: []string{"签名不同"},
		},
		{
			name:    "白名单继承：类型在表里，它的字段一并算欠条",
			doc:     md("type SyncRequest struct {\n    Force bool\n}"),
			src:     "type X int",
			pending: map[string]string{"SyncRequest": "v0.2.0"},
			want:    []string{okMark, "2 项记为「尚未实现」：1 项白名单直接写明，1 项由所属类型继承"},
			notWant: []string{"找不到"},
		},
		{
			// 继承来的欠条【同样会到期】——否则继承就成了逃生通道。
			name:    "继承来的欠条同样过期",
			doc:     md("type SyncRequest struct {\n    Force bool\n}"),
			src:     "type X int",
			pending: map[string]string{"SyncRequest": "v0.2.0"},
			tagged:  []string{"v0.2.0"},
			want: []string{
				"白名单已过期",
				"SyncRequest —— 白名单写着 v0.2.0",
				"SyncRequest.Force —— 白名单写着 v0.2.0",
			},
		},
		{
			// 文档要先跟自己一致，才谈得上跟代码一致。
			name: "文档内部：同一标识符两处声明不一致",
			doc: "# x\n\n```go\ntype S interface {\n    Caps() int\n}\n```\n\n" +
				"更多说明\n\n```go\nfunc (s S) Caps(k int) int\n```\n",
			src:  "type S interface {\n\tCaps(k int) int\n}",
			want: []string{"【文档内部】同一标识符声明了两次且不一致", "S.Caps"},
		},
		{
			// ⛔ 负数常量：**两侧口径必须一致**。
			// 源码侧原来只认 *ast.BasicLit，而 `-1` 在 AST 里是 UnaryExpr ⇒ 给空；
			// 文档侧 litOf 明写着接受前导 `-` ⇒ 给 "-1" ⇒ **一处假的「签名不同」**。
			// 而源码侧那句注释当时写着「与文档侧 litOf 的口径一致」—— 它是假的。
			name:    "负数常量两侧口径一致（曾经假报「签名不同」）",
			doc:     md("const BatchDaysUnbounded = -1"),
			src:     "const BatchDaysUnbounded = -1",
			want:    []string{okMark},
			notWant: []string{"签名不同", "BatchDaysUnbounded"},
		},
		{
			// 对照：负数常量**真的不一致**时仍然要报 —— 否则上一条也可能是「它不再比负数了」。
			name: "负数常量真的不同，仍要报",
			doc:  md("const BatchDaysUnbounded = -1"),
			src:  "const BatchDaysUnbounded = -2",
			want: []string{"名字在、【签名不同】", "BatchDaysUnbounded", "文档: -1", "源码: -2"},
		},
		{
			name:    "源码里找不到、又不在白名单",
			doc:     md("type Ghost struct {\n    A int\n}"),
			src:     "type X int",
			want:    []string{"源码里找不到，且不在白名单", "Ghost", "Ghost.A"},
			notWant: []string{okMark},
		},
	} {
		t.Run(f.name, func(t *testing.T) {
			got := f.run(t)
			for _, w := range f.want {
				if !strings.Contains(got, w) {
					t.Errorf("报告里缺少 %q\n--- 实际报告 ---\n%s", w, got)
				}
			}
			for _, w := range f.notWant {
				if strings.Contains(got, w) {
					t.Errorf("报告里【不该】出现 %q\n--- 实际报告 ---\n%s", w, got)
				}
			}
		})
	}
}

// TestReportListsAnActionForEveryCategory 每个报得出来的类别，
// 收尾提示里都要有对应的处置动作。
//
// 「欠条已兑现」那一类曾经报得出来，而收尾提示里【根本没有】
// 「从 pending.txt 删掉」这个动作——**一个报得出来却没告诉你怎么办的类别，
// 等于报了一半**。
func TestReportListsAnActionForEveryCategory(t *testing.T) {
	f := findings{
		empty:     []string{"g"},
		missing:   []string{"a"},
		mismatch:  []string{"b"},
		staleWL:   []string{"c"},
		orphan:    []string{"d"},
		paid:      []string{"e"},
		conflicts: []string{"f"},
		used:      map[string]bool{},
	}
	got := f.render()
	for _, pair := range [][2]string{
		// ⚠️ 第一列要挑一个【只出现在小节标题里】的串。
		// 「什么都没读到」同时是处置行的前缀 ⇒ 拿它当第一列，
		// 小节整个不打印这条断言也照样绿（okMark 那一格的同族）。
		{"⛔ 什么都没读到（这【不是】一致）", "什么都没读到   →"},
		{"源码里找不到，且不在白名单", "源码里找不到   →"},
		{"名字在、【签名不同】", "签名不同       →"},
		{"白名单已过期", "白名单已过期   →"},
		{"白名单里的孤儿项", "孤儿项         →"},
		{"欠条已兑现，但白名单没清", "欠条已兑现     →"},
		{"【文档内部】同一标识符声明了两次且不一致", "文档内部矛盾   →"},
	} {
		if !strings.Contains(got, pair[0]) {
			t.Errorf("少了类别 %q", pair[0])
		}
		if !strings.Contains(got, pair[1]) {
			t.Errorf("类别 %q 报得出来，但收尾提示里没有它的处置动作", pair[0])
		}
	}
	if f.count() != 7 {
		t.Errorf("count() = %d，期望 7", f.count())
	}
}

// TestFromDocsIsIdempotent docConflicts 是包级的，跨调用累积就会让
// 第二次调用继承第一次的结论。main 只调一次看不出来。
func TestFromDocsIsIdempotent(t *testing.T) {
	f := fixture{
		doc: "# x\n\n```go\ntype S interface {\n    Caps() int\n}\n```\n\n" +
			"```go\nfunc (s S) Caps(k int) int\n```\n",
		src: "type S interface {\n\tCaps(k int) int\n}",
	}
	first := strings.Count(f.run(t), "S.Caps")
	second := strings.Count(f.run(t), "S.Caps")
	if first != second {
		t.Errorf("同样的输入跑两次结论不同：第一次 %d 处、第二次 %d 处——"+
			"包级状态在跨调用累积", first, second)
	}
}

// TestReadingNothingIsNotAgreement 复现 2026-09-09 那次假 PASS。
//
// 在 tools/doccheck/ 里跑（而不是仓库根），两侧都读到 0 处，
// 而报告与仓库根那次成功【同一句话、同一个退出码】：
//
//	仓库根          文档 134 · 源码 204 · 白名单 18  ⇒「一致。」exit 0
//	tools/doccheck  文档 0   · 源码 0   · 白名单 0   ⇒「一致。」exit 0
//
// ⇒ 一个 cd 错地方的 CI 步骤会全程绿灯，而它守的正是文档与源码的一致。
// 这是本仓记过的第一副面孔：**空输出 ⇒ 假 PASS**。
func TestReadingNothingIsNotAgreement(t *testing.T) {
	// 一个既没有 docs/、也没有带导出声明的 .go 的根 —— 正是跑错目录时的样子。
	root := t.TempDir()

	docDecls, err := fromDocs(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("fromDocs 对着不存在的目录是【无错地】返回空的——"+
			"正是这一点让假 PASS 成立；它要是改成报错，这条测试就该换成断言那个错：%v", err)
	}
	srcDecls, err := fromSource(root)
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 先验前提。这个 fixture 要的就是「两侧都空」——
	// 前提不成立的话，下面那几条断言【什么也没验】，而它们照样会绿。
	if len(docDecls) != 0 || len(srcDecls) != 0 {
		t.Fatalf("这个 fixture 要的是两侧都空，实得 文档 %d 处 / 源码 %d 处——"+
			"不是断言错了，是前提没成立", len(docDecls), len(srcDecls))
	}

	res := compare(docDecls, srcDecls, nil, nil, func(string) bool { return false })
	got := res.render()

	if strings.Contains(got, okMark) {
		t.Errorf("两侧读到 0 处，报告却打了 %q——空输出被读成了「全部一致」\n%s", okMark, got)
	}
	if res.count() == 0 {
		t.Errorf("count() = 0 ⇒ main 会 exit 0，跑错目录的 CI 步骤全程绿灯\n%s", got)
	}
	// 两侧【分开报】：成因不同、下一步动作也不同，合成一条会把人指向错方向。
	for _, want := range []string{
		"文档侧【一处声明都没读到】",
		"源码侧【一处声明都没读到】",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("报告里少了 %q\n%s", want, got)
		}
	}
}
