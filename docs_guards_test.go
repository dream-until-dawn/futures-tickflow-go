package tickflow

// 这一份是【手写的】守卫，与 docs_test.go 分开放。
//
// 为什么分开（评审方 2026-09-08 实测出来的）：
// 原来这些守卫和三张生成的表在同一个文件里，而那个文件只有一半是生成的——
// `rebuild_docs_test.py` 把 MARK 之前的 9212 字节【照抄】回去。
// 于是两条分支各自改了同一个守卫、冲突、按「取任一侧 + 重跑生成器」处理
// ⇒ **另一侧的修改没了，而 rebuild 退 0、gofmt 干净、vet 过、测试全绿。**
//
// ⇒ 拆开之后：这一份【只手写】，docs_test.go 【全生成】。
// 合并冲突时的判据因此变得干净：
//
//	docs_guards_test.go  是手写代码，该怎么合怎么合
//	docs_test.go         是生成物，别合，重跑 tools/audit/rebuild_docs_test.py
//
// 而「全生成」这件事是可验的，不靠人读代码确认：
// **删掉 docs_test.go，跑 rebuild，它应当逐字节一模一样地回来。**
//
// 这一拆的通用形状（三个实例）：
//
//	半生成的文件       → 合并时不知道该合哪半
//	第二份拷贝（.gen） → 今天一致，明天分叉
//	派生出来的阈值     → 在生成那一刻恒真
//
// **三个都是「一个东西同时有两个来源」**，而处置都一样：
// **与其给规矩加例外，不如把事实改成规矩说的那样。**

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
// ⚠️⚠️ **本条只堵 `<!--` 这一个入口。它不堵那一类，别把它读成「那一类关上了」。**
//
// 同一条规矩，用【不含 `<!--`】的写法照样能让它在渲染后消失。两种已实测（对照组：
// 真的删掉那一行 → 红，所以下面这两个绿不是仪器坏了）：
//
//	A2  在那一行前后各加一行 ```      → 渲染成代码块 → 全绿
//	A3  只在行首加四个空格            → 同样渲染成代码块，而 ruleLines 先 TrimSpace，
//	                                    登记表照样认得它 → 全绿
//
// 还有没试过的：改渲染标记、把整节挪进已有的代码块、在前面加一行 `> `……
// **只要不动那一行的字节，两张表就都不响。**
//
// ⇒ 这一类是【没有机械守卫】的，写在 docs/method-landing.md 第二节（第三类）。
// 守卫的自述不能比守卫本身宽——**一个检查的注释也是它对外做出的承诺**。
//
// 之所以仍然值得加：写这条时五份载体里 `<!--` 共 0 处，零成本，
// 而且它堵的是**最顺手的那个入口**。堵一扇门是对的，把它说成堵住了整间屋才是错的。
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

