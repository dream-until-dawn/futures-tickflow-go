package tickflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 这两条测试守的不是代码，是 docs/method-landing.md 那张落点表。
//
// 那张表的立意是「规矩写在你那一刻会打开的文件里」，
// 而它自己列了三个已知失效，其中一个是可以机械挡住的：
//
//	载体文件被改名/删掉/清空 → 链接还在，规矩没了。
//
// 剩下两个（「那个文件其实没人打开」「新规矩只在对话里」）挡不住，
// 已在 method-landing.md 第四节写明——**别把这两条测试读成「那张表守住了」。**

var mdLink = regexp.MustCompile(`\]\(([^)]+)\)`)

// TestDocLinksResolve 断言仓库里每个 .md 的相对链接都指向真实存在的文件。
//
// 变红通常意味着：有人移动或改名了一个载体文件，而落点表还指着旧路径。
func TestDocLinksResolve(t *testing.T) {
	var checked int
	seen := map[string]bool{}
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
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		seen[p] = true
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range mdLink.FindAllStringSubmatch(string(b), -1) {
			target := strings.SplitN(m[1], "#", 2)[0]
			if target == "" || strings.HasPrefix(target, "http") ||
				strings.HasPrefix(target, "mailto:") {
				continue
			}
			checked++
			full := filepath.Join(filepath.Dir(p), target)
			if _, err := os.Stat(full); err != nil {
				t.Errorf("%s 里的链接 %q 指不到东西（解析为 %s）", p, target, full)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走 .md 文件时出错：%v", err)
	}
	// 前提也要打印出来：不然「一个都没检查」和「全都通过」长得一样。
	t.Logf("检查了 %d 条相对链接，走过 %d 个 .md", checked, len(seen))

	// —— 对照组 ——
	// 「链接全对」和「遍历根本没跑到那儿」在结果上长得一样，所以要有已知为真的正例。
	//
	// 第一版这里写的是 `checked < 10`，而变异 X3（把递归打瘸、只看根目录）
	// **没有变红**：光根目录两个 .md 就凑够了 10 条链接。
	// 一个挡不住危险方向的检查比没有更坏——它让人以为那一格有人守。
	//
	// 换成点名两个【不同深度的子目录】里必然存在的文件：递归一断，两个一起消失。
	for _, must := range []string{
		filepath.Join("docs", "README.md"),           // 深一层
		filepath.Join("tools", "probe", "README.md"), // 深两层
	} {
		if !seen[must] {
			t.Fatalf("遍历没走到 %s——不是链接变少了，是【这次检查根本没覆盖到那里】。"+
				"在断言链接都对之前，先确认走到了", must)
		}
	}
}

// 落点表的载体：每个文件必须存在，且**还在讲它该讲的那件事**。
//
// needle 不是随手挑的一句话，是那个落点【最贵的那条规矩】——
// 它没了，这个落点就等于空的。加一个落点时在这里加一行。
var landingCarriers = []struct {
	file   string
	needle string
	why    string
}{
	{"tools/probe/README.md", "得拆一次，看它塌",
		"写探针：焊了对照组 ≠ 对照组在承重"},
	{"tools/audit/README.md", "它已经无法被审计了",
		"复核一条记录：不记怎么量的，格子就废了"},
	{"docs/README.md", "grep",
		"改文档：一处改正要覆盖同一断言的全部载体"},
	{"docs/README.md", "我是在提一个竞争性的说法，还是在取消这个问题",
		"反驳/更正：两种反驳的举证责任不同"},
	{"docs/README.md", "「撤回」和「改挂」是两个动作",
		"改文档：撤回弱证据别把结论一起降级"},
	{"docs/design.md", "编译不过不算变红",
		"破坏性验证：Go 的未使用即错误让变异根本没发生"},
	{"docs/design.md", "写自己的断言【之前】别看实现",
		"写断言：顺序不是权限"},
	{"CONTRIBUTING.md", "把疑虑打印出来，不等于解决了疑虑",
		"送审放行：需要解释的合并就是需要问一句的合并"},
	{"docs/method-landing.md", "按什么来源审",
		"落点表自己：审计的完整性受限于它用的来源"},
}

// TestLandingCarriersStillCarry 断言落点表的每个载体还在承重。
//
// 变红意味着：载体文件还在、链接也活着，但**那条最贵的规矩被删掉了**——
// 这正是 method-landing.md 第四节列的第三种失效。
func TestLandingCarriersStillCarry(t *testing.T) {
	for _, c := range landingCarriers {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Errorf("%s 读不到：%v（落点「%s」没有载体了）", c.file, err, c.why)
			continue
		}
		if !strings.Contains(string(b), c.needle) {
			t.Errorf("%s 里找不到 %q\n  这个落点是：%s\n"+
				"  文件还在、链接还活着，但这条规矩没了——"+
				"如果是有意删的，连这一行一起删；不然它只是变成了一个空落点",
				c.file, c.needle, c.why)
		}
	}
}

