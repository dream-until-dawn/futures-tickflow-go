package segfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件是 docs/design.md §6.1 那张不变量表的【片二】：6 条 × 2 侧 = 12 次。
//
//	B1 两个计数都要        B2 与一次真正的枚举比      B3 没走查过只能答「答不了」
//	C1 落盘之后才扩 coverage  C2 上游出错不许扩       C3a Open 时截残尾
//
// ⚠️ 本版仍然缺席的：C3b（截断要进 SyncReport）、D2b（两种结果都进 SyncReport）
// —— SyncReport 不存在，挪到「做 Source 那一版」。**整条缺席，不是测了一半。**

func bar(day tickflow.TradingDay, ts int64) tickflow.Bar {
	return tickflow.Bar{Ts: ts, TsEnd: ts + 60000, TradingDay: day, Close: 1.5}
}

// newStore 造一个空目录上的 Store，外加测试日历。
func newStore(t *testing.T) (*Store, tickflow.Calendar, tickflow.ProductKey) {
	t.Helper()
	cal, k := testCalendar(t)
	s, truncated, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("空目录不该有残尾，实得 %d 字节", truncated)
	}
	t.Cleanup(func() { s.Close() })
	return s, cal, k
}

// ───────────────── C3a：Open 时检出并截断残尾 ─────────────────

func TestInvariantC3a_Red(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "1m.dat")
	// 两条完整记录 + 30 字节半截。
	body := make([]byte, 0, RecordSize*2+30)
	for _, b := range []tickflow.Bar{bar(20200731, 1), bar(20200803, 2)} {
		r := EncodeBar(b)
		body = append(body, r[:]...)
	}
	body = append(body, make([]byte, 30)...)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, truncated, err := OpenDat(p)
	if err != nil {
		t.Fatalf("OpenDat 失败：%v", err)
	}
	defer f.Close()
	if truncated == 0 {
		t.Fatal("C3a 没有响：文件长度不是记录长的整数倍，却没被判成有残尾" +
			"\n（不截 ⇒ 下一次追加会落在那半截【后面】）")
	}
	if truncated != 30 {
		t.Fatalf("C3a：截掉的字节数应当是 30，实得 %d", truncated)
	}
	st, _ := f.Stat()
	if st.Size() != RecordSize*2 {
		t.Fatalf("C3a：截断后应当剩 %d 字节，实得 %d", RecordSize*2, st.Size())
	}
}

func TestInvariantC3a_Green(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "1m.dat")
	// 与 _Red 只差一件事：末尾那 30 个字节不写。
	body := make([]byte, 0, RecordSize*2)
	for _, b := range []tickflow.Bar{bar(20200731, 1), bar(20200803, 2)} {
		r := EncodeBar(b)
		body = append(body, r[:]...)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, truncated, err := OpenDat(p)
	if err != nil {
		t.Fatalf("OpenDat 失败：%v", err)
	}
	defer f.Close()
	if truncated != 0 {
		t.Fatalf("C3a 误伤：整数倍的文件被截掉了 %d 字节", truncated)
	}
	st, _ := f.Stat()
	if st.Size() != RecordSize*2 {
		t.Fatalf("C3a 误伤：文件被改动了，%d → %d", RecordSize*2, st.Size())
	}
}

// ───────── A1a/A1b 的【第二个入口】：Open 读进来的 coverage 也要过 ─────────
//
// ⚠️ 这不是新不变量，是同一条不变量的另一个入口。
// 它们此前只在 CommitSpan（写）那一侧执行 ⇒ 一份手改的 .meta 进得来。
//
// ⚠️ A1c（端点是交易日）**不在这一格**：它要日历，而 Open 没有。
// 那是一个被声明的边界，写在 Open 的注释里。

