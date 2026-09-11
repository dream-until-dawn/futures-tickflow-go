package tickflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— 这一条守的是【那一步没有靠人手搬运】——
//
// ⛔ 由来（评审方 2026-09-11 提出结构性改法，我采纳并补上这一格）：
// **tag 注解是本仓这套门禁里唯一一件既不可改、又不在任何树里的产物** ——
// 比树 / 筛子 / 突变 / 自检 / porcelain **全都够不着它**。
// 而它的代价这一轮实测过一次：一版处置表有**两格是反的**，**已经准备刻进版本了**，
// 是靠人读出来的，不是靠任何闸门。
//
// ⇒ 处置是把注解落成 `docs/release/<tag>.md`，走常规评审。
// ⚠️ **而那样只解了一半**：从「评审放行的那个文件」到「真正打进 tag 的那串字节」，
// 中间还有一步。若那一步靠人手复制，**刚关上的洞又开了一半**。
// ⇒ 那一步是 `tools/audit/tag_body.py`，**而这条守卫钉的是它的判据**：
// 「开头恰好一个 `<!-- ... -->` 头，它之后是正文」。
//
// 🔴 **一个会漂的判据，要么有守卫，要么就不该被依赖。**
// 有人哪天把那个头删了、或加了**第二个 `-->` ＋空行** ⇒ **当场红**，
// 而不是等到打 tag 那一刻才发现裁错了。
//
// ⚠️ **而这句射程上一版说宽了**（评审方 2026-09-11 打了两格，我复现）：
// 判据数的是「`-->` 紧跟一个空行」这个组合，**不是单个 `-->`**
// ⇒ 一个**孤零零的 `-->`**（后面不跟空行）**守卫放行，而它会原样进注解**（实测）。
// 🔴 这不是缺陷（脚本与守卫用的是同一个判据，二者一致），**是那句话比判据宽**。
// ⇒ 本仓那条：**一句关于守卫能力的话，会被下一个人当成守卫的契约用。**

// guard: docs/release/*.md 的注解正文必须裁得出来 —— 从「放行的文件」到「打进 tag 的字节」不靠手搬。
func TestReleaseNotesTagBodyIsExtractable(t *testing.T) {
	const dir = "docs/release"
	entries, err := os.ReadDir(dir)
	// ⛔ **这里【不许】Skip** —— 本仓那条守卫（`TestGuardsDoNotSkipThemselves`）当场抓住了我：
	// 我第一版写的是「目录不存在就 `t.Skipf`」，理由听起来很合理（「还没用这条流程发过版本」）。
	// 🔴 而一条会自己跳过的守卫，**在它最该说话的那一天是哑的**：
	// 目录被误删、或有人换了路径 ⇒ 它不红，它「跳过」。
	// ⇒ 目录不在就是**红**，而报文要说清处置。
	if err != nil {
		t.Fatalf("读不到 %s：%v\n"+
			"  ⇒ 若这个目录还不存在：本仓的发布流程要求注解正文落成 `docs/release/<tag>.md`\n"+
			"     （理由见本文件顶部那一段：tag 注解是唯一一件既不可改、又不在任何树里的产物）。\n"+
			"  ⇒ 若它被删了：那正是这条守卫要报的事。", dir, err)
	}

	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		checked++

		if !strings.HasPrefix(text, "<!--") {
			t.Errorf("%s 开头不是 `<!--` 注释块\n"+
				"  ⇒ `tools/audit/tag_body.py` 的裁法没有依据，它会拒绝出正文；\n"+
				"     而那一刻是【打 tag 的时候】——太晚了。", p)
			continue
		}
		// ⛔ 判据要求【恰好一个】：两个的话裁刀会切在第一个上，而那多半不是意图。
		if n := strings.Count(text, "-->\n\n"); n != 1 {
			t.Errorf("%s 里 `-->` ＋空行 出现 %d 次，要恰好 1 次\n"+
				"  ⇒ 裁法会切错地方，而切出来的东西【看起来仍然像一份注解】。", p, n)
			continue
		}
		body := text[strings.Index(text, "-->\n\n")+len("-->\n\n"):]
		if strings.TrimSpace(body) == "" {
			t.Errorf("%s 裁出来的正文是空的 —— 一份空注解会被原样打进 tag", p)
		}
	}

	// ⛔ 前提自检：真的扫到文件了。
	// 🔴 目录在而里面一个 .md 都没有时，上面那个循环一次都不跑，**而这条测试照绿** ——
	// 空转与「全都合规」同形。
	if checked == 0 {
		t.Fatalf("%s 在，而里面一个 .md 都没有 —— 这条守卫是空转的，读数作废", dir)
	}
	t.Logf("检查了 %d 份发布说明，注解正文都裁得出来", checked)
}