// —— 规则锚点表 ——
//
// 评审方拆出来的洞：上面那张 landingCarriers 每份文件只点名【一句】，
// 于是同一份文件里另外七八条规矩可以被静默删掉，两道守卫都不响。
// 他删的是 CONTRIBUTING.md 里「一条被丢弃了输出的命令…」——全绿。
//
// 而正确的形状两个提交之前就在仓库里了：tools/probe/shinny 的
// TestGuardListCoversEveryProbeFile——**新探针文件必须显式登记或豁免**。
// 我发明了那个模式，却没有把它用在自己刚建的这套东西上
// （「一条刚学会的规则，默认只作用在学它的那个位置上」，而这次学它的位置是我自己）。
//
// 所以往下套一层：**每一节规矩都要有一个锚点，或者一条写明理由的豁免。**
// 新加一节而不登记 ⇒ 变红；登记了但那一节没了 ⇒ 变红（幽灵项）；
// 那一节还在但锚点句被删 ⇒ 变红。
//
// 这张表是【生成】的，不是手敲的（tools/audit/gen_rule_anchors.py 从文件里抠出来）——
// 「凡是照着一个值去操作的动作，都别手敲」同样适用于登记表本身。
var ruleAnchors = []struct {
	file    string
	heading string // 一字不差的标题行
	needle  string // 必须出现在【这一节正文】里；豁免时为空
	exempt  string // 这一节为什么不承载规矩；有锚点时为空
}{
	{`tools/probe/README.md`, `## 写一条新探针之前，这八条`, ``, `小节标题，规矩在它下面各条里`},
	{`tools/probe/README.md`, `### 1. 必须有对照组，而且**要拆一次看它塌**`, `「真的没有 X」和「这条探针坏了」长得一模一样`, ``},
	{`tools/probe/README.md`, `### 2. 对照组的价值在它们之间的**差**，不在各自的**值**`, `1 个已知值挡不住「恰好返回那个值」；2 个不同的已知值挡得住任何常量。`, ``},
	{`tools/probe/README.md`, `### 3. 对照组还要问第三个问题：**它在你最需要它的那一天还在不在**`, `GFEX 有日盘，白天跑会走进`, ``},
	{`tools/probe/README.md`, `### 4. **取不到 ≠ 0；数据不全 ≠ 结论不符**`, `首末跨度 vs 根数`, ``},
	{`tools/probe/README.md`, `### 5. 把**判据**和**覆盖面**打进输出`, `只有把内容摆出来给人看，才有机会发现内容是错的`, ``},
	{`tools/probe/README.md`, `### 6. **依赖「恰好够」的探针 = 迟早会错的探针**`, `不是沉默，是发出一条指控`, ``},
	{`tools/probe/README.md`, `### 7. **间歇性的错误指控比确定性的更坏**`, `永远不会被修，只会被习惯`, ``},
	{`tools/probe/README.md`, `### 8. 探针有**生命周期**：提问期 → 守基线期`, `永久丧失发现答案翻转的能力`, ``},
	{`tools/probe/README.md`, `## 还有一条，关于「没有输入能触发这个分支」`, `通常没有能杀死它的输入`, ``},
	{`tools/probe/README.md`, `## 跑`, ``, `只是跑法，不是规矩`},
	{`tools/probe/README.md`, `### 为什么探针不许 import 本库——**在你想加一条豁免之前先读这段**`, `最省事的动作就是加一条豁免`, ``},
	{`docs/README.md`, `## 改文档之前，这八条`, ``, `小节标题，规矩在它下面各条里`},
	{`docs/README.md`, `### 1. **一处改正必须覆盖同一断言的全部载体**`, "`grep` 全仓", ``},
	{`docs/README.md`, `### 2. **一条只存在于对话里的规则，等于没有这条规则**`, `因为它感觉上最像「已经有了」`, ``},
	{`docs/README.md`, `### 3. 记一个数之前，先问它是**这个东西的属性**还是**这一次观测的属性**`, `连观测条件一起记`, ``},
	{`docs/README.md`, `### 3b. **「可忽略」要落到目标尺度上算一遍**——跨尺度比较时相对量会骗人`, `在目标尺度上是错的`, ``},
	{`docs/README.md`, "### 4. `X = A + B` 形式的数字：**那个拆解是打印出来的，还是推出来的？**", `一个总数看起来不够，拆开就够了。`, ``},
	{`docs/README.md`, `### 5. **读回别手敲**`, `刚写进去的那个变量本身`, ``},
	{`docs/README.md`, "### 6. 不是探针输出的数字，**产生它的脚本要落进 `tools/audit/`**", `内联跑的、没落盘`, ``},
	{`docs/README.md`, `### 7. **结论的强度是谁给的**`, `读者默认是写文档的人验的`, ``},
	{`docs/README.md`, `### 7b. **这条结论挂在哪根证据上？更强的那一根是不是就在旁边**`, `七个交易所，零条 GFEX`, ``},
	{`docs/README.md`, `### 8. 撤回一条结论时，**先问「换一条」和「撤掉这一维」哪个才对**`, `重新量能验证「是什么」，验证不了「在哪」——如果「在哪」本身不是个稳定的量。`, ``},
	{`docs/README.md`, `## 反驳 / 更正（别人的，或自己的）`, `反驳分两种，举证责任完全不同。动手前先分清你在做哪一种。`, ``},
	{`docs/README.md`, `## 置信度：三级，**别悄悄升格**`, `实测 / 推定 / 未验`, ``},
	{`CONTRIBUTING.md`, `## 一、送审要给三样，缺一退回`, `版本号 + 对应的排期行`, ``},
	{`CONTRIBUTING.md`, `### 材料里说「我做不到 / 分不开 / 想不出」的地方，**必须紧跟一句报价**`, `同时骗过写的人和审的人`, ``},
	{`CONTRIBUTING.md`, `## 二、每封报告状态的消息`, `把「哪一刻的快照」写进快照本身。`, ``},
	{`CONTRIBUTING.md`, `## 三、送审之后往同一分支加东西`, `评审方能【机械地】验证新增部分与送审对象不相交`, ``},
	{`CONTRIBUTING.md`, `## 四、放行清单：**每一项标明「已批准」还是「需另行送审」**`, `「我建议你做 X」不等于「X 已放行」——建议是输入，批准是输出，清单上必须分列。`, ``},
	{`CONTRIBUTING.md`, `### 发现「批准的 SHA ≠ 要合并的 SHA」时，**停下来问**`, `我把它打印出来了，然后我合并了。`, ``},
	{`CONTRIBUTING.md`, `## 五、并行分支：**同一文件上都有改动，就串成线**`, `分别评 A、分别评 B，不等于评了 A+B。`, ``},
	{`CONTRIBUTING.md`, `## 六、打 tag 之前`, `跟实际状态走，不跟提交走`, ``},
	{`CONTRIBUTING.md`, `### 会在打 tag 那一刻失败的守卫，**打之前先空跑一次**`, `打 tag 这个动作本身可能让检查变红。`, ``},
	{`CONTRIBUTING.md`, `### 不要提前给一个红色发通行证`, `不对。那天不该红；红了就是有一步没做`, ``},
	{`CONTRIBUTING.md`, `## 七、合并 / 删除**之后**，回头用一条独立命令核对`, "别用 `&&` 串两步", ``},
	{`CONTRIBUTING.md`, `## 八、没问题的时候要**明说没问题**`, `规则就失去信息量`, ``},
	{`CONTRIBUTING.md`, `## 自检（送审前跑，输出贴进第 3 样材料）`, ``, `命令清单，不是规矩`},
	{`tools/audit/README.md`, `## 现有脚本`, ``, `目录清单，随脚本增减`},
	{`docs/method-landing.md`, `## 一、落点表`, ``, `表本身，由 TestDocLinksResolve 顶着`},
	{`docs/method-landing.md`, `## 二、落不进去的（第③类）——**这是合法结果，不是遗漏**`, ``, `第三类清单，内容随审计变`},
	{`docs/method-landing.md`, `## 三、这次审计**审到哪一步为止、按什么来源审的**`, ``, `一次性的来源声明`},
	{`docs/method-landing.md`, `## 四、这张表自己会怎么坏`, ``, `自曝清单，内容随守卫增减`},
	{`docs/method-landing.md`, `### 覆盖面的机械定义，**以及它量错过一次**`, `第一版只认 ①，漏掉 11 条 ②`, ``},
}