func TestInvariantA1b_RedOnOpen(t *testing.T) {
	dir := t.TempDir()
	// 有序但重叠 —— 所以红了只可能是 A1b。
	bad := `{"format":1,"coverage":[` +
		`{"from":20200731,"to":20200804,"bars":3,"days":3},` +
		`{"from":20200803,"to":20200806,"bars":3,"days":3}]}`
	if err := os.WriteFile(filepath.Join(dir, "1m.meta"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, err := Open(dir)
	if err == nil {
		s.Close()
		t.Fatal("A1b 在读那一侧没有响：Open 收下了一份重叠的 coverage" +
			"\n（不变量只在写那一侧设卡 ⇒ 手改的 .meta 进得来）")
	}
	if !errors.Is(err, errOverlap) {
		t.Fatalf("响了，但响的是别的判据：%v", err)
	}
}

func TestInvariantA1b_GreenOnOpen(t *testing.T) {
	dir := t.TempDir()
	// 与 _Red 只差第二段的起点：挪到前一段结束之后就不重叠了。
	good := `{"format":1,"coverage":[` +
		`{"from":20200731,"to":20200803,"bars":2,"days":2},` +
		`{"from":20200804,"to":20200806,"bars":3,"days":3}]}`
	if err := os.WriteFile(filepath.Join(dir, "1m.meta"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, err := Open(dir)
	if err != nil {
		t.Fatalf("A1b 在读那一侧误伤：合法的 coverage 被 Open 拒了：%v", err)
	}
	defer s.Close()
	if len(s.Coverage()) != 2 {
		t.Fatalf("Open 读出来的 coverage 应当有 2 段，实得 %v", s.Coverage())
	}
}

// ───────── .meta 的写：能测的那两条（原子性本身测不了）─────────
//
// ⚠️ 这一对**不是**不变量表里的编号，所以它不叫 TestInvariantXxx。
// 它测的是「写完之后目录干净、目标可解」——**而这两条都不是原子性**。
// 原子性靠的是 rename 的结构性论证，写在 writeMeta 的注释里。
// **把这两条读成「原子性被测了」，正是本仓反复栽的那一类。**

// TestMetaSurvivesCrashBeforeRename 测的是【失败路径】。
//
// ⚠️ 我原来写「原子性只能靠结构性论证」——那句话罩得太宽。
// 评审方指出并跑通了可测的那一半：
//
//	**「崩在中间」测不了，「中间状态是安全的」测得了** —— 而后者正是 temp+rename 在这里的全部用处。
//
// ⇒ 上面那条 LeavesNoTemp 测的是**成功路径**；这一条测的才是这一格存在的理由。
// 真正「崩在写的那一瞬间」仍然是声明的边界 —— **而结构性论证现在只罩那一小块，不罩整格。**
//
// ⛔ 而这一条【没有定点突变对照组，并且不可能有】—— 写清楚，别让人以为漏了：
//
//	它手工构造「好的 final + 半截的 tmp」再调 Open，**全程不经过 writeMeta**。
//	⇒ 它测的是「**Open 容不容得下这个中间态**」，
//	  不是「**writeMeta 会不会产生这个中间态**」。
//	⇒ 所以把 writeMeta 改回直接写目标，这条测试照样绿 —— 它本来就没在看那一侧。
//
// **两者之间那一环（「writeMeta 产生的中间态恰好是这一个」）仍然是结构性论证。**
// 这一格的诚实说法是：**测到的是一半，另一半靠读代码。**
// （我给它加过一格突变，结果是 BUILD ——
//
//	查下去才发现就算编译得过它也抓不到。**BUILD 那一档这次是把注意力引到了对的地方。**）
func TestMetaSurvivesCrashBeforeRename(t *testing.T) {
	s, cal, k := newStore(t)
	dir := s.dir
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, k,
		tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	s.Close()

	good, err := os.ReadFile(filepath.Join(dir, "1m.meta"))
	if err != nil {
		t.Fatal(err)
	}
	// 崩在 rename 之前的样子：临时文件半截，目标没动。
	if err := os.WriteFile(filepath.Join(dir, "1m.meta.tmp"), good[:len(good)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	s2, _, err := Open(dir)
	if err != nil {
		t.Fatalf("崩在 rename 之前，上一份好的 .meta 应当还能打开，实得 %v", err)
	}
	defer s2.Close()
	if len(s2.Coverage()) != 1 {
		t.Fatalf("上一份好的 coverage 应当原样在，实得 %v", s2.Coverage())
	}
}

func TestMetaWriteLeavesNoTemp(t *testing.T) {
	s, cal, k := newStore(t)
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, k,
		tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("写完之后目录里还留着临时文件 %s —— 它会冒充别的东西", e.Name())
		}
	}
	// 目标写完必须可解 —— 否则下一次 Open 直接走 E1a。
	b, err := os.ReadFile(filepath.Join(s.dir, "1m.meta"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeMeta(b); err != nil {
		t.Fatalf("写出去的 .meta 自己解不开：%v", err)
	}
}

// ───────────────── C1：coverage 只能在数据落盘之后扩大 ─────────────────

func TestInvariantC1_Red(t *testing.T) {
	s, cal, k := newStore(t)
	// 没有 AppendBars，直接扩 —— 这正是「声称拉过而其实没有」的那一步。
	err := s.CommitSpan(cal, k, tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}, OutcomeComplete)
	if err == nil {
		t.Fatal("C1 没有响：数据还没落盘就扩了 coverage" +
			"\n（崩溃就会留下「声称拉过而其实没有」——静默漏数据且不会自愈）")
	}
	if !errors.Is(err, errNotDurable) {
		t.Fatalf("C1 响了，但响的是别的判据：%v", err)
	}
	if len(s.Coverage()) != 0 {
		t.Fatalf("C1：被拒之后 coverage 不该变，实得 %v", s.Coverage())
	}
}

func TestInvariantC1_Green(t *testing.T) {
	s, cal, k := newStore(t)
	// 与 _Red 只差一件事：先把数据落盘。
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, k, tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}, OutcomeComplete); err != nil {
		t.Fatalf("C1 误伤：数据已经落盘，扩 coverage 却被拒：%v", err)
	}
	if len(s.Coverage()) != 1 {
		t.Fatalf("C1：coverage 应当有 1 段，实得 %v", s.Coverage())
	}
}

// ───────────────── C2：上游出错时不许扩 coverage ─────────────────

func TestInvariantC2_Red(t *testing.T) {
	s, cal, k := newStore(t)
	// 数据已经落盘 —— 所以红了只可能是 C2，不会是 C1。
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	span := tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}
	for _, out := range []Outcome{OutcomeFailed, Outcome(0)} {
		err := s.CommitSpan(cal, k, span, out)
		if err == nil {
			t.Fatalf("C2 没有响：outcome=%d 也扩了 coverage"+
				"\n（把空响应/超时当成「确认没有」）", int(out))
		}
		if !errors.Is(err, errIncomplete) {
			t.Fatalf("C2 响了，但响的是别的判据（outcome=%d）：%v", int(out), err)
		}
		if errors.Is(err, errNotDurable) {
			t.Fatalf("这个 fixture 触发了 C1 —— 那它证明不了 C2：%v", err)
		}
	}
	if len(s.Coverage()) != 0 {
		t.Fatalf("C2：被拒之后 coverage 不该变，实得 %v", s.Coverage())
	}
}

func TestInvariantC2_Green(t *testing.T) {
	s, cal, k := newStore(t)
	// 与 _Red 只差一件事：outcome 是【完整成功】。
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, k, tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}, OutcomeComplete); err != nil {
		t.Fatalf("C2 误伤：完整成功的响应被拒：%v", err)
	}
}

