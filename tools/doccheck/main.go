// doccheck 核对【文档里声明的东西】与【源码里真实存在的东西】。
//
// 为什么要它：docs/design.md 里的类型名、字段名、函数签名是**设计稿**。
// 设计稿和代码分头演化时，文档不会报错，它只会慢慢变成一份自信的谎。
// 人眼核对签名一致性，是那种做过一次就不会再做第二次的事。
//
// 四条设计要点，各自防一类失效（前两条评审提的，后两条是拿真改动去试守卫时试出来的）：
//
//  1. **比签名，不比名字。** 只查「标识符在源码里找得到」挡不住最要紧的那类：
//     `IntradayPeriod.Bars` 名字还在，签名从 `(tmpl, td)` 变成了 `(td)`——
//     名字对得上，**契约已经变了**，而守卫全绿。那比没有守卫更坏。
//
//  2. **白名单会自己过期。** 「尚未实现」的条目必须写明预定版本；
//     **一旦那个版本已经打过 tag，守卫直接 FAIL**。不靠人自觉去清理，
//     靠时间推着清——否则白名单迟早变成垃圾桶，和「一个会红的必过项
//     迟早被加 || true」是同一个形状。
//     类型在白名单里时，它的字段一并继承那张欠条（含到期日），
//     否则白名单会从 13 行涨到 40 行——**一张没人愿意读的表等于没有表**。
//
//  3. **结构体字段也比类型。** 原先类型只登记名字，于是
//     `Bar.Flags` 文档写 uint32、源码是 BarFlags，`Bar.TradingDay` 文档写 int32、
//     源码是 TradingDay——**四处真实分歧，守卫全绿**。而具名类型正是 design.md
//     自己论证过的东西（「具名类型而非 int32 别名，防的正是它与自然日混用」）。
//
//  4. **文档内部也要自洽。** 同一个标识符可能在两节里各声明一遍
//     （§2 给改本库的人，§12 与下游记账内核对齐）。map 直接覆盖的话，
//     只有后一个会被拿去对源码，前一个错了没有声音。
//     实测抓到一处：`Source.Caps` 在 §5 是 `()`、在下一小节是 `(ProductKey)`。
//
// 3 与 4 的由来值得记：它们是**拿一处真实改动去试这个守卫**时暴露的
// （给 BarBound 加了 Anomalous 字段，然后问「我改了文档没改代码，它会不会红」——
// 答案是不会）。**守卫也要被验，不然它只是让人放心。**
//
// 用法：
//
//	go run ./tools/doccheck          # 核对
//	go run ./tools/doccheck -list    # 只列出文档声明了什么
//
// ⚠️ 这个工具【比不了】的两格，写在这里而不是留给下一个人去发现
// （前两条是评审方 2026-09-08 实测出来的，第三条 2026-09-09；三条都退出码 0）：
//
//	一、**表达式形态的值只比名字，不比值。**
//	   只有基本字面量（带引号的串、数字）才进 Sig；函数调用、iota、表达式一律留空。
//	   实测：把契约里 `CST = time.FixedZone("CST", 8*3600)` 的 8 改成 9 ⇒ 全绿。
//
//	   ⚠️ 而这一格有一层反讽必须说破：`CST` 之所以被写进 contract.md，
//	   理由正是「**值就是契约**」（照文档写 `SHFEX` 的人会得到一个查不到的交易所）——
//	   **而 `CST` 的值恰恰是那个比不了的形态：一个函数调用里的数字。**
//	   时区偏移写错一小时，症状是**所有墙钟时间整体偏移一小时**：
//	   结构完好、数值合法、内容全错。
//
//	   不改成硬比，理由是**假警报**：`time.FixedZone` 两边写法可以合法地不同，
//	   而**一条假警报教人忽略整个工具**。所以这一格的处置是【写明】，不是【加严】。
//
//	二、**iota 成员的值由【顺序】给，而顺序不比。**
//	   实测：把 `AggRule` 那个 iota 块里两个成员调换顺序 ⇒ 名字全在、值全变、全绿。
//	   今天危害有限（落盘用的是名字形态 `…-ratioback-v1`，不是 iota 的数值），
//	   敞口在 API 层。
//
//	三、**围栏里的匿名内嵌结构体，会让它【静默少看】后面的声明。**
//	   实测（2026-09-09，我自己写文档时撞的）：往 `SyncReport` 的围栏里加一个
//
//	       NightAbsentRun struct {
//	           Days int
//	       }
//
//	   ⇒ 「文档声明」从 **121 掉到 113**，**8 处凭空消失，而 doccheck 报「一致」。**
//	   成因是解析遇到嵌套的 `}` 就当成整个结构体结束，其后的字段全被丢掉。
//
//	   ⚠️ **这一条和前两条不是同一档，危险性也不同：**
//
//	       前两条  比得【不够严】——数量不变，内容没比
//	       这一条  比的【东西变少了】——而少比的那部分，**输出里连个数都不给你看**
//
//	   > **一个「一致」，可以是「都对上了」，也可以是「没剩几个要对」。
//	   > 而这两者在输出里长得一模一样。**
//
//	   处置分两半：
//	   **① 立刻能做的**：文档里**不用匿名内嵌结构体，改具名类型**——
//	      本仓 `NightAbsent` 就是为此拆出来的，理由写在它旁边。
//	   **② 真正的修法**：把「文档声明数」也纳入高水位（`tools/audit/high_water.txt`）——
//	      表缩水就当场红。那套机制已经在了，只差把这个数接进去。
//	      **它没做，所以这一条今天靠人读到这段话顶着。**
//
// 为什么把这三条写在这儿：用的是本仓刚对 `CST` 用过的那一句——
// **要么比，要么写明它不比；原来那样是第三种：没人决定过。**
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

	res := compare(docDecls, srcDecls, pending, docConflicts,
		func(v string) bool { return tagExists(*root, v) })
	fmt.Print(res.render())
	if res.count() > 0 {
		os.Exit(1)
	}
}

