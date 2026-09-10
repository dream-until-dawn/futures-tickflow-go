package segfile

import (
	"os"
	"path/filepath"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件是 design.md §十八「周期进身份」的验收，三条都是评审方 2026-09-10 给的，
// 而**第一条的措辞被改过一次**：原来写「第二个周期应当在哪一步被拦」，
// 而走丁之后它**根本不到「被拦」那一步** —— 两个周期是两个文件。
// ⇒ 该问的不是「拦没拦住」，是「**第一个周期的库有没有被动过**」。

// mustSnapshot 读出一个周期那两个文件的字节。文件不在就 Fatal ——
// 「文件不在」和「内容没变」在下面那个断言里必须分得开。
func mustSnapshot(t *testing.T, dir, name string) (dat, meta []byte) {
	t.Helper()
	var err error
	if dat, err = os.ReadFile(filepath.Join(dir, name+".dat")); err != nil {
		t.Fatalf("读 %s.dat：%v", name, err)
	}
	if meta, err = os.ReadFile(filepath.Join(dir, name+".meta")); err != nil {
		t.Fatalf("读 %s.meta：%v", name, err)
	}
	return dat, meta
}

// TestSecondPeriodLeavesFirstUntouched 是丁的承重断言。
//
// ⚠️ **必须用会撞的那一对**：`MustIntraday(1)` 与 `Monthly` ——
// 它们的 `String()` 恰是 `"1m"` 与 `"1M"`，在 Windows/macOS 上是同一个文件。
// 用 `1d`/`1m` 跑这一格，它在两个平台上都绿，**而那正是它挡不住的那一种**。
func TestSecondPeriodLeavesFirstUntouched(t *testing.T) {
	dir := t.TempDir()
	cal, k := testCalendar(t)

	first, _, err := Open(dir, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatalf("开第一个周期：%v", err)
	}
	if err := first.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200803, 2)}); err != nil {
		t.Fatalf("AppendBars：%v", err)
	}
	if err := first.CommitSpan(cal, k, tickflow.Span{From: 20200731, To: 20200803, Bars: 2, Days: 2}, OutcomeComplete); err != nil {
		t.Fatalf("CommitSpan：%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close：%v", err)
	}

	n1, err := tickflow.PeriodDirName(tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	nM, err := tickflow.PeriodDirName(tickflow.Monthly)
	if err != nil {
		t.Fatal(err)
	}
	// 前提自检：这两个名字在【大小写无关】的意义下不同 —— 否则下面测的不是我以为的东西。
	if equalFold(n1, nM) {
		t.Fatalf("前提不成立，本格作废：两个落盘名折成小写后相同（%q / %q）", n1, nM)
	}
	datBefore, metaBefore := mustSnapshot(t, dir, n1)

	second, _, err := Open(dir, tickflow.Monthly)
	if err != nil {
		t.Fatalf("开第二个周期：%v", err)
	}
	if err := second.AppendBars([]tickflow.Bar{bar(20200803, 9), bar(20200804, 10)}); err != nil {
		t.Fatalf("第二个周期 AppendBars：%v", err)
	}
	if err := second.CommitSpan(cal, k, tickflow.Span{From: 20200803, To: 20200804, Bars: 2, Days: 2}, OutcomeComplete); err != nil {
		t.Fatalf("第二个周期 CommitSpan：%v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close：%v", err)
	}

	datAfter, metaAfter := mustSnapshot(t, dir, n1)
	if string(datAfter) != string(datBefore) {
		t.Fatalf("第二个周期落盘之后，第一个周期的 .dat 变了：%d -> %d 字节\n"+
			"⇒ 两个周期共用了一个库 —— 而在 Windows 上这恰恰是 String() 拼名的后果",
			len(datBefore), len(datAfter))
	}
	if string(metaAfter) != string(metaBefore) {
		t.Fatalf("第二个周期落盘之后，第一个周期的 .meta 变了：\n旧 %s\n新 %s",
			metaBefore, metaAfter)
	}

	// 而第一个周期的库仍然自洽（不只是「字节没变」，是「它还是对的」）。
	again, _, err := Open(dir, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatalf("重开第一个周期：%v", err)
	}
	defer again.Close()
	if err := again.Verify(tickflow.Span{From: 20200731, To: 20200803, Bars: 2, Days: 2}); err != nil {
		t.Fatalf("第一个周期的库在第二个周期落盘之后走查失败：%v", err)
	}
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// TestOpenRefusesToCreateBesideAnUnknownLibrary 是迁移那一格（§十八 三）。
//
// 今天磁盘上的库都是「日线数据躺在一个叫 1m.dat 的文件里」（当时两个源都只有 Daily）。
// `Open(dir, Daily)` 会去找 `1d.dat`，而 `OpenDat` 带 `O_CREATE` ——
// 不拦的话它会**当场造一个空库**，旧数据还在盘上而库当它不存在。
func TestOpenRefusesToCreateBesideAnUnknownLibrary(t *testing.T) {
	for _, c := range []struct {
		what string
		meta string
	}{
		{"旧 format", `{"format":1,"coverage":[]}`},
		{"没有 format 字段", `{"coverage":[]}`},
	} {
		t.Run(c.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "1m.meta"), []byte(c.meta), 0o644); err != nil {
				t.Fatal(err)
			}
			r := EncodeBar(bar(20200731, 1))
			if err := os.WriteFile(filepath.Join(dir, "1m.dat"), r[:], 0o644); err != nil {
				t.Fatal(err)
			}

			s, _, err := Open(dir, tickflow.Daily)
			if err == nil {
				s.Close()
				t.Fatal("Open 收下了：旧库还在旁边，而它悄悄新建了一个空的 1d 库 ⇒ 一次静默的数据消失")
			}

			// ⛔ 这一条比上面那条更要紧：**拦截必须在 O_CREATE 之前**。
			// 「报了错，而文件已经建出来了」在报文上看不出来。
			if _, serr := os.Stat(filepath.Join(dir, "1d.dat")); serr == nil {
				t.Fatalf("Open 报了错，【而 1d.dat 已经被建出来了】 —— "+
					"拦截落在 OpenDat 后面了。原报错：%v", err)
			}
			// 而旧库一个字节没动。
			b, rerr := os.ReadFile(filepath.Join(dir, "1m.dat"))
			if rerr != nil || len(b) != int(RecordSize) {
				t.Fatalf("旧的 1m.dat 被动过了：err=%v len=%d", rerr, len(b))
			}
		})
	}
}

// TestOpenRefusesUnstorablePeriod：一个拼不出名字的周期，不许在盘上留下任何东西。
func TestOpenRefusesUnstorablePeriod(t *testing.T) {
	for _, c := range []struct {
		what string
		p    tickflow.Period
	}{
		{"IntradayPeriod 零值", tickflow.IntradayPeriod{}},
		{"CalendarPeriod 越界", tickflow.CalendarPeriod(99)},
		{"nil", nil},
	} {
		t.Run(c.what, func(t *testing.T) {
			dir := t.TempDir()
			s, _, err := Open(dir, c.p)
			if err == nil {
				s.Close()
				t.Fatal("Open 收下了一个拼不出落盘名的周期")
			}
			ents, rerr := os.ReadDir(dir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(ents) != 0 {
				var names []string
				for _, e := range ents {
					names = append(names, e.Name())
				}
				t.Fatalf("Open 报了错，而目录里留下了 %v —— 一个拼不出名字的周期不该落任何东西", names)
			}
		})
	}
}
