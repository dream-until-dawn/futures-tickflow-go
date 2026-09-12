package tickflow_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// —— 这一条守的是【合并提交里没有偷渡的内容】——
//
// 合并提交是**唯一可以塞进「两个父都没有的内容」的地方**。
// 本仓原来的规矩是「自带 diff 必须 0 行」，而 2026-09-11 那次合并把它顶翻了：
// `tools/audit/high_water.txt` 的体例明文要求「**合并时必须写一条合流记录**」，
// 而合流记录按定义就是两个父都没有的内容。
//
// ⚠️ 我当时提的处置是「0 行 **或** 逐行摊在送审信里」，**评审方否掉了，理由我认**：
// 🔴 **「摊开」不是闸门，是礼节** —— 它核不了，且强度随读信人的注意力变化。
// 📎 而更锋利的那半句（我自己那一格）：**一道闸门的强度不在【通过它要付多少】，
// 在【绕过它要付多少】** —— 摊开的绕过成本是零：少贴几行没人知道。
//
// ⇒ 换成可机器核的：**自带 diff 的【文件集合】⊆ 一张明示白名单**，
// 判据与豁免名单都写在 `tools/audit/merge_gate.py` 里。
//
// —— ⭐ 而这一条有一件本仓别的守卫都没有的东西：**它一出生就有全部历史当输入** ——
//
// 我们刚记过「新守卫的头几次读数没有真值（作者是唯一走那条路径的人）」。
// 而这道门禁**立起来那一刻就有 42 颗历史合并**：36 颗自带 diff 为空 · 4 颗只有账本 ·
// **2 颗越界，而我们知道它们为什么越界**（都含 `docs_test.go` —— 一个已有「别手合」处置的生成物）。
// 🔴 ⇒ **它是我们造过的唯一一道【不需要等第一个外人】的守卫。**
//
// ⚠️ 射程（写明它不比什么）：
//
//	断言的是  文件集合 ⊆ 白名单
//	**不是**  「越界的恰好 2 颗」—— 那会在下一次合法的账本合并时红，而那是误报
//	不管的    那些行**写了什么** —— 由账本自己那三条守卫（链 / 来历 / 合流位置）管
//	不管的    非合并提交 —— 它们本来就要逐颗过评审

// —— ⛔ 2026-09-11 第二轮：那三档退出码，此前只是【一段帮助文本】——
//
// 这条守卫原来把图例写在 `t.Fatalf` 的报文里：「1 ＝ 有违例 · 2 ＝ 扫到 0 颗 ·
// 其它 ＝ 跑不起来」。⇒ 实测：**「跑不起来」当时给的就是 1。**
// `raise SystemExit("refuse: …")` 传字符串 ⇒ 退出码 **1** ⇒ 与「有违例」同一个字节。
//
// 🔴 那句图例**不是假的，是【为真而有害】**：读到 1 的人被它送去查「哪颗合并越界」，
// 而真相是这道门禁根本没跑起来。⇒ 本仓收的：**一句处置有三种坏法 ——
// 为假 · 为真而有害 · 循环。**
//
// ⚠️ 它是怎么过了评审的，评审方自己给的答案（原话）：
// **「评审读的是断言，而一份【图例】不长得像断言 —— 它长得像帮助文本。」**
// ⇒ 所以改法不是把图例写对，是**让三档各自有一个走得到它的用例**，
// 并且**调用方一律断言 `rc == 0`，不许写 `rc != 1`**（后者会把 2 和 3 读成通过）。