// findings 是一次比对的全部结论，按类别分开。
//
// 把它从 main 里拆出来，是为了让测试能【读到措辞】。
// 本工具至今出过三个真 bug，三个的形态一模一样：**退出码正确、文本错误**——
// 一行多名的字段只登记第一个（误报三个「找不到」）、单行结构体扫进了下一个类型、
// 「欠条已兑现」被说成「文档里没声明」。
// 这类失败自动化天然抓不住，除非把**期望的输出文本**也写进断言。
type findings struct {
	docN, srcN, wlN int
	missing         []string
	mismatch        []string
	staleWL         []string
	orphan          []string
	paid            []string
	conflicts       []string
	used            map[string]bool
	direct          int
}

func (f findings) count() int {
	return len(f.missing) + len(f.mismatch) + len(f.staleWL) +
		len(f.orphan) + len(f.paid) + len(f.conflicts)
}

// compare 是全部判定逻辑。tagged 注入，测试不必真去建 git tag。
func compare(docDecls, srcDecls map[string]decl, pending map[string]string,
	conflicts []string, tagged func(string) bool) findings {
	f := findings{
		docN: len(docDecls), srcN: len(srcDecls), wlN: len(pending),
		conflicts: conflicts, used: map[string]bool{},
	}
	for _, d := range sorted(docDecls) {
		s, ok := srcDecls[d.Name]
		if !ok {
			if v, wl := whitelisted(pending, d.Name); wl {
				f.used[d.Name] = true
				if tagged(v) {
					f.staleWL = append(f.staleWL,
						fmt.Sprintf("%s —— 白名单写着 %s，而 %s 已经打过 tag", d.Name, v, v))
				}
				continue
			}
			f.missing = append(f.missing,
				fmt.Sprintf("%s (%s) —— 文档 %s", d.Name, d.Kind, d.Src))
			continue
		}
		if d.Sig != "" && s.Sig != d.Sig {
			f.mismatch = append(f.mismatch, fmt.Sprintf(
				"%s\n      文档: %s   (%s)\n      源码: %s   (%s)",
				d.Name, d.Sig, d.Src, s.Sig, s.Src))
		}
	}

	// 白名单里没被用上的条目，分两种——**它们要做的事不一样，就不能共用一句话**。
	//
	// 原来两种都报「文档里没有声明它」，而其中一种文档明明声明了、
	// 源码也实现了——**报告本身在说一件不真的事**，而这个工具的全部价值
	// 就是「报告说的是真的」。
	for name, v := range pending {
		if _, done := srcDecls[name]; done {
			// 欠条已经兑现：v0.3 把 Source 写出来了，这一行该删了。
			// 这【不是】守卫坏了，也不是「设计如此的红」——
			// 它是版本收尾动作里漏了一步，红得其所。
			f.paid = append(f.paid, fmt.Sprintf(
				"%s (%s) —— 源码里已经有了，欠条该销：把这一行从 pending.txt 删掉",
				name, v))
			continue
		}
		if !f.used[name] {
			f.orphan = append(f.orphan, fmt.Sprintf(
				"%s (%s) —— 白名单里有，但【文档里】没有声明它（写错名字？还是文档删了没同步？）",
				name, v))
		}
	}
	sort.Strings(f.paid)
	sort.Strings(f.orphan)
	// 分开数：直接写在 pending.txt 里的，和由所属类型继承来的。
	// 合成一个数会让白名单看起来比实际长——**报告里的每个数也要是真的**。
	for n := range f.used {
		if _, ok := pending[n]; ok {
			f.direct++
		}
	}
	return f
}

