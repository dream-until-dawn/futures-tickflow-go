package tickflow_test

import (
	"os/exec"
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

// guard: main 上每一颗合并的自带 diff，文件集合都要落在白名单里。
func TestMergeOwnDiffStaysInsideWhitelist(t *testing.T) {
	cmd := exec.Command("python", "tools/audit/merge_gate.py")
	out, err := cmd.CombinedOutput()
	text := string(out)

	// ⛔ 这里【不许】Skip。本仓那条守卫（TestGuardsDoNotSkipThemselves）会当场抓住，
	// 而理由比规矩本身重要：**一条会自己跳过的守卫，在它最该说话的那一天是哑的。**
	// ⇒ 跑不起来（没有 python / 不在 git 仓里 / 浅克隆没有合并历史）一律是**红**，
	//   而报文要分得清「违规」和「量不了」—— 脚本用退出码 2 表示后者。
	if err != nil {
		t.Fatalf("合并门禁没过：%v\n%s\n"+
			"  ⇒ 退出码 1 ＝ 有合并的自带 diff 越出白名单（报文里点了名）\n"+
			"  ⇒ 退出码 2 ＝ 一颗合并都没扫到（浅克隆？）——那不是「合规」，是【量不了】\n"+
			"  ⇒ 其它 ＝ 跑不起来（没有 python？不在 git 仓里？）",
			err, text)
	}

	// ⛔ 前提自检：它真的扫到了东西。
	// 🔴 一个「什么都没扫到」的成功，和「全都合规」在退出码上是同一个字节 ——
	// 而脚本自己也挡了这一格（退出码 2），这里再钉一次是因为**两处的失败方向不同**：
	// 脚本挡的是「扫到 0 颗」，这里挡的是「脚本换了输出而我们没发现」。
	if !strings.Contains(text, "扫了") {
		t.Fatalf("门禁的输出里没有那句计数 —— 它的输出格式变了，而这条守卫是照着它读的：\n%s", text)
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

	t.Run("非合并提交上拒绝出读数", func(t *testing.T) {
		sha := gitOut(t, "log", "--no-merges", "--format=%h", "-1", "main")
		out, err := exec.Command("python", "tools/audit/merge_gate.py", sha).CombinedOutput()
		if err == nil {
			t.Fatalf("喂它一颗非合并提交 %s，而它给了读数：\n%s\n"+
				"  ⇒ `git show --cc` 对非合并印的是普通 diff（`diff --git`），\n"+
				"     文件集合为空 ⇒ 会被读成「自带 diff 为空」＝合规。\n"+
				"  ⇒ 「量不了」和「量出来是 0」在这里长得一模一样，所以它必须拒绝。", sha, out)
		}
		if !strings.Contains(string(out), "不是合并提交") {
			t.Errorf("它拒了，而没拒在那一条上（要「不是合并提交」）：\n%s", out)
		}
	})

	t.Run("零颗合并时报作废而不是合规", func(t *testing.T) {
		root := gitOut(t, "rev-list", "--max-parents=0", "--format=%h", "HEAD")
		// `--format=%h` 会多印一行 `commit <sha>`；取最后一行。
		if lines := strings.Fields(root); len(lines) > 0 {
			root = lines[len(lines)-1]
		}
		out, err := exec.Command("python", "tools/audit/merge_gate.py", "--rev="+root).CombinedOutput()
		if err == nil {
			t.Fatalf("从根提交起一颗合并都没有，而它报了合规：\n%s", out)
		}
		if !strings.Contains(string(out), "空转") {
			t.Errorf("它红了，而没红在那一条上（要「空转…读数作废」）：\n%s", out)
		}
	})
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