// TestNoOrphanedSentences 抓「一句话被拆散在两个地方」。
//
// 成因是一个**改文档的动作**，不是打字失误：用「锚定某一行 + 在其后插入」改文档，
// 而锚定的那一行是**多行段落的第一行** ⇒ 续行被新插入的整块挤到十几二十行以外，
// 成了孤句，而两半各自读起来都不像坏的。
//
// 发生过三次，**三次都进了 main**：
//
//	docs/method-landing.md  一句话重复了两遍（一份在原位，一份悬在引用块后）
//	docs/README.md          「（这一格后来作废了…」 的下半句被挤开 15 行
//	CONTRIBUTING.md         两处，其中一处的下半句被挤开 60 行
//
// 前一次我只扫了「重复的长行」——那只抓得到第一种。这一条抓的是另外两种：
// **按空行切块，块内的全角括号必须成对。** 拆散一个括号句，两边立刻不平衡。
//
// 它抓不到不带括号的拆散——**那一格没有守卫**，和「可见 ≠ 可读」并列。
// 写在这里是为了让下一个人知道边界在哪，而不是以为这一类关上了。
func TestNoOrphanedSentences(t *testing.T) {
	var checked int
	pairs := [][2]string{{"（", "）"}, {"【", "】"}, {"「", "」"}}
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
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// ⚠️ 两个【前提】，都是评审方 2026-09-08 量出来的，都一行：
		//
		// ① CRLF：`\r\n\r\n` 里两个 \n 中间隔着 \r，Split("\n\n") 切不开，
		//    整份文件塌成一段 ⇒ 这份文件实际上只剩「全文括号总数配平」这么弱的检查，
		//    **而且不报错，只是变弱**。今天 9 份 .md 全是 LF，条件不成立——
		//    但这是 Windows 仓库，我自己在这棵树上被 CRLF 咬过一次。
		// ② 行号：报出来的行号是这个守卫的【产品】（它让人去找另外半句），
		//    报错了地方等于把人支到别处。
		//
		// 这两条守的都是【守卫自己的前提】，同「重名小节会让 R4 的修变弱」。
		src := strings.ReplaceAll(string(b), "\r\n", "\n")
		off := 0
		for _, block := range strings.Split(src, "\n\n") {
			checked++
			// 行号按【这一段看得见的内容】算，不按段的起点算。
			//
			// 原来是 line += Count(block,"\n") + 2 —— 对，但对的是【段的起点】，
			// 而段前若有落单的换行，起点落在空行上，读的人会在那一行找不到东西。
			// 实测（评审方给的三种）：段间 1 空行 偏 0 / 2 空行 偏 1 / 3 空行 偏 0
			// —— **偏移不是单调的，所以读的人也没法心算修正**。
			lead := len(block) - len(strings.TrimLeft(block, "\n"))
			line := strings.Count(src[:off+lead], "\n") + 1
			off += len(block) + 2
			for _, q := range pairs {
				if strings.Count(block, q[0]) != strings.Count(block, q[1]) {
					first := strings.TrimSpace(block)
					if i := strings.Index(first, "\n"); i > 0 {
						first = first[:i]
					}
					if r := []rune(first); len(r) > 50 {
						first = string(r[:50])
					}
					t.Errorf("%s:%d 这一段里 %s%s 不成对（%d vs %d）：\n  %s…\n"+
						"  多半是一句话被拆散了——用「锚定第一行 + 其后插入」改文档时，"+
						"锚到了多行段落的第一行，续行被挤走了。\n"+
						"  找那半句在哪，接回去；不是的话，把括号补齐",
						p, line, q[0], q[1],
						strings.Count(block, q[0]), strings.Count(block, q[1]), first)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走 .md 文件时出错：%v", err)
	}
	if checked < 100 {
		t.Fatalf("只切出 %d 段，不像走过了整个仓库——先确认遍历没坏", checked)
	}
	t.Logf("检查了 %d 段", checked)
}

// TestNoDuplicateHeadingsInCarriers 禁止同一份载体里出现两个同名小节。
//
// 起因（评审方 2026-09-08 挂的账）：R4 的修法是把「所属小节」并进登记键，
// 而**那个键是小节的标题文本**。于是同一份文件里若有两个同名小节，
// 一条规矩在它们之间挪动，键不变 ⇒ **R4 那个修在那两节之间静默变弱**。
//
// 这不是「守卫坏了」，是**守卫的一个前提**：小节标题在文件内唯一。
// 前提没人守的时候，它迟早不成立——所以在这里守住它。
//
// 敢直接禁的理由和禁 HTML 注释一样：写这条时五份载体里重名小节共 0 个，
// 零成本；而规矩文档里两个同名小节本来就会让读者分不清引用的是哪一节。
func TestNoDuplicateHeadingsInCarriers(t *testing.T) {
	for _, f := range carrierFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s 读不到：%v", f, err)
			continue
		}
		seen := map[string]int{}
		for n, raw := range strings.Split(string(b), "\n") {
			line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
			if !mdHeading.MatchString(line) {
				continue
			}
			if first, dup := seen[line]; dup {
				t.Errorf("%s 有两个同名小节：第 %d 行与第 %d 行\n  %s\n"+
					"  登记表用【小节标题文本】当键，重名会让「规矩挪节」在这两节之间"+
					"静默不被发现（R4 那个修在这里变弱）。\n"+
					"  改一个标题即可——它们本来也会让读者分不清引用的是哪一节",
					f, first, n+1, line)
			} else {
				seen[line] = n + 1
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