// guard: main 上每一颗合并的自带 diff，文件集合都要落在白名单里。
func TestMergeOwnDiffStaysInsideWhitelist(t *testing.T) {
	rc, text := runGate(t, "")

	// ⛔ 这里【不许】Skip。本仓那条守卫（TestGuardsDoNotSkipThemselves）会当场抓住，
	// 而理由比规矩本身重要：**一条会自己跳过的守卫，在它最该说话的那一天是哑的。**
	// ⇒ 跑不起来（没有 python / 不在 git 仓里 / 没有 main）一律是**红**，
	//   而分得清是哪一种，由退出码那三档管 —— 见下面三个子用例。
	if err := gateVerdict(rc, text); err != nil {
		t.Fatalf("合并门禁没过：%v\n%s", err, text)
	}
	t.Log(strings.TrimSpace(text))

	// —— ⛔ 下面两格是【补回来的】，而它们补的是这条守卫自己的洞 ——
	//
	// 评审方 2026-09-11 打突变发现：`merge_gate.py` 里那条**父数断言**
	// （父数 < 2 就拒绝出读数）**没有任何断言落在它身上** —— 把它整条拿掉，
	// 这条守卫照样 PASS。成因不是它写错了：**Go 这一侧只跑 `main` 上的合并，
	// 而那些全是两个父** ⇒ 那条断言存在、正确，而从不被走到。
	//
	// 🔴 ⇒ 这正是 (c) 那一片的形状（「写着我是守卫，而没有断言落在它身上」），
	// **而这一次轮到守卫自己。**
	// 📎 收一句：**一条断言只在【它会失败的那种输入】被喂进来时才算被守着** ——
	// 而「正常输入全都合规」恰恰保证了那种输入永远不出现。

	t.Run("第 1 档 有违例", func(t *testing.T) {
		// ⛔ 这一格要的输入本仓【造不出来】：main 上仅有的两颗越界合并都在历史豁免里，
		// 而喂它们只会走到「豁免」那一支。⇒ 现造一个样本仓。
		// 📎 本仓那条：一条断言只在【它会失败的那种输入】被喂进来时才算被守着 ——
		// 而「正常输入全都合规」恰恰保证了那种输入永远不出现。
		dir := repoWithOffendingMerge(t)
		rc, out := runGate(t, dir)
		if rc != 1 {
			t.Fatalf("造了一颗自带 diff 含 a.txt 的合并，而它报 rc=%d（%s）：\n%s",
				rc, tierOf(rc), out)
		}
		if !strings.Contains(out, "a.txt") {
			t.Errorf("它红了，而报文没点出是哪个文件：\n%s", out)
		}
	})

	t.Run("第 2 档 一颗都没扫到", func(t *testing.T) {
		root := gitOut(t, "rev-list", "--max-parents=0", "HEAD")
		if f := strings.Fields(root); len(f) > 0 {
			root = f[len(f)-1] // 可能有多个根；取哪个都行，反正从它起没有合并
		}
		rc, out := runGate(t, "", "--rev="+root)
		if rc != 2 {
			t.Fatalf("从根提交起一颗合并都没有，而它报 rc=%d（%s）：\n%s", rc, tierOf(rc), out)
		}
		if !strings.Contains(out, "空转") {
			t.Errorf("它红了，而没红在那一条上（要「空转…读数作废」）：\n%s", out)
		}
	})

	t.Run("第 3 档 喂一颗非合并提交", func(t *testing.T) {
		sha := gitOut(t, "log", "--no-merges", "--format=%h", "-1", "main")
		rc, out := runGate(t, "", sha)
		if rc != 3 {
			t.Fatalf("喂它一颗非合并提交 %s，而它报 rc=%d（%s）：\n%s\n"+
				"  ⇒ `git show --cc` 对非合并印的是普通 diff（`diff --git`），\n"+
				"     文件集合为空 ⇒ 会被读成「自带 diff 为空」＝合规。\n"+
				"  ⇒ 「量不了」和「量出来是 0」在这里长得一模一样，所以它必须拒绝，\n"+
				"     而且要拒在它自己那一档上 —— 拒在 1 上等于说「有合并越界」。",
				sha, rc, tierOf(rc), out)
		}
		if !strings.Contains(out, "不是合并提交") {
			t.Errorf("它拒了，而没拒在那一条上（要「不是合并提交」）：\n%s", out)
		}
	})

	t.Run("第 3 档 喂坏环境_而主格那条判据仍然说红", func(t *testing.T) {
		// —— 🔴 这一格的重点不是那个 3，是后半句 ——
		//
		// 「环境坏掉时这道门禁仍然红」是一句**关于调用方的**断言，
		// 而调用方就是上面主格那两行。⇒ 所以这里把 `gateVerdict` 原样施加一次。
		// ⛔ 不许在这里另写一份判据：本仓实测过「注释写『抽出来』而实际是【复制】」，
		// 两份会漂开，漂开之后这一格守的就不再是主格那条判据了。
		dir := repoWithoutMain(t)
		rc, out := runGate(t, dir)
		if rc != 3 {
			t.Fatalf("喂了一个没有本地 main 的仓，而它报 rc=%d（%s）：\n%s", rc, tierOf(rc), out)
		}
		err := gateVerdict(rc, out)
		if err == nil {
			t.Fatalf("主格那条判据说这份读数通过了 —— 那意味着环境坏掉时这道门禁是哑的：\n%s", out)
		}
		// ⛔ 而「它红了」不够：主格那条判据有两个失败方向，坏环境的输出里本来就
		// 没有那句计数 ⇒ 只问「红没红」时，第二条方向会替第一条把它接住。
		// 实测：判据突变成 `rc == 1` 时这一格照样绿。⇒ 要问【红在哪一条上】。
		if !errors.Is(err, errGateExitCode) {
			t.Fatalf("它红了，而没红在退出码那一条上（红的是 %v）。\n"+
				"  ⇒ 那意味着判据若写成 rc == 1（只把「有违例」当失败），这一格照样绿，\n"+
				"     而「量不了」的 2 和 3 会被读成通过。", err)
		}
	})
}

