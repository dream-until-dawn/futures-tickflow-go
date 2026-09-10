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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// TestGuardsDoNotSkipThemselves 禁止【已登记的守卫】跳过自己。
//
// 起因：`guardNames` 登记的是名字 ⇒ **存在 ≠ 一致**（见生成文件里那段「代价二」）。
// 两种绕法都让名字留在原地：把函数体掏空，或者在开头加一句 Skip。
// 前者只有对照组能看见；**后者是一个行首关键字，可以机械地堵掉**——
// 评审方 2026-09-08 改判：他上一封否掉「加严」的理由（哈希函数体会让每次合法修改
// 守卫都要重生成）**只管掏空那一种**，对 Skip 不成立。
//
// ⚠️ 而 Skip 这一种比掏空更隐蔽，**理由在自检命令本身**：
//
//	CONTRIBUTING 的自检是 `go test ./... -count=1`，**非 -v**
//	⇒ 一个被 Skip 的守卫，输出里【连一个字节都不变】，三行照样全是 ok
//	（掏空至少动了函数体，diff 里是一块；Skip 是加一行。）
//
// ⚠️ **判据用 go/ast，不用子串**，理由是一处【已经存在的假阳性】：
// `docs_test.go` 里有两处 `t.Skip` 字样，**都是注释**——正是上一版为了记录这个盲点
// 写下的那段解释。**一个朴素的子串禁令，会打中解释它自己的那段话。**
// 用 parser 解析之后，注释天然不参与，**所以本文件和别处都可以放心地在散文里提它**。
//
//	⇒ 这一格是本仓「先量、再定判据」的第三次：禁 `<!--` 前数过 0、
//	  禁冲突标记前数过 `||` 是 0，**而这次数出来不是 0，来源正是这条守卫自己的文档。**
//
// ⚠️ 射程（照例写明它不比什么）：
//
//	堵的     已登记守卫函数体内的 Skip / Skipf / SkipNow 调用
//	不堵的   业务测试里的 Skip —— 那是合法的（比如需要外网），而它们不在 guardNames 里
//	不堵的   把函数体掏空 —— 那是「代价二」的另一半，只有对照组能看见
//	不堵的   在守卫【调用的辅助函数】里 Skip —— 只看守卫自己的函数体
//
// ⚠️ **登记⑰：它还【顺带】守住了一件没被承诺的事 —— 全部 `_test.go` 语法可解析。**
//
// 2026-09-09 实例：一次写坏的补丁让 `source/sinasource/client_test.go` 出现
// `string literal not terminated`，**而是这条守卫当场报出来的**（它必须先 parse 才能找函数体）。
//
//	⛔ **副作用型覆盖是真的覆盖，而它没有被承诺**：
//	把这里的 go/parser 换成正则扫，那一下就没了，**且不会有任何东西提示**。
//	⇒ 写在这里不是为了承诺它，是**让改它的人知道自己在退什么保** ——
//	  一份没人知道的保险，退保时也不会有人发现。
//
// 范围不写成文件清单，而是「名字在 guardNames 里的函数，不管它在哪一份 _test.go」——
// **和 TestGuardsStillExist 同一个判据**：挪个文件不算消失，也不该逃出这条守卫。
func TestGuardsDoNotSkipThemselves(t *testing.T) {
	want := map[string]bool{}
	for _, n := range guardNames {
		want[n] = true
	}
	if len(want) == 0 {
		t.Fatal("guardNames 是空的 —— 这个守卫没在守任何东西")
	}

	seen := map[string]bool{}
	fset := token.NewFileSet()
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
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		// 最后一个参数是 0：**不解析注释**。判据只看真的调用。
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return perr
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !want[fd.Name.Name] {
				continue
			}
			seen[fd.Name.Name] = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Skip", "Skipf", "SkipNow":
					t.Errorf("守卫 %s 在 %s:%d 调用了 %s —— 守卫不许跳过自己。\n"+
						"  自检用的 go test ./... 是非 -v 的，被跳过的守卫在它眼里是 ok，"+
						"输出连一个字节都不变\n"+
						"  · 真的不该再守 ⇒ 删掉它，并跑 tools/audit/rebuild_docs_test.py\n"+
						"  · 只是暂时不想红 ⇒ 那正是这条守卫要挡的东西",
						fd.Name.Name, p, fset.Position(call.Pos()).Line, sel.Sel.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走 _test.go 时出错：%v", err)
	}
	// 前提也要打印出来：不然「一个守卫都没读到」和「全都干净」长得一样。
	if len(seen) != len(want) {
		t.Fatalf("只读到 %d 个已登记的守卫，登记的有 %d 个 —— "+
			"这条守卫没覆盖到全部（缺失的那些由 TestGuardsStillExist 点名）",
			len(seen), len(want))
	}
	t.Logf("查了 %d 个已登记的守卫，没有一个跳过自己", len(seen))
}

// conflictMarker 报告一行是不是 git 的合并冲突标记。
//
// ⚠️ 四种，不是三种（评审方 2026-09-08 指出，我复现过：上一版认三种，
// 往 .md 里插一行 `||||||| merged common ancestors`，守卫退 0 看不见）：
//
//	<<<<<<< HEAD                    ours
//	||||||| merged common ancestors base —— 只在 merge.conflictStyle = diff3/zdiff3 下出现
//	=======                         分隔
//	>>>>>>> other-branch            theirs
//
// 严重度不高（一次完整的 diff3 冲突里另外三种都在，照样会被抓；`|||||||` 单独存活
// 要有人手工删掉另外三行却留下它）——**但上一版注释里那句「只认 git 的三种形态」
// 是一句【关于 git 的错话】，与能不能被利用无关。要么比，要么写明它不比。这里选比。**
//
// ⚠️ 长度按 **>= 7** 认，不按精确 7：`conflict-marker-size` 可以在 `.gitattributes`
// 里按路径改。本仓当前没配（查过 `.gitattributes` 全文，无此项）⇒ 实际就是 7。
// 上一版 `=======` 写成精确相等，于是一行 8 个等号看不见——**这是同一个洞的第二半**，
// 而它不是评审方指出的，是加第四种时顺手量出来的。
//
// 判据：同一个字符连续 >= 7 个，其后要么行尾、要么一个空格（标记后面跟的是分支名）。
// 这样 markdown 表格行（`| a | b |`，连续竖线只有 1 个）不会误伤。
//
// ⚠️ 它会误伤的：一行 >= 7 个等号的 setext 标题下划线。本仓一律用 `#` 标题，
// 实测 0 处——**写在这儿是因为它是真的会误伤，不是因为它不会。**
func conflictMarker(line string) bool {
	for _, c := range []byte{'<', '|', '=', '>'} {
		n := 0
		for n < len(line) && line[n] == c {
			n++
		}
		if n >= 7 && (n == len(line) || line[n] == ' ') {
			return true
		}
	}
	return false
}

