package segfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// TestSYN10VerifyPointsAtTheRealCause 走查报的错必须指得到**真因**。
//
// ⛔ 零值 `TradingDay` 不落在任何一段 coverage 里 ⇒ 它天然掉进
// 「谁的段都不属于 ⇒ 这才是真的损坏」那一支，而那句话**指向文件**：
// 人会去查磁盘、比长度、怀疑截断。**而真因是「源没填这个字段」，处置在另一头。**
//
// ⚠️ 这一条与 `TestAppendBarsRejectsZeroTradingDay` **不是同一件事**，
// 而两者都必须在：
//
//	SRC-7（AppendBars 拒零值）  守的是【未来写进来的】
//	SYN-10（走查指得到真因）    守的是【已经在盘上的】
//
// ⇒ SRC-7 落地之后，这种记录**只可能**来自本版写入口之外 ——
// 旧版写的、别的写者写的、手工造的。**而那正是走查存在的理由。**
// ⇒ 所以本条**绕开 AppendBars**，直接把字节写进 `.dat`：
// 走 AppendBars 的话它在写入口就被拦下，这条路一次都走不到。
func TestSYN10VerifyPointsAtTheRealCause(t *testing.T) {
	dir := t.TempDir()
	cal, k := testCalendar(t)

	// 一条正常记录 ＋ 一条 TradingDay 为零值的记录，直接写进 .dat。
	body := make([]byte, 0, RecordSize*2)
	for _, b := range []tickflow.Bar{
		{Ts: 1, TsEnd: 61000, TradingDay: 20200806, Close: 1.5},
		{Ts: 2, TsEnd: 62000, TradingDay: 0, Close: 1.5},
	} {
		r := EncodeBar(b)
		body = append(body, r[:]...)
	}
	if err := os.WriteFile(filepath.Join(dir, "1m.dat"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	s, truncated, err := Open(dir, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer s.Close()
	if truncated != 0 {
		t.Fatalf("前提没成立：不该有残尾，实得 %d 字节", truncated)
	}

	span := tickflow.Span{From: 20200806, To: 20200806, Bars: 1, Days: 1}
	if err := s.CommitSpan(cal, k, span, tickflow.OutcomeComplete); err != nil {
		t.Fatalf("前提没成立，扩 coverage 失败：%v", err)
	}

	err = s.Verify(span)
	if err == nil {
		t.Fatal("盘上有一条零值 TradingDay 的记录，走查却什么都没说")
	}
	if errors.Is(err, errRecordOutside) {
		t.Fatalf("走查把它报成了【文件损坏·不落在任何一段里】——\n"+
			"  那句话指向文件，而真因是「源没填 TradingDay」；两者的处置在不同的头上\n"+
			"  实得：%v", err)
	}
	if !errors.Is(err, errZeroTradingDay) {
		t.Fatalf("报错了，但不是 errZeroTradingDay：%v", err)
	}

	// ⛔ 对照：一条**真的**不落在任何段里的记录（合法日期、但不在 coverage 内）
	// 仍然要报 errRecordOutside —— 否则上面那条也可能是「它把什么都报成零值」。
	dir2 := t.TempDir()
	body2 := make([]byte, 0, RecordSize*2)
	for _, b := range []tickflow.Bar{
		{Ts: 1, TsEnd: 61000, TradingDay: 20200806, Close: 1.5},
		{Ts: 2, TsEnd: 62000, TradingDay: 20200811, Close: 1.5},
	} {
		r := EncodeBar(b)
		body2 = append(body2, r[:]...)
	}
	if err := os.WriteFile(filepath.Join(dir2, "1m.dat"), body2, 0o644); err != nil {
		t.Fatal(err)
	}
	s2, _, err := Open(dir2, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.CommitSpan(cal, k, span, tickflow.OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	if err := s2.Verify(span); !errors.Is(err, errRecordOutside) {
		t.Fatalf("一条合法日期但不在任何段里的记录，应当仍报 errRecordOutside，实得 %v", err)
	}
}
