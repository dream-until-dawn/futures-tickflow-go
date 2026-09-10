package segfile

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// backticked 抓 `elsewhere` 条目里反引号包着的名字。
var backticked = regexp.MustCompile("`([^`]+)`")

// registryDecl 是 `elsewhere` 那张表的声明。**按性质找它，不按文件名找** ——
// 表搬了家，排除也要跟着搬（本仓那条：射程写成位置就照不到搬走的那份）。
//
// 🔴 **它拼出来，而不是写成一个完整的字面 —— 这一格是基线红出来的**：
// 写成完整字面时，**这个文件自己也含有那个字符串** ⇒ 守卫把自己也当成登记表排掉了
// ⇒ 排掉 2 个而不是 1 个 ⇒ 基线当场红（而那正是那条基线存在的理由）。
//
// > **一个检查器，把它要找的东西写进自己，就变成了它要找的东西。**
// ⇒ 本仓已记过它的另一形态：**别去数一个你正在书写的记号 —— 书写本身会改变计数**
// （`tools/audit/high_water.txt`）。那次是「提及」，这次是「匹配」。
var registryDecl = "var elsewhere = " + "map[string]string{"

// TestElsewhereNamesExist ⑦：`elsewhere` 里那些【地址】指的名字，必须真的还在。
//
// 🔴 **这一格补的是 `elsewhere` 自己写下的价值上界**：
// 它当初就写着「它挡的是『忘了说它在哪』，**挡不住『说了个假地址』**」——
// 而假地址是**零成本的散文**（评审方 2026-09-09 提，我认）。
// ⇒ 抬高它一格是便宜的，因为那几条地址点的是**真名字**。
//
// ⛔ **而这条检查的头两版都是【空转】的，两个毛病各自独立，都是我自己的对照组抓到的：**
//
//	一 用 `strings.Contains` 比对 ⇒ 把 `disposeOpenState` 改名成
//	  `disposeOpenStateRenamed` ⇒ **0 红**（新名字把旧名字整个含在里面）。
//	  > **一个子串检查，认不出「把名字加长」这种改名 —— 而那正是最常见的一种。**
//	二 语料**包含登记表自己所在的那个文件** ⇒ 往表里塞一个
//	  `TestThisGuardDoesNotExistAnywhere` ⇒ **0 红**。
//	  > **一个把【被检查的东西】收进自己语料的检查器，永远只会说「有」。**
//	  ⇒ 同本仓那条：**别用把 X 当前提的工具去测量 X。**
//
// ⛔ **它今天的射程，三条，和它一起读**：
//
//	一 它**不核语义** —— 名字在，不等于那儿真的在守这一条不变量。
//	二 它按【最后一段】比对（`Store.OpenState` ⇒ 只查 `OpenState`）——
//	  于是把 `Store` 改名、方法留着，这条检查**看不见**。
//	  ⇒ 收窄成全限定名做不到：源码里根本不存在 `Store.OpenState` 这个字面。
//	三 它查的是「某个 .go 文件里出现过这个词」，**不是「它是一个声明」**。
//
// ⇒ 那它挡住什么：**最可能的那种腐烂 —— 对面改名或删掉。**
// 而这与 `GapKind` 那一格同形：**绑在名字上的检查，需要一张盯着同一个名字的第二张网。**
func TestElsewhereNamesExist(t *testing.T) {
	root := filepath.Join("..", "..")
	corpus, files := repoGoCorpus(t, root, selfPath(t))

	// 基线：语料不能是空的 —— 否则下面每一条都会「通过」。
	// ⛔ 本仓记过那一类：**一个只会报「有」的检查器，和不跑差不多。**
	if len(files) < 10 || len(corpus) < 10000 {
		t.Fatalf("语料只有 %d 个 .go 文件 / %d 字节 —— 太少，多半是路径不对；"+
			"这时【不能】当成通过", len(files), len(corpus))
	}

	var checked int
	for id, where := range elsewhere {
		names := backticked.FindAllStringSubmatch(where, -1)
		if len(names) == 0 {
			t.Errorf("elsewhere[%s] 里一个反引号名字都没有 —— "+
				"「写明在哪」这条要求它只是看起来满足了", id)
			continue
		}
		for _, m := range names {
			raw := strings.TrimSpace(m[1])
			checked++
			if strings.HasSuffix(raw, ".go") {
				if _, ok := files[raw]; !ok {
					t.Errorf("elsewhere[%s] 指着文件 %s，而全仓没有这个文件", id, raw)
				}
				continue
			}
			// 取最后一段并去掉调用括号：`Store.OpenState` ⇒ OpenState；
			// `OpenState().LegacyMeta` ⇒ LegacyMeta。
			seg := raw
			if i := strings.LastIndex(seg, "."); i >= 0 {
				seg = seg[i+1:]
			}
			seg = strings.TrimSuffix(strings.TrimSpace(seg), "()")
			if seg == "" {
				t.Errorf("elsewhere[%s] 里的 %q 归一化之后是空的 —— 这条检查对它是瞎的", id, raw)
				continue
			}
			// ⛔ **整词匹配，不是子串**（毛病一的处置）。
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(seg) + `\b`).MatchString(corpus) {
				t.Errorf("elsewhere[%s] 指着 %s（归一化为 %s），而全仓的 .go 里【找不到它】\n"+
					"  ⇒ 对面多半改名或删了；而这张表会继续说「去那儿看」", id, raw, seg)
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个名字都没查到 —— 要么 elsewhere 空了，要么反引号的形状变了。" +
			"这时【不能】当成通过")
	}
	t.Logf("查了 elsewhere 里的 %d 个名字", checked)
}

