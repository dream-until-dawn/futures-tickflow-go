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

// carrierFiles 是那五份「规矩写在里面」的文件。
// 与 ruleAnchors / quotedRules / carrierCensus 覆盖的是同一批。
var carrierFiles = []string{
	"tools/probe/README.md",
	"docs/README.md",
	"CONTRIBUTING.md",
	"tools/audit/README.md",
	"docs/method-landing.md",
}

// TestCarriersHaveNoHTMLComments 禁掉载体里的 HTML 注释。
//
// 起因（评审方 2026-09-08 的攻击 A）：把一条【已登记】的规矩用 `<!-- -->` 包起来——
//
//	那一行的原文一个字节都没改（登记表比得上）
//	新增的两行不含 `**`（普查数也对得上）
//	而在渲染后的文档里，这条规矩【不见了】
//
// ⇒ 这不是「模式又窄了」。加宽模式救不了它——**那一行根本没变**。
//
//	两张表守的都是「文件的字节形态」，而规矩活在【渲染后的文档】里。
//	这一次不是模式选窄了，是【度量选错了对象】。
//
// 禁注释关掉的只是**一个已知入口**，不是那一类：
// 把整节移进代码块、改渲染标记、在前面加一行 `> `……只要不动那一行的字节，
// 两张表就都不响。那一类是【没有机械守卫】的，已写进 docs/method-landing.md 第二节。
//
// 之所以敢直接禁：写这条时五份载体里 `<!--` 共 0 处，且规矩文档里没有注释的正当用途。
func TestCarriersHaveNoHTMLComments(t *testing.T) {
	for _, f := range carrierFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s 读不到：%v", f, err)
			continue
		}
		for n, line := range strings.Split(string(b), "\n") {
			if commentOutsideCode(line) {
				t.Errorf("%s:%d 出现 HTML 注释：\n  %s\n"+
					"  载体里不许用注释——它能让一条规矩在【渲染后】消失，"+
					"而那一行的字节一个都没变，两张登记表都不会响。\n"+
					"  要删一条规矩就真的删掉它（那样守卫会响）；要暂时停用，"+
					"写明「已废弃」并留在原处。",
					f, n+1, strings.TrimSpace(line))
			}
		}
	}
	if len(carrierFiles) < 5 {
		t.Fatalf("carrierFiles 只有 %d 份，载体不止这些", len(carrierFiles))
	}
}

// commentOutsideCode 判断这一行是不是【真的】有 HTML 注释。
//
// 行内代码里的注释记号不是注释，是在【讲】注释——本仓的文档就得这么讲它。
// 第一版没分这个，于是这条守卫在「描述该攻击的那一行」上变红了：
// 一条会在正常编辑上误报的守卫，三天之内就会被人关掉，
// 所以「假警报比没有警报更糟」在这里是直接适用的。
//
// 判据：反引号成对 ⇒ 出现位置之前的反引号数为奇数，说明它在代码段里面。
func commentOutsideCode(line string) bool {
	for _, tok := range []string{"<!" + "--", "--" + ">"} {
		off := 0
		for {
			i := strings.Index(line[off:], tok)
			if i < 0 {
				break
			}
			pos := off + i
			if strings.Count(line[:pos], "`")%2 == 0 { // 不在代码段里 ⇒ 是真注释
				return true
			}
			off = pos + len(tok)
		}
	}
	return false
}

