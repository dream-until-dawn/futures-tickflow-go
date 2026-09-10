package segfile

import (
	"os"
	"path/filepath"
	"strings"
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

// TestMigrationInstructionTerminates 执行的是【上面那句报错里写给人的处置】本身。
//
// ⛔ 它的由来是一条必改（评审方 2026-09-10）：原来那句写的是
// 「确认周期、把文件改成对应的名字、再重开」——**照做走不通**：
// 改完名重开，第二堵墙是 `format=1，本版只认 2`，而那句报错**不带任何处置**。
//
// 🔴 判据：**一句处置是一个关于系统的断言，而它可以为假** ——
// 「写得不够全」和「它承诺了一个不终止的过程」不是一回事，后者是缺陷。
// ⇒ 而它的量法就是这条测试：**你写了一个过程，那就把它执行一遍。**
//
// ⚠️ 这条测试与那句报文之间只有一个【粗】的连接：下面断言报文提到 "format"。
// 粗是故意的（本仓那条：判别符不能比被判别的东西更细），
// 而它挡得住「有人把第三步从报文里删掉」这一种。
func TestMigrationInstructionTerminates(t *testing.T) {
	dir := t.TempDir()
	cal, k := testCalendar(t)

	// 造一份【旧版留下的】库：名字是写死的 1m.*，而里面其实是日线。
	r := EncodeBar(bar(20200731, 1))
	if err := os.WriteFile(filepath.Join(dir, "1m.dat"), r[:], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1m.meta"),
		[]byte(`{"format":1,"coverage":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := Open(dir, tickflow.Daily)
	if err == nil {
		t.Fatal("前提不成立，本格作废：旧库还在旁边而 Open 收下了")
	}
	// ⛔ 这里原来写的是 strings.Contains(err.Error(), "format")，而它【空转】——
	// 实测：把整段处置从报文里删光，这条断言照样绿。
	// 成因：外层错误包着 DecodeMeta 自己那句，而那句本身就含 "format"。
	// 🔴 一条建在【包装后的整串】上的文本断言，测不到【外层自己写了什么】。
	// ⇒ 换成一个只有这段处置才有的词。它仍然是【粗】的代理，
	//   而真正承重的是下面那几步：这条测试把那个过程执行了一遍。
	if !strings.Contains(err.Error(), "三步") {
		t.Fatalf("报文里没有那段【走得完的处置】——照它做的人会停在第二堵墙上：%v", err)
	}

	// —— 照那三步做 ——
	// 一 确认周期：这份数据是日线（本测试知道，因为是它造的）
	// 二 改名
	if err := os.Rename(filepath.Join(dir, "1m.dat"), filepath.Join(dir, "1d.dat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "1m.meta"), filepath.Join(dir, "1d.meta")); err != nil {
		t.Fatal(err)
	}
	// 三 把 format 从 1 改成 2
	b, rerr := os.ReadFile(filepath.Join(dir, "1d.meta"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	bumped := strings.Replace(string(b), `"format":1`, `"format":2`, 1)
	if bumped == string(b) {
		t.Fatal("前提不成立，本格作废：.meta 里没有找到要改的那个 format")
	}
	if err := os.WriteFile(filepath.Join(dir, "1d.meta"), []byte(bumped), 0o644); err != nil {
		t.Fatal(err)
	}

	// —— 过程该在这里终止 ——
	s, truncated, err := Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("照那三步做完，仍然打不开 —— 那句处置承诺了一个不终止的过程：\n%v", err)
	}
	defer s.Close()
	if truncated != 0 {
		t.Fatalf("迁移之后报了残尾 %d 字节 —— 那份数据被动过了", truncated)
	}

	// 而数据还在：那一根根走查得过。
	if err := s.AppendBars(nil); err != nil {
		t.Fatalf("AppendBars(nil)：%v", err)
	}
	if err := s.CommitSpan(cal, k,
		tickflow.Span{From: 20200731, To: 20200731, Bars: 1, Days: 1}, OutcomeComplete); err != nil {
		t.Fatalf("迁移之后提交那一段失败 —— 数据没跟过来：%v", err)
	}
	if err := s.Verify(tickflow.Span{From: 20200731, To: 20200731, Bars: 1, Days: 1}); err != nil {
		t.Fatalf("迁移之后走查失败：%v", err)
	}
}