// TestNoConflictMarkersInDocs 禁掉 .md 里的合并冲突标记。
//
// 起因（评审方 2026-09-08 提出，我复现过）：`docs_test.go` 的生成器读的是这些 `.md`。
// 而这条「生成物冲突时重造」的规矩，**使用现场就是解冲突**——那时载体自己也可能
// 还带着标记。实测：两侧各留一条规矩 + 冲突标记，然后照规矩重造——
//
//	rebuild 退 0；两侧的规矩正文【都进了登记表】；
//	标记本身【没进生成物】（它不是加粗行，被过滤掉了）；go test 全绿。
//
// ⇒ 得到一份自洽、全绿、把两侧内容一起烤进去的生成物，而生成物上看不出异常。
//
// ⚠️ 而真正把它从「另行送审」推成「必须做」的，是它的【延迟发作】那一面：
// 冲突态重造会把 `high_water` 一起抬上去，而高水位**只涨不落**。于是后来
// **正确地**解掉冲突再重造的那个人会红，提示还告诉他
// 「动手把 high_water.txt 调低，**并在提交信息里说删了什么**」——
// **而他什么都没删。照做，他得编一句删除说明。**
//
//	⇒ 这不是「假警报」那一类，是**「要求一个做对了的人写下一句假话」**那一类。
//
// 通则（比这一格重要）：
//
//	**一个只涨不落的东西，把它抬高的那个动作必须比它本身更可信。**
//	high_water 自动抬高的条件是「重造成功」——于是每一次成功的重造都被当成可信的，
//	包括跑在没解干净的载体上那一次。**单调机制会把错误固化成基线。**
//
// ⚠️ 这条守卫【只堵这一个入口】，和禁 HTML 注释那条一样：
//   - 只扫 `.md`。**因为 `.md` 是标记能【静默】存活的地方**——`.go` / `.py` 里的编译不过。
//   - 认四种标记行首形态（见下面 conflictMarker），不认别人自制的分隔符。
//   - 它**不检查**载体内容对不对，只检查「有没有一次没解干净的合并留在这儿」。
//
// ⚠️ 关于 `pending.txt`：它**也**会拦，但**那是副作用，不是设计**，而且三种标记
// 走的是【两条不同的路】（评审方 2026-09-08 指出我原来那句只描述了三分之一，实测）：
//
//	<<<<<<< HEAD          → 两个字段 ⇒ 当成一条欠条 ⇒ 报【孤儿项】
//	>>>>>>> other-branch  → 同上，报【孤儿项】
//	=======               → 一个字段 ⇒ 报【白名单格式错误（缺预定版本）】
//
// **抓到了，但前两种的诊断指向错误的方向**——读到「孤儿项」的人会去找哪个标识符写错了，
// 不会想到那是一行冲突标记。
//
//	**一个靠副作用成立的保护，必须写明它是副作用**——否则下一个人会以为它是设计出来的，
//	从而不敢动、或者动了不知道自己动了什么。
//	`loadPending` 哪天改了解析，这层保护会静默消失。
//
// 成本：当前 9 份 `.md`，四种标记各命中 0 处；且**没有一行以两个以上竖线开头**
// （markdown 空单元格表行是 `|||||||` 唯一可能的假阳性来源，实测 0 处），
// 也没有一行是纯等号的 setext 标题下划线（本仓一律用 `#` 标题）。
// **现在是 0，所以零成本，而它堵的是最顺手的那个入口。**
func TestNoConflictMarkersInDocs(t *testing.T) {
	var scanned int
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
		scanned++
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if conflictMarker(line) {
				t.Errorf("%s:%d 有一行合并冲突标记：%q\n"+
					"  先把载体解干净，再动生成物。顺序反了，重造会把两侧内容一起烤进去，"+
					"而且退 0、全绿；还会把 high_water 抬到一个错的基线上",
					p, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走 .md 文件时出错：%v", err)
	}
	// 前提也要打印出来：不然「一个都没扫」和「全都干净」长得一样。
	if scanned == 0 {
		t.Fatal("一份 .md 都没扫到 —— 这个守卫没在守任何东西")
	}
	t.Logf("扫了 %d 份 .md，无冲突标记", scanned)
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
//
// ⛔ 而它还有第二条边界，比上面那条更容易被忘：**这个 Walk 只收 `.md`。**
//
// 2026-09-09 的实测漏例（评审方发现，当时在 `store/segfile/store_test.go`，本格已删）：
//
//	那一句是同一个形状 —— 前半句被改写吸走，续行留在原地，结尾一个没有开头的右全角括号
//	把那一行原样放进一个临时 .md ⇒ **这条守卫 FAIL**（当次扫了 1549 段）
//	而它待在 .go 注释里   ⇒ 这条守卫从头到尾没看它一眼
//
// ⇒ **本仓早就有一条守卫认得这个形状；它逃掉不是因为形状生，是因为射程停在 `.md`。**
// 而它长出来的地方是一段【专门为了把话写准而重写】的注释 —— 改写正是本条开头写的那个成因，
// 只是这一次成因落在了射程之外。
//
// ⚠️ 记这一段的理由，比这一段本身值钱：
// **一个已知的射程边界，有一个真实的漏例，和只有一句声明，是两回事。**
// 上一段那条「抓不到不带括号的拆散」至今只是声明——没有人验过它真的漏得掉；
// 这一条有日期、有形状、有复现方式（拷进临时 .md 就能重跑）。
// ⇒ **要放宽射程（把 .go 的 `//` 段也收进来）就照这个漏例改，改完它必须当场红一次。**
//
// ⚠️ 而放宽之前有一道坎，是 2026-09-09 拿探针量出来的，不是想出来的：
// 把本仓的 .go 注释翻成 .md 喂给这条守卫（`//` 光杆行翻成空行，好和它的切块方式对上），
// 1651 段里报 4 处 —— 其中 **2 处是这条守卫【自己的注释】**：
// 上面那份「发生过三次」的清单里，`docs/README.md` 那一条**引用的例子本身就是个没有开头的括号**。
//
//	⇒ 射程放宽到 .go 的那一格，**必须同时给「被引用的例子」一条出路**
//	  （例如只检查非缩进段、或认反引号里的内容为字面量），
//	  否则新射程第一次运行就在守卫自己身上红 —— 而**一条永远红的守卫会被人关掉**。
//
// 另外 2 处是真的：一个全角括号**跨过了一个空注释行**（`store/segfile/store_test.go`
// 那段讲 BUILD 的注释），本格已改。**跨空行的括号和被拆散的句子，在这条守卫眼里是同一个形状** ——
// 而它眼里没错：读者读到那个右括号时，开头那半句已经隔了一个段落。
//
// ⛔ 而上面那条「要给被引用的例子一条出路」不是个小角落 —— **写这段说明的过程本身就证明了它**：
// 讲这条守卫，就得举一个「没有开头的右全角括号」当例子；而只要把那个字符原样写进来，
// **这段说明自己就不配平了**。写这一段时连撞三次，三次都是同一个探针当场抓住，
// 三次的修法都只有一个：把那个字符换成描述它的词。
// ⇒ **凡是讲这条守卫的文字，都会自然地引用一个不配平的括号。**
// 那不是偶发的假阳性，是**这条规则和它自己的文档之间的结构冲突**：
// 上面「发生过三次」那份清单为了讲清楚，也非引用不可，所以它至今是红的。
// ⇒ 放宽射程那一格必须正面处理它，办法上面写了（认反引号里的内容为字面量）——
// **那是一格代码改动，要自己红一次，不在本格。**
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

// TestScriptsWrapBothStreams 抓「带中文的 Python 脚本没把两个流都包成 UTF-8」。
//
// 在这台机器上，Python 的 stdout/stderr 默认走系统代码页，
// 于是**每一个中文字都是乱码**——而输出乱码最要命的地方是失败路径：
//
//	**一条读不懂的失败信息，和没有失败信息差不多。**
//
// # 为什么是守卫而不是一句约定
//
// 因为约定当晚就失效了，而且是**写约定的人自己**（2026-09-09）：
//
//	扫了一遍 tools/audit/*.py，补上 stderr，写进注释说「以后都要包」
//	⇒ 同一晚，`tools/probe/probe.py` 被发现【连 stdout 都没包】，输出一直是乱码
//	  —— 而本仓的规矩恰恰是「把探针输出贴进送审材料」
//
// ⇒ 评审方钉过的那句，这一格照抄：
//
//	**一条「必须有人写点东西」的规矩，只要缺了它就会红，那它就不是靠人记得，是机器要求。**
//
// # ⛔ 那次扫描漏掉它的原因，有两个，而它们是同一个
//
//	① 范围写成【目录】       只扫 tools/audit ⇒ tools/probe 在射程外
//	② 范围写成【当前的样子】 只改「现在就有中文 assert 的」⇒ 今天没有、明天加一条的漏掉三份
//
//	**范围写成了「我现在看到的样子」，而不是「这条性质本身」。**
//
// 这是同一个形状在本仓的第四、第五次（前三次：普查表写「本节」、孤儿句守卫写 `.md`、
// 守卫登记写死一个文件名）。**而这两次发生在【当天写完前三次之后】** ——
// 所以它不是「没想到」，是**光想到不够**。
//
// # 判据只有一句
//
//	这份 .py 里有非 ASCII ⇒ stdout 与 stderr 两个流都要包。
//
// 不问它现在往哪儿写：**未捕获异常的 traceback 走 stderr，而它带得动文件名与字面量。**
//
// ⚠️ **射程**（照例写明它不比什么）：
//
//	抓的     .py 文件里有非 ASCII，而缺了两行包流里的任意一行
//	不抓的   包了但写错了参数（例如 errors 不是 replace）——只比整行，不解析
//	不抓的   Go / 其它语言的输出编码
//	不抓的   「库模块被 import 时包流是副作用」这件事
//
// ⇒ 最后一条今天不成立，是量过的：**本仓 12 个 .py 没有一个被别的 .py import**，
// 全是独立脚本。**哪天出现了库模块，这条判据要重想**——
// 那时它会红，而**红在那儿正好是提醒**，不是误伤。
func TestScriptsWrapBothStreams(t *testing.T) {
	const (
		wantOut = `sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")`
		wantErr = `sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")`
	)
	var checked int
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
		if !strings.HasSuffix(p, ".py") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		if !strings.ContainsFunc(s, func(r rune) bool { return r > 127 }) {
			return nil // 全 ASCII：包不包都不影响可读性
		}
		checked++
		missing := []string{}
		if !strings.Contains(s, wantOut) {
			missing = append(missing, "stdout")
		}
		if !strings.Contains(s, wantErr) {
			missing = append(missing, "stderr")
		}
		if len(missing) > 0 {
			t.Errorf("%s 里有中文，但没包 %s。\n"+
				"在 import 之后加上这两行（缺哪行加哪行）：\n"+
				"  %s\n  %s\n"+
				"⚠️ 不包的话，这个脚本【失败时】印出来的每个中文字都是乱码 ——\n"+
				"   而失败路径正是最需要读懂的那一条。",
				p, strings.Join(missing, " 与 "), wantOut, wantErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Fatalf("只检查了 %d 份带中文的 .py —— 本仓有 12 份，"+
			"这个数掉下来多半是 Walk 的起点或过滤条件被改坏了", checked)
	}
	t.Logf("检查了 %d 份带中文的 .py", checked)
}

// TestHighWaterChain 抓「来历这条链断了」。
//
// 起因是一次真的合并（`1f62620`）：两条分支各自抬过高水位，合并时来历行取**并集**，
// 于是并集里的值**不再单调**——`rules` 走到 `263 266 268 270 275 276` 之后冒出一个 `271`。
//
//	**那不是错，那是另一条分支的历史被接了进来。**
//
// ⛔ 而在这条守卫之前，**没有任何东西看得见它**：`TestHighWaterProvenance` 只比
// 「最后一行来历」和「当前值」，中间怎么走它不看。
//
// # 判据是 running max，不是「和上一行比」
//
// 两者在 `1f62620` 上给出不同的读数（2026-09-09 实测）：
//
//	和上一行比    2 处（`271` 那行，和它后面的 `277` 那行）
//	running max   1 处（只有 `271` 那行）
//
// 取后者，理由是**`277` 那一行没有错**：真实的最大值就是 `276`，从 276 抬到 277 是对的。
//
//	**一条会把正确的行也标红的规则，会教人忽略它。**
//
// # 合流记录：一条「必须有人写点东西」的规矩
//
//	# 来历 <日期> <名字> <值> 合流 <另一侧的最大值> <说明>
//
// 它放宽随后那些行：该名字下值不超过「另一侧最大值」的，是被接进来的历史。
// **两个正式字段**（第 6 个是 `合流`，第 7 个是数），不靠说明文字里的括号去猜——
// 括号是散文，而这条链是要被机器走的。
//
// ⚠️ **射程**（照例写明它不比什么）：
//
//	抓的     值低于此前见过的最大值，而没有合流记录罩着
//	抓的     合流记录**自己不自洽**：它的值低于「此前见过的最大值」或「另一侧最大值」
//	不抓的   合流记录里的数**是不是真的**——它挡的是「忘了说」，不是「说了假话」
//
// ⇒ 前两条的分界要说准：**自洽 ≠ 为真**。
// 一行写着「合流后最大值 100，另一侧 271」是**自相矛盾**，机器判得了；
// 一行写着「另一侧 271」而另一侧其实是 269，是**假话**，机器判不了（它没有另一侧）。
// **而合流记录是这套机制里唯一由人手写的一行，所以自洽这一档尤其值得花。**
//
//	不抓的   来历行的**说明文字**对不对；那是散文，没有守卫
//	不抓的   合流之后又出现的、比另一侧最大值还小的行——它会被同一条合流记录放行
//
// ⇒ 最后一条是有意留的口子，**而「为什么不收窄」比「有个口子」值钱**：
//
//	收窄的办法是【下一次真正抬高时关窗】。而一次合并可能接进来好几行，
//	其中一行若高过本侧，关窗就会把它后面【合法的】被接行判成断链 ——
//	那正是上面刚否掉的「把正确的行也标红」。
//
// ⇒ 代价：一条合流记录会**长期**许可低值行。而它有一条补偿，写在这儿免得读者高估这个洞：
//
//	`TestHighWaterProvenance` 仍然钉死「最后一行来历 == 当前值」
//	⇒ 一行落在窗口里的低值行**降不低高水位**，它只是【没被解释】。
//
// 对照组 `tools/audit/provenance_control.py` 的 `C5` 格就是它，**期望绿**。
//
// ⚠️ 这条规则在 `tools/audit/rebuild_docs_test.py` 的 `chainBreaks` 里**还有一份**。
// 两份是有意的（一份在写盘前拦，一份拦绕过 rebuild 的那条路），
// **而它们会分岔，分岔本身没有守卫**——照实写在这儿。
func TestHighWaterChain(t *testing.T) {
	type seen struct {
		max   int
		allow int
		hasA  bool
	}
	st := map[string]*seen{}
	var lines int

	// ⛔ 先收一遍「每个名字出现过哪些值」——给下面那条 `自` 的检查用。
	//
	// 为什么要有它（评审方 2026-09-09）：`自 <前值>` 落地之后是一个
	// **只写不读**的字段 —— 没有任何东西要求它存在，也没有任何东西检查它的值。
	//
	//	`TestHighWaterProvenance`  只读 f[3]/f[4]
	//	本守卫（上一版）            只在 f[5] == "合流" 时读 f[6]
	//	⇒ 生成器那个格式串哪天写错（old,old,new 写成 old,new,new），**没有任何东西会红**
	//
	// 而这正是我自己在另一条分支上写下的判据：
	//
	//	**那一格填了、而守卫没被登记或从来没红过 —— 比空着更糟：
	//	空着说「还没做」，填错了说「已经守住了」。**
	//	⇒ **`自` 今天就是那样一格。**
	//
	// ⚠️ 判据取【集合成员关系】，**不是「跟上一行比」** —— 这一点要紧：
	// 别在治顺序病的这一格里，用一个看顺序的检查把病再犯一次。
	// 它抓得住写错的 `自`，也抓得住凭空填的 `自`；
	// 它**抓不住**「两行都从同一个值抬起」那件事 —— 那是分叉判据的活，留给评审那一格。
	//
	// ✅ 而它有一个**没打算要的好处**，实测出来的：它让本文件那条
	// 「手动调低的人得自己补一行来历」从一句约定变成了一条会红的规矩。
	//
	//	实测：把 `census` 从 5 手动调到 4 而【不补来历行】，再让生成器抬回去
	//	⇒ 生成器写出 `census 5 自 4`，而 `census` 名下没有任何一行的值是 4
	//	⇒ **下一次重造时当场红。**
	//
	// ⇒ 也就是说：**调低的人不补来历，账不会当场爆，但它在下一次抬高时一定爆。**
	// 这不是设计出来的，是集合成员这个判据自带的 —— 记下来，免得哪天有人以为它是巧合。
	values := map[string]map[int]bool{}
	for _, raw := range strings.Split(highWaterRaw, "\n") {
		f := strings.Fields(raw)
		if len(f) < 5 || f[0] != "#" || f[1] != "来历" {
			continue
		}
		if v, err := strconv.Atoi(f[4]); err == nil {
			if values[f[3]] == nil {
				values[f[3]] = map[int]bool{}
			}
			values[f[3]][v] = true
		}
	}
	for i, raw := range strings.Split(highWaterRaw, "\n") {
		f := strings.Fields(raw)
		if len(f) < 5 || f[0] != "#" || f[1] != "来历" {
			continue
		}
		name := f[3]
		val, err := strconv.Atoi(f[4])
		if err != nil {
			continue // 字段读不懂由 TestHighWaterProvenance 去报，这里不重复报
		}
		lines++
		s := st[name]
		if s == nil {
			s = &seen{}
			st[name] = s
		}
		if len(f) >= 7 && f[5] == "合流" {
			other, err := strconv.Atoi(f[6])
			if err != nil {
				t.Errorf("high_water.txt:%d 合流记录的「另一侧最大值」读不懂：%q\n"+
					"格式是「# 来历 <日期> <名字> <值> 合流 <另一侧的最大值> <说明>」",
					i+1, f[6])
				continue
			}
			// 合流行【自己那个值】要自洽：它是合流之后的最大值，
			// 既不能低于此前见过的最大值，也不能低于另一侧的最大值。
			// 这一行是整套机制里唯一【由人手写】的 —— 手写的那行最该自洽。
			floor := s.max
			if other > floor {
				floor = other
			}
			if val < floor {
				t.Errorf("high_water.txt:%d 合流记录自己就不自洽：值 %d，"+
					"而合流之后的最大值至少该是 %d（此前见过 %d，另一侧 %d）。\n"+
					"合流行的 <值> 填的是【合流之后】的最大值，不是某一侧的。",
					i+1, val, floor, s.max, other)
				continue
			}
			s.allow, s.hasA = other, true
			s.max = val
			continue
		}
		// `自 <前值>`：那个前值必须是同名的某一行来历的值。
		// 与顺序无关 —— 它是一条集合成员关系。
		if len(f) >= 7 && f[5] == "自" {
			frm, err := strconv.Atoi(f[6])
			if err != nil {
				t.Errorf("high_water.txt:%d `自` 后面读不懂：%q\n"+
					"格式是「# 来历 <日期> <名字> <值> 自 <前值> <说明>」", i+1, f[6])
			} else if !values[name][frm] {
				t.Errorf("high_water.txt:%d 这一行说它从 %d 抬起，"+
					"而 %q 名下【没有任何一行的值是 %d】。\n"+
					"  ⇒ 要么那个前值填错了，要么它抬起的那一行被删了。\n"+
					"  ⚠️ 这条查的是【集合成员】，与顺序无关 ——"+
					"别把它读成「跟上一行比」，那正是这一格要治的病。",
					i+1, frm, name, frm)
			}
		}
		if val < s.max && !(s.hasA && val <= s.allow) {
			t.Errorf("high_water.txt:%d 链断了：%q = %d，而此前已经见过 %d。\n"+
				"两种成因，两种改法：\n"+
				"  ① 合并把另一条分支的来历接了进来 ⇒ 在【被接进来那一行之前】插一行合流记录：\n"+
				"       # 来历 <日期> <名字> <值> 合流 <另一侧的最大值> <说明>\n"+
				"  ② 有人手动把某个数改小了却没说 ⇒ 补一行来历，写明为什么\n"+
				"⚠️ 插入是【只增】：别去改已有的那些行，diff 里不该出现减号。",
				i+1, name, val, s.max)
		}
		if val > s.max {
			s.max = val
		}
	}
	if lines == 0 {
		t.Fatal("一行来历都没读到 —— 这条守卫没在守任何东西")
	}
	t.Logf("走过 %d 行来历，%d 个名字", lines, len(st))
}

// TestHighWaterProvenance 守 tools/audit/high_water.txt 里的【来历】行。
//
// 为什么它必须存在：`rebuild_docs_test.py` 抬高水位时会自动追加一行来历，
// 而**没人核对的来历只是装饰**——一个假的来历比没有来历更危险，
// 因为它让读者以为这个数被人想过。（评审方 2026-09-08 提②时给的理由。）
//
// 判据：每个名字【最后一行】来历记的值，必须等于它当前的值。
//
//	自动抬高  ⇒ 同一趟里改数 + 追加来历 ⇒ 恒相等 ⇒ 绿
//	手动调低  ⇒ 数变了、来历没跟上     ⇒ 红，直到调的人自己补一行
//	手动调高  ⇒ 同上
//
// ⚠️ 它的射程：这条守卫【不】声称来历写的内容是真的，
// 它只保证「有人签了字，而且签的是当前这个数」。
// 机器签的字由机器保证；手签的字只保证它被写下来了。**别当成更多。**
//
// ⚠️ 具体挡不住的是这一格（评审方 2026-09-08 实测，我复现：绿）：
//
//	把当前值和最后一行来历【一起】改成一致 ⇒ 这条守卫绿。
//
// 按上面那句射程读，**它本来就该绿**：签了字，签的也是当前这个数。
// 要抓它得读 git 历史，那会让这条测试依赖仓库状态 ——
// ⇒ 所以改的是【宣称】，不是检查。（gen_quoted_rules.py 里那条：
// 要么把检查改成它宣称的样子，要么把宣称改成检查的样子；这里没有便宜的前者。）
//
// ⚠️ 而 rebuild 里那条「只增不改」也救不了这一格：它只约束【这一趟】没改动已有的
// 来历行，管不着上一趟盘上发生过什么。
// 实测（我复现评审方那一格时顺带量到的，与他的记述不同）：调低之后【下一次重造会把它
// 抬回来】，于是那条断言**确实执行了**——执行了也抓不到，因为它比对的起点
// 已经是被改过的盘面。**「那条断言没跑」和「它跑了但起点已经脏了」是两回事，
// 而它们给出同一个绿。**
//
// ⚠️ 格式判据必须与 tools/audit/rebuild_docs_test.py 的 provOf 一致：
// 两边都按【空白切开的字段】读，不用正则——少一个会分岔的地方。
// 于是 high_water.txt 里那行讲格式的「#     # 来历 <日期> …」不会被误当成来历，
// 因为它切开之后第二个字段是「#」而不是「来历」。
// **这一条不是运气：它是「用同一条判据、而不是用子串」换来的。**
func TestHighWaterProvenance(t *testing.T) {
	cur := highWater(t)
	last := map[string]int{}
	where := map[string]int{}
	for i, raw := range strings.Split(highWaterRaw, "\n") {
		f := strings.Fields(raw)
		if len(f) < 2 || f[0] != "#" || f[1] != "来历" {
			continue
		}
		if len(f) < 5 {
			t.Fatalf("high_water.txt:%d 来历行字段不够：%q\n"+
				"格式是「# 来历 <日期> <名字> <值> <说明>」", i+1, raw)
		}
		n, err := strconv.Atoi(f[4])
		if err != nil {
			t.Fatalf("high_water.txt:%d 来历行的值读不懂：%q", i+1, f[4])
		}
		last[f[3]] = n
		where[f[3]] = i + 1
	}
	if len(last) == 0 {
		t.Fatal("high_water.txt 里一行来历都没有 —— 那几个下限就成了没有出处的数")
	}
	for k, v := range cur {
		n, ok := last[k]
		if !ok {
			t.Errorf("high_water.txt 里 %q = %d，却没有任何一行来历。\n"+
				"下限是一个会挡住别人的数，它得说明自己怎么来的。\n"+
				"补一行：# 来历 <日期> %s %d <为什么是这个数>", k, v, k, v)
			continue
		}
		if n != v {
			t.Errorf("high_water.txt 里 %q 现在是 %d，而最后一行来历（第 %d 行）记的是 %d。\n"+
				"两种成因：\n"+
				"  ① 有人手动改了这个数而没补来历 ⇒ 补一行，写明为什么改\n"+
				"  ② 自动抬高那一趟改了数却没追加来历 ⇒ 那是 rebuild_docs_test.py 坏了，去查它\n"+
				"⚠️ 而这条守卫【挡不住】你改来历去迁就当前值：两边一起改成一致，它就绿了。\n"+
				"   那一步只有【读 diff 的人】看得见 —— 改一行来历必然在 diff 里留下一个减号，\n"+
				"   而「只增不改」这条性质正是靠那个减号被看见的，不是靠这条守卫。",
				k, v, where[k], n)
		}
	}
}

// TestHighWaterRuleAdjacency 断言那条合并规矩**紧挨着**来历块的第一行。
//
// ⛔ 为什么它必须存在（评审方 2026-09-09 拆开的一处，我认）：
//
//	「解冲突的人有没有【读到】那条规矩」   —— 确实不可守
//	「那条规矩有没有【在它该在的位置上】」 —— **纯文本关系，完全可守**
//
// 而 2026-09-09 刚出事的正是**可守的那一半**：我为了补一段解释，
// 把 30 行插在了规矩与来历块【之间】，两者从相邻变成相隔 31 行 ——
// 也就是把上一格的修法拆掉了，而当时没有任何东西会红。
//
//	**一条靠位置起作用的规矩，任何往它和目标之间插东西的改动，都是在拆它。**
//
// ⚠️ 而上一版我把这件事写成了「这一处没有守卫」——
// **一句自称不可守的话，会劝退本来要去写守卫的人。**
// 那句话把两件事混成了一件，而其中一半是免费可守的。
//
// ⚠️ 射程：
//
//	守的   规矩块的**最后一行**必须紧挨着第一条 `# 来历`
//	不守的 那条规矩的**内容**对不对（在一种并集顺序里机器分不出来，见文件里那张 2×2）
//	不守的 解冲突的人有没有真的读它
//
// TestMergeRecordPrecedesWhatItLicenses 断言：每条合流记录都出现在**它要许可的那一行之前**。
//
// # 这条守卫的来历，比它本身值钱
//
// 2026-09-09 我在 gfex 那一格手写第一条合流记录时**放错了位置** —— 把它放在两条分叉行
// 【之后】。测试全绿，而它其实什么都没做：
//
//	把来历行顺序倒过来测   有记录 ⇒ **FAIL**   无记录 ⇒ FAIL   ← **一模一样**
//
// 因为赦免是**读到那一行才生效**的，而它排在被许可的那一行后面 —— 那一刻它还不存在。
//
//	⇒ **一条排在被许可行【之后】的合流记录是惰性的：
//	  它读起来像保护，而在需要它的那一刻还不存在。**
//
// # 判据为什么是【位置】，不是【有没有许可到东西】
//
// 评审方先提的判据是「一条 `合流 X` 之后必须还存在值低于记录时最大值的同名行，
// 否则它是惰性的」。**那条被证伪了，反例是我这条记录本身**：
//
//	把它整个删掉 ⇒ **四个包全绿** ⇒ 它今天没有许可任何东西
//	⇒ 按那条判据它会被判成惰性，**而它是对的、必须留**
//	  （它只在「来历行顺序倒过来」那一种情形里起作用，而那取决于谁先合）
//
// ⇒ 换成**位置**判据（评审方给的替代版，我核过正反两向）：
//
//	一条 `合流 X` 记录，必须出现在【同名、值等于 X 的那一行】之前。
//
// 关键差别：**它不问「许没许可到东西」，只问「站没站对地方」** ——
// 所以**休眠但正确**的记录照样通过，而「排在被许可行之后」那种放法照样被抓。
//
// ⚠️ 射程，两条：
//
//	一、它**不**回答「赦免该不该永久」。那是 high_water.txt 里记的语义②/③，
//	  是一个真的设计决定，不是这条守卫的活。**两件事，两条守卫，别混。**
//	二、它假设「记录要许可的那一行，值恰好等于 X」。X 是另一侧的最大值，
//	  今天两条记录都成立；**若哪天那一行被别的改动挪走或改值，这条会误报** ——
//	  而误报的处置是回来看一眼，不是把守卫删掉。
//
// # 它在【多条记录】下的形状（2026-09-09 实测，七种，全部如预期）
//
// 这一格我和评审方连着几封都标着「没构造过」，现在有读数了。
// ⚠️ 第一步是**先跑一个已知为真的样本**（现状不动 ⇒ 必须绿）——
// 否则后面每一格的「红」都可能只是脚本坏了：
//
//	① 现状不动（两条记录都站对）                       ⇒ 绿 ✅ ← 已知为真的那一格
//	② 同名【三条】，各自都在自己的值行之前              ⇒ 绿 ✅
//	③ 同名三条，**中间那条**排到它的值行之后           ⇒ 红 ✅（点名那一条，不是第一条）
//	④ 两个【不同名字】各一条，其中一条站错              ⇒ 红 ✅
//	⑤ 同名两条、**X 相同**，值行在两条之后              ⇒ 绿 ✅（两条都被满足）
//	   同名两条、X 相同，值行在两条【之前】             ⇒ 红 ✅
//	⑥ 值行的**名字不同**（`合流 310` 而值 310 那行叫别的名） ⇒ 红 ✅
//
// ⇒ ⑥ 是这组里最值得留的一格：**判据是「同名 ＋ 值等于 X ＋ 在它之后」三件事，
//
//	少任何一件都不算满足。** 只按「值等于 X」找，会被另一个计数器的同值行骗过去。
//
// mergeRecordViolations 是上面那条判据的**本体**：吃一组行，吐出违规描述。
//
// ⛔ **抽成纯函数不是重构洁癖，是因为原来那版【一个条件都没在行使】**
// （评审方 2026-09-09 实测，我自己复现了）：
//
//	判据是三件事：`p.i > i`（在它之后）＋ `p.name == f[3]`（同名）＋ `p.val == other`（值等于 X）
//	把它们**逐个删掉**，再跑 `high_water.txt` ⇒ **三次全绿，真文件一次都抓不住。**
//	因为今天的文件只有两条记录、都站对 —— **它一个条件都没被行使过。**
//
// ⇒ 而本仓自己那句判语在这儿原样适用：**没被行使过的拦截，和没有拦截差不多。**
//
//	**实测过 ≠ 有对照组。**
//	前者是「那一天它是对的」，后者是「明天有人改坏它会红」。
//	那七种形状原来只写在注释里 —— 测过一次，然后被 `diff` 还原掉了。
//
// ⇒ 所以判据搬进纯函数，七种形状变成**表驱动用例**（`TestMergeRecordCriterion`），
// 从此**每次 `go test` 都跑**；而守卫本身只负责把真文件喂进来。
//
// 返回：违规描述（每条一行）＋ **扫到的合流记录条数**（前提检查用）。
func mergeRecordViolations(lines []string) (violations []string, records int) {
	type prov struct {
		i    int
		name string
		val  int
	}
	var all []prov
	for i, raw := range lines {
		f := strings.Fields(raw)
		if len(f) < 5 || f[0] != "#" || f[1] != "来历" {
			continue
		}
		if v, err := strconv.Atoi(f[4]); err == nil {
			all = append(all, prov{i, f[3], v})
		}
	}
	for i, raw := range lines {
		f := strings.Fields(raw)
		if len(f) < 7 || f[0] != "#" || f[1] != "来历" || f[5] != "合流" {
			continue
		}
		other, err := strconv.Atoi(f[6])
		if err != nil {
			continue // 格式本身由 TestHighWaterChain 管，这里不重复报
		}
		records++
		ok := false
		for _, p := range all {
			// ⛔ 三件事缺一不可，而**每一件由表里哪几格守着，是量出来的**
			// （2026-09-09：把条件逐个删掉，看 TestMergeRecordCriterion 哪几格红）：
			//
			//	`p.i > i`（在它之后）  ⇒ ③ ④ ⑤b **三格**
			//	`p.val == other`（值等于 X）⇒ ③ ⑤b **两格**
			//	`p.name == f[3]`（同名）  ⇒ **只有 ⑥ 一格** ⛔ ← 单点
			//
			// ⚠️ 评审方推测「每个条件多半只有一格在守」——**实测只有「同名」是那样**。
			// ⇒ **删掉用例 ⑥，「同名」这一件从此无人守，而且是静默的**
			//   （实测：只删 ⑥ ⇒ 全绿；⑥ 删了再删「同名」⇒ 仍全绿；⑥ 还在时删「同名」⇒ 红）。
			//   而 ⑥ 恰恰是三件里**最像多余的相等判断**的那一件。
			if p.i > i && p.name == f[3] && p.val == other {
				ok = true
				break
			}
		}
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"第 %d 行这条合流记录站错了地方：它写着「合流 %d」，"+
					"而【它之后】没有任何一行是 `%s` 且值为 %d。\n"+
					"  ⇒ 赦免是【读到这一行才生效】的；它要许可的那一行在它【前面】，"+
					"那一刻这条记录还不存在 ⇒ 【它是惰性的】。\n"+
					"  ⇒ 处置：把这一行挪到那一行【之前】（不是改它的数）。",
				i+1, other, f[3], other))
		}
	}
	return violations, records
}