var mdHeading = regexp.MustCompile(`(?m)^#{2,3} .+$`)

// sections 把一份 markdown 切成「标题 -> 该节正文」。
func sections(src string) ([]string, map[string]string) {
	locs := mdHeading.FindAllStringIndex(src, -1)
	order := make([]string, 0, len(locs))
	body := make(map[string]string, len(locs))
	for i, loc := range locs {
		title := src[loc[0]:loc[1]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		order = append(order, title)
		body[title] = src[loc[1]:end]
	}
	return order, body
}

// TestEveryRuleSectionHasAnAnchor 断言落点载体里的每一节规矩都被锚住。
//
// 它守的不是「文件还在」，是「文件里的每一条规矩还在」——
// 上面那条 TestLandingCarriersStillCarry 只点名每份文件里最贵的那一句，
// 覆盖不到其余各节。两条【分工不同】：
//
//	TestLandingCarriersStillCarry  覆盖 docs/design.md（大文件，多数小节不是规矩）
//	TestEveryRuleSectionHasAnAnchor 覆盖五份规矩文件的【每一节】
//
// 交集之外各有一半，所以两条都留着。
func TestEveryRuleSectionHasAnAnchor(t *testing.T) {
	// 先按文件分组，省得每个条目读一次盘。
	type entry struct{ heading, needle, exempt string }
	byFile := map[string][]entry{}
	for _, a := range ruleAnchors {
		if (a.needle == "") == (a.exempt == "") {
			t.Errorf("%s / %s：needle 与 exempt 必须【恰好】设一个"+
				"（都空 = 登记了却什么都没守；都填 = 说不清它到底算不算规矩）",
				a.file, a.heading)
			continue
		}
		byFile[a.file] = append(byFile[a.file], entry{a.heading, a.needle, a.exempt})
	}

	for file, entries := range byFile {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s 读不到：%v", file, err)
			continue
		}
		order, body := sections(string(b))

		registered := make(map[string]entry, len(entries))
		for _, e := range entries {
			registered[e.heading] = e
		}

		// ① 文件里有、表里没有 —— 新加了一节规矩却没锚点
		for _, title := range order {
			e, ok := registered[title]
			if !ok {
				t.Errorf("%s 新增了一节而没登记锚点：\n  %s\n"+
					"  在 ruleAnchors 里加一行：要么给它一个【正文里的】锚点句，"+
					"要么写明它为什么不承载规矩。\n"+
					"  不登记的后果是：这一节以后被人删掉，没有任何东西会响",
					file, title)
				continue
			}
			// ③ 那一节还在，但锚点句被删掉了
			if e.needle != "" && !strings.Contains(body[title], e.needle) {
				t.Errorf("%s 这一节还在，但它的锚点句没了：\n  节：%s\n  锚：%q\n"+
					"  如果是有意改写，把 ruleAnchors 里那一行一起改；"+
					"如果不是，这条规矩刚被静默删掉了", file, title, e.needle)
			}
		}

		// ② 表里有、文件里没有 —— 幽灵项
		have := make(map[string]bool, len(order))
		for _, title := range order {
			have[title] = true
		}
		for _, e := range entries {
			if !have[e.heading] {
				t.Errorf("%s 的 ruleAnchors 里有一条【幽灵项】，文件里已经没有这一节了：\n  %s\n"+
					"  标题被改写过？还是整节被删了？删了就把这一行也删掉——"+
					"留着它会让下一个人以为那条规矩还在", file, e.heading)
			}
		}
	}

	// 对照组：这张表本身要够大，否则「全通过」可能只是因为它是空的。
	if len(ruleAnchors) < 20 {
		t.Fatalf("ruleAnchors 只有 %d 条，不像覆盖了五份载体——"+
			"先确认这张表没被清空，再谈它有没有全过", len(ruleAnchors))
	}
	t.Logf("锚住 %d 节，其中 %d 节写明豁免", len(ruleAnchors), countExempt())
}