// repoGoCorpus 把全仓 .go 的内容拼成一份语料，并记下每个文件的**基名**。
//
// ⛔ **它排掉【登记表自己所在的那个文件】**（毛病二的处置）：
// `elsewhere` 住在一个 .go 文件里，不排的话**写进它的任何名字都自动「存在」**。
func repoGoCorpus(t *testing.T, root, self string) (string, map[string]bool) {
	t.Helper()
	var sb strings.Builder
	files := map[string]bool{}
	skipped := 0
	selfSkipped := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), registryDecl) {
			skipped++
			return nil // 登记表自己：它在语料里就等于自证
		}
		// ⛔ **本守卫自己的文件也要排掉** —— 它的注释里写着它查过哪些名字、
		// 造过哪个假名字，**而那些字一旦落在语料里就自动「存在」**。
		if abs, err := filepath.Abs(p); err == nil && abs == self {
			selfSkipped++
			return nil
		}
		sb.Write(b)
		sb.WriteByte('\n')
		files[filepath.Base(p)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("走查 %s 失败：%v", root, err)
	}
	// 基线：**必须真的排掉了一个** —— 排不掉说明那个声明的形状变了，
	// 而那时这条检查会静静地退回【自我满足】。
	if skipped != 1 {
		t.Fatalf("排掉了 %d 个登记表文件，期望正好 1 个——"+
			"多半是 %q 这个形状变了；这时【不能】当成通过", skipped, registryDecl)
	}
	if selfSkipped != 1 {
		t.Fatalf("排掉了 %d 个「本守卫自己」，期望正好 1 个——"+
			"排不掉的话，写在本文件注释里的每一个名字都会自动【存在】", selfSkipped)
	}
	return sb.String(), files
}

// selfPath 用 runtime.Caller 拿本文件的绝对路径。
//
// 🔴 **用 runtime.Caller 而不是「按文件名排除」，是因为这一格【天然自指】**：
// 任何用来认出「本文件」的字符串，写进本文件之后就成了它自己的一部分。
// 我在这一格上连栽三次，每次换一个装：
//
//	一 registryDecl 写成完整字面 ⇒ 本文件也含它 ⇒ 守卫把自己当成登记表排掉
//	二 注释里写 `disposeOpenState` ⇒ 那个名字留在语料里 ⇒ 改名突变【0 红】
//	三 注释里写那个造出来的假名字 ⇒ **假名字成真** ⇒ 假地址突变【0 红】
//
// > 🔴 **一个文本检查器的文档，是它自己的语料。**
// > 而本仓早记过它的另一形态：**别去数一个你正在书写的记号 —— 书写本身会改变计数。**
// ⇒ `runtime.Caller` 不经过文本，所以它是这一族里唯一不自指的那个办法。
func selfPath(t *testing.T) string {
	t.Helper()
	_, f, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 拿不到本文件路径 —— 没有它就排不掉自己")
	}
	abs, err := filepath.Abs(f)
	if err != nil {
		t.Fatalf("解不出绝对路径：%v", err)
	}
	return abs
}