func TestMergeRecordPrecedesWhatItLicenses(t *testing.T) {
	v, n := mergeRecordViolations(strings.Split(highWaterRaw, "\n"))
	for _, s := range v {
		t.Errorf("high_water.txt:%s", s)
	}
	if n == 0 {
		t.Fatal("一条合流记录都没扫到 —— 这条守卫没在守任何东西（它的前提是文件里有合流记录）")
	}
}

// TestMergeRecordCriterion 是上面那条判据的对照组，**常驻**。
//
// ⛔ **而这张表自己没有对照组 —— 删掉一格是静默的。这一层是【有意】停在这儿的。**
//
//	第一层  守卫的三个条件没有对照组   ⇒ 这张表修好了
//	第二层  **表的八格自己没有对照组** ⇒ 删一格不会红
//	第三层  给对照组做对照组           ⇒ **无限递归，不做**
//
// ⇒ 停下来的**代价**写在这儿，而不是假装没有：
// **删掉一格用例不会红；而每个条件由哪几格守着，写在 `mergeRecordViolations` 里那三行。**
// 那三行不是机器检查，它买到的是：**删用例的人在 diff 里会看见「⑥ 守的是同名那一件」** ——
// 从「静默删除」变成「删的时候有话在旁边」。
//
//	（本仓一直在做的同一件事：**不能守的部分，至少要写下它不能守。**）
//
// # 这八格分别在守什么 —— 三个方向的完整矩阵（2026-09-09 实测）
//
// ⚠️ 我原来写过一句「按【哪几格红】收，和按【每格在守哪几件】收，两者不等价」——
// **那句是错的**（评审方指出）：**一格同时行使两件，表现就是它出现在两行里。
// 它们是同一张矩阵的行与列，转置不需要再跑一次。**
//
//	方向一  判据**恒真**（过松）⇒ 红：**③ ④ ⑤b ⑥**
//	方向二  判据**恒假**（过严）⇒ 红：**① ② ③ ④ ⑤a**
//
// 转置之后每一格的职责：
//
//	① ② ⑤a   只守【过严】方向
//	③ ④       **两个方向都守**
//	⑤b ⑥      只守【过松】方向
//	⑦         **两个方向都不红**（它的身份见下）
//	⑨         也在【过严】那一侧，**而轴不同**：前面几格动的是「判据」，
//	          ⑨ 动的是「`all` 收哪些行」。实测：把 `all` 收窄成「只收非合流行」
//	          ⇒ **只有 ⑨ 红**（2026-09-09，评审方推出来的形状，我写成用例跑了）
//
// ⇒ **这张表不是「八格守三件事」，是「八格守两个方向」** ——
//
//	而 `TestMergeRecordCriterion` 末尾那条「表必须两侧都有，否则 Fatal」的前提检查，
//	守的正是**这两个方向都还有人管**。
//
// # ⑦ 到底在守什么：**任何【单点】突变都动不了它**
//
// 评审方在这一格停住了（他没找对注入点）。我沿第三个方向（「哪一行算合流记录」）做完了：
//
//	只去掉 `len(f) < 7`        ⇒ 绿 —— `f[5] != "合流"` 还把着
//	只去掉 `f[5] != "合流"`     ⇒ 绿 —— `len(f) < 7` 还把着
//	**两条一起**去掉            ⇒ **panic: index out of range [6] with length 6**
//
// ⛔ **我上一版在这里写过「⑦ 接住了那个 panic」—— 那是错的，已退回。**
//
// 评审方 2026-09-09 给了决定性对照，我自己复现了：
//
//	两条一起放松，**⑦ 还在**  ⇒ rc=1，panic，**用例编号：一个都没有**
//	两条一起放松，**删掉 ⑦**  ⇒ rc=1，panic，**完全相同**
//	⇒ **⑦ 在不在都一样，它没有接住任何东西。**
//
// 成因：`val()` 只有 6 个字段，而表里**除 ⑦ 之外每一格都含 `val()` 行**
// ⇒ 放松之后第一格（①）就 `f[6]` 越界，**整张表死在那儿，⑦ 根本没跑到**。
//
// ⛔ **而比归属更要紧的，是我读错的方式：**
//
//	**一个 panic 不是「某一格红了」—— 它是【整张表都没跑完】。**
//	输出里一个用例编号都没有，正是这个意思。
//	⇒ 拿 panic 当「某格在守」的证据，是把**测试崩了**读成**测试抓到了** ——
//	  而这两件事在 `returncode` 上长得一模一样。
//
// ⚠️ 这把上面那条 harness 规矩又推了一格：
// 我写的是「判据得是【它到底跑起来没有】」，**而这一次它跑起来了，只是没跑完。**
// ⇒ **「跑起来」和「跑完」不是一回事。** 对一张表，唯一可信的运行标志是
// **它有没有逐格给出结论**（输出里有没有用例编号）—— 不是 `rc`，也不是有没有 `FAIL` 字样。
//
// ⇒ ⑦ 的当前状态，如实写：
//
//	它在迄今造过的**三个方向**上都不红（过松 / 过严 / 识别条件放松），
//	**也没有任何组合突变能让它红**。
//	而那**不等于它没用**，只等于 —— **我们还没找到它守的是什么。**
//	⇒ **状态：未验；且已经排除了一个候选答案（那个 panic 不是它）。**
//
// # ⚠️ 而收这张矩阵的过程本身值得记：同一个脚本错了三次，三次长得一模一样
//
//	① `grep` 模式里带全角冒号 ⇒ 跨进程后不匹配 ⇒ 输出**空**
//	② 恒真/恒假写成 `if true` ⇒ 循环变量 `p` 未使用 ⇒ **编译不过**，
//	   而我的「build 失败」检测找的是 `cannot`/`undefined`，Go 报的是 `declared and not used`
//	③ 两条识别条件一起放松 ⇒ **panic**，而我的正则只认断言行 ⇒ 又是**空**
//
//	⇒ **三次的外观完全一样：「红的格：（无）」。**
//	  **一个失败样子恒为「无」的验证器，和一个真的负结果分不开。**
//	  ⇒ 判据不能是「输出里有没有某几个词」，得是**「它到底跑起来没有」**
//	    （returncode ＋ 有没有出现 `--- FAIL` / `ok` 这类**运行过**的标志）。
//
// ⚠️ 用例里的行全部是**字面写死的**，不从 `high_water.txt` 派生 ——
// 否则真文件一变，这些用例就跟着变，那就又回到「实测过一次」那种状态。
func TestMergeRecordCriterion(t *testing.T) {
	rec := func(name string, val, x int) string {
		return fmt.Sprintf("# 来历 2026-09-09 %s %d 合流 %d 说明", name, val, x)
	}
	val := func(name string, v int) string {
		return fmt.Sprintf("# 来历 2026-09-09 %s %d 自动：说明", name, v)
	}
	cases := []struct {
		name  string
		lines []string
		want  int // 期望的违规条数
	}{
		{"① 一条，站对（已知为真的那一格）",
			[]string{rec("rules", 300, 271), val("rules", 271)}, 0},
		{"② 同名三条，各自都在自己的值行之前",
			[]string{rec("rules", 400, 310), val("rules", 310),
				rec("rules", 410, 320), val("rules", 320),
				rec("rules", 420, 330), val("rules", 330)}, 0},
		{"③ 同名三条，中间那条排到它的值行之后",
			[]string{rec("rules", 400, 310), val("rules", 310),
				val("rules", 320), rec("rules", 410, 320),
				rec("rules", 420, 330), val("rules", 330)}, 1},
		{"④ 两个不同名字各一条，其中一条站错",
			[]string{rec("anchors", 400, 310), val("anchors", 310),
				val("census", 320), rec("census", 410, 320)}, 1},
		{"⑤a 同名两条、X 相同，值行在两条之后",
			[]string{rec("rules", 400, 310), rec("rules", 410, 310), val("rules", 310)}, 0},
		{"⑤b 同名两条、X 相同，值行在两条之前",
			[]string{val("rules", 310), rec("rules", 400, 310), rec("rules", 410, 310)}, 2},
		{"⑥ 值行的【名字不同】—— 不该被当成满足",
			[]string{rec("rules", 400, 310), val("census", 310)}, 1},
		{"⑦ 没有合流记录 ⇒ 零违规，而 records 也是 0（前提检查归守卫）",
			[]string{val("rules", 310), val("rules", 320)}, 0},
		// ⑨ 是评审方 2026-09-09 手推八格期望值时撞出来的一个**空位**：
		//
		//	`all` 收的是**所有**能解析出值的来历行 —— **合流记录自己也在里面**（它的值是 `f[4]`）
		//	⇒ 一条 `合流 X` 记录，可以被【另一条合流记录】满足，只要那条记录的**值**恰好是 X
		//	⇒ 而原来八格里**没有一格**走过这条路
		//	  （⑤a 看起来像，其实不是：`rec@1` 的值是 410，满足 `rec@0` 的是 `val@2`）
		//
		// ⚠️ **这个语义是对的**（「那个名字后面确实出现过值为 X 的一行」），
		// 而**它没有用例 ⇒ 它是「今天碰巧对」，不是「被守着」** —— 正是本仓一直在分的那条界线。
		//
		// ⇒ 它属于【过严】那个方向：哪天有人把 `all` 收窄成「只收非合流行」，**只有 ⑨ 会红**。
		{"⑨ 一条记录被【另一条记录】满足（合流记录也进 all）",
			[]string{rec("rules", 300, 271), rec("rules", 271, 250), val("rules", 250)}, 0},
	}
	for _, c := range cases {
		v, n := mergeRecordViolations(c.lines)
		if len(v) != c.want {
			t.Errorf("%s：违规 %d 条，要 %d 条\n  实得：%v", c.name, len(v), c.want, v)
		}
		if c.want > 0 && n == 0 {
			t.Errorf("%s：判出了违规，而扫到的记录数却是 0 —— 计数与判据对不上", c.name)
		}
	}
	// ⛔ 前提：这张表自己必须**同时覆盖两侧**。只有「该过」的用例，等于把门拆了。
	pass, fail := 0, 0
	for _, c := range cases {
		if c.want == 0 {
			pass++
		} else {
			fail++
		}
	}
	if pass == 0 || fail == 0 {
		t.Fatalf("这张表只有一侧的用例（该过 %d ／ 该拒 %d）—— 单侧的表不是对照组", pass, fail)
	}
}

