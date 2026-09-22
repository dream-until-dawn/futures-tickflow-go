package tickflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 文档里的 Markdown 表格：每一行的列数必须等于表头（v0.11，评审方 2026-09-22 要求）。
//
// 起因：v0.11 Q-a（7d13a43）改 contract.md 风险表的一行时，替换把原来的单元格分隔符「| 」一起吃掉了 ⇒
// 那一行少了一列，「后果」与「怎么办」并成了一格 —— 渲染出来不报错，只是读的人把处置读成了后果。评审方以为是少一个标点，
// 我数列数才看出来。同一次还数出一行早就多两列的（行内代码里的 `grep … | grep`：GFM 表格里行内代码中的 `|` 也切格，要写成 `\|`）。
//
// 判据（与 GitHub 的 GFM 表格一致的那一部分）：
//   - 先剥掉行首的引用块前缀（`>`，可重复、前后可有空格）—— 本仓有 9 张表在引用块里（2026-09-22 扫：design.md 1 · probe.md 8）
//   - 表 ＝ 连续的、（剥完之后）以 `|` 开头的行，且第二行是分隔行（只有 `|` `-` `:` 与空白）
//   - 列数 ＝ 去掉 `\|` 之后数 `|`（行首行尾的 `|` 各算一个边）
//   - 围栏代码块（剥完之后以 ``` 开头的行之间）里的不算
//
// ⚠️ 本守卫只认这一种形状（评审方要求写明）。2026-09-22 扫全仓 16 份 .md（扫描脚本先用每种形状各一个样本标定过）：
// 分隔行以 | 开头的表 111 张 ＝ 围栏外普通 87 · 围栏外引用块里 9 · 围栏里 15（不算）⇒ 本守卫认 96 张（与 TestMarkdownTableColumnsMatchHeader 印的数一致）·
// 列表里缩进的表 0 · 首尾不带 | 的表 0 · ~~~ 围栏 0 · 4 个以上反引号的围栏 0
// ⇒ 这四种形状出现时本守卫看不见：缩进的表不当表 · 省略首尾 | 的表不当表 · ~~~ 围栏里以 | 开头的行会被当成表来数 · ```` 围栏按 ``` 翻转。
// ⚠️ 那次扫描的脚本第一版漏了引用块（分隔行正则不接受行首的 >），报「缩进表 0 处」；拿样本标定才抓到 —— 上面的数是修过之后的。
// ⚠️ 射程：只查「列数与表头相同」，不查内容。

// tableColumnIssues 返回 text 里列数与表头不同的行（1 起的行号），认出了几张表，其中几张在引用块里。
func tableColumnIssues(text string) (issues []string, tables, quoted int) {
	lines := strings.Split(text, "\n")
	// unquote 剥掉引用块前缀：只在（去掉前导空格之后）以 > 开头时剥；不是引用块的行原样返回
	// （不去缩进 —— 缩进四格的是代码块，不能当表）
	unquote := func(l string) string {
		for {
			t := strings.TrimLeft(l, " ")
			if !strings.HasPrefix(t, ">") {
				return l
			}
			l = strings.TrimPrefix(strings.TrimPrefix(t, ">"), " ")
		}
	}
	cols := func(l string) int { return strings.Count(strings.ReplaceAll(l, `\|`, ""), "|") - 1 }
	isSep := func(l string) bool {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "|") || !strings.Contains(t, "-") {
			return false
		}
		return strings.Trim(t, "|-: \t") == ""
	}
	var tbl []int             // 当前这张表的行号（0 起）
	inQuote := map[int]bool{} // 这一行原本在引用块里
	flush := func() {
		if len(tbl) >= 2 && isSep(lines[tbl[1]]) {
			tables++
			if inQuote[tbl[0]] {
				quoted++
			}
			h := cols(lines[tbl[0]])
			for _, i := range tbl {
				if c := cols(lines[i]); c != h {
					issues = append(issues, fmt.Sprintf("第 %d 行：表头 %d 列，这一行 %d 列", i+1, h, c))
				}
			}
		}
		tbl = tbl[:0]
	}
	fence := false
	for i, raw := range lines {
		r := strings.TrimSuffix(raw, "\r")
		l := unquote(r)
		inQuote[i] = l != r
		lines[i] = l
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			flush()
			fence = !fence
			continue
		}
		if !fence && strings.HasPrefix(l, "|") {
			tbl = append(tbl, i)
			continue
		}
		flush()
	}
	flush()
	return issues, tables, quoted
}