// render 把结论渲染成报告。测试比对的就是这段文本。
func (f findings) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "文档声明 %d 处；源码声明 %d 处；白名单 %d 项\n\n", f.docN, f.srcN, f.wlN)
	sec := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "== %s（%d）==\n", title, len(items))
		for _, s := range items {
			fmt.Fprintln(&b, "  "+s)
		}
		fmt.Fprintln(&b)
	}
	sec("源码里找不到，且不在白名单", f.missing)
	sec("名字在、【签名不同】", f.mismatch)
	sec("白名单已过期（预定版本已打 tag）", f.staleWL)
	sec("白名单里的孤儿项", f.orphan)
	sec("欠条已兑现，但白名单没清", f.paid)
	sec("【文档内部】同一标识符声明了两次且不一致", f.conflicts)

	if f.count() == 0 {
		// 这个数有【双重身份】，两个都要说出来。
		//
		// 它是「欠条数」，也是「盲区大小」：字段与签名的比对只在类型
		// 【已实现】时才触发，而白名单里的类型没有源码可比——
		// 所以这些项的字段与签名**当前不受任何检查**。
		//
		// 实测代价：v0.3/v0.6 设计里三处把 TradingDay 写成 int32，
		// 与本工具在 Bar.TradingDay 上抓到的是同一族，而它们在这里活了下来。
		//
		// 盲区是**必然的**——白名单存在的理由就是「源码里还没有」，那就必然没得比。
		// 所以处置不是消除，是**让它每次跑都在眼前**：
		// **提示会被读，而躺在风险表里的一行不会。**
		fmt.Fprintf(&b, "一致。（%d 项记为「尚未实现」：%d 项白名单直接写明，%d 项由所属类型继承）\n",
			len(f.used), f.direct, len(f.used)-f.direct)
		fmt.Fprintf(&b, "⚠️ 这 %d 项同时也是【盲区大小】：尚未实现 ⇒ 它们的字段与签名"+
			"当前不受任何检查。\n"+
			"   实现那天比对第一次生效，冒出来的分歧【不是新引入的】，别当成回归。\n",
			len(f.used))
		return b.String()
	}
	fmt.Fprintf(&b, "%d 处不一致。\n", f.count())
	b.WriteString("按类别处置：\n" +
		"  源码里找不到   → 实现它，或改文档，或写进 pending.txt 并注明预定版本\n" +
		"  签名不同       → 改源码或改文档，让两边一致\n" +
		"  白名单已过期   → 那个版本到了而实现没做完：做完它，或推迟预定版本并说明\n" +
		"  欠条已兑现     → 实现做完了：把那几行从 pending.txt 删掉\n" +
		"  孤儿项         → 名字写错了，或文档删了没同步\n" +
		"  文档内部矛盾   → 同一标识符两处声明不一致，先让文档跟自己一致\n")
	return b.String()
}