func TestHighWaterRuleAdjacency(t *testing.T) {
	lines := strings.Split(highWaterRaw, "\n")
	first := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "# 来历 ") {
			first = i
			break
		}
	}
	if first <= 0 {
		t.Fatal("high_water.txt 里找不到来历块 —— 这条守卫没在守任何东西")
	}
	// ⛔ **窗口取 3 行。它【曾经】有一个机制推导，而那个推导被实测否掉了 —— 原文留在下面，
	// 因为一个被推翻的理由必须和它的反证一起读，否则下一个人会把它重新发明一遍。**
	//
	//	原文：「这个 3 是 `git diff` 默认的上下文行数。这条规矩靠【解冲突的人在冲突
	//	hunk 里看得见它】起作用，而 hunk 带的正是 3 行上下文 ⇒ 超出 3 行它就不在那个人眼前了。」
	//
	// ⛔ **反证（评审方 2026-09-09 按计划顺序实走，我自己在分支上另量了一遍，结论一致）：**
	//
	//	评审方（合并后的树）   冲突离 marker **+16 / −163** 行
	//	我（本分支的结构）     marker 到来历块【尾】**19** 行；到值区块 **−157** 行
	//	⇒ **没有任何一次真实冲突落在这 3 行窗口里。**
	//
	// 而原因是结构性的，不是运气：
	//
	//	rebuild_docs_test.py 里是 `hwLines.append(...)` ⇒ 新来历行追加到【文件末尾】
	//	⇒ 冲突永远发生在来历块【尾部】；而 marker 锚在来历块【首部】
	//	⇒ 两者之间永远隔着**整个来历块**，而那个块**只会变长**（今天 17 行）。
	//
	// ⇒ **所以这个 3 今天守的是【规矩块自己不被撑开】，不是【它出现在冲突 hunk 里】。**
	//   而失去那个推导之后，**3 就变回了一个约定，不再是一个推出来的数** —— 这一点必须写明，
	//   否则它读起来仍像有依据。
	//
	// ⛔ **余量是 0，不是 1**（2026-09-09 实测：在 marker 与来历块之间插一行 ⇒ 当场红）：
	//
	//	「中间隔着几行」    186 − 183 − 1 = **2**   ← 评审方和我上一封报的都是这个
	//	守卫自己的算法      first − found = **3** = ctx 上限 ⇒ **余量 0**
	//	⇒ **两个人报了同一个错的数，因为报的是【另一种数法】。**
	//	  一个阈值的余量，只能按【判据自己的算术】数。
	//
	// ⇒ **真正的修法是换锚点，那是单独一格**（评审方提的，我认）：
	//
	//	让生成器把新来历行插在一个【尾部哨兵】之前（现在是 append 到最后），
	//	守卫改成「哨兵必须在最后一条 `# 来历` 之后 3 行内」
	//	⇒ `-U3` 那条推导**第一次真正成立**；而且规矩块想写多长写多长，
	//	  因为窗口不再夹在规矩块和来历块之间 ⇒ **余量 0 这个问题一起消失。**
	//	⚠️ 它要同时动生成器与本守卫，还会改 high_water.txt 的格式 ⇒ **单独一格、单独评审。**
	//
	// ⚠️ 上一版写的是「必须在【紧邻的上一行】」，而那太脆：规矩本身是多行的，
	// 给它补一句话就会把 marker 推离末行。
	// **我在写这一格的过程中，自己把它推离了三次** —— 前两次没有守卫、事后才发现，
	// 第三次是这条守卫当场抓的。
	//
	//	**一条我在【为它写守卫的同时】还犯了三次的错，
	//	正是「这一处不用守」这个判断最不该被相信的证据。**
	const marker = "你现在正在读它"
	const ctx = 3 // git diff 默认上下文行数
	lo := first - ctx
	if lo < 0 {
		lo = 0
	}
	found := -1
	for i := lo; i < first; i++ {
		if strings.Contains(lines[i], marker) {
			found = i
			break
		}
	}
	if found < 0 {
		t.Fatalf("那条合并规矩不在来历块首行之前的 %d 行里。\n"+
			"  来历块首行是第 %d 行；它上面那 %d 行是：\n    %s\n"+
			"  ⇒ 多半是有人往【规矩与来历块之间】插了东西。\n"+
			"  ⇒ 那条规矩靠位置起作用：合并冲突的 hunk 只带 %d 行上下文，"+
			"【插进来就是把它挤出那个人的视野】。\n"+
			"  ⇒ 要加解释就加在规矩【之上】，别加在它和来历块之间。",
			ctx, first+1, ctx, strings.Join(lines[lo:first], "\n    "), ctx)
	}
	t.Logf("合并规矩在第 %d 行，来历块首行在第 %d 行 —— 相隔 %d 行（上限 %d）",
		found+1, first+1, first-found-1, ctx)
}