// guard: 全仓 .md 的每张表，每一行的列数都等于表头；并断言认出的表数 > 0。
func TestMarkdownTableColumnsMatchHeader(t *testing.T) {
	files, tables, quoted := 0, 0, 0
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "__pycache__", "node_modules", "testdata":
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
		files++
		issues, n, q := tableColumnIssues(string(b))
		tables += n
		quoted += q
		for _, is := range issues {
			t.Errorf("%s %s —— 多半是单元格里有没转义的 `|`（行内代码里也要写成 `\\|`），或者改这一行时吃掉了一个分隔符", p, is)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 表数也要断言：检测器坏到一张表都认不出时，「0 处不对」与「全都对」长得一样（评审方要求）
	if files == 0 || tables == 0 {
		t.Fatalf("扫了 %d 份 .md、认出 %d 张表 —— 这个守卫没在守任何东西", files, tables)
	}
	// 引用块里的表也要认得出（本仓有 9 张，design.md:235 是一张）：剥前缀那一步坏了时，全仓这一格也红，不只靠标定格（评审方 2026-09-22）
	if quoted == 0 {
		t.Errorf("引用块里一张表都没认出（本仓有，design.md:235 就是）—— 剥引用块前缀那一步多半坏了")
	}
	t.Logf("扫了 %d 份 .md，认出 %d 张表（其中引用块里 %d 张）", files, tables, quoted)
}

// guard: 列数这把尺子先标定 —— 两个方向各一格（少一列 · 多列）· 转义过的 `\|` 不报 · 围栏里的不报 · 没有分隔行的不算表 ·
// 引用块里的表照样查、引用块里的围栏照样跳过 · 表数认得对。
func TestTableColumnCheckerItself(t *testing.T) {
	cells := []struct {
		name string
		text string
		want int
	}{
		{"少一列（7d13a43 那种：4 列表里一行 3 列）", "| a | b | c | d |\n|---|---|---|---|\n| 1 | 2 | 3 4 |\n", 1},
		{"多列（第 359 行修之前那种：4 列表里一行 6 列，行内代码里两个没转义的 |）", "| a | b | c | d |\n|---|---|---|---|\n| 1 | 2 | 3 | `grep x | grep -v y | grep -v z` |\n", 1},
		{"转义过的 \\| 不算", "| a | b |\n|---|---|\n| `x \\| y` | 2 |\n", 0},
		{"围栏代码块里的不算", "```\n| a | b |\n|---|---|\n| 1 |\n```\n", 0},
		{"没有分隔行就不算表", "| a | b |\n| 1 |\n", 0},
		{"对齐写法的分隔行也认", "| a | b |\n|:--|--:|\n| 1 |\n", 1},
		{"引用块里的表照样查", "> | a | b |\n> |---|---|\n> | 1 |\n", 1},
		{"引用块里的围栏也跳过", "> ```\n> | a | b |\n> |---|---|\n> | 1 |\n> ```\n", 0},
	}
	for _, c := range cells {
		if got, _, _ := tableColumnIssues(c.text); len(got) != c.want {
			t.Errorf("%s：报了 %d 处 %v，应为 %d", c.name, len(got), got, c.want)
		}
	}
	if _, n, q := tableColumnIssues("| a |\n|---|\n\n> | b |\n> |---|\n\n```\n| c |\n|---|\n```\n"); n != 2 || q != 1 {
		t.Errorf("表数：认出 %d 张（引用块里 %d 张），应为 2（普通 1 · 引用块 1 · 围栏里的不算）", n, q)
	}
}