func countExempt() int {
	n := 0
	for _, a := range ruleAnchors {
		if a.exempt != "" {
			n++
		}
	}
	return n
}

// —— 规矩登记表（逐条，不是逐节） ——
//
// 评审方的 X2 打穿的是上面那一层：按【小节】锚，一节只锚一句，
// 于是同一节里另外几条可以被静默删掉。我实测过，按小节锚【仍然全绿】。
//
// 这一层的覆盖面有个**机械定义**，不是「我记得锚住了哪些」：
//
//	载体文件里，① `> ` 引用块中的加粗行  ② 独立成行的加粗句
//	——这两种格式就是本仓写「一句可以单独拿走的规矩」时实际在用的。
//
// ⚠️ 这个定义是【量出来的，而且量错过一次】：第一版只认 ①，漏掉 11 条 ②，
// 其中就有 CONTRIBUTING.md 那条「把「哪一刻的快照」写进快照本身」。
// 是一次变异（改写一条已登记规矩）打不中目标才发现的 —— 那条根本没被登记。
// **「有个机械定义」这句话本身也可能是「看起来像守住了」。**
//
// 仍然盖不住的：写成普通行文、加粗只在句中的那些。它们多半是解释而不是规矩，
// 但这是**判断**，不是测量 —— 见 docs/method-landing.md 第四节。
//
// 针取每行前若干字符（可读前缀，不是哈希）：断言失败时要能看出它在守什么。
// 表是生成的（tools/audit/gen_quoted_rules.py），不手敲。
var quotedRules = []struct {
	file   string
	needle string
}{
	{`tools/probe/README.md`, `焊了对照组 ≠ 对照组在承重。得拆一次，看它塌。`},
	{`tools/probe/README.md`, `1 个已知值挡不住「恰好返回那个值」；2 个不同的已`},
	{`tools/probe/README.md`, `⚠️ 这条最容易只做一半。 同一次提交里，夜盘那半照`},
	{`tools/probe/README.md`, `因为夜盘那半是刚被提醒的。一条刚学会的规则，默认只作`},
	{`tools/probe/README.md`, `转换时机就是拿到定论那一刻。`},
	{`tools/probe/README.md`, `「我想不出怎么触发它」是一个缺口；「我找过了，当前触`},
	{`tools/probe/README.md`, `探针断言的是「外部世界是什么样」。用本库去解析外部世`},
	{`tools/probe/README.md`, `再拿结果验本库，两边会因为同一个原因同时错——那不是`},
	{`docs/README.md`, `会被撞到的，正是照 contract.md 写代码的`},
	{`docs/README.md`, `更正落在维护者那份、没落在使用者那份——`},
	{`docs/README.md`, `信息在库里 ≠ 信息在他会看的地方。`},
	{`docs/README.md`, `说「可以忽略」之前，把那个量换算到【你正在比的那个尺`},
	{`docs/README.md`, `「一个不解释的数字读起来像没查过；一个被拆开的数字读`},
	{`docs/README.md`, `于是我做的不是查，是让它看起来像查过了。」`},
	{`docs/README.md`, `没有一个字是假的，而整体是一个未被支持的断言。`},
	{`docs/README.md`, `有些格子不是审计漏了，是它已经无法被审计了——`},
	{`docs/README.md`, `而记录当时【怎么量的】，本来可以让它可审。`},
	{`docs/README.md`, `不是发明了一个错误，是把一个错误的强度提高了一档。`},
	{`docs/README.md`, `传播一条解释之前，先问：这条是我验的，还是我读来的？`},
	{`docs/README.md`, `改写的那一下，出处就没了。`},
	{`docs/README.md`, `重新量能验证「是什么」，验证不了「在哪」——如果「在`},
	{`docs/README.md`, `「我的数据分不开这两种解释」是一句关于【我手上这批数`},
	{`docs/README.md`, `不是关于【这个问题】的话。说它之前必须先问：再取一次`},
	{`docs/README.md`, `不问就说，那不是克制，是把一次没做的检查写成了一条认`},
	{`docs/README.md`, `反驳分两种，举证责任完全不同。动手前先分清你在做哪一`},
	{`docs/README.md`, `动手前问一句：我是在提一个竞争性的说法，还是在取消这`},
	{`docs/README.md`, `一条够用的证据，被写成了一条不够用的论证。`},
	{`docs/README.md`, `认错、caveat、自我批评都会让读者停止追问，因为`},
	{`docs/README.md`, `一句 caveat 若同时解释掉好几个不一致，先别信`},
	{`docs/README.md`, `同理：一句听起来像结论的话，即便内容没错，也会让人停`},
	{`CONTRIBUTING.md`, `一条被丢弃了输出的命令，不能拿来当「已完成」的依据。`},
	{`CONTRIBUTING.md`, `要么留输出，要么事后用一条独立命令查状态。`},
	{`CONTRIBUTING.md`, `凡出现「分不开 / 想不出 / 做不到 / 无法确定`},
	{`CONTRIBUTING.md`, `给不出成本的，那不是一条限制，是一个没做的检查。`},
	{`CONTRIBUTING.md`, `不是「把『我没做 X』说成『X 不成立』」，是说成【`},
	{`CONTRIBUTING.md`, `肯定式的更危险，因为它读起来像信息，不像判断。`},
	{`CONTRIBUTING.md`, `把「哪一刻的快照」写进快照本身。`},
	{`CONTRIBUTING.md`, `「我建议你做 X」不等于「X 已放行」——建议是输入`},
	{`CONTRIBUTING.md`, `送审方打算做而清单上没有的，动手【之前】问，不是做完`},
	{`CONTRIBUTING.md`, `「事后主动说」和「事前确认」不是一回事——前者让错误`},
	{`CONTRIBUTING.md`, `我在合并那一刻就知道 SHA 不同——我把它打印出来`},
	{`CONTRIBUTING.md`, `把疑虑打印出来，不等于解决了疑虑。`},
	{`CONTRIBUTING.md`, `我为它写了一行解释，说明我心里知道它需要解释——需要`},
	{`CONTRIBUTING.md`, `空跑不是为了看它退不退非零，是为了【读它说了什么】。`},
	{`CONTRIBUTING.md`, `一条只在未来才执行的分支，它既没被执行过，也没被读过`},
	{`CONTRIBUTING.md`, `空跑解决前者，读输出解决后者——而只做前者会让你以为`},
	{`CONTRIBUTING.md`, `remotes/origin/ 不是远端状态，是「上`},
	{`tools/audit/README.md`, `有些格子不是审计漏了，是它已经无法被审计了——`},
	{`docs/method-landing.md`, `有五六个时刻，人会真的伸手去做一件容易做错的事。`},
	{`docs/method-landing.md`, `那一刻他手边打开的是哪个文件？规矩就写在那个文件里。`},
	{`docs/method-landing.md`, `能写进工具输出的，就别写进文档。`},
	{`docs/method-landing.md`, `⚠️ 原本这里有第六条「编造一个拆解没有机械守卫」，`},
	{`docs/method-landing.md`, `tools/audit/ 那条规矩就是那道闸门的一半`},
	{`docs/method-landing.md`, `若不是探针输出，产生它的脚本要落盘。一个数字若有脚本`},
	{`docs/method-landing.md`, `拆解就是脚本打印的，编不出来。剩下的那一半（散文里仍`},
	{`docs/method-landing.md`, `⇒ 「没有机械守卫」和「机械守卫只盖住一半」是两回事`},
	{`docs/method-landing.md`, `一次审计的完整性受限于它用的来源。「审计过了」这句话`},
	{`docs/method-landing.md`, `我用的来源，三样，按可靠性从高到低：`},
	{`docs/method-landing.md`, `第 3 条要单说，因为它改变了这次审计的性质：`},
	{`docs/method-landing.md`, `不会因为同一个原因同时漏。`},
	{`docs/method-landing.md`, `四道刹车分工不同，已逐个拆过：`},
	{`docs/method-landing.md`, `⚠️ 这里有两处「第一版是绿的」，都值得记，因为它们`},
	{`docs/method-landing.md`, `X3 第一次绿：判据写的是 checked < 10`},
	{`docs/method-landing.md`, `换成点名两个不同深度的已知文件才真挡住——递归一断，`},
	{`docs/method-landing.md`, `X2 第一次绿：我按【小节】锚，一节只锚一句，于是同`},
	{`docs/method-landing.md`, `是评审方拆出来的，而正确的形状两个提交之前就在仓库里`},
	{`docs/method-landing.md`, `我发明了那个模式，却没把它用在自己刚建的这套东西上。`},
	{`docs/method-landing.md`, `「它有一个机械定义」这句话本身，也可能是一个「看起来`},
	{`docs/method-landing.md`, `定义要和文件里实际用的格式对齐，而那要数一遍，不是想`},
	{`docs/method-landing.md`, `模式识别型的守卫，覆盖面 = 那个模式；而模式是人写`},
	{`docs/method-landing.md`, `这不是这一版没做好，是这类守卫的固有形状。证据就在本`},
	{`docs/method-landing.md`, `我为覆盖面写的第一个模式漏掉了 11 条，而发现它靠`},
	{`docs/method-landing.md`, `不是靠读那个模式。模式自己不会告诉你它漏了什么。`},
	{`docs/method-landing.md`, `⇒ 所以这类守卫的正确读法是：它保证的是「符合这个模`},
	{`docs/method-landing.md`, `不是「规矩没被删」。 两句话的差就是模式的边界，而那`},
	{`docs/method-landing.md`, `上表那三个 ❌ 是这张表最诚实的部分。`},
}