// —— 规则锚点表 ——
//
// 评审方拆出来的洞：landingCarriers 每份文件只点名【一句】，
// 于是同一份文件里另外七八条规矩可以被静默删掉，两道守卫都不响。
//
// 而正确的形状两个提交之前就在仓库里了：tools/probe/shinny 的
// TestGuardListCoversEveryProbeFile —— 新探针文件必须显式登记或豁免。
// 我发明了那个模式，却没有把它用在自己刚建的这套东西上。
//
// 所以往下套一层：每一节规矩都要有一个锚点，或者一条写明理由的豁免。
//
// 表是生成的（tools/audit/rebuild_docs_test.py），不手敲。
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
	{`docs/README.md`, `## 先说一句：**什么算一条「规矩」**`, `一个实现出来的定义，而没有人声明过它`, ``},
	{`docs/README.md`, `## 改文档之前，这八条`, ``, `小节标题，规矩在它下面各条里`},
	{`docs/README.md`, `### 1. **一处改正必须覆盖同一断言的全部载体**`, "`grep` 全仓", ``},
	{`docs/README.md`, `### 2. **一条只存在于对话里的规则，等于没有这条规则**`, `因为它感觉上最像「已经有了」`, ``},
	{`docs/README.md`, `### 3. 记一个数之前，先问它是**这个东西的属性**还是**这一次观测的属性**`, `连观测条件一起记`, ``},
	{`docs/README.md`, `### 3b. **「可忽略」要落到目标尺度上算一遍**——跨尺度比较时相对量会骗人`, `在目标尺度上是错的`, ``},
	{`docs/README.md`, "### 4. `X = A + B` 形式的数字：**那个拆解是打印出来的，还是推出来的？**", `一个总数看起来不够，拆开就够了。`, ``},
	{`docs/README.md`, `### 5. **读回别手敲**`, `刚写进去的那个变量本身`, ``},
	{`docs/README.md`, "### 6. 不是探针输出的数字，**产生它的脚本要落进 `tools/audit/`**", `内联跑的、没落盘`, ``},
	{`docs/README.md`, `### 7. **结论的强度是谁给的**`, `读者默认是写文档的人验的`, ``},
	{`docs/README.md`, `### 7c. 记下一处差异是对的，**而「原因是……」是另一条要单独验的断言**`, `原因是选的行不同`, ``},
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

