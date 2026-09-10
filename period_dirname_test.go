package tickflow

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// lowerDup 是本文件的判别器：**大小写无关**地找重复。
// 判重名的不是本程序，是文件系统，而 Windows/macOS 默认大小写不敏感。
func lowerDup(names []string) (string, bool) {
	seen := map[string]string{}
	for _, n := range names {
		k := strings.ToLower(n)
		if prev, ok := seen[k]; ok {
			return fmt.Sprintf("%s 与 %s（都折成 %q）", prev, n, k), true
		}
		seen[k] = n
	}
	return "", false
}

// allPeriods 是本文件用的样本。**它不是全集**（日内是开的），
// 但它含着那对会撞的：MustIntraday(1) 的 String() 是 "1m"，Monthly 的是 "1M"。
func allPeriods() []Period {
	return []Period{
		MustIntraday(1), MustIntraday(5), MustIntraday(15), MustIntraday(30), MustIntraday(60),
		Daily, Weekly, Monthly,
	}
}

// TestPeriodDirNameIsCaseInsensitivelyUnique 是丁的承重断言。
//
// ⚠️ 它自带对照组，而且**对照组在断言里，不在注释里**：
// 拿同一个判别器去喂 String()，**必须报重复** —— 否则这个判别器对
// 「1m / 1M 这种撞法」根本不敏感，那么下面那个「没撞」就什么也不意味着。
func TestPeriodDirNameIsCaseInsensitivelyUnique(t *testing.T) {
	ps := allPeriods()

	var strs []string
	for _, p := range ps {
		strs = append(strs, p.String())
	}
	dup, ok := lowerDup(strs)
	if !ok {
		t.Fatal("对照组塌了：同一个判别器喂 String() 【没有】报重复 ⇒ " +
			"它对大小写撞名不敏感 ⇒ 下面那个绿不算数。先修判别器，别改被测方。")
	}
	t.Logf("对照组在承重：String() 里撞的是 %s", dup)

	var names []string
	for _, p := range ps {
		n, err := PeriodDirName(p)
		if err != nil {
			t.Fatalf("PeriodDirName(%v) 报错，而它是一个合法周期：%v", p, err)
		}
		names = append(names, n)
	}
	if dup, ok := lowerDup(names); ok {
		t.Fatalf("PeriodDirName 撞名（大小写无关）：%s\n"+
			"⇒ 在 Windows/macOS 上这两个周期会共用一个文件 —— 正是丁要挡的那件事", dup)
	}
}

// calendarPeriodsByAsking 把【已知的日历周期】**问出来**，而不是抄一份名单。
//
// ⛔ 抄名单是这条守卫上一版的坏法，而它有实测：日历侧写死成 Daily/Weekly/Monthly，
// 于是加一个 Quarterly 并给它 "3m"（**正是这条守卫要挡的那种撞法**）⇒ 全仓 0 红。
// 评审方 2026-09-10 构造，我复现过：突变施加后 `go test ./...` 红 0 条。
// 🔴 **新成员根本不进来 —— 一条列成员的守卫，射程就是那张名单。**
//
// ⇒ 改成向 PeriodDirName 问：它对未知取值报错 ⇒ **不报错的那些就是已知集合**。
// ⚠️ 而这里【不能】走到第一个错就停：成员未必从 0 起连续
// （`Quarterly CalendarPeriod = 10` 是合法写法）⇒ 扫一段区间，收下所有不报错的。
func calendarPeriodsByAsking() map[CalendarPeriod]string {
	out := map[CalendarPeriod]string{}
	for n := -16; n <= 255; n++ {
		if name, err := PeriodDirName(CalendarPeriod(n)); err == nil {
			out[CalendarPeriod(n)] = name
		}
	}
	return out
}

