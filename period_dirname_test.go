package tickflow

import (
	"fmt"
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

// TestPeriodDirNameFamiliesAreDisjointByShape 钉的是【结构不相交】，不是「我检查过」。
//
// 「我检查过没重复」会随新成员失效；结构性质不会。
// 日内名永远是「十进制数字 + m」，日历名永远是那三个词。
func TestPeriodDirNameFamiliesAreDisjointByShape(t *testing.T) {
	cal := map[string]bool{}
	for _, p := range []CalendarPeriod{Daily, Weekly, Monthly} {
		n, err := PeriodDirName(p)
		if err != nil {
			t.Fatalf("PeriodDirName(%v)：%v", p, err)
		}
		cal[strings.ToLower(n)] = true
	}
	// 扫一大片日内周期，没有一个可以落进日历那一族。
	for n := 1; n <= 1440; n++ {
		got, err := PeriodDirName(MustIntraday(n))
		if err != nil {
			t.Fatalf("PeriodDirName(Intraday(%d))：%v", n, err)
		}
		if !strings.HasSuffix(got, "m") || got != fmt.Sprintf("%dm", n) {
			t.Fatalf("日内名跑出了「数字+m」这个形状：Intraday(%d) ⇒ %q", n, got)
		}
		if cal[strings.ToLower(got)] {
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