// TestEveryRuleSectionHasAnAnchor 断言载体里的每一节规矩都被锚住。
func TestEveryRuleSectionHasAnAnchor(t *testing.T) {
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
		for _, title := range order {
			e, ok := registered[title]
			if !ok {
				t.Errorf("%s 新增了一节而没登记锚点：\n  %s\n"+
					"  在 ruleAnchors 里加一行（跑 tools/audit/rebuild_docs_test.py）。\n"+
					"  不登记的后果是：这一节以后被人删掉，没有任何东西会响", file, title)
				continue
			}
			if e.needle != "" && !strings.Contains(body[title], e.needle) {
				t.Errorf("%s 这一节还在，但它的锚点句没了：\n  节：%s\n  锚：%q\n"+
					"  有意改写就重跑生成器；不是的话，这条规矩刚被静默删掉了",
					file, title, e.needle)
			}
		}
		have := make(map[string]bool, len(order))
		for _, title := range order {
			have[title] = true
		}
		for _, e := range entries {
			if !have[e.heading] {
				t.Errorf("%s 的 ruleAnchors 里有一条【幽灵项】，文件里已经没有这一节了：\n  %s\n"+
					"  留着它会让下一个人以为那条规矩还在", file, e.heading)
			}
		}
	}

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
// 评审方的 X2 打穿的是上一层：按【小节】锚，一节只锚一句，
// 于是同一节里另外几条可以被静默删掉。实测过：按小节锚仍然全绿。
//
// 覆盖面的模式化定义：
//
//	载体里，① `> ` 引用块中的加粗行  ② 行首加粗且有闭合的行（表格行按行首是 | 排除）
//
// ⚠️ 这个模式改过两次，两次都是【外部】发现的，不是我读出来的：
//
//	v1 只认 ①          → 漏 11 条；一次打不中目标的变异暴露的
//	v2 加「整行加粗」   → 仍漏 2 条，评审方实测找到（一条被「排除表格行」误伤，
//	                      判据写成了「含竖线」而竖线在行内代码里；
//	                      一条是「加粗开头 + 后续文字」）
//
// 「什么算一条规矩」现已在 docs/README.md 开头声明，不再由这个模式默认决定。
var quotedRules = []struct {
	file   string
	needle string
}{
	{`tools/probe/README.md`, `探针不是测试。 测试断言「本库的行为」，探针断言「外部世界现在是什么样」。`},
	{`tools/probe/README.md`, `焊了对照组 ≠ 对照组在承重。得拆一次，看它塌。`},
	{`tools/probe/README.md`, `1 个已知值挡不住「恰好返回那个值」；2 个不同的已知值挡得住任何常量。`},
	{`tools/probe/README.md`, `⚠️ 这条最容易只做一半。 同一次提交里，夜盘那半照做了、日盘那半没做——`},
	{`tools/probe/README.md`, `因为夜盘那半是刚被提醒的。一条刚学会的规则，默认只作用在学它的那个位置上。`},
	{`tools/probe/README.md`, `只有把内容摆出来给人看，才有机会发现内容是错的`},
	{`tools/probe/README.md`, `这就是 || true 的形状，只不过不用谁去加那三个字符，它自己就会变绿。`},
	{`tools/probe/README.md`, `退出码不再区分。所以答案一旦拿到，必须把它转成「守基线」`},
	{`tools/probe/README.md`, `转换时机就是拿到定论那一刻。`},
	{`tools/probe/README.md`, `「我想不出怎么触发它」是一个缺口；「我找过了，当前触发不了」是一个结论。`},
	{`tools/probe/README.md`, `探针断言的是「外部世界是什么样」。用本库去解析外部世界，`},
	{`tools/probe/README.md`, `再拿结果验本库，两边会因为同一个原因同时错——那不是两条路，是一条。`},
	{`docs/README.md`, `一个实现出来的定义，而没有人声明过它（评审方 2026-09-08 指出）。现在声明：`},
	{`docs/README.md`, `一条你希望被守住的规矩，必须写成【独立成行的加粗句】或【 引用块】。`},
	{`docs/README.md`, `写在句子中间的加粗是【强调】，不是规矩，不受守卫。`},
	{`docs/README.md`, `14 条裸着。逐条读过之后，把其中 7 条真规矩提成了独立行，其余是行文强调。`},
	{`docs/README.md`, `⚠️ 代价要写明：这条约定意味着「一条规矩没被守住」可以是`},
	{`docs/README.md`, `作者没按约定写，而不只是「守卫漏了」。`},
	{`docs/README.md`, `检查清单上因此多一句：我刚写的这条，是独立一行吗？`},
	{`docs/README.md`, `会被撞到的，正是照 contract.md 写代码的那个人。`},
	{`docs/README.md`, `更正落在维护者那份、没落在使用者那份——`},
	{`docs/README.md`, `信息在库里 ≠ 信息在他会看的地方。`},
	{`docs/README.md`, `同一条事实要放几份，取决于有几种人会从几个不同入口来找它。`},
	{`docs/README.md`, `必须同时写下绝对偏移与当时的 total。`},
	{`docs/README.md`, `说「可以忽略」之前，把那个量换算到【你正在比的那个尺度】上再看一眼。`},
	{`docs/README.md`, `「一个不解释的数字读起来像没查过；一个被拆开的数字读起来像查过了。`},
	{`docs/README.md`, `于是我做的不是查，是让它看起来像查过了。」`},
	{`docs/README.md`, `没有一个字是假的，而整体是一个未被支持的断言。`},
	{`docs/README.md`, `把手敲那一步整个去掉，而不是要求下次小心。`},
	{`docs/README.md`, `有些格子不是审计漏了，是它已经无法被审计了——`},
	{`docs/README.md`, `而记录当时【怎么量的】，本来可以让它可审。`},
	{`docs/README.md`, `同一封信里他还要求我给另一组数标出处。 后来实测：两种口径给出同一个数，`},
	{`docs/README.md`, `不是发明了一个错误，是把一个错误的强度提高了一档。`},
	{`docs/README.md`, `传播一条解释之前，先问：这条是我验的，还是我读来的？`},
	{`docs/README.md`, `改写的那一下，出处就没了。`},
	{`docs/README.md`, `「我记下了差异」和「我解释了差异」是两件事，而后者是一条新的断言，`},
	{`docs/README.md`, `要单独验。 一句没验过的「原因是」，会让那处差异看起来已经结案了——`},
	{`docs/README.md`, `传播一条解释之前，先问：这条是我验的，还是我读来的？`},
	{`docs/README.md`, `改写的那一下，出处就没了。`},
	{`docs/README.md`, `那是一次采样。而同一份文档里就有完整枚举：那两份截断快照被整份解析过，`},
	{`docs/README.md`, `七个交易所，零条 GFEX。`},
	{`docs/README.md`, `重新量能验证「是什么」，验证不了「在哪」——如果「在哪」本身不是个稳定的量。`},
	{`docs/README.md`, `先问那句「我分不开这两种解释」值多少钱`},
	{`docs/README.md`, `「我的数据分不开这两种解释」是一句关于【我手上这批数据】的话，`},
	{`docs/README.md`, `不是关于【这个问题】的话。说它之前必须先问：再取一次要多少钱？`},
	{`docs/README.md`, `不问就说，那不是克制，是把一次没做的检查写成了一条认识论限制。`},
	{`docs/README.md`, `反驳分两种，举证责任完全不同。动手前先分清你在做哪一种。`},
	{`docs/README.md`, `动手前问一句：我是在提一个竞争性的说法，还是在取消这个问题？`},
	{`docs/README.md`, `手上有第二种的证据，却写成了第一种的论证`},
	{`docs/README.md`, `一条够用的证据，被写成了一条不够用的论证。`},
	{`docs/README.md`, `认错、caveat、自我批评都会让读者停止追问，因为它们看起来已经把问题处理过了。`},
	{`docs/README.md`, `一句 caveat 若同时解释掉好几个不一致，先别信它。`},
	{`docs/README.md`, `同理：一句听起来像结论的话，即便内容没错，也会让人停止追问它的依据。`},
	{`CONTRIBUTING.md`, `他的独立性正是他的价值，本仓不替他规定。`},
	{`CONTRIBUTING.md`, `一条被丢弃了输出的命令，不能拿来当「已完成」的依据。`},
	{`CONTRIBUTING.md`, `要么留输出，要么事后用一条独立命令查状态。`},
	{`CONTRIBUTING.md`, `截断那一步报 substring not found，而后面几步照跑——往一个残缺的文件上又接了两张表，`},
	{`CONTRIBUTING.md`, `「后面几步跑完了」不等于「整条跑成了」。`},
	{`CONTRIBUTING.md`, `一条流水线要么 set -e，要么写成一个脚本，每步断言、失败即整份还原。`},
	{`CONTRIBUTING.md`, `凡出现「分不开 / 想不出 / 做不到 / 无法确定」，必须紧跟【再做一次要多少钱】。`},
	{`CONTRIBUTING.md`, `给不出成本的，那不是一条限制，是一个没做的检查。`},
	{`CONTRIBUTING.md`, `不是「把『我没做 X』说成『X 不成立』」，是说成【任何一个关于 X 的断言】。`},
	{`CONTRIBUTING.md`, `肯定式的更危险，因为它读起来像信息，不像判断。`},
	{`CONTRIBUTING.md`, `写下这一句是必要的：不写，它就会变成又一个「看起来那一格有人守」的东西。`},
	{`CONTRIBUTING.md`, `把「哪一刻的快照」写进快照本身。`},
	{`CONTRIBUTING.md`, `评审对象必须是个不动的靶子。`},
	{`CONTRIBUTING.md`, `批的 SHA 不等于当时的 tip——那时他批的是一个已经不存在的状态。`},
	{`CONTRIBUTING.md`, `规则的字面满足了，目的没满足。`},
	{`CONTRIBUTING.md`, `他已经评完了，并且判了不通过——我们只是消息交叉。`},
	{`CONTRIBUTING.md`, `「评审方还没开始看」不是送审方能确立的事实，因此它不能用来支持任何自我豁免。`},
	{`CONTRIBUTING.md`, `冻结之后要动，唯一安全的做法是问一句——那个前提只有对面知道。`},
	{`CONTRIBUTING.md`, `不是「你的解释不算数」，是【你用来解释的那个事实，你不掌握】。`},
	{`CONTRIBUTING.md`, `「我建议你做 X」不等于「X 已放行」——建议是输入，批准是输出，清单上必须分列。`},
	{`CONTRIBUTING.md`, `送审方打算做而清单上没有的，动手【之前】问，不是做完再报。`},
	{`CONTRIBUTING.md`, `「事后主动说」和「事前确认」不是一回事——前者让错误可纠正，后者让它不发生。`},
	{`CONTRIBUTING.md`, `我在合并那一刻就知道 SHA 不同——我把它打印出来了，然后我合并了。`},
	{`CONTRIBUTING.md`, `把疑虑打印出来，不等于解决了疑虑。`},
	{`CONTRIBUTING.md`, `我为它写了一行解释，说明我心里知道它需要解释——需要解释的合并，就是需要问一句的合并。`},
	{`CONTRIBUTING.md`, `你正在为一个动作写解释，这件事本身就是那个动作需要被批准的证据。`},
	{`CONTRIBUTING.md`, `分别评 A、分别评 B，不等于评了 A+B。 合并后的那份文件是第三个产物，`},
	{`CONTRIBUTING.md`, `空跑不是为了看它退不退非零，是为了【读它说了什么】。`},
	{`CONTRIBUTING.md`, `一条只在未来才执行的分支，它既没被执行过，也没被读过；`},
	{`CONTRIBUTING.md`, `空跑解决前者，读输出解决后者——而只做前者会让你以为两者都解决了。`},
	{`CONTRIBUTING.md`, `不对。那天不该红；红了就是有一步没做（实现完了要删 pending.txt 那几行）。`},
	{`CONTRIBUTING.md`, `提前解释一个红色，和给检查加 || true，最终下场一样：没人再读它。`},
	{`CONTRIBUTING.md`, `remotes/origin/ 不是远端状态，是「上次 fetch 时的远端状态」。`},
	{`tools/audit/README.md`, `这个目录存在的理由（2026-09-08 学到的，代价是同一段文档改了三次）：`},
	{`tools/audit/README.md`, `有些格子不是审计漏了，是它已经无法被审计了——`},
	{`tools/audit/README.md`, `用生成器自己的模式测生成器，得到的一定是满分——这个脚本存在就是为了绕开那一点。`},
	{`tools/audit/README.md`, `登记表不手敲：手敲的登记表会在你以为它覆盖住的地方漏掉东西`},
	{`docs/method-landing.md`, `有五六个时刻，人会真的伸手去做一件容易做错的事。`},
	{`docs/method-landing.md`, `那一刻他手边打开的是哪个文件？规矩就写在那个文件里。`},
	{`docs/method-landing.md`, `第 5 行是这张表的样板：doccheck 不是把规矩写在文档里，而是写进它自己的`},
	{`docs/method-landing.md`, `能写进工具输出的，就别写进文档。`},
	{`docs/method-landing.md`, `本表里【只能靠人】的失效现在有三条（另两条：编造一个拆解、消息里的松）。`},
	{`docs/method-landing.md`, `评审方那句该原样留着：知道边界在哪，比把网织得更密有用。`},
	{`docs/method-landing.md`, `「查过了，不适用」和「没查」长得不一样。`},
	{`docs/method-landing.md`, `第一条的两个实例，双方各一（2026-09-08）：`},
	{`docs/method-landing.md`, `我在消息里写「这一层没有『哪句被锚住』，是全部」，而文档里一直写的是有射程的版本；`},
	{`docs/method-landing.md`, `评审方在消息里写「（原表 85，口径差）」——那句从没进过任何文档，`},
	{`docs/method-landing.md`, `所以没有任何机制会碰它，而它正是他转手一个未验解释的那一次。`},
	{`docs/method-landing.md`, `⇒ 两次都是：文档里守着射程，消息里松了一档，而松的那一档没有任何东西会发现。`},
	{`docs/method-landing.md`, `⚠️ 原本这里有第六条「编造一个拆解没有机械守卫」，评审把它挪走了，他对：`},
	{`docs/method-landing.md`, `tools/audit/ 那条规矩就是那道闸门的一半——凡是写进 probe.md 的数字，`},
	{`docs/method-landing.md`, `若不是探针输出，产生它的脚本要落盘。一个数字若有脚本产出，`},
	{`docs/method-landing.md`, `拆解就是脚本打印的，编不出来。剩下的那一半（散文里仍然可以编）才是「只能靠人」。`},
	{`docs/method-landing.md`, `⇒ 「没有机械守卫」和「机械守卫只盖住一半」是两回事，而前者读起来像已经想过了。`},
	{`docs/method-landing.md`, `一次审计的完整性受限于它用的来源。「审计过了」这句话必须说清按什么来源审。`},
	{`docs/method-landing.md`, `我用的来源，三样，按可靠性从高到低：`},
	{`docs/method-landing.md`, `第 3 条要单说，因为它改变了这次审计的性质：`},
	{`docs/method-landing.md`, `理由是两边各扫一遍才是两条独立的路。而我读了他那份记录之后，`},
	{`docs/method-landing.md`, `这两条路已经共用了一个来源 ⇒ 按本仓自己的判据（两条路独立与否，`},
	{`docs/method-landing.md`, `不会因为同一个原因同时漏。`},
	{`docs/method-landing.md`, `四道刹车分工不同，已逐个拆过：`},
	{`docs/method-landing.md`, `⚠️ 这里有两处「第一版是绿的」，都值得记，因为它们形状相同：`},
	{`docs/method-landing.md`, `X3 第一次绿：判据写的是 checked < 10，而光根目录两个 .md 就凑够十条。`},
	{`docs/method-landing.md`, `换成点名两个不同深度的已知文件才真挡住——递归一断，两个一起消失。`},
	{`docs/method-landing.md`, `X2 第一次绿：我按【小节】锚，一节只锚一句，于是同节里另外七八条可以被静默删掉。`},
	{`docs/method-landing.md`, `是评审方拆出来的，而正确的形状两个提交之前就在仓库里`},
	{`docs/method-landing.md`, `我发明了那个模式，却没把它用在自己刚建的这套东西上。`},
	{`docs/method-landing.md`, `「它有一个机械定义」这句话本身，也可能是一个「看起来像守住了」的东西。`},
	{`docs/method-landing.md`, `定义要和文件里实际用的格式对齐，而那要数一遍，不是想一遍。`},
	{`docs/method-landing.md`, `仍然盖不住：写成普通行文、加粗只在句中的那些。我判断它们多半是解释而不是规矩——`},
	{`docs/method-landing.md`, `但那是判断，不是测量，所以它进上面那张表的 ❌ 行。`},
	{`docs/method-landing.md`, `一个「由模式生成」的登记表，它的覆盖面永远只能被【另一个模式】测出来。`},
	{`docs/method-landing.md`, `生成器和验证器用同一个模式 ⇒ 它对自己永远自洽。`},
	{`docs/method-landing.md`, `我曾有 76 条登记、44 节锚点、五种拆除验证全绿，`},
	{`docs/method-landing.md`, `而当时实际漏着两条——因为那五种拆除也在同一个模式内部。`},
	{`docs/method-landing.md`, `这个模式改过两次，两次都是外部发现的，一次都不是我读出来的：`},
	{`docs/method-landing.md`, `⚠️ 我上一版在这里写的残留是「纯散文 / 表格单元格」——那是【低估】的。`},
	{`docs/method-landing.md`, `实测漏掉的两条都是加粗行，落在我认为已经覆盖的格式里。`},
	{`docs/method-landing.md`, `一个低估的残留比不写残留更糟：它给出的边界，让人以为边界在别处。`},
	{`docs/method-landing.md`, `它当时不管。 针取的是前 26 字，比对又是双向 HasPrefix ⇒`},
	{`docs/method-landing.md`, `一个检查的报错文案，是它对外做出的承诺。承诺和实现不符时，`},
	{`docs/method-landing.md`, `先坏的不是那个洞——是【读到文案的人以为那一格有人守】。`},
	{`docs/method-landing.md`, `⇒ 要么把检查改成它宣称的样子，要么把宣称改成检查的样子，两者必须对上。`},
	{`docs/method-landing.md`, `此后改任何一条规矩的一个标点，测试都会红，必须重跑生成器。`},
	{`docs/method-landing.md`, `我认为这正是想要的，但它确实是多出来的手工——`},
	{`docs/method-landing.md`, `如果将来有人觉得太吵，正确的动作是回来改这一格的取舍并写明，`},
	{`docs/method-landing.md`, `不管那条规矩长什么样。两张表因不同原因失效：`},
	{`docs/method-landing.md`, `⚠️ 那个行数是快照，不是常量——这几份文件一改它就变。`},
	{`docs/method-landing.md`, `记它是为了「当时扫了多少」，不是为了拿来比对；要现在的数就重跑那个脚本。`},
	{`docs/method-landing.md`, `有些行根本不在登记表里。       ← 16 个字符 / 40 个字节`},
	{`docs/method-landing.md`, `同一个阈值 16，两边的单位不同。 于是登记表少一条、验证器多认一条，`},
	{`docs/method-landing.md`, `两处「必须一致」的判据写在两种语言里，一致性就得自己有人守。`},
	{`docs/method-landing.md`, `这次守住它的是那条测试本身——因为它比的是两边的【产物】，不是两边的【源码】。`},
	{`docs/method-landing.md`, `⚠️ 对照组这一步不能省。「299 行全响」和「这个 harness 永远返回非零」`},
	{`docs/method-landing.md`, `在输出上长得一模一样。第一遍我跑出 0 的时候，harness 正在抛编码异常，`},
	{`docs/method-landing.md`, `而我只用了 returncode——一个favorable 的结果配一个没验过的仪器，`},
	{`docs/method-landing.md`, `正是这份文档从头到尾在讲的那件事。 重跑：改成 bytes，加对照组。`},
	{`docs/method-landing.md`, `上表那三个 ❌ 是这张表最诚实的部分。`},
}