// ───────── 输入的第一维：段数 ≥ 2 ─────────
//
// ⚠️ 这一组是补的，而它补的不是一条不变量，是**一个输入维度**。
//
// 上一版 B1/B2/B3 的每一个用例都只有【一段】—— 21 格突变全塌、四包全绿，
// 而两个功能级缺陷活着（Verify 走整个文件 / HasBars 按段答）。
// 评审方是从「为什么 21 格全绿」到达同一处的，我是从「我为什么没发现」到达的。
//
// > **突变对照组证明的是「这些测试在真的守着」，不是「这些测试覆盖了输入空间」。**
// > 每条不变量的红/绿之外，还要问：**这条的输入空间有几维，我固定了哪几维。**
//
// ⇒ 这一组把「段数」这一维从 1 放开到 2。

// twoSpanStore 造一个【两段不相邻】的库：0731 一段、0806 一段，中间隔着交易日。
func twoSpanStore(t *testing.T) (*Store, []tickflow.Span) {
	t.Helper()
	s, cal, k := newStore(t)
	a := tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, k, a, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	b := tickflow.Span{From: 20200806, To: 20200806, Bars: 1, Days: 1}
	if err := s.AppendBars([]tickflow.Bar{bar(20200806, 3)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, k, b, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	if len(s.Coverage()) != 2 {
		t.Fatalf("这一格要的是【两段】，实得 %v —— 若它们被合并了，这一维就没放开", s.Coverage())
	}
	return s, []tickflow.Span{a, b}
}

func TestMultiSpanVerify(t *testing.T) {
	s, spans := twoSpanStore(t)
	// 两段各自走查都必须过 —— 别的段的记录是【别的段的】，不是损坏。
	for i, sp := range spans {
		if err := s.Verify(sp); err != nil {
			t.Fatalf("第 %d 段走查失败：%v"+
				"\n（上一版这里必报 errRecordOutside ⇒ 任何有缺口的序列都通不过，"+
				"而那正是同步中的常态；更坏的是它把一个【健康的库】报成【损坏】）", i, err)
		}
	}
}

func TestMultiSpanHasBars(t *testing.T) {
	s, spans := twoSpanStore(t)
	for _, sp := range spans {
		if err := s.Verify(sp); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		day  tickflow.TradingDay
		want bool
		why  string
	}{
		{20200731, true, "第一段里，有 2 根"},
		{20200806, true, "第二段里，有 1 根"},
		{20200803, false, "两段【之间】，不在任何 coverage 里 ⇒ 没拉过，不是「确认没有」"},
	} {
		got, err := s.HasBars(c.day)
		if err != nil {
			t.Errorf("HasBars(%s) 报错：%v（%s）", c.day, err, c.why)
			continue
		}
		if got != c.want {
			t.Errorf("HasBars(%s) = %v，应当 %v（%s）", c.day, got, c.want, c.why)
		}
	}
}

func TestMultiSpanTrulyOrphanRecordIsCorruption(t *testing.T) {
	// 而「谁的段都不属于」仍然必须是损坏 —— 否则上面那一改就把 B2 也放松了。
	s, cal, k := newStore(t)
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200806, 2)}); err != nil {
		t.Fatal(err)
	}
	sp := tickflow.Span{From: 20200731, To: 20200731, Bars: 1, Days: 1}
	if err := s.CommitSpan(cal, k, sp, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	// 20200806 不在任何一段里 ⇒ 它是孤儿记录。
	err := s.Verify(sp)
	if err == nil {
		t.Fatal("一条不属于任何 coverage 段的记录被放过了 —— 那是真的损坏")
	}
	if !errors.Is(err, errRecordOutside) {
		t.Fatalf("响了，但响的是别的判据：%v", err)
	}
}