// —— 状态断言：文档说的「还没做」，必须和仓库里量得到的事实一致 ——
//
// 起因（2026-09-09）：README、contract.md §〇、design.md 排期表**三处**都写着
// 「代码尚未开始 / 设计中」，而那时 `v0.1.0` 与 `v0.2.0` 早已发布、库代码两千余行。
//
//	这个仓库有一百多个测试在守【文档里的规矩】，
//	**却没有一个在守【文档对自己的状态描述】。**
//
// ⇒ 而状态句正是最容易过期的一类：它在写下的那一刻为真，
// 之后**每一次交付都在削弱它，而没有任何一次交付会经过它。**
// （同族见 memory 里那条「状态声明伪装成记账」——读起来像记账，所以没人问它要证据。）

// libraryGoFiles 返回【库代码】的文件数与总行数。
//
// 范围按【性质】划，不按目录清单划：**非 `_test.go`、且不在 `tools/` 下的 `.go`**。
// 这两条讲的都是「这个文件属于哪一层」，不是「它现在放在哪个目录」——
// 所以新增一个包不用改这条守卫，而把库代码搬进 `tools/` 会被算成不是库（那也是对的）。
func libraryGoFiles(t *testing.T) (files, lines int) {
	t.Helper()
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		q := filepath.ToSlash(p)
		if !strings.HasSuffix(q, ".go") || strings.HasSuffix(q, "_test.go") {
			return nil
		}
		if strings.HasPrefix(q, "tools/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		lines += strings.Count(string(b), "\n") + 1
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败：%v", err)
	}
	return files, lines
}

// noCodeYetClaims 是【整仓级】的「还没有代码」措辞，登记在这里。
//
// ⚠️ 为什么是清单而不是模式：这三条是**实际出现过并且实际过期了**的原话。
// 一个新发明的说法（「尚在纸面上」之类）不在射程内 —— 写下来，别假装它在。
var noCodeYetClaims = []string{
	"任何一行库代码",
	"代码尚未开始",
	"一条都还不存在",
}

// hasLibraryGo 判断一个路径下（含子目录）有没有【非测试】的 `.go`。
// 路径本身就是一个 `.go` 文件时，直接看它是不是测试文件。
func hasLibraryGo(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
	}
	found := false
	_ = filepath.Walk(path, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		q := filepath.ToSlash(p)
		if strings.HasSuffix(q, ".go") && !strings.HasSuffix(q, "_test.go") {
			found = true
		}
		return nil
	})
	return found
}

var backtickPath = regexp.MustCompile("`([^`]+)`")

// TestStatusClaimsMatchRepo 守两条，都只认【表格行】，不认散文。
//
// ⚠️ 「只认表格行」不是取巧，是**一处已经存在的假阳性**逼出来的：
// contract.md §〇 现在有一段专门解释「它曾经写着『任何一行库代码 ❌ 尚未开始』」——
// **一个朴素的子串禁令，会打中解释它自己的那段话。**
// 而过期的那一条当时【是一个表格行】，解释是散文 ⇒ 行首 `|` 把两者分得干净。
// （同一个形状在 `TestGuardsDoNotSkipThemselves` 里出现过：那次用 go/ast 把注释排除掉。）
//
//	一、整仓级：任何 `.md` 的表格行里，不得出现 noCodeYetClaims 里的措辞
//	           —— 除非库代码真的是 0。
//	二、包级：  contract.md §〇 状态表里【含斜杠的反引号路径】，
//	           标 ✅ ⇒ 那条路径下必须有非测试 `.go`；标 ❌ ⇒ 必须没有。
//
// ⚠️ 射程（照例写明它不比什么）：
//
//	堵的     表格行里的整仓级「还没有代码」措辞；§〇 里【带路径】的 ✅/❌ 行
//	不堵的   散文里的同类说法 —— 结构上分不开，见上面那处假阳性
//	不堵的   不含斜杠的 token（`Syncer` `Feed` `refdata`）——它们是类型名或未定形状，
//	         「存在」在文件系统上没有对应物；要堵得先给它们一个可测量的定义
//	不堵的   ✅ 行上的**行数/测试数**（833 行、43 个测试）—— 那是数，会自己变旧，
//	         而给每个数配一台机器的代价大于它挡住的错。**写下来，别假装它被守着。**
func TestStatusClaimsMatchRepo(t *testing.T) {
	files, lines := libraryGoFiles(t)

	// 先验守卫自己的前提：库代码为 0 时规则一恒真 ⇒ 这条守卫会变成一个不可能红的断言。
	if files == 0 {
		t.Fatalf("量到 0 个库代码文件 —— 要么判据打偏了，要么这个仓库真的空了。\n"+
			"  两种情况下这条守卫的规则一都【结构上不可能红】，所以它必须自己先报出来。\n"+
			"  （判据：非 _test.go、且不在 tools/ 下的 .go；量得 files=%d lines=%d）", files, lines)
	}

	// ── 规则一：整仓级 ──
	var mdFiles []string
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".md") {
			mdFiles = append(mdFiles, filepath.ToSlash(p))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 .md 失败：%v", err)
	}
	if len(mdFiles) == 0 {
		t.Fatal("一份 .md 都没扫到 —— 规则一在空集上恒绿，先修判据")
	}

	rows := 0
	for _, f := range mdFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s：%v", f, err)
		}
		for i, ln := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(ln), "|") {
				continue
			}
			rows++
			for _, claim := range noCodeYetClaims {
				if strings.Contains(ln, claim) {
					t.Errorf("%s:%d 的表格行里写着「%s」，\n"+
						"  而仓库里量到 %d 个库代码文件、%d 行。\n"+
						"  ⇒ 这句状态断言过期了。要么改它，要么这条守卫的判据错了。\n"+
						"  行：%s", f, i+1, claim, files, lines, strings.TrimSpace(ln))
				}
			}
		}
	}
	if rows == 0 {
		t.Fatal("一行表格都没扫到 —— 规则一在空集上恒绿，先修判据")
	}

	// ── 规则二：contract.md §〇 里带路径的 ✅ / ❌ ──
	b, err := os.ReadFile(filepath.Join("docs", "contract.md"))
	if err != nil {
		t.Fatalf("读 docs/contract.md：%v", err)
	}
	all := strings.Split(string(b), "\n")
	lo, hi := -1, len(all)
	for i, ln := range all {
		if lo < 0 && strings.HasPrefix(ln, "## 〇") {
			lo = i
			continue
		}
		if lo >= 0 && strings.HasPrefix(ln, "## ") {
			hi = i
			break
		}
	}
	if lo < 0 {
		t.Fatal("docs/contract.md 里找不到 `## 〇` 那一节 —— 规则二无处可扫，恒绿")
	}

	checked := 0
	for i := lo; i < hi; i++ {
		ln := all[i]
		if !strings.HasPrefix(strings.TrimSpace(ln), "|") {
			continue
		}
		// 🚧「进行中」也算【已经有东西了】——不然写一个 🚧 就能绕过这条守卫，
		// 而那正是本仓驳回过的那种「按名字生效的洞」，只是换成了按符号。
		// （这一条是守卫上线当天自己撞出来的：source/ 从「没有」变成「有一半」，
		//  而表里当时只有 ✅/❌ 两格可选。）
		done := strings.Contains(ln, "✅") || strings.Contains(ln, "🚧")
		notYet := strings.Contains(ln, "❌")
		if done == notYet { // 两个都有或都没有 ⇒ 不是一条状态行
			continue
		}
		for _, m := range backtickPath.FindAllStringSubmatch(ln, -1) {
			raw := m[1]
			// ⚠️ 先判斜杠、再剥通配 —— 顺序反了的话 `source/*` 剥成 `source`，
			// 就会被下面这条「不含斜杠 ⇒ 射程之外」判出去，**而它恰恰是最该核的一行**。
			// 这个洞是对照组抓到的：把 `source/*` 标成 ✅，守卫不红。
			if !strings.Contains(raw, "/") {
				continue // 射程之外：见上面那条说明
			}
			tok := strings.TrimSuffix(raw, "/*")
			checked++
			exists := hasLibraryGo(tok)
			if done && !exists {
				t.Errorf("docs/contract.md:%d 把 `%s` 标成 ✅，"+
					"而那条路径下没有任何非测试 .go。\n  行：%s", i+1, tok, strings.TrimSpace(ln))
			}
			if notYet && exists {
				t.Errorf("docs/contract.md:%d 把 `%s` 标成 ❌【尚未开始】，"+
					"而那条路径下已经有非测试 .go 了。\n  行：%s", i+1, tok, strings.TrimSpace(ln))
			}
		}
	}
	if checked == 0 {
		t.Fatal("§〇 里一条带路径的状态行都没扫到 —— 规则二在空集上恒绿，先修判据")
	}

	t.Logf("库代码 %d 文件 / %d 行；扫过 %d 个 .md 的 %d 行表格；§〇 里核了 %d 条带路径的状态",
		files, lines, len(mdFiles), rows, checked)
}