// —— 主格那条判据的两个失败方向，做成哨兵 ——
//
// ⛔ 由来（2026-09-11 实测，突变打出来的）：坏环境那一格原来只断言「`gateVerdict` 说红」，
// 而把判据突变成 `rc == 1`（只把「有违例」当失败 —— 正是评审方明令禁止的那种写法）之后，
// **那一格照样绿**：坏环境的输出里本来就没有那句计数，
// ⇒ **第二条失败方向替第一条把它接住了。**
//
// 🔴 收：**一条判据有两个失败方向时，「它红了」这个断言会被另一条方向替着满足** ——
// 而那正是本仓那条「判别符的粒度要和命题一样」：
// 命题是【退出码那一条接住了它】，而我的断言只问【它红没红】。
var (
	errGateExitCode = errors.New("门禁的退出码不是 0")
	errGateFormat   = errors.New("门禁的输出里没有那句计数")
)

// gateVerdict 是**主格的判据本身**：退出码必须是 0，且输出里必须有那句计数。
//
// ⚠️ 写成 `rc != 0`，**不许**写成 `rc != 1` —— 后者会把「一颗都没扫到」（2）
// 和「跑不起来」（3）都读成通过，而那两档恰恰是「量不了」。
//
// ⛔ 第二条判据（那句计数）留着的理由和脚本里的第 2 档**不同**：
// 脚本挡的是「扫到 0 颗」，这里挡的是「脚本换了输出格式而我们没发现」。
func gateVerdict(rc int, text string) error {
	if rc != 0 {
		return fmt.Errorf("%w：%d，要 0 —— %s", errGateExitCode, rc, tierOf(rc))
	}
	if !strings.Contains(text, "扫了") {
		return fmt.Errorf("%w —— 门禁的输出格式变了，而这条守卫是照着它读的", errGateFormat)
	}
	return nil
}