// ───────────────── B1：两个计数都要比 ─────────────────

func TestInvariantB1_Red(t *testing.T) {
	s, _, _ := newStore(t)
	// 三条记录，两个交易日。bars 报对（3），days 报错（说 3，实为 2）。
	if err := s.AppendBars([]tickflow.Bar{
		bar(20200731, 1), bar(20200731, 2), bar(20200803, 3),
	}); err != nil {
		t.Fatal(err)
	}
	span := tickflow.Span{From: 20200731, To: 20200803, Bars: 3, Days: 3}
	err := s.Verify(span)
	if err == nil {
		t.Fatal("B1 没有响：days 对不上却通过了" +
			"\n（只比 bars ⇒ 某天起点丢失时 bars 仍然相符，那天会读成「拉过，确认没有」）")
	}
	if !errors.Is(err, errDaysMismatch) {
		t.Fatalf("B1 响了，但响的是别的判据：%v", err)
	}
	// fixture 要坏对地方：bars 是对的，所以【不该】触发 bars 那一条。
	if errors.Is(err, errBarsMismatch) {
		t.Fatalf("这个 fixture 同时触发了 bars 那一条 —— 那它证明不了「days 也要比」：%v", err)
	}
}

func TestInvariantB1_Green(t *testing.T) {
	s, _, _ := newStore(t)
	// 与 _Red 只差一个数字：days 报 2（实为 2）。
	if err := s.AppendBars([]tickflow.Bar{
		bar(20200731, 1), bar(20200731, 2), bar(20200803, 3),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(tickflow.Span{From: 20200731, To: 20200803, Bars: 3, Days: 2}); err != nil {
		t.Fatalf("B1 误伤：两个计数都对，却报错：%v", err)
	}
}

// ───────────────── B2：与一次真正的枚举比，不与长度除法比 ─────────────────

func TestInvariantB2_Red(t *testing.T) {
	s, _, _ := newStore(t)
	// 三条记录，其中一条的交易日【落在本段之外】。
	// ⚠️ 关键：文件长度 ÷ 记录长 == 3 == span.Bars ⇒ **长度除法会放它过去。**
	if err := s.AppendBars([]tickflow.Bar{
		bar(20200731, 1), bar(20200803, 2), bar(20200810, 3), // 20200810 不在 [0731,0803] 里
	}); err != nil {
		t.Fatal(err)
	}
	span := tickflow.Span{From: 20200731, To: 20200803, Bars: 3, Days: 3}

	// 先把「长度除法会通过」这件事钉住 —— 否则这一格证明不了它想证明的东西。
	st, err := os.Stat(filepath.Join(s.dir, "1m.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if got := CountRecords(st.Size()); got != int64(span.Bars) {
		t.Fatalf("fixture 不成立：长度除法给出 %d，而 span.Bars=%d —— "+
			"它们必须相等，这一格才是在测「枚举 vs 除法」", got, span.Bars)
	}

	err = s.Verify(span)
	if err == nil {
		t.Fatal("B2 没有响：一条记录落在本段之外，而长度除法看不出来" +
			"\n（「长度 ÷ 记录长」抓得住少了/多了/残尾，抓不住「记录都在而读不到」）")
	}
	if !errors.Is(err, errRecordOutside) {
		t.Fatalf("B2 响了，但响的是别的判据：%v", err)
	}
}

func TestInvariantB2_Green(t *testing.T) {
	s, _, _ := newStore(t)
	// 与 _Red 只差一件事：那条越界的记录换成段内的。
	if err := s.AppendBars([]tickflow.Bar{
		bar(20200731, 1), bar(20200803, 2), bar(20200803, 3),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(tickflow.Span{From: 20200731, To: 20200803, Bars: 3, Days: 2}); err != nil {
		t.Fatalf("B2 误伤：记录都在段内，却报错：%v", err)
	}
}

// ───────────────── B3：没走查过时只能答「答不了」 ─────────────────

func TestInvariantB3_Red(t *testing.T) {
	s, cal, k := newStore(t)
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	span := tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}
	if err := s.CommitSpan(cal, k, span, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	// 没有 Verify ⇒ 那两个计数什么也不意味着。
	_, err := s.HasBars(20200731)
	if err == nil {
		t.Fatal("B3 没有响：这一段还没走查过，却给出了一个肯定的答案" +
			"\n（「没验过」和「验过了，是空的」都答「确认没有」⇒ 那两个计数只是把洞挪了个位置）")
	}
	if !errors.Is(err, tickflow.ErrSpanUnverified) {
		t.Fatalf("B3 响了，但响的不是 ErrSpanUnverified：%v", err)
	}
}

func TestInvariantB3_Green(t *testing.T) {
	s, cal, k := newStore(t)
	if err := s.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	span := tickflow.Span{From: 20200731, To: 20200731, Bars: 2, Days: 1}
	if err := s.CommitSpan(cal, k, span, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	// 与 _Red 只差一件事：走查过了。
	if err := s.Verify(span); err != nil {
		t.Fatalf("走查失败：%v", err)
	}
	has, err := s.HasBars(20200731)
	if err != nil {
		t.Fatalf("B3 误伤：走查过了还答不了：%v", err)
	}
	if !has {
		t.Fatal("B3：这一天有 2 根，却答成没有")
	}

	// ⛔ 而【段内另一天一根都没有】那一格，必须答「没有」。
	//
	// 这一格是补的：上一版这一对用的是【单日区间】，
	// 而在单日区间上「整段有没有根」与「那一天有没有根」给出同一个答案
	// ⇒ 两种实现在那个输入上**分不开**，于是一个「拿 sp.Bars 当那一天的答案」
	// 的实现照样全绿。**一条不变量的射程，是由它的测试输入划的。**
	s2, cal2, k2 := newStore(t)
	if err := s2.AppendBars([]tickflow.Bar{bar(20200731, 1), bar(20200731, 2)}); err != nil {
		t.Fatal(err)
	}
	wide := tickflow.Span{From: 20200731, To: 20200803, Bars: 2, Days: 1}
	if err := s2.CommitSpan(cal2, k2, wide, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	if err := s2.Verify(wide); err != nil {
		t.Fatalf("走查失败：%v", err)
	}
	if has, err := s2.HasBars(20200803); err != nil || has {
		t.Fatalf("B3：20200803 在这一段里、一根都没有、而这一段走查过了 "+
			"⇒ 应当答【确认没有】。实得 has=%v err=%v"+
			"\n（答「有」说明它拿的是整段的 Bars —— 那两个计数说不出是哪一天）", has, err)
	}
	// 而【不在任何 coverage 里】的那天是「没拉过」，不是「答不了」，也不是「确认没有」。
	has, err = s.HasBars(20200806)
	if err != nil {
		t.Fatalf("B3 误伤：没拉过的那天不该报错：%v", err)
	}
	if has {
		t.Fatal("B3：没拉过的那天不该答「有」")
	}
}