// —— 印出来的消息里不许有 Markdown 的 `**` ——
//
// 这条规矩 2026-09-09 被【执行过两次、写下来零次】：
// 一次是评审方扫出两处，一次是我重扫又清掉两处。**而它至今没有守卫**，
// 于是第三次照样会长出来 —— 本仓自己的话：**记成事实的规矩不会被想起。**
//
// 判据（从那次提交里取回来的，不是凭印象重编）：
//
//	会被打印        必要条件：在 Errorf/Fatalf/Logf/Printf 一类调用里
//	被当成什么读    还要问这一句 —— 终端不渲染 Markdown，`**` 原样打出来是噪声
//
// ⛔ **而例外有两格，且【理由不同】，所以分开写、各带一句话**：
// 合成一个清单会让下一个人以为它们是同一种豁免。
//
// ⚠️ 上一次扫这件事的分类器**只看单行**，于是跨行拼接的调用整个隐形
// （实测：单行判据得 1 处，跨行判据得 10 处 —— 差的 9 处全在续行上）。
// **本测试按括号配平找整个调用**，这一格就是它存在的第一个理由。
//
// ⛔ **而本测试自己也有一个射程洞，写下来免得被当成「已经守住了」**：
// 它只看【打印调用括号里】的字面量。**先赋给变量、后打印的消息，它看不见。**
//
//	tools/probe/shinny/main.go:947  note = "…**换过品种…**…"  ⇒ 本测试【不报】
//	tools/probe/shinny/main.go:1202 同一句话直接进 Fprintf     ⇒ 本测试【报】
//
// **同一句话，两个写法，一个被守一个不被守。** 947 那处是我在修 1202 时
// 手工撞见的（替换命中数是 2 而不是 1），不是它报出来的。
// ⇒ 没有把射程扩到「全部字面量」，是因为那一版量过：docs_test.go 里有
// **265 处规矩文本数据**会一起进来，而它们不是消息。
// **两种射程都不对，而这一种的错法是【漏报】** —— 写在这儿，好让下一个人
// 知道「全绿」覆盖不到哪里。
var emphasisExempt = []struct{ file, needle, why string }{
	{
		"docs_test.go", "含 `**` 的行数",
		"这条消息在【谈论 `**` 本身】：它报的就是载体里含 `**` 的行数。" +
			"把它清掉，这条守卫就没法说出自己在数什么",
	},
	{
		"store/segfile/coverage_test.go", "**空 —— <凭什么还没做>**",
		"这对 `**` 是【被引用的模板】的一部分：design.md 第五列实际就写成 " +
			"`**空 —— …**`（2026-09-09 实测 6 处）。清掉它，这条消息" +
			"就在教人写一个【错的】模板 —— 而这一格原来的判据里没有",
	},
}

// splitGoLine 把一行 Go 源码切成【代码】与【字面量】两半。
//
// 两半各有用处，而**用错哪一半，就各出一种错**——两种都是实测出来的：
//
//	代码半   数括号用。字面量里的 "(" 不能算：
//	         `fmt.Sprintf("%s括在 (%s, %s] 内…")` 只有左括号 ⇒ 配平永远不收敛
//	         ⇒ span 一路吃到下一个 ")"，把它后面【40 行注释】读成了这条消息的一部分
//	         ⇒ 报出 8 处，其中 5 处是注释里的 `**`（假指控）
//	字面量半 找 `**` 用。注释里的 `**` 是本仓的行文体例，**不该报** ——
//	         而按整行找的话，紧跟在调用后面的注释会被算进去
//
// ⇒ **一个「整行」的判据同时犯了这两个错，而它们指向相反的方向**
// （一个多报、一个会少报）。分成两半之后，两边各自只回答自己那一问。
func splitGoLine(s string) (code, lits string) {
	r := []rune(s)
	var c, l []rune
	for i := 0; i < len(r); i++ {
		if r[i] == '/' && i+1 < len(r) && r[i+1] == '/' {
			break // 行注释：两半都到此为止
		}
		if r[i] == '"' || r[i] == '`' {
			q := r[i]
			for i++; i < len(r); i++ {
				if q == '"' && r[i] == rune(92) { // 反斜杠转义，跳过下一个字符
					i++
					continue
				}
				if r[i] == q {
					break
				}
				l = append(l, r[i])
			}
			continue
		}
		c = append(c, r[i])
	}
	return string(c), string(l)
}

// TestNoMarkdownEmphasisInPrintedMessages 见上。
func TestNoMarkdownEmphasisInPrintedMessages(t *testing.T) {
	// ⚠️ 动词表是量出来的，不是列出来的（2026-09-09 全仓实测）：
	//
	//	errors.New / fmt.Errorf   各 0 处  ⇒ 加进来【今天不花钱】，而它们的字串照样会被读到
	//	fmt.Sprintf               6 处     ⇒ 「先拼好、后打印」那一族里，它是抓得住的一半
	//
	// ⇒ 判据不是「这是不是一个打印函数」，是【这个字串会不会被人读到】。
	//
	// ⛔ **而命中 0 的动词【不该】被清理掉 —— 它和幽灵豁免方向【正好相反】**
	// （评审方 2026-09-09；写在这儿是因为下一个人最可能「顺手清理没命中的动词」）：
	//
	//	幽灵【豁免】      留着 ⇒ **静默加宽规矩**   ⇒ 该报（下面那条自净检查干的就是这个）
	//	命中 0 的【动词】  留着 ⇒ **预置射程**       ⇒ **不该报**
	//
	// ⇒ 判据：**给一张表加自净检查之前，先问「这张表长大是危险还是安全」。**
	// 豁免表长大 = 规矩变松；动词表长大 = 射程变宽。**方向反了就别把那一手抄过来。**
	printCall := regexp.MustCompile(
		`\b(t\.(Errorf|Fatalf|Logf|Error|Fatal|Skipf)` +
			`|fmt\.(Printf|Println|Fprintf|Sprintf|Errorf)` +
			`|errors\.New)\(`)

	files, spans, used := 0, 0, map[int]bool{}
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if b := filepath.Base(p); b == ".git" || b == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		rel := filepath.ToSlash(p)
		lines := strings.Split(string(b), "\n")
		for i := 0; i < len(lines); i++ {
			code, _ := splitGoLine(lines[i])
			if !printCall.MatchString(code) {
				continue
			}
			// 按括号配平找整个调用 —— 跨行拼接的消息全在这里面。
			// **数的是代码半**，理由见 splitGoLine。
			depth, j := 0, i
			var lits []string
			for ; j < len(lines); j++ {
				c, l := splitGoLine(lines[j])
				depth += strings.Count(c, "(") - strings.Count(c, ")")
				lits = append(lits, l)
				if depth <= 0 {
					break
				}
			}
			spans++
			whole := strings.Join(lits, "\n")
			if !strings.Contains(whole, "**") {
				i = j
				continue
			}
			// ⛔ 豁免要和规矩【同粒度】。
			//
			// 上一版是 `Contains(whole, needle) ⇒ 整段放行`，而 whole 是【整个调用】：
			// 一个 t.Errorf 里有两处 `**`，豁免命中其中一处，**另一处一起被放行**。
			// 实测（评审方 2026-09-09 造的对照组，我复现一致）：
			// 把 coverage_test.go 那条真违规单独退回违规写法 ⇒ 守卫 **ok，一个字没报**
			// —— 而那一处正是【这颗提交自己手工清掉的】。
			//
			// ⇒ 改成【先删再查】：把豁免的原文（连它自己那对 `**`）从 whole 里去掉，
			// 剩下的还有 `**` 就照报。
			// **一条粗一格的豁免，是按【容器】开的口子，不是按【那一处】开的。**
			rest := whole
			for n, e := range emphasisExempt {
				if strings.HasSuffix(rel, e.file) && strings.Contains(rest, e.needle) {
					used[n] = true
					rest = strings.ReplaceAll(rest, e.needle, "")
				}
			}
			if strings.Contains(rest, "**") {
				t.Errorf("%s:%d 这条消息里有 Markdown 的粗体标记（两个星号）——"+
					"终端不渲染它，原样打出来是噪声。\n  %s\n"+
					"  ⇒ 要强调用【】；真的必须留（在谈论这个标记本身，"+
					"或在引用一段会被 Markdown 渲染的模板）⇒ 加进 emphasisExempt，"+
					"并写清【是哪一种】",
					rel, i+1, strings.TrimSpace(lines[i]))
			}
			i = j
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// ⛔ 底线：什么都没扫到，不许读成「干净」。
	// 与 tools/doccheck 那一格同族（空输出 ⇒ 假 PASS）。
	if files == 0 || spans == 0 {
		t.Fatalf("扫过 %d 个 .go、%d 个调用——【读到 0 不是通过】："+
			"要么 Walk 的起点不对，要么 printCall 认不出它的形状", files, spans)
	}

	// ⛔ 豁免的原文里【必须自己带着那两个星号】——否则「先删再查」删不掉任何东西，
	// 这条豁免就是一个**一声不响的空操作**：它在表里、它命中、而它什么也没豁免。
	// （上一版的两条 needle 都不带 `**`，靠的是「命中就整段放行」那个粗粒度；
	// 粒度修好之后，不带 `**` 的 needle 会安静地失效 —— 所以这一条要和它同时落地。）
	for _, e := range emphasisExempt {
		if !strings.Contains(e.needle, "**") {
			t.Errorf("emphasisExempt 的 needle 里【没有】那两个星号：%s / %q\n"+
				"  ⇒ 先删再查删不掉东西 ⇒ 这条豁免是空操作，而它看起来在工作", e.file, e.needle)
		}
	}

	// 幽灵豁免：一条豁免不再命中任何东西，就该删。
	// 留着它会静默加宽这条规矩 —— 同 ruleAnchors 的幽灵项那一格。
	for n, e := range emphasisExempt {
		if !used[n] {
			t.Errorf("emphasisExempt 里有一条【幽灵项】：%s / %q 已经不匹配任何消息了。\n"+
				"  ⇒ 删掉它。留着等于给这条规矩开一个没有对象的口子", e.file, e.needle)
		}
	}
	t.Logf("扫过 %d 个 .go 里的 %d 个调用，豁免 %d 条全部命中", files, spans, len(emphasisExempt))
}

// ⚠️ **这是一条【代码】守卫，却住在一份叫 docs_guards 的文件里** —— 写下理由，
// 免得下一个人「顺手归位」把它搬走：
//
//	生成器抽 guardNames 的来源是 `staticTpl + docs_guards_test.go`，**那是一个【位置】**
//	⇒ 放进别的 _test.go：它【不会进 guardNames】，**删掉它不会有任何东西响**
//	⇒ 本文件下方那条约定写得很死：**手写守卫一律写进 docs_guards_test.go**
//
// 我第一版正是放在了 `errors_test.go` 里（读起来更整齐），
// **而那一版是一条没有登记的守卫** —— 量了一遍 guardNames 的来源才发现。
// ⇒ **「按文件名归类」在这里和「被守住」冲突，取后者。**

// —— 根包的错误哨兵集合 ——
//
// 这一条守的不是「哨兵对不对」，是**「这个集合有没有变大」**。
//
// ⛔ 它的由来是一句验不了的话（design.md 七之零，2026-09-09）：
// `Syncer` 的缺口分类要穷举「一天可能得到哪些答案」，而**穷举性是【接口】的性质**——
// `Store` 接口还不存在 ⇒ 那句「一共就这六类」**今天无处可验**。
//
//	⇒ 判据（评审方给的形式）：**穷举性验不了的时候，退一步验「集合有没有变大」。**
//	  后者今天就能做，因为那几个哨兵**全都住在根包**。
//
// ⚠️ 而这条守卫真正要逼出来的**不是名字，是一个决定**：
// 加一个哨兵 ⇒ 必须回答「**它在缺口分类里落在哪一格**」——
// 第几类缺口？还是那个不封闭的兜底（「坏了」）？
// **不回答就红。** 因为一个新哨兵最容易的下场，是被折进某个已有的分支里。
//
// 体例照 `periodImplementors` 那一手：**左边扫源码，右边手写**，两半各补对方的盲区。
type sentinelDisposition struct {
	Kind GapKind // 0 = 这个哨兵【不进缺口分类】
	Why  string
}

var errorSentinelDisposition = map[string]sentinelDisposition{
	"ErrNotTradingDay":  {GapNotTrading, "日历知道，而那天不交易"},
	"ErrUncovered":      {GapCalendarUnknown, "日历【答不了】，不是「没有交易」"},
	"ErrSpanUnverified": {GapStoreUnverified, "存储答不了·未走查；瞬时，走一遍就行"},
	"ErrLegacyMeta":     {GapStoreLegacy, "存储答不了·旧格式；必须问人"},

	// ⚠️ ErrClosed 是唯一一个【不进缺口分类】的：它回答的是「这一【时刻】在不在时段内」，
	// 而缺口分类问的是「这一【天】要不要拉」。两者不同维度 ——
	// 写在这儿而不是省略，是因为**「它不属于那个分类」本身就是一个要被写下来的决定**。
	"ErrClosed": {0, "不进缺口分类 —— 它是【时刻】级的答案，而缺口是【交易日】级的"},

	// ⚠️ ErrBudgetExhausted 同样【不进缺口分类】，而理由与 ErrClosed 不同，
	// 所以写成两句，不合并：
	//
	//	ErrClosed          维度不同（时刻 vs 交易日）
	//	ErrBudgetExhausted **层级不同** —— 它回答「这次同步为什么停」，
	//	                   而缺口分类回答「这一天要不要拉」
	//
	// ⇒ 预算耗尽之后，没拉到的那些天仍然是 GapNeverFetched ——
	// 而那一格由【它们不在任何 Span 里】给出，**不由这个错误给出**。
	// **一次中止不改变任何一天的可答性，它只是让我们不再去问。**
	"ErrBudgetExhausted": {0, "不进缺口分类 —— 它回答「为什么停」，不回答「这一天要不要拉」"},
}

// gapKindsNotFromSentinel 是【不由任何哨兵映射来】的那几类。
//
// 它们的真值来自 coverage 的「在不在某个 Span 里」，那不是一个错误值。
// ⇒ 登记在这儿，是为了让下面那条断言能说出一句完整的话：
// **每一个 GapKind，要么由某个哨兵映射来，要么在这张表里** ——
// 于是**加第七类时，必须先回答它属于哪一边**。
var gapKindsNotFromSentinel = map[GapKind]string{
	GapNeverFetched:   "这一天不在任何 Span 里 —— 是一个【缺席】，不是一个错误值",
	GapConfirmedEmpty: "在某 Span 里、那一段走查过、而 .dat 里没有它 —— 同样是缺席",
}

// rootErrorSentinels 扫【根包的源码】，找出所有包级、导出、名字以 Err 开头的哨兵。
//
// 判据按性质划，不按文件：包内任何地方加一个，它都看得见。
// 范围排除 _test.go —— 测试里的哨兵不是对外契约。
//
// ⛔ **射程：它认的是【var 声明】＋ 名字以 `Err` 开头 ＋ 导出。三样都要。**
// 走 go/ast 的 GenDecl/ValueSpec ⇒ `var ErrX = …` 与 `var ( ErrX = … )` **两种写法都逮得到**。
// **而照不到的是【自定义错误类型】** —— 一个 `func (e xxxError) Error() string`
// 同样是这个包对外交出的一种「答不了/出错了」，而它：
//
//	不是 var          ⇒ 判据的第一样就不满足
//	不必叫 Err 开头   ⇒ Go 的惯例恰恰是 `XxxError`，**第二样也不满足**
//
// 今天不花钱（2026-09-09 实测，两条独立的路）：
//
//	根包 `func (…) Error() string`  **0 处**  ⇒ 没有自定义错误类型
//	`func Err…` / `type Err…`       各 **0 处**
//	`go doc -all .` 抽出的 Err*     **5 个**，与本扫描器逐名相同
//
// ⚠️ **不扩判据，只写下射程** —— 理由是本仓那条：**沉默的断言只会被继承。**
// 扩到「所有实现 error 的东西」要另做一套（认接口，不认名字），
// 而今天它的对象是空集：**一条守着空集的判据，红不了，也证明不了什么。**
// ⇒ 真出现第一个自定义错误类型那天，这段话就是那时该读的东西。
func rootErrorSentinels(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读目录：%v", err)
	}
	fset := token.NewFileSet()
	var out []string
	files := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", e.Name(), err)
		}
		if f.Name.Name != "tickflow" {
			continue
		}
		files++
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, s := range gd.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if strings.HasPrefix(n.Name, "Err") && n.IsExported() {
						out = append(out, n.Name)
					}
				}
			}
		}
	}
	// ⛔ 底线：一个文件都没解析到 ⇒ 这条在空集上恒绿。
	// 与 tools/doccheck 那一格同族：**读到 0 不许读成「干净」。**
	if files == 0 {
		t.Fatal("一个本包 .go 都没解析到 —— 这条在空集上恒绿，先修判据")
	}
	sort.Strings(out)
	return out
}