// tierOf 把退出码翻成处置。三档在上面各有一个走得到它的子用例；
// 认不出的码**不猜**，直接指回脚本里那段登记。
func tierOf(rc int) string {
	switch rc {
	case 1:
		return "有合并的自带 diff 越出白名单，报文里点了名"
	case 2:
		return "一颗合并都没扫到（浅克隆？）—— 那不是「合规」，是「量不了」"
	case 3:
		return "跑不起来／前提不成立：没有 python？不在 git 仓里？没有那个 ref？喂了非合并提交？"
	case 0:
		// ⛔ 2026-09-12 补。它原来落在兜底上，印「这个退出码没有登记」——
		// 而 0 登记过，它是「全部合规」。这一支会在【门禁该报 1/2/3 而报了 0】时印出来，
		// 也就是这道守卫真正抓到东西的那一刻 ⇒ 那一刻它把人送去查一个不存在的新档。
		// 🔴 本仓那条（**一句处置有四种坏法**）里的头一种：**为假**。
		return "全部合规 —— 而这一格期待的不是这一档"
	}
	return "这个退出码没有登记 —— 去 tools/audit/merge_gate.py 那段「退出码」看它新增了什么"
}

// —— 这一格守的是【图例本身】——
//
// 🔴 由来：上面那段长注释记着「三档退出码此前只是一段帮助文本，而它是【为真而有害】的」。
// 处置是「让三档各有一个走得到它的用例」——⛔ **而 `tierOf` 自己一格都没有**：
// 它是把退出码翻成【处置】的那一步，也就是读这道门禁的人真正会照着做的那句话。
//
// ⚠️ 它是纯函数，一行就走得到，**所以它属于「欠账」而不是「缺口」**
// （本仓那三分：缺口 ＝ 今天验不了 · 保险 ＝ 今天没有受益人 · **欠账 ＝ 今天验得了而没验**）。
//
// —— 它挡的是哪一种改动 ——
//
//	兜底写成 `default: return "跑不起来…"`   ⇒ 新增的第 4 档会被认领成第 3 档
//	                                            —— 正是 2026-09-11 那次「1 与 1 同一个字节」的形状
//	两档的话被改成同一句                     ⇒ 读的人分不开两种处置

// guard: 退出码翻成处置的那张图例，认不出的码不许被任何一档认领。
func TestGateTierLegendIsHonest(t *testing.T) {
	// 登记过的码 ⇒ 每一档都得有自己的话。
	registered := map[int]string{}
	for _, rc := range []int{0, 1, 2, 3} {
		registered[rc] = tierOf(rc)
	}

	t.Run("标定 登记过的四档_两两不同", func(t *testing.T) {
		// ⛔ 这一格是下一格的零点：两档若说同一句话，下一格的「兜底与它们都不同」
		// 就只证明了「兜底与那一句不同」，而不是「兜底没认领任何一档」。
		seen := map[string]int{}
		for rc, msg := range registered {
			if prev, dup := seen[msg]; dup {
				t.Errorf("rc=%d 与 rc=%d 翻出同一句话：%q\n"+
					"  ⇒ 两种处置共用一句话 ＝ 读的人分不开它们，"+
					"而那正是 2026-09-11 「跑不起来」与「有违例」同用 1 的形状。",
					rc, prev, msg)
			}
			seen[msg] = rc
			if msg == "" {
				t.Errorf("rc=%d 翻出一句空话", rc)
			}
		}
	})

	t.Run("被测 没登记过的码_不许被任何一档认领", func(t *testing.T) {
		const unknown = 99
		got := tierOf(unknown)
		for rc, msg := range registered {
			if got == msg {
				t.Fatalf("rc=%d（没登记过）被翻成了第 %d 档的话：%q\n"+
					"  ⇒ 兜底若写成 `default: return <某一档>`，"+
					"脚本新增的那一档会被【静默认领】，\n"+
					"     而读的人拿到的是一句【为真而有害】的处置："+
					"它把人送去查一件没发生的事。\n"+
					"  ⇒ 认不出的码不许猜，只许指回 tools/audit/merge_gate.py 那段登记。",
					unknown, rc, got)
			}
		}
		if !strings.Contains(got, "没有登记") {
			t.Errorf("兜底那一支没说出「没有登记」：%q\n"+
				"  ⇒ 它是这条链上唯一会告诉下一个人「去脚本里看新增了什么」的地方。", got)
		}
		if !strings.Contains(got, "merge_gate.py") {
			t.Errorf("兜底那句没带地址（要点名 tools/audit/merge_gate.py）：%q\n"+
				"  ⇒ 本仓那条：【「够不到」是一句带地址的话】—— "+
				"一句没有地址的诊断，指不出下一个动作。", got)
		}
	})
}