// ruleLines 把一份 markdown 里所有「独立成句的规矩行」抠出来。
// 判据必须和 tools/audit/gen_quoted_rules.py 一致，否则登记表和检查各说各的。
func ruleLines(src string) []string {
	stripMark := strings.NewReplacer("*", "", "`", "", ">", "")
	var out []string
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		quoted := strings.HasPrefix(line, "> ") && strings.Contains(line, "**")
		standalone := strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") &&
			strings.Count(line, "**") == 2 && !strings.Contains(line, "|") &&
			len(line) > 16
		if !quoted && !standalone {
			continue
		}
		plain := strings.TrimSpace(stripMark.Replace(line))
		if len([]rune(plain)) < 12 {
			continue
		}
		out = append(out, plain)
	}
	return out
}

// TestEveryQuotedRuleIsRegistered 断言载体里的规矩【一条不多、一条不少】。
//
//	删掉一条 → 登记表里那条的针找不到
//	改写一条 → 同上（有意的：改规矩就该顺手改登记表）
//	新增一条 → 文件里多出一行没登记的
//
// 第三条是关键：**新加规矩必须登记**，否则这张表会慢慢落后于文件，
// 而它看起来仍然全绿 —— 同 tools/probe/shinny 的 TestGuardListCoversEveryProbeFile。
func TestEveryQuotedRuleIsRegistered(t *testing.T) {
	byFile := map[string][]string{}
	for _, q := range quotedRules {
		byFile[q.file] = append(byFile[q.file], q.needle)
	}

	for file, needles := range byFile {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s 读不到：%v", file, err)
			continue
		}
		actual := ruleLines(string(b))

		hasPrefix := func(list []string, n string) bool {
			for _, a := range list {
				if strings.HasPrefix(a, n) {
					return true
				}
			}
			return false
		}

		for _, n := range needles {
			if !hasPrefix(actual, n) {
				t.Errorf("%s 少了一条登记过的规矩：\n  %q\n"+
					"  被删了还是被改写了？改写就把 quotedRules 里那一行一起改，"+
					"删了就把那一行也删掉。\n"+
					"  这一格正是评审方 X2 打穿的那一格：按小节锚的守卫在这里【不会响】",
					file, n)
			}
		}
		for _, a := range actual {
			if !hasPrefix2(needles, a) {
				r := []rune(a)
				if len(r) > 40 {
					r = r[:40]
				}
				t.Errorf("%s 多了一条没登记的规矩：\n  %q…\n"+
					"  跑 tools/audit/gen_quoted_rules.py 重新生成 quotedRules。\n"+
					"  不登记的后果不是现在出错，是【以后它被删掉时没有东西会响】",
					file, string(r))
			}
		}
	}

	// 对照组：表空的时候上面每个循环都不执行，而结果同样是「全过」。
	if len(quotedRules) < 40 {
		t.Fatalf("quotedRules 只有 %d 条，不像覆盖了五份载体 —— "+
			"先确认这张表没被清空", len(quotedRules))
	}
	t.Logf("登记了 %d 条规矩", len(quotedRules))
}

// hasPrefix2 问的是反方向：actual 这一行，是不是某个已登记的针的延长。
func hasPrefix2(needles []string, actual string) bool {
	for _, n := range needles {
		if strings.HasPrefix(actual, n) {
			return true
		}
	}
	return false
}