// docConflicts 记的是【同一个标识符在文档里被声明了两次、而且不一样】。
//
// design.md 的 §2（给改本库的人）与 §12（与下游记账内核对齐）各声明了一遍
// SessionTemplate。map 直接覆盖的话，doccheck 只会拿【后一个】去对源码——
// 若后一个碰巧对得上，前一个错了也全程没有声音。
//
// 两处都在文档里、都被人读、彼此矛盾，而守卫全绿：
// 这和「名字对得上但签名变了」是同一类，只是发生在文档内部。
//
// 用包级变量是因为这是个跑一次就退出的小工具，
// 为它把 scanBlock / scanStruct 的签名全改一遍不划算。
var docConflicts []string

// put 登记一处文档声明，并在与已有声明矛盾时记一笔。
func put(out map[string]decl, d decl) {
	if old, ok := out[d.Name]; ok && old.Sig != d.Sig {
		docConflicts = append(docConflicts, fmt.Sprintf(
			"%s\n      %s: %s\n      %s: %s",
			d.Name, old.Src, old.Sig, d.Src, d.Sig))
	}
	out[d.Name] = d
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
	// 每次调用重置：docConflicts 是包级的，跨调用累积就会让第二次调用
	// 继承第一次的结论。main 只调一次看不出来，测试一调两次就现形。
	docConflicts = nil
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
// valueName 从一行 `Name = …` / `Name Type = …` 里取出导出的标识符名。
// 只认行首就是名字的形式；`x, y = …` 这种多名的返回空（本仓没有，出现了该单独想）。
func valueName(t string) string {
	f := strings.Fields(t)
	if len(f) < 2 || strings.Contains(f[0], ",") {
		return ""
	}
	if !isExported(f[0]) {
		return ""
	}
	// 必须真的是一条声明：`Name =` 或 `Name Type =`
	if f[1] != "=" && !(len(f) > 2 && f[2] == "=") {
		return ""
	}
	return f[0]
}

// valueNameInBlock 用于 `var (` / `const (` 分组【内部】：行首是导出标识符就算一条声明，
// 不要求有 `=`（iota 续行没有）。注释行与空行由调用方之外的判断挡掉。
func valueNameInBlock(t string) string {
	if t == "" || strings.HasPrefix(t, "//") {
		return ""
	}
	f := strings.Fields(t)
	if len(f) == 0 || strings.Contains(f[0], ",") || !isExported(f[0]) {
		return ""
	}
	return f[0]
}

// litOf 取一行声明里 `=` 右边那个【字面量】，取不到就返回空串。
//
// ⚠️ 为什么要取值，不能只登记名字：
// 第一版只登记名字，于是把文档里的 `SHFE = "SHFE"` 改成 `"SHFEX"` —— **全绿**。
// 而对常量来说【值就是契约】：照着文档写 `SHFEX` 的人会得到一个查不到的交易所。
//
// 这和评审方当初找到的「类型只登记名字、字段不参与比对」是同一个形状：
// **存在 ≠ 一致。** 只登记名字的守卫，挡的是改名，不是改值。
//
// 只认基本字面量（带引号的串、数字）：`iota`、函数调用、表达式一律返回空串 ⇒
// 那些项只比名字。**这是有意的**——`time.FixedZone("CST", 8*3600)` 两边的写法
// 可以合法地不同，硬比会变成假警报，而一条假警报教人忽略整个工具。
func litOf(t string) string {
	i := strings.Index(t, "=")
	if i < 0 {
		return ""
	}
	rhs := strings.TrimSpace(t[i+1:])
	if j := strings.Index(rhs, "//"); j >= 0 { // 去掉行尾注释
		rhs = strings.TrimSpace(rhs[:j])
	}
	if rhs == "" {
		return ""
	}
	if rhs[0] == '"' || rhs[0] == '`' || (rhs[0] >= '0' && rhs[0] <= '9') ||
		(rhs[0] == '-' && len(rhs) > 1 && rhs[1] >= '0' && rhs[1] <= '9') {
		return rhs
	}
	return ""
}

func scanBlock(lines []string, file string, base int, out map[string]decl) {
	// inValue 表示正处在 `var (` / `const (` 的分组里。
	//
	// ⚠️ 加这一路的原因（2026-09-08，评审方与我各验一次）：
	// scanBlock 原来只认 func 与 type，于是**所有 var / const 声明都不进表**。
	// 构造验证：把文档里的 ErrUncovered 改名 → doccheck 退出码 0，声明数一字未变。
	// ⇒ ErrNotTradingDay / ErrClosed / ErrUncovered 从来没被比对过，
	//   而它们正是 contract.md §5 那条破坏性变更的全部内容。
	//
	// 评审方把它的性质说准了：**「没有东西在检查它」不等于「它是错的」。**
	// 他逐字比过那三个，今天是一致的 ⇒ 这是【逾期】，不是【告急】：
	// 它没有藏错，只是藏错了也不会有人知道。
	inValue := false
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		t := strings.TrimSpace(ln)
		where := fmt.Sprintf("%s:%d", file, base+i)

		if inValue {
			if t == ")" {
				inValue = false
				continue
			}
			// ⚠️ 分组【里面】的判据比外面松：iota 续行没有 `=`
			//（`AggTradingAxis   // 交易时间轴`），而它同样是一条声明。
			// 第一版只认带 `=` 的，于是每个 iota 块【只有第一个】进表——
			// 那是「修了一半」的又一次，所以这里单独放宽。
			if name := valueNameInBlock(t); name != "" {
				put(out, decl{Name: name, Kind: "value", Sig: litOf(t), Src: where})
			}
			continue
		}

		switch {
		case t == "var (" || t == "const (":
			inValue = true
			continue
		case strings.HasPrefix(t, "var ") || strings.HasPrefix(t, "const "):
			if name := valueName(strings.TrimSpace(t[strings.Index(t, " "):])); name != "" {
				put(out, decl{Name: name, Kind: "value", Sig: litOf(t), Src: where})
			}
			continue
		}

		switch {
		case strings.HasPrefix(t, "func "):
			if d, ok := parseFunc(t, where); ok {
				put(out, d)
			}
		case strings.HasPrefix(t, "type "):
			name, kind := typeName(t)
			if name == "" {
				continue
			}
			put(out, decl{Name: name, Kind: "type", Src: where})
			switch kind {
			case "interface":
				// 接口里的方法也算契约
				for j := i + 1; j < len(lines); j++ {
					mt := strings.TrimSpace(lines[j])
					if mt == "}" || startsNewDecl(mt) {
						i = j
						break
					}
					if mt == "" || strings.HasPrefix(mt, "//") {
						continue
					}
					if d, ok := parseIfaceMethod(name, mt,
						fmt.Sprintf("%s:%d", file, base+j)); ok {
						put(out, d)
					}
				}
			case "struct":
				// 结构体的字段同样算契约。
				//
				// 原先类型只登记名字、Sig 为空，于是【文档写 int、代码是 bool】
				// 完全查不出来——那正是「存在 ≠ 一致」在类型上的形状，
				// 和签名变了是同一类。这个洞是拿一处真实改动去试守卫时试出来的：
				// **守卫也要被验，不然它只是让人放心。**
				i = scanStruct(name, lines, i, file, base, out)
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
				// var / const：两侧必须【同时】认，否则一侧多出一批、另一侧没有，
				// 报告会立刻被一堆单向差异淹没。文档侧的那一路见 scanBlock。
				if n.Tok == token.VAR || n.Tok == token.CONST {
					for _, sp := range n.Specs {
						vs, ok := sp.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for k, id := range vs.Names {
							if !isExported(id.Name) {
								continue
							}
							// 只取基本字面量当 Sig，与文档侧 litOf 的口径一致；
							// iota / 函数调用 / 表达式 ⇒ 空，那些项只比名字。
							sig := ""
							if k < len(vs.Values) {
								if bl, ok := vs.Values[k].(*ast.BasicLit); ok {
									sig = bl.Value
								}
							}
							out[id.Name] = decl{Name: id.Name, Kind: "value", Sig: sig,
								Src: fmt.Sprintf("%s:%d", rel, fset.Position(id.Pos()).Line)}
						}
					}
					continue
				}
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
					if st, ok := ts.Type.(*ast.StructType); ok {
						structFields(fset, ts.Name.Name, st, rel, out)
					}
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

// structFields 把源码里结构体的导出字段登记成 `Type.Field`。
func structFields(fset *token.FileSet, typeName string, st *ast.StructType,
	rel string, out map[string]decl) {
	if st.Fields == nil {
		return
	}
	for _, f := range st.Fields.List {
		// 一行多名（`Open, High, Low, Close float64`）要【逐个】登记。
		// 只取 Names[0] 会让另外三个在源码侧凭空消失，
		// 于是文档里写着的它们被报成「源码里找不到」——
		// 一条假警报比没有警报更糟，它会教人忽略这个工具。
		for _, id := range f.Names {
			if !isExported(id.Name) {
				continue
			}
			n := typeName + "." + id.Name
			out[n] = decl{Name: n, Sig: render(fset, f.Type), Kind: "field",
				Src: fmt.Sprintf("%s:%d", rel, fset.Position(id.Pos()).Line)}
		}
	}
}

// startsNewDecl 报告这一行是不是另起了一个顶层声明。
//
// 文档里的结构体常写成一行（`type Session struct{ Start, End int64 }`），
// 那样往下扫是找不到独立的 `}` 的——会一路跑进【下一个】类型的body，
// 把后者的字段登记到前者名下。这个函数就是那道刹车。
func startsNewDecl(t string) bool {
	for _, p := range []string{"type ", "func ", "const ", "var ", ")"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// scanStruct 把文档里一个结构体的导出字段登记成 `Type.Field`，返回消费到的行号。
//
// 整块交给 go/parser，不逐行切字符串——`Foo map[string][]Bar` 这种切不对，
// 而「切错了却不报错」正是本工具要防的那类东西的同款。
func scanStruct(name string, lines []string, i int, file string, base int,
	out map[string]decl) int {
	end := i
	if !strings.Contains(lines[i], "}") { // 一行写完的结构体不必往下扫
		for j := i + 1; j < len(lines); j++ {
			mt := strings.TrimSpace(lines[j])
			if mt == "}" {
				end = j
				break
			}
			if startsNewDecl(mt) { // 结构体没闭合就撞上了下一个声明
				end = j - 1
				break
			}
			end = j
		}
	}
	body := append([]string{"package d"}, lines[i:end+1]...)
	if !strings.Contains(lines[end], "}") {
		body = append(body, "}")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "d.go", strings.Join(body, "\n"), parser.ParseComments)
	if err != nil {
		return end // 片段解析不了就跳过，不猜
	}
	ast.Inspect(f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, fl := range st.Fields.List {
			for _, id := range fl.Names {
				if !isExported(id.Name) {
					continue
				}
				// 合成源码第 1 行是 "package d"，第 2 行对应 lines[i]
				docLine := base + i + fset.Position(id.Pos()).Line - 2
				fn := name + "." + id.Name
				put(out, decl{Name: fn, Sig: render(fset, fl.Type), Kind: "field",
					Src: fmt.Sprintf("%s:%d", file, docLine)})
			}
		}
		return true
	})
	return end
}

// whitelisted 查白名单，并让【字段继承所属类型的欠条】。
//
// `SyncRequest` 记着 v0.3.0 尚未实现，那么 `SyncRequest.Force` 当然也还不存在——
// 逼着为每个字段单写一行，白名单会从 13 行涨到 40 行，
// 而**一张没人愿意读的表和没有这张表是一回事**。
//
// 继承来的欠条同样带着那个版本，所以「预定版本一打 tag 就 FAIL」照旧生效：
// v0.3.0 落地时，SyncRequest 的字段会和 SyncRequest 本身一起到期。
func whitelisted(pending map[string]string, name string) (string, bool) {
	if v, ok := pending[name]; ok {
		return v, true
	}
	if i := strings.Index(name, "."); i > 0 {
		if v, ok := pending[name[:i]]; ok {
			return v, true
		}
	}
	return "", false
}
