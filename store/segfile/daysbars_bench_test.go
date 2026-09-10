package segfile

import (
	"os"
	"path/filepath"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 换读法之后的第一次【实测】：量的是【真方法】，不是本地复制的形状 ——
//
// ⛔ **它与同目录那组 `ReadShape` 基准不是同一件事，差别要写清楚**：
//
//	ReadShape…   v0.4 的**探针**：两种形状都在那个文件里**本地实现**，
//	             一个方法都没加到 `Store` 上 —— 它自己写着「这里只交代价，不交形状」
//	本文件       形状**已经定了**（v0.5 落了 `DaysWithBars`）
//	             ⇒ 量的是 `(*Store).DaysWithBars` 与 `(*Store).HasBars` **本身**
//
// 🔴 **这一步不是重复**：一组量在副本上的数，会随副本与真件漂开而静默变假 ——
// 本仓刚在守卫那边打过同一个形状（「复制」与「抽出」的差别只有 grep 分得开）。
//
// —— ⛔ 它存在的直接理由：`docs/design.md` 二十·六里那些秒数**全是外推** ——
//
//	问四  「1.9 小时」 = 2600 × 89 万 × 每次 2.96 µs   ← 外推
//	问五乙「2.6 秒」   = 89 万 × 2.91 µs               ← 外推
//	而两处的**单价**都来自同一组小规模读数（D=80、总记录 19,200）
//
// ⇒ 本文件回答的是那一格：**把 N 放大 46 倍（19,200 → 890,000），那条比例定律还成立吗。**
// ⚠️ 而它**答不了**「2600 天那一档」——那要跑 1.9 小时。
// 它答的是**单价**，而「× 天数」是一个**循环次数**，不是一个需要外推的量
// （`classifyTradingDay` 每天调一次，这一点是读代码就能定的）。
// 🔴 **把「测得的单价」与「数得出的次数」分开写，是这组数与被它替换的那些外推的全部差别。**
//
// 跑法（一次就写 78 MB，所以固定 1 次）：
//
//	go test ./store/segfile/ -run XXX -bench DaysWithBars -benchmem -benchtime=1x -count=4
//
// —— 读数（2026-09-10，AMD Ryzen 7 5700X，windows/amd64，N = 890,000）——
//
//	命令  go test ./store/segfile/ -run XXX -bench 'DaysWithBars|HasBars' -benchtime=1x -count=3
//
//	新读法 `DaysWithBars` 整段一次      **2500.3 – 2536.1 ms**   ⇒ 单价 **2.83 µs/次**
//	旧读法 `HasBars` 首日（提前返回）   **28.1 – 31.5 µs**
//	旧读法 `HasBars` 末日              2482.5 – 2504.3 ms
//	旧读法 `HasBars` 缺席（最坏档）     2483.7 – 2507.5 ms       ⇒ 单价 **2.81 µs/次**
//
// ⭐ **这组数回答的那一格**：设计里两处外推用的单价是 **2.91 / 2.96 µs**，
// 而它们都来自 **N = 19,200** 那组小规模读数。
// ⇒ **N 放大 46 倍之后实测 2.81 µs ⇒ 那条比例定律成立**，
// 而这一条此前**只是被假定的** —— 页缓存、预读、TLB 都可能在这个量级上把它打断。
//
// ✅ 三处对得上：
//
//	问五乙「新读法恒 ≈2.6 秒」        ⇒ 实测 **2.52 秒**
//	问四「1.9 小时」＝ 2600 × 缺席档   ⇒ 实测单价重算 **1.80 小时**
//	问五乙「旧读法可低到 1 次读，比值无上界可言」
//	                                 ⇒ 实测 首日 30 µs vs 缺席 2.5 秒，**约 8.3 万倍**
//
// 🔴 **而最后那一行才是必改那次真正被证的东西**：上一版写的「新最多慢约 2 倍」
// 若不改，会让下一个人拿最坏档去量时读到一个**差四个数量级**的结果。
//
// ⛔ **射程（与 ReadShape 那组同底，一并读）**：
// 单趟顺序读 · 页缓存热 · 无并发写者 · 无网络 ⇒ **这些是【下界】，真实场景只会更慢**。
// ⚠️ 而**「× 2600 天」那一步仍然是外推** —— 它外推的是一个**循环次数**
// （`classifyTradingDay` 每天调一次，读代码可定），不是一个耗时。
// **把「测得的单价」与「数得出的次数」分开，是这组数与它替换掉的那些外推的全部差别。**

// benchSpanDays 是合成库里每一天的根数（`buildDat` 用 `i/240` 分天）。
const benchSpanDays = 240

// openSyntheticStore 造一份 benchBars 根的库，并把它**摆成「可读」的状态**。
//
// ⛔ **coverage 与 verified 是【在包内直接置位】的，不走 CommitSpan/Verify** ——
// 理由要写下来，因为它绕开的正是让这次读取合法的那两道检查：
//
//	一、走 CommitSpan 要一份覆盖这些日子的**真日历**，而合成的那些「交易日」
//	   （`20200101 + i/240`）不是真日期，也不在任何一份日历里
//	二、`Verify` 会把 89 万条记录再走一遍 —— 那是**这次要测的那个量本身**，
//	   放进 setup 里会让「准备」和「被测」纠缠在一起
//
// ✅ 而它对**计时**无害，理由是可核的：`DaysWithBars` 与 `HasBars` 读到 `verified`
// 之后就只剩「扫 `.dat`」那一段，**它们的代价与 `verified` 是怎么变成 true 的无关**。
// 🔴 ⚠️ 而它对**正确性**当然不是无害的 —— 所以本文件**一个正确性断言都不下**：
// 那一半由 `equivalence_test.go` 在一份**正经走查过**的小库上负责。
// ⇒ 两个文件各答一半：**这里只答代价，那里只答对不对。**
func openSyntheticStore(tb testing.TB) (*Store, tickflow.Span) {
	tb.Helper()
	dir := tb.TempDir()
	writeSyntheticDat(tb, filepath.Join(dir, "1m.dat"))

	// ⚠️ `Intraday` 返回两个值 —— 零值 `IntradayPeriod` 是它**拒绝产出**的值，
	// 所以构造必须过它的手，不能直接写字面量。
	p, err := tickflow.Intraday(1)
	if err != nil {
		tb.Fatalf("造 1m 周期失败：%v", err)
	}
	s, truncated, err := Open(dir, p)
	if err != nil {
		tb.Fatalf("开库失败：%v", err)
	}
	if truncated != 0 {
		tb.Fatalf("合成库不该有残尾，却报了 %d —— 前提不成立，读数作废", truncated)
	}
	tb.Cleanup(func() { _ = s.Close() })

	lo := tickflow.TradingDay(20200101)
	hi := tickflow.TradingDay(20200101 + int32((benchBars-1)/benchSpanDays)%10000)
	span := tickflow.Span{From: lo, To: hi, Bars: benchBars,
		Days: (benchBars + benchSpanDays - 1) / benchSpanDays}
	s.meta.Coverage = []tickflow.Span{span}
	s.verified = map[tickflow.Span]bool{span: true}

	// 前提自检：这一步真的把库摆成了「可读」——否则下面量到的是一条 error 返回的耗时。
	if _, err := s.DaysWithBars(span); err != nil {
		tb.Fatalf("摆好的库读不了：%v —— 这组读数作废", err)
	}
	return s, span
}

// writeSyntheticDat 与 ReadShape 那组用同一套合成规则，**而它是另写的一份**。
//
// ⚠️ 复用那边的 `buildDat` 更省，而它返回的是**路径**、不造 `.meta`，
// 且它的名字与用途都绑在那组探针上。⇒ 这里另写，并把差别写明：
// **本文件要的是一个能被 `Open` 认出来的目录，那边要的是一个裸文件。**
func writeSyntheticDat(tb testing.TB, path string) {
	tb.Helper()
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	buf := make([]byte, 0, RecordSize*1024)
	for i := 0; i < benchBars; i++ {
		r := EncodeBar(tickflow.Bar{
			Ts:         int64(i) * 60000,
			TsEnd:      int64(i)*60000 + 60000,
			TradingDay: tickflow.TradingDay(20200101 + int32(i/benchSpanDays)%10000),
			Close:      1.5,
		})
		buf = append(buf, r[:]...)
		if len(buf) >= RecordSize*1024 {
			if _, err := f.Write(buf); err != nil {
				tb.Fatal(err)
			}
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		if _, err := f.Write(buf); err != nil {
			tb.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		tb.Fatal(err)
	}
	// 独立算一遍大小 —— 不走 CountRecords（拿 X 当前提去验 X）。
	st, err := os.Stat(path)
	if err != nil {
		tb.Fatal(err)
	}
	if want := int64(benchBars) * RecordSize; st.Size() != want {
		tb.Fatalf("造出来的 .dat 大小不对：%d 字节，要 %d", st.Size(), want)
	}
}

// BenchmarkDaysWithBarsWholeSpan 量【新读法】：一整段一次。
func BenchmarkDaysWithBarsWholeSpan(b *testing.B) {
	s, span := openSyntheticStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m, err := s.DaysWithBars(span)
		if err != nil {
			b.Fatal(err)
		}
		// 前提自检放在计时循环里也无妨（一次 map 长度）：
		// **一个恒返回空 map 的实现会在这里当场红**，而它跑得飞快。
		if len(m) == 0 {
			b.Fatal("新读法返回了空 map —— 这组读数量的是一条空转的路径")
		}
	}
}

// BenchmarkHasBarsFirstDay 量【旧读法·最好档】：那一天的记录在文件最前面。
//
// ⛔ 它与下面那条**必须成对读** —— `docs/design.md` 问五乙那一格的全部内容
// 就是「旧读法的代价是 1 … N，而新读法恒 ≈N」。
// **只量其中一档，就会把一个区间读成一个点。**
func BenchmarkHasBarsFirstDay(b *testing.B) {
	s, span := openSyntheticStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		has, err := s.HasBars(span.From)
		if err != nil {
			b.Fatal(err)
		}
		if !has {
			b.Fatal("第一天该有根 —— 构造不成立，读数作废")
		}
	}
}

// BenchmarkHasBarsLastDay 量【旧读法·中间档】：那一天的记录在文件最后。
func BenchmarkHasBarsLastDay(b *testing.B) {
	s, span := openSyntheticStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		has, err := s.HasBars(span.To)
		if err != nil {
			b.Fatal(err)
		}
		if !has {
			b.Fatal("最后一天该有根 —— 构造不成立，读数作废")
		}
	}
}

// BenchmarkHasBarsAbsentDay 量【旧读法·最坏档】：那一天缺席 ⇒ 扫完整表。
//
// 🔴 本仓那条：**缺口规划最忙的时候，正是库【没填满】的时候** —— 而那一档没有提前返回。
func BenchmarkHasBarsAbsentDay(b *testing.B) {
	s, span := openSyntheticStore(b)
	absent := span.To + 1 // 落在段内？不 —— 见下面那句
	// ⚠️ `HasBars` 先找「这一天落在哪一段 coverage 里」，找不到就返回「没拉过」而**不读文件**。
	// ⇒ 要量「缺席那一档」，那一天必须**落在段内**。这里把段撑大一天。
	s.meta.Coverage = []tickflow.Span{{From: span.From, To: absent, Bars: span.Bars, Days: span.Days}}
	s.verified = map[tickflow.Span]bool{s.meta.Coverage[0]: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		has, err := s.HasBars(absent)
		if err != nil {
			b.Fatal(err)
		}
		if has {
			b.Fatal("这一天不该有根 —— 构造不成立，读数作废")
		}
	}
}