// —— 普查表：一个【不用任何模式】的数 ——
//
// 评审方指出的结构性问题：
//
//	一个「由模式生成」的登记表，它的覆盖面永远只能被【另一个模式】测出来。
//	生成器和验证器用同一个模式 ⇒ 它对自己永远自洽。
//
// 所以这里记一个最宽的数：每份载体里含 `**` 的行数。删掉任何一条规矩它都会掉，
// 不管那条规矩长什么样。两张表因【不同原因】失效：
//
//	普查数    挡不住「原地改写」（行数不变）    挡得住「没进登记表的行被删」
//	登记表    挡得住改写（针对不上）            挡不住没进表的行被删
//
// 代价：改这五份文件时，加/删任何一个加粗短语都会让它变红，得重跑生成器。
// 有意的摩擦，同 pending.txt —— 不这样，覆盖面就只能靠回想。
var carrierCensus = []struct {
	file      string
	boldLines int
}{
	{`tools/probe/README.md`, 56},
	{`docs/README.md`, 99},
	{`CONTRIBUTING.md`, 84},
	{`tools/audit/README.md`, 24},
	{`docs/method-landing.md`, 135},
}

// ruleLines 把一份 markdown 里所有「独立成句的规矩行」抠出来。
// 判据必须和 tools/audit/gen_quoted_rules.py 的 is_rule 一致，否则两边各说各的。
func ruleLines(src string) []string {
	stripMark := strings.NewReplacer("*", "", "`", "", ">", "")
	var out []string
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if strings.HasPrefix(line, "|") { // 表格行：判据是【行首】，不是「含竖线」
			continue
		}
		quoted := strings.HasPrefix(line, "> ") && strings.Contains(line, "**")
		// 用 rune 不用 byte：生成器那边是 Python 的 len()，数的是【字符】。
		// 同一个阈值 16，Go 的 len() 数字节 ⇒ 16 个汉字的一行会落到不同的一侧。
		bold := strings.HasPrefix(line, "**") && strings.Count(line, "**") >= 2 &&
			len([]rune(line)) > 16
		if !quoted && !bold {
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

// TestEveryQuotedRuleIsRegistered 断言载体里的规矩【逐字相同、一条不多、一条不少】。
//
// ⚠️ 比对是【整行相等】，不是前缀。
//
// 上一版针取前 26 字、比对用双向 HasPrefix ⇒ 一条已登记规矩，前 26 字之后的
// 内容是自由的。评审方 2026-09-08 实证：把一条规矩的后半截改成意思相反的话，
// 两张表都点头。量过面：144 条里 93 条（65%）正文长于 needle，尾部合计 859 字。
//
// 而逼着它改的不是这个洞，是这条测试【自己的报错文案】曾经写着
// 「改写就把 quotedRules 里那一行一起改」——**那句在宣称它管改写，而它不管**。
//
//	要么把检查改成它宣称的样子，要么把宣称改成检查的样子。
//
// 这里选前者。代价是改任何一条规矩的措辞都要重跑生成器——取舍写在
// docs/method-landing.md，那是评审方委派我判、并要求写下来的一格。
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
		// 多重集比对：同一行出现两次也要登记两次。
		want := map[string]int{}
		for _, n := range needles {
			want[n]++
		}
		got := map[string]int{}
		for _, a := range ruleLines(string(b)) {
			got[a]++
		}
		for n, c := range want {
			if got[n] < c {
				t.Errorf("%s 少了一条登记过的规矩（登记 %d 次，实际 %d 次）：\n  %q\n"+
					"  它被【删掉】或【改过一个字】了。有意的话重跑 "+
					"tools/audit/rebuild_docs_test.py；不是的话，"+
					"这条规矩刚被静默改掉了。\n"+
					"  注意：这一层比的是【整行逐字相同】，改标点也会红——这是有意的",
					file, c, got[n], n)
			}
		}
		for a, c := range got {
			if want[a] < c {
				r := []rune(a)
				if len(r) > 44 {
					r = r[:44]
				}
				t.Errorf("%s 多了一条没登记的规矩（或某条被改写后的新样子）：\n  %q…\n"+
					"  跑 tools/audit/rebuild_docs_test.py。\n"+
					"  不登记的后果不是现在出错，是【以后它被删掉时没有东西会响】",
					file, string(r))
			}
		}
	}
	if len(quotedRules) < 60 {
		t.Fatalf("quotedRules 只有 %d 条，不像覆盖了五份载体 —— 先确认这张表没被清空",
			len(quotedRules))
	}
	t.Logf("登记了 %d 条规矩", len(quotedRules))
}

// TestCarrierCensus 用一个【不带模式】的数守着同一批文件。
func TestCarrierCensus(t *testing.T) {
	for _, c := range carrierCensus {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Errorf("%s 读不到：%v", c.file, err)
			continue
		}
		n := 0
		for _, raw := range strings.Split(string(b), "\n") {
			if strings.Contains(raw, "**") {
				n++
			}
		}
		if n != c.boldLines {
			t.Errorf("%s 含 `**` 的行数：登记 %d，实际 %d（%+d）\n"+
				"  · 你刚改过这份文件 ⇒ 跑 tools/audit/rebuild_docs_test.py\n"+
				"  · 你没改过 ⇒ 有人动了它，而【登记表可能看不见那一处】——"+
				"这一条守的正是登记表的模式盖不住的地方",
				c.file, c.boldLines, n, n-c.boldLines)
		}
	}
	if len(carrierCensus) < 5 {
		t.Fatalf("普查表只有 %d 份文件，载体不止这些", len(carrierCensus))
	}
}