// TestRootErrorSentinelsAreDisposed 盯住「根包的错误哨兵集合没有悄悄变大」。
//
// ⚠️ 它**不能**代替「缺口一共就这几类」那句话 —— 那句要等 `Store` 接口定型。
// 这一条只保证：**集合变了，有人会知道**。两者的关系写在文件头。
func TestRootErrorSentinelsAreDisposed(t *testing.T) {
	found := rootErrorSentinels(t)
	if len(found) == 0 {
		t.Fatal("根包一个 Err* 哨兵都没扫到 —— 要么它们搬走了，要么判据打偏了")
	}
	have := map[string]bool{}
	for _, n := range found {
		have[n] = true
		if _, ok := errorSentinelDisposition[n]; !ok {
			t.Errorf("根包新增了一个错误哨兵而没有写它的处置：%s\n"+
				"  ⇒ 在 errorSentinelDisposition 里加一行，回答【它在缺口分类里落在哪一格】：\n"+
				"     是第几类缺口？还是那个不封闭的兜底（读坏了）？还是不属于这个分类？\n"+
				"  ⇒ 别只补名字 —— 一个新哨兵最容易的下场，是被折进某个已有的分支里", n)
		}
	}
	for n := range errorSentinelDisposition {
		if !have[n] {
			t.Errorf("errorSentinelDisposition 里有一条【幽灵项】：%s 在根包源码里已经没有了\n"+
				"  ⇒ 删掉它。留着会让下一个人以为那一格还有东西守着", n)
		}
	}

	// ⛔ **到期条件已兑现（2026-09-09）：`GapKind` 落成了 Go 常量，比对对象换过来了。**
	//
	// 上一版这里是一条【到期检查】：`GapKind` 一出现就报「该换比对对象了」。
	// 它按设计开了口，而这一格记录它是**被解决**的，不是被绕过的：
	//
	//	上一版  disposition 的值是【散文】（「缺口第三类 GapNotTrading —— …」）
	//	        ⇒ 类别名只活在文档里 ⇒ **键有守卫、值没有**：写错一个名字不会有东西响
	//	这一版  值是【类型化的 Go 常量】 ⇒ **改名或删掉一个 GapKind，这个文件编译不过**
	//	        ⇒ 比「抽出名字再比字符串」强一格：它不是一条断言，是编译期
	//
	// ⚠️ 而下面这条断言管的是另一半 —— 不是「名字对不对」，是**「一类都没漏」**：
	// 每一个 GapKind 常量，要么由某个哨兵映射来，要么登记在 gapKindsNotFromSentinel 里。
	// ⇒ **加第七类时，必须先回答它属于哪一边。不回答就红。**
	checkGapKindsAreDisposed(t)

	t.Logf("根包 %d 个错误哨兵，处置全部登记：%s", len(found), strings.Join(found, " "))
}

// gapKindNames 是【每个 GapKind 常量一个实例】。
//
// 它是手写的，而它**不会悄悄变旧**：checkGapKindsAreDisposed 拿
// 【源码里真正声明的常量名】来核这张表的键。体例照 periodSamples 那一手：
//
//	扫源码那半  抓「加了一类而没登记」——它枚举的是性质，不是我写下的名字
//	这张表那半  给出【值】，才做得了「哪一类被哪个哨兵指着」这个比对
var gapKindNames = map[string]GapKind{
	"GapNeverFetched":    GapNeverFetched,
	"GapConfirmedEmpty":  GapConfirmedEmpty,
	"GapNotTrading":      GapNotTrading,
	"GapCalendarUnknown": GapCalendarUnknown,
	"GapStoreUnverified": GapStoreUnverified,
	"GapStoreLegacy":     GapStoreLegacy,
}

