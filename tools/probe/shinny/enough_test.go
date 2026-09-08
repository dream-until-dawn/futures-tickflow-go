package main

import (
	"strings"
	"testing"
	"time"
)

// enoughDays 是「取到的数够不够下结论」那条判据。它是纯函数，所以这里离线测。
//
// # 为什么这一条必须有测试，而且必须有【拒收】的那一侧
//
// 它的上一版是 `len(all) < 2000` —— 一个绝对天数。2026-09-09 实测，
// 三个品种被它挡在门外，而它们的 `fails` 都是 0（**取数一次都没失败**）：
//
//	KQ.m@GFEX.si    900 天 ⇒ 被拒
//	KQ.m@GFEX.lc    761 天 ⇒ 被拒
//	KQ.m@SHFE.ss   1990 天 ⇒ 被拒（差 10 天）
//
// ⛔ 而这次修的方向是**放宽**。一条放宽了的判据，最容易犯的错是**什么都收**——
// 那样它就不再是判据了。所以下面每一条「该收」的旁边，都有一条「该拒」的。
//
// **只测该收的那一侧，等于把门拆了再说门修好了。**

// 造一串交易日：从 start 起，跳过周六周日，取 n 天。
func weekdays(start string, n int) []string {
	t, err := time.Parse("2006-01-02", start)
	if err != nil {
		panic(err)
	}
	out := make([]string, 0, n)
	for len(out) < n {
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday {
			out = append(out, t.Format("2006-01-02"))
		}
		t = t.AddDate(0, 0, 1)
	}
	return out
}

// 从一串日子里每 keep 天留 1 天 —— 用来造「中间有洞」。
func thin(days []string, keep int) []string {
	var out []string
	for i, d := range days {
		if i%keep == 0 {
			out = append(out, d)
		}
	}
	return out
}

func TestEnoughDays(t *testing.T) {
	cases := []struct {
		name  string
		days  []string
		fails int
		want  bool
	}{
		// —— 该收的一侧 ——
		{"十年、连续", weekdays("2016-01-04", 2600), 0, true},
		{"年轻但连续：si 那一档（约 900 天）", weekdays("2022-12-22", 900), 0, true},
		{"更年轻：ps 那一档（约 413 天）", weekdays("2024-12-26", 413), 0, true},
		{"刚够下限 120 天", weekdays("2025-01-06", 120), 0, true},
		{"失败 2 窗仍收（阈值是 > 2）", weekdays("2020-01-06", 800), 2, true},

		// —— 该拒的一侧：每一条都对应上面某一条，只改一个变量 ——
		{"差一天不够下限 119", weekdays("2025-01-06", 119), 0, false},
		{"失败 3 窗", weekdays("2020-01-06", 800), 3, false},
		{"跨度够长但中间有洞（每 3 天留 1 天）",
			thin(weekdays("2016-01-04", 2600), 3), 0, false},
		{"空", nil, 0, false},
	}
	for _, c := range cases {
		got, why := enoughDays(c.days, c.fails)
		if got != c.want {
			t.Errorf("%s：enoughDays(%d 天, fails=%d) = %v，要 %v\n  理由：%s",
				c.name, len(c.days), c.fails, got, c.want, why)
		}
		// 拒收的时候必须说得出理由 —— 一句「不够」而不说为什么，
		// 会让下一个人以为是数据的问题（**这次就是这么错的**）。
		if !got && why == "" {
			t.Errorf("%s：拒收了却没给理由", c.name)
		}
	}
}

// TestEnoughDaysRejectsTheOldThreshold 把那次实际的误拒写成回归。
//
// ⚠️ 判据不是「900 > 某个数」，是**900 天密铺 2022-12-22..今天这段跨度**。
// 所以这条测试要用真实的形状去问，不能只喂一个长度。
func TestEnoughDaysRejectsTheOldThreshold(t *testing.T) {
	// 旧判据：len < 2000 就拒。下面三条当年全被它拒了，而 fails 都是 0。
	for _, c := range []struct {
		sym, start string
		n          int
	}{
		{"GFEX.si", "2022-12-22", 900},
		{"GFEX.lc", "2023-07-21", 761},
		{"SHFE.ss", "2019-09-25", 1686},
	} {
		days := weekdays(c.start, c.n)
		if ok, why := enoughDays(days, 0); !ok {
			t.Errorf("%s（%d 天，自 %s，0 窗失败）被拒了 —— "+
				"这正是旧判据犯的错，理由：%s", c.sym, c.n, c.start, why)
		}
		if c.n >= 2000 {
			t.Fatalf("%s 有 %d 天，旧判据本来就收 —— 这条回归没测到东西",
				c.sym, c.n)
		}
	}
}

// TestEnoughDaysWhyMentionsTheNumbers 拒收的理由里必须带上数。
//
// 「取数不足」四个字对读的人没有用：他分不清是取数坏了、还是品种年轻。
// **而这次的教训正是「一个写错的判据把自己的失败伪装成数据的局限」。**
func TestEnoughDaysWhyMentionsTheNumbers(t *testing.T) {
	_, why := enoughDays(weekdays("2025-01-06", 30), 0)
	if why == "" {
		t.Fatal("拒收却没有理由")
	}
	if !strings.Contains(why, "30") {
		t.Errorf("理由里没有出现实到的天数 30：%q", why)
	}
	_, why2 := enoughDays(nil, 5)
	if !strings.Contains(why2, "5") {
		t.Errorf("理由里没有出现失败窗数 5：%q", why2)
	}
}