// runGate 在 dir 里跑门禁（dir 为空 ＝ 仓根），回（退出码，合并输出）。
//
// ⛔ 起不起得来也是读数的一部分：python 不在 PATH 上时 ProcessState 是 nil，
// 那一刻**红**，不是跳过 —— 一道跑不起来的闸门和一道放行的闸门，在调用方眼里长得一样。
func runGate(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	script, err := filepath.Abs("tools/audit/merge_gate.py")
	if err != nil {
		t.Fatalf("取不到门禁脚本的绝对路径：%v", err)
	}
	cmd := exec.Command("python", append([]string{script}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if cmd.ProcessState == nil {
		t.Fatalf("门禁根本没起来（python 在不在 PATH 上？）：%v\n%s", err, out)
	}
	return cmd.ProcessState.ExitCode(), string(out)
}

// repoWithOffendingMerge 造一个样本仓：一颗**自带 diff 含 a.txt** 的真合并。
// 造法是让两支改同一个文件 ⇒ 冲突 ⇒ 解成第三个值（两个父都没有的内容）。
func repoWithOffendingMerge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("造样本仓失败：git %s ⇒ %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(s), 0o644); err != nil {
			t.Fatalf("写样本文件失败：%v", err)
		}
	}
	run("init", "-q", "-b", "main", ".")
	run("config", "user.email", "gate@test")
	run("config", "user.name", "gate")
	write("1\n")
	run("add", "a.txt")
	run("commit", "-qm", "base")
	run("checkout", "-q", "-b", "side")
	write("2\n")
	run("commit", "-qam", "side")
	run("checkout", "-q", "main")
	write("3\n")
	run("commit", "-qam", "main side")

	// ⛔ 前提自检一：这次合并【必须】冲突。它若干净地并掉了，下面那颗就是一颗
	// 普通提交，而这一格会红在一个与判据无关的理由上。
	merge := exec.Command("git", "merge", "side")
	merge.Dir = dir
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("前提没成立：这次合并本该冲突，而它并干净了 ⇒ 样本作废：\n%s", out)
	}
	write("4\n") // 两个父都没有的值
	run("add", "a.txt")
	run("commit", "-qm", "merge with own diff")

	// ⛔ 前提自检二：它真的是一颗两个父的合并 —— 本仓那条「先断言父数，再读那个量」。
	parents := exec.Command("git", "rev-list", "--parents", "-n1", "HEAD")
	parents.Dir = dir
	out, err := parents.Output()
	if err != nil {
		t.Fatalf("数不出父数：%v", err)
	}
	if n := len(strings.Fields(string(out))) - 1; n != 2 {
		t.Fatalf("前提没成立：造出来那颗有 %d 个父，不是 2 ⇒ 样本作废", n)
	}
	return dir
}

// repoWithoutMain 造一个**没有本地 main** 的仓 —— 门禁默认去问 `main`，问不到就该拒。
// 📎 这个输入是评审方给的：比伪造浅克隆便宜得多，而走到的是同一档。
func repoWithoutMain(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("造样本仓失败：git %s ⇒ %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "trunk", ".")
	run("config", "user.email", "gate@test")
	run("config", "user.name", "gate")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatalf("写样本文件失败：%v", err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "base")

	// ⛔ 前提自检：确实没有 main。
	show := exec.Command("git", "rev-parse", "--verify", "-q", "main")
	show.Dir = dir
	if err := show.Run(); err == nil {
		t.Fatal("前提没成立：这个样本仓里居然有 main ⇒ 它走不到那一档")
	}
	return dir
}

// gitOut 跑一条 git 并把输出去掉首尾空白。跑不起来就红 —— 这条守卫本来就依赖 git。
func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git %s 失败：%v —— 这条守卫依赖 git，跑不起来是红，不是跳过",
			strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