// TestPeriodDirNameFamiliesAreDisjointByShape 钉的是【结构不相交】，不是「我检查过」。
//
// 「我检查过没重复」会随新成员失效；结构性质不会 —— **而前提是这条守卫看得见新成员**，
// 那正是 calendarPeriodsByAsking 存在的理由。
func TestPeriodDirNameFamiliesAreDisjointByShape(t *testing.T) {
	cal := calendarPeriodsByAsking()

	// 前提自检（防空转）：若 PeriodDirName 对所有输入都报错，下面那条断言会【绿着空转】。
	for _, want := range []CalendarPeriod{Daily, Weekly, Monthly} {
		if _, ok := cal[want]; !ok {
			t.Fatalf("前提不成立，本格作废：连 %v 都没被问出来——"+
				"PeriodDirName 可能对所有输入都在报错，那样下面那条断言是空转的", want)
		}
	}
	t.Logf("问出来的日历周期 %d 个：%v", len(cal), cal)

	intraday := regexp.MustCompile(`^[0-9]+m$`)
	for p, name := range cal {
		if intraday.MatchString(strings.ToLower(name)) {
			t.Fatalf("日历周期 %v 的落盘名 %q 匹配了【日内形状】^[0-9]+m$\n"+
				"⇒ 两族不再不相交：它会和某个 Intraday(n) 同名，而大小写也不救", p, name)
		}
	}

	// 反向：日内那一族必须【全部】落在那个形状里，且不落进日历名集合。
	byName := map[string]bool{}
	for _, n := range cal {
		byName[strings.ToLower(n)] = true
	}
	for n := 1; n <= 1440; n++ {
		got, err := PeriodDirName(MustIntraday(n))
		if err != nil {
			t.Fatalf("PeriodDirName(Intraday(%d))：%v", n, err)
		}
		if !intraday.MatchString(got) {
			t.Fatalf("日内名跑出了 ^[0-9]+m$ 这个形状：Intraday(%d) ⇒ %q", n, got)
		}
		if byName[strings.ToLower(got)] {
			t.Fatalf("日内 Intraday(%d) 的落盘名 %q 落进了日历那一族 ⇒ 两族不再不相交", n, got)
		}
	}
}

// TestPeriodDirNameRejectsWhatStringAccepts 是它与 String() 分开的**全部理由**。
//
// 四格：每一格里 String() 都给得出一个值，而 PeriodDirName 必须报错。
func TestPeriodDirNameRejectsWhatStringAccepts(t *testing.T) {
	cases := []struct {
		what      string
		p         Period
		strGives  string
		whyItsBad string
	}{
		{"IntradayPeriod 零值", IntradayPeriod{}, "0m",
			"Intraday() 拒绝产出这个值，而它照样有名字 ⇒ 会静默落一个 0m.dat"},
		{"CalendarPeriod(99)", CalendarPeriod(99), "?",
			"越界全糊成同一个值 ⇒ Windows 上拼不出合法文件名，Linux 上会建出一个 ?.dat"},
		{"CalendarPeriod(-1)", CalendarPeriod(-1), "?",
			"同上，且与 99 的名字相同 ⇒ 两个不同的坏值共用一个库"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			// 前提：String() 确实给得出那个值 —— 否则这一格测的不是我以为的东西。
			if got := c.p.String(); got != c.strGives {
				t.Fatalf("前提不成立，本格作废：String() 给的是 %q，而本格是按 %q 写的",
					got, c.strGives)
			}
			if got, err := PeriodDirName(c.p); err == nil {
				t.Fatalf("PeriodDirName 收下了 %s ⇒ 落盘名 %q\n理由：%s",
					c.what, got, c.whyItsBad)
			}
		})
	}
	t.Run("nil 周期", func(t *testing.T) {
		if got, err := PeriodDirName(nil); err == nil {
			t.Fatalf("PeriodDirName(nil) 收下了 ⇒ %q", got)
		}
	})
}

// TestPeriodDirNameMatchesDocumentedLayout 钉住 §六 布局里写出来的那两个名字。
//
// 它防的是「改名改爽了，把文档里写着的名字也改了」——
// 那会让一份按文档手动摆好的目录，在代码这边变成一个空库。
func TestPeriodDirNameMatchesDocumentedLayout(t *testing.T) {
	for _, c := range []struct {
		p    Period
		want string
	}{
		{MustIntraday(1), "1m"}, // design.md §六：SHFE.rb2701/1m.dat
		{Daily, "1d"},           // design.md §六：_continuous/…/1d.dat
	} {
		got, err := PeriodDirName(c.p)
		if err != nil {
			t.Fatalf("PeriodDirName(%v)：%v", c.p, err)
		}
		if got != c.want {
			t.Fatalf("§六 布局写的是 %q，而 PeriodDirName 给 %q —— "+
				"改这一格之前先改 design.md §六", c.want, got)
		}
	}
}