func checkGapKindsAreDisposed(t *testing.T) {
	t.Helper()

	// 一、源码里声明了哪些 GapKind 常量 —— 与手写那张表逐名对。
	found := rootGapKindNames(t)
	if len(found) == 0 {
		t.Fatal("根包一个 GapKind 常量都没扫到 —— 要么它们搬走了，要么判据打偏了")
	}
	for _, n := range found {
		if _, ok := gapKindNames[n]; !ok {
			t.Errorf("源码里新增了一类缺口 %s，而 gapKindNames 里没有它\n"+
				"  ⇒ 先补进那张表，然后回答下面那一问：它从哪来", n)
		}
	}
	for n := range gapKindNames {
		if !containsStr(found, n) {
			t.Errorf("gapKindNames 里有一条【幽灵项】：%s 在根包源码里已经没有了", n)
		}
	}

	// 二、每一类要么由哨兵映射来，要么登记为「不由哨兵来」。
	fromSentinel := map[GapKind]string{}
	for n, d := range errorSentinelDisposition {
		if d.Kind == 0 {
			continue
		}
		if prev, dup := fromSentinel[d.Kind]; dup {
			t.Errorf("%s 与 %s 两个哨兵指向【同一类】%s\n"+
				"  ⇒ 那意味着两种「答不了」被折成了一格，而它们的处置未必相同", prev, n, d.Kind)
		}
		fromSentinel[d.Kind] = n
	}
	for name, k := range gapKindNames {
		_, bySentinel := fromSentinel[k]
		_, byAbsence := gapKindsNotFromSentinel[k]
		if bySentinel && byAbsence {
			t.Errorf("%s 既登记为「不由哨兵来」，又被哨兵 %s 指着 —— 两张表说了相反的话",
				name, fromSentinel[k])
		}
		if !bySentinel && !byAbsence {
			t.Errorf("缺口 %s 没说它【从哪来】：\n"+
				"  ⇒ 由某个哨兵映射来 ⇒ 写进 errorSentinelDisposition 的那一条\n"+
				"  ⇒ 不由哨兵来（真值是一个【缺席】）⇒ 写进 gapKindsNotFromSentinel\n"+
				"  ⇒ 别只加常量：一个新类别最容易的下场，是被折进某个已有的分支里", name)
		}
	}
	t.Logf("缺口 %d 类：%d 类由哨兵映射来，%d 类真值是一个缺席",
		len(gapKindNames), len(fromSentinel), len(gapKindsNotFromSentinel))
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// rootGapKindNames 扫根包源码里所有【类型是 GapKind 的包级常量】的名字。
//
// ⚠️ 判据认的是 `const ( X GapKind = … )` 这种带类型的 const 块：
// 同一个块里后续没写类型的行**沿用前一行的类型**（Go 的 iota 惯例），本函数照这条读。
func rootGapKindNames(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读目录：%v", err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", e.Name(), err)
		}
		if f.Name.Name != "tickflow" {
			continue
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			typed := false
			for _, s := range gd.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if id, isIdent := vs.Type.(*ast.Ident); isIdent {
					typed = id.Name == "GapKind"
				}
				if !typed {
					continue
				}
				for _, n := range vs.Names {
					out = append(out, n.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// ───────── 内容定位必须指得到出处 ─────────
//
// 由来是一条判据在本仓被违反了**两次，而两次都发生在【说明它的那一段里】**：
//
//	一 我把一批会失效的引用（章节号）换成「引那句原话」的内容定位 ——
//	  而我自己写的三串里有两串是【概括】不是原话 ⇒ grep 它们只命中引用自己
//	二 更干净的一次：我写下「命中 = 出处 ＋ 引用 ✅」来说明第一次那个错，
//	  而那句话把那个串抄了进去 ⇒ **命中从 2 变成 3**
//	  ⇒ **一句自我证伪的话：写下它的那个动作，让它写的那个读数变成假的。**
//
// 🔴 而我为这一族立过一条触发（「写完一段【关于某个字符串的说明】就 grep 它一次」），
// **它没有响** —— 因为那一段在作者心里是「关于一次失误的说明」，那个串只是例子里的细节：
//
//	触发按【这一段在说什么】划，而危险按【这一段含哪些字符】划。
//	而主题是作者自己定的 ⇒ **越是「这一段主要在讲别的」，它越不触发。**
//
// ⇒ 所以它要一条机械守卫，不是一条要人想起来的规矩
// （同 rule-as-fact 那一格：规矩要绑在一个一定会做的动作上，不是绑在我对这段话的分类上）。
//
// ⚠️ 分隔符用 `**` 而不是引号 —— **被引的串自己就含引号**。
// 评审方 2026-09-10 实测：以引号为分隔符，三条**没有一条捕对**
// （含内层引号的那条被截断，另两条把 `**` 一起捕了进去 ⇒ 拿去 grep 命中 0）。
// ⇒ 这正是本仓那条「**判别符不能比被判别的东西更细**」——
// 而这一次它的长法是：**分隔符出现在载荷里面。**
//
// 单列它【抓不住】的那一格（本仓那条：全是勾的报告分不出「核过了」和「没核出东西」）：
// 它守的是【定位指不指得到】。而由来里那第二次（说明段抄了那个串 ⇒ 命中从 2 变成 3）
// 它【抓不住】—— 出处那一行还在，hasSource 仍为真。
// ⇒ 那一格是「说明里写的读数变假了」，不是「定位坏了」，是两件事。
// 而我【不】把判据改成「命中恰好 2」：一个串合法地被引用两次就会误报，
// 而一条会误报的守卫最终会被关掉（本仓 ③ 那一格记过）。
// ⇒ 那一格留给人，而这条守卫只承诺它承诺的那一半。

// locatorRe 捞出 `搜…「**X**」` 里的 X。
var locatorRe = regexp.MustCompile(`搜[^「]*「\*\*(.+?)\*\*」`)

// contentLocators 返回文本里全部内容定位的载荷。
func contentLocators(text string) []string {
	var out []string
	for _, m := range locatorRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// locatorResolves 判「这条定位指得到出处吗」：
// 命中里必须有一条**不是定位行本身**。
//
// ⚠️ 判据不是「命中 ≥ 2」—— 两条定位互相命中时读数也是 2，
// **与「出处 ＋ 引用」一模一样**。
//
// ⛔ 判「这一行是不是【本条定位自己】」用的是**同一条正则**，不是「这行含不含『搜』」。
// 第一版用的是后者，而评审方 2026-09-10 打出了它的假红：
// 把「搜」塞进【出处】那一行 ⇒ 守卫报「命中 2 行，而每一行都是定位行本身」——
// **那句话是假的**（其中一行就是出处）。
// 📎 而误报面今天已经存在：design.md 里 6 行含「搜」，其中 **2 行不是定位行**
// （「先搜裸名字」「git log -S 搜它的标题形式」）——
// 它们今天不含任何载荷所以没出事，**而那是巧合，不是设计**；
// 且这份文档的题材保证这个碰撞会长大（规矩要求写更多定位，讲检索的散文也在变多）。
// 🔴 ⇒ 一句话：**判「是不是定位行」要和「捞定位」用同一把尺子。**
// （而它撞上本文件自己写下的那条：一条会误报的守卫最终会被关掉。）
func locatorResolves(text, payload string) (hits int, hasSource bool) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, payload) {
			continue
		}
		hits++
		if !isOwnLocatorLine(line, payload) {
			hasSource = true
		}
	}
	return hits, hasSource
}

// isOwnLocatorLine 判「这一行是不是【payload 这条定位】自己那一行」。
//
// ⚠️ 判的是「本条」而不是「任何一条定位」：一行可以是 A 的定位行，
// 同时是 B 的出处 —— 按「任何一条」判会把那一行对 B 也排除掉。
func isOwnLocatorLine(line, payload string) bool {
	for _, m := range locatorRe.FindAllStringSubmatch(line, -1) {
		if m[1] == payload {
			return true
		}
	}
	return false
}

func TestContentLocatorsResolve(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("docs", "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	locs := contentLocators(text)

	// 前提自检（防空转）：捞不到定位时，下面那个循环一次都不跑，而测试照绿。
	//
	// ⛔ 这个数是【高水位】，不是「够用就行」的下限 —— 评审方 2026-09-10 实测：
	// 第一版写 3 而实际是 4 ⇒ **删掉一条定位，它绿着不响**。
	// ⇒ 语义按 tools/audit/high_water.txt 那一条：**任何缩水都要是一个显式动作**
	// （涨了就把这个数抬上来；而降下去必须有人来这里说明为什么）。
	//
	// ⚠️ 单列它抓不住的：这是一个**手抬**的高水位，不像 high_water.txt 那样由生成器自动抬。
	// ⇒ 有人加了第五条而没抬这个数时，此后从 5 掉回 4 不会响。
	// 没做成自动的理由是代价：那要改 rebuild_docs_test.py 的登记表结构，
	// 而这条守卫今天只有 4 条载荷。**写下来，不假装它是自动的。**
	const locatorHighWater = 4
	if len(locs) < locatorHighWater {
		t.Fatalf("只捞到 %d 条内容定位（%v）——低于高水位 %d。\n"+
			"⇒ 若你确实删了一条：把上面那个常数改下来，并在这儿写一句为什么。\n"+
			"⇒ 若你没删：多半是正则与文中写法漂开了 —— 先修捞法，别改被测方。",
			len(locs), locs, locatorHighWater)
	}
	for _, p := range locs {
		hits, hasSource := locatorResolves(text, p)
		if !hasSource {
			t.Errorf("内容定位指不到出处：搜「%s」在 docs/design.md 里命中 %d 行，"+
				"而【每一行都是定位行本身】。\n"+
				"⇒ 多半是你写了一句【概括】而不是文件里的【原话】；"+
				"把出处那一行的原文粘过来。\n"+
				"⚠️ 注意「命中 ≥ 2」不算数：两条定位互相命中时读数也是 2。", p, hits)
		}
	}
}

// TestContentLocatorCheckerItself 是上面那条守卫的对照组 —— **它在断言里，不在注释里**。
//
// 三格：一条真指得到的、一条只命中定位行的、以及**载荷里含引号的那一条**
// （最后这格是评审方实测出来的坑：用引号当分隔符时它会被截断）。
func TestContentLocatorCheckerItself(t *testing.T) {
	const good = "一行原话：门会写\n引用见 搜「**门会写**」\n"
	const bad = "引用见 搜「**从没写过的串**」\n另一处引用 搜「**从没写过的串**」\n"
	const nested = "一行原话：和「一分钟」是同一个库\n引用见 搜「**和「一分钟」是同一个库**」\n"

	if got := contentLocators(good); len(got) != 1 || got[0] != "门会写" {
		t.Fatalf("捞法坏了：good 捞到 %q", got)
	}
	if _, ok := locatorResolves(good, "门会写"); !ok {
		t.Fatal("对照组塌了：一条【真指得到】的定位被判成指不到 ⇒ 下面那格的红不算数")
	}
	if hits, ok := locatorResolves(bad, "从没写过的串"); ok || hits != 2 {
		t.Fatalf("坏例没被抓住：命中 %d、hasSource=%v —— "+
			"而它正是「两条定位互相命中，读数也是 2」那一格", hits, ok)
	}
	// 载荷含引号：以引号为分隔符会在这里截断，用 ** 不会。
	got := contentLocators(nested)
	if len(got) != 1 || got[0] != "和「一分钟」是同一个库" {
		t.Fatalf("载荷含引号时捞错了：%q\n"+
			"⇒ 这正是「分隔符出现在载荷里面」那一格；分隔符必须是那对星号，不能是引号", got)
	}
	if _, ok := locatorResolves(nested, got[0]); !ok {
		t.Fatal("载荷含引号的那条被判成指不到")
	}
}

// —— 射程的两张名单，以及一条【每一类都必须有归属】的断言 ——
//
// ⛔ 上一版这里只有一张白名单，注释里写着「这一维不封闭：新增一种文本扩展名时它不会自己进来」。
// 🔴 **而那是一句关于【将来】的话，评审方 2026-09-10 把它量成了一张关于【今天】的表：
// 今天就有 4 个文本文件在射程外** —— `LICENSE` · `.gitignore` · `.gitattributes` · `.env.example`。
// 他还往 `LICENSE` 里塞了一个 0x08：**白名单守卫顶层红 0，漏掉**。
//
// ⇒ 这与本仓这两天反复打的是同一条：**白名单是对【开放】的那一侧做枚举。**
// ⇒ 处置不是换判据，是**加一格**：把「不封闭」变成一条**会红**的断言 ——
// **仓里出现的每一类（扩展名，或无扩展名文件的文件名）都必须落在两张名单之一里**，
// 否则红。⇒ 新类进仓时**强制一次决定**，而不是静默落在射程外。
//
// ⚠️ 而我加了一格评审方没提的：**`.env` 也在 `filepath.Walk` 的射程里，而它装着密钥**
// （untracked ＋ gitignored）。本守卫只报「文件:行号 ＋ 码位」，**从不印内容** ——
// 而**下一个想让报文更好读的人，会想把那一行原文印出来**。
// ⇒ 所以它进 skip 名单，并把理由写在这儿。
var (
	// scanExt / scanName：要扫的。
	scanExt = map[string]string{
		".go": "源码", ".md": "文档", ".py": "工具脚本", ".txt": "高水位等纯文本",
		".mod": "go.mod", ".sum": "go.sum", ".example": "配置模板（.env.example）",
		".yml": "尚未出现，先占位", ".yaml": "同上", ".json": "同上",
	}
	scanName = map[string]string{
		"LICENSE": "纯文本", ".gitignore": "纯文本",
		".gitattributes": "纯文本，而且它决定 git 怎么处理换行",
	}
	// skipExt / skipName：**显式**不扫的，每条写明理由。
	skipExt = map[string]string{
		".exe":   "二进制（doccheck.exe 之类），本来就含控制字符",
		".jsonp": "fixture：上游原样存下来的响应，不该被本仓的规矩改写",
		".xml":   "同上",
	}
	skipName = map[string]string{
		".env": "**装着密钥**，untracked ＋ gitignored ⇒ 本守卫不读它" +
			"（今天只报文件名与码位、从不印内容，而下一个想让报文更好读的人会想印那一行）",
	}
)

// classOf 给一个文件名一个【类】：无扩展名的用文件名本身，其余用小写扩展名。
//
// ⚠️ 两种分类法都说得通（评审方按「.gitignore 是一类扩展名」数出 12 类，
// 我按 `filepath.Ext` 数出 11 类）—— **而要断言的那件事不受它影响**：
// **每个文件都要有归属。** 这里选文件名那一种，因为无扩展名的那一桶里
// 同时装着「要扫的 LICENSE」和「绝不能读的 .env」，**按扩展名分它们会塌进同一格**。
func classOf(name string) string {
	if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
		return ext
	}
	return name
}

// TestNoControlCharactersInSources 挡的是一族**只有 `cat -A` 看得见**的字符。
//
// ⛔ 它的由来是两天里各咬我们一次（2026-09-10）：
//
//	送审方：heredoc 把注释里的 `\b` 变成了**一个真的退格字符（0x08）**写进 source.go
//	评审方：同族的另一次，写进了记忆文件
//
// 🔴 而它的失败方向是**不可见**：那个 0x08 在源文件里
// **编译过、`go vet` 过、`gofmt` 过、`doccheck` 过** —— 一路全绿。
// ⇒ 这正是本仓那条：**一个静默的错误，比一个大声的错误贵**。
//
// ⚠️ 而更贵的一格在后面：**修它的时候我把 `\x08` 换成了 `b`**
// ⇒ 那条命令变成 `grep "\.HasOIb"` ⇒ **它跑出来是 0，而我要的答案正好也是 0**
// ⇒ **一个对的答案、错的理由。** 若不是顺手做了对照（比【总命中】而不只比目标范围），
// 这一格会以「读数没变」通过。
//
// —— 判据与射程 ——
//
// 查的：C0 控制字符里除 `\t` `\n` `\r` 之外的那些（`\x00-\x08 \x0b \x0c \x0e-\x1f`）
// 范围：文本类扩展名（.go .md .py .txt .mod .sum .yml .yaml .json）——
//
//	⚠️ 按**扩展名**划而不是按目录划，因为仓里还有 .exe / .jsonp / .xml 这些
//	**本来就可能含控制字符**的东西（fixture 与二进制）。
//	⇒ 这一维不封闭：**新增一种文本扩展名时它不会自己进来**，
//	  而那个失败方向是「少查一类」，不是误报。
//
// ⚠️ 前提自检：扫到的文件数必须 > 0 —— 否则这条守卫是空转的，而空转和「全干净」同形。
func TestNoControlCharactersInSources(t *testing.T) {
	isBad := func(r rune) bool {
		if r == '\t' || r == '\n' || r == '\r' {
			return false
		}
		return r < 0x20 || r == 0x7f
	}

	var scanned int
	var bad, unclassified []string
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
		cls := classOf(info.Name())
		if _, skip := skipExt[cls]; skip {
			return nil
		}
		if _, skip := skipName[cls]; skip {
			return nil
		}
		_, wantExt := scanExt[cls]
		_, wantName := scanName[cls]
		if !wantExt && !wantName {
			// ⛔ 这一类既不在「要扫」也不在「显式不扫」里 ⇒ 它静默落在射程外。
			unclassified = append(unclassified, fmt.Sprintf("%s（%s）", cls, p))
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			for _, r := range line {
				if isBad(r) {
					bad = append(bad, fmt.Sprintf("%s:%d 有 %U", p, i+1, r))
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫文件失败：%v", err)
	}
	if scanned == 0 {
		t.Fatal("一个文本文件都没扫到 —— 这条守卫是空转的，读数作废")
	}
	// ⛔ 承重的那一格：**每一类都必须有归属**。
	if len(unclassified) > 0 {
		t.Fatalf("有 %d 类文件既不在【要扫】也不在【显式不扫】里：%s"+
			"  ⇒ 它们静默落在这条守卫的射程外。"+
			"  ⇒ 处置：把它加进 scanExt/scanName（要扫）"+
			"或 skipExt/skipName（不扫，并写明理由）。"+
			"  ⇒ 这一格存在的理由：上一版只有一张白名单，"+
			"而白名单是对【开放】的那一侧做枚举 —— 当时就已经有 4 个文本文件在射程外了。",
			len(unclassified), strings.Join(unclassified, " · "))
	}
	if len(bad) > 0 {
		t.Fatalf("扫过 %d 个文本文件，发现 %d 处控制字符：\n  %s\n"+
			"  ⇒ 它们编译过、vet 过、gofmt 过、doccheck 过 —— 只有 `cat -A` 看得见（形如 ^H）。\n"+
			"  ⇒ 多半来自 heredoc：写含反斜杠的内容时用 Write/Edit 工具，别走 heredoc。\n"+
			"  ⇒ ⚠️ 修的时候别把 \\x08 换成 b —— 那会造出一条【坏掉却给出正确答案】的选择器。",
			scanned, len(bad), strings.Join(bad, "\n  "))
	}
	t.Logf("扫过 %d 个文本文件，控制字符 0 处", scanned)
}
