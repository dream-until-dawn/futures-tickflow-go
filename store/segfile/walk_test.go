package segfile

import (
	"crypto/md5"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— Walk（v0.6 片 B）：读口子，边扫边核 ——
//
// 库形状都取 twoSpanLib：coverage [d0,d1] 与 [d3,d4]（d2 是两段之间有交易的空档），每段 2 条。
//	d0=20200805 d1=20200806 d2=20200807 d3=20200812 d4=20200813 d5=20200814

var (
	d0, d1, d2, d3, d4, d5 = tradingDays[0], tradingDays[1], tradingDays[2], tradingDays[3], tradingDays[4], tradingDays[5]
)

// reopen 关库 ⇒ 施加损坏 ⇒ 重开，并确认坏记录没被开库时截掉。
func reopen(t *testing.T, s *Store, dir string, damage func(t *testing.T, dir string)) *Store {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("关库失败：%v", err)
	}
	damage(t, dir)
	s2, truncated, err := Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("前提没成立：重开砍掉了 %d 字节 ⇒ 坏记录没留住，读数作废", truncated)
	}
	t.Cleanup(func() { s2.Close() })
	return s2
}

func datPath(t *testing.T, dir string) string {
	t.Helper()
	var dat string
	filepath.Walk(dir, func(p string, fi os.FileInfo, e error) error {
		if e == nil && !fi.IsDir() && strings.HasSuffix(p, ".dat") {
			dat = p
		}
		return e
	})
	if dat == "" {
		t.Fatal("没找到 .dat —— 构造作废")
	}
	return dat
}

// overwriteRecord 把第 i 条记录原地换成 b（绕过写入口）。
func overwriteRecord(t *testing.T, dir string, i int64, b tickflow.Bar) {
	t.Helper()
	f, err := os.OpenFile(datPath(t, dir), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rec := EncodeBar(b)
	if _, err := f.WriteAt(rec[:], i*RecordSize); err != nil {
		t.Fatal(err)
	}
}

// badLibs 是一批坏库（加一个健康库），每格带它该报的哨兵（nil ＝ 全过）。
var badLibs = []struct {
	name   string
	damage func(t *testing.T, dir string)
	want   error
	whole  bool // 全库错误 ⇒ 每一段都坏；否则只有第二段坏
}{
	{"健康库", nil, nil, false},
	{"零值记录", func(t *testing.T, dir string) {
		appendRaw(t, dir, tickflow.Bar{Ts: 1, TsEnd: 2, TradingDay: 0, Close: 1})
	}, errZeroTradingDay, true},
	{"倒序记录", func(t *testing.T, dir string) {
		appendRaw(t, dir, bar(d0, 99))
	}, errRecordDisorder, true},
	{"段外记录", func(t *testing.T, dir string) {
		appendRaw(t, dir, bar(d5, int64(d5)))
	}, errRecordOutside, true},
	{"第二段多一条（bars 对不上）", func(t *testing.T, dir string) {
		appendRaw(t, dir, bar(d4, int64(d4)+1))
	}, errBarsMismatch, false},
	{"第二段少一天（bars 相同而 days 对不上）", func(t *testing.T, dir string) {
		overwriteRecord(t, dir, 2, bar(d4, int64(d4)-1)) // [d3,d4] 两条都变成 d4
	}, errDaysMismatch, false},
}

// guard: Walk 与 VerifyCoverage 喂同一个核对器 —— 同一批坏库上结论相同（哨兵相同、置 verified 的段相同）。
func TestWalkAgreesWithVerifyCoverage(t *testing.T) {
	for _, c := range badLibs {
		t.Run(c.name, func(t *testing.T) {
			build := func() *Store {
				s, dir := twoSpanLib(t)
				if c.damage != nil {
					s = reopen(t, s, dir, c.damage)
				}
				return s
			}
			// —— 参照：VerifyCoverage（逐条 ReadAt）——
			sv := build()
			cov := sv.Coverage()
			if len(cov) != 2 {
				t.Fatalf("前提没成立：期望 2 段，实得 %v", cov)
			}
			res, err := sv.VerifyCoverage()
			if err != nil {
				t.Fatalf("VerifyCoverage 跑不起来：%v", err)
			}
			// —— 被测：Walk（缓冲读），另开一份同样的库，区间取第一段 ——
			sw := build()
			calls := 0
			werr := sw.Walk(d0, d1, func(tickflow.Bar) bool { calls++; return true })

			if c.want == nil {
				if werr != nil {
					t.Fatalf("健康库上 Walk 报了：%v", werr)
				}
				if calls != 2 {
					t.Errorf("健康库上 [d0,d1] 回调 %d 次，应为 2", calls)
				}
			} else if !errors.Is(werr, c.want) {
				t.Errorf("Walk 报的不是 %v：%v", c.want, werr)
			}
			for j, sp := range cov {
				ref := res[sp.Key()]
				if c.want != nil && (j == 1 || c.whole) && !errors.Is(ref, c.want) {
					t.Errorf("前提：参照实现在 %v 上应报 %v，实得 %v", sp, c.want, ref)
				}
				_, derr := sw.DaysWithBars(sp)
				walkVerified := !errors.Is(derr, tickflow.ErrSpanUnverified)
				if walkVerified != (ref == nil) {
					t.Errorf("%v：Walk 之后置 verified=%v，而 VerifyCoverage 的结论是 %v —— 两边对「这一段过没过」答得不一样",
						sp, walkVerified, ref)
				}
			}
		})
	}
}

func TestWalkRangeAndOrder(t *testing.T) {
	s, _ := twoSpanLib(t)
	cells := []struct {
		name     string
		from, to tickflow.TradingDay
		want     []tickflow.TradingDay // nil ⇒ 应报 errWalkRange 且一次都不回调
	}{
		{"整段 [d0,d1]", d0, d1, []tickflow.TradingDay{d0, d1}},
		{"段首 [d0,d0]", d0, d0, []tickflow.TradingDay{d0}},
		{"段尾 [d4,d4]", d4, d4, []tickflow.TradingDay{d4}},
		{"第二段 [d3,d4]", d3, d4, []tickflow.TradingDay{d3, d4}},
		{"跨过两段之间的空档 [d1,d3] ⇒ 拒", d1, d3, nil},
		{"空档里 [d2,d2] ⇒ 拒", d2, d2, nil},
		{"段外 [d5,d5] ⇒ 拒", d5, d5, nil},
	}
	for _, ce := range cells {
		var got []tickflow.TradingDay
		err := s.Walk(ce.from, ce.to, func(b tickflow.Bar) bool { got = append(got, b.TradingDay); return true })
		if ce.want == nil {
			if !errors.Is(err, errWalkRange) || len(got) != 0 || !strings.Contains(err.Error(), "没拉过") {
				t.Errorf("%s：err=%v 回调 %v，应报 errWalkRange（说「没拉过」）且不回调", ce.name, err, got)
			}
			continue
		}
		if err != nil || len(got) != len(ce.want) {
			t.Errorf("%s：err=%v 回调 %v，应为 %v", ce.name, err, got, ce.want)
			continue
		}
		for i := range got {
			if got[i] != ce.want[i] {
				t.Errorf("%s：第 %d 根是 %s，应为 %s", ce.name, i, got[i], ce.want[i])
			}
		}
	}
	if err := s.Walk(d1, d0, func(tickflow.Bar) bool { return true }); err == nil {
		t.Error("from 晚于 to 应报错")
	}
	if err := s.Walk(d0, d1, nil); err == nil {
		t.Error("fn 为 nil 应报错")
	}
}

// 报错 ⇒ 回调作废；而坏的那一条【没核过】就不许交出去。
func TestWalkChecksBeforeDeliveringAndErrorVoidsCallbacks(t *testing.T) {
	s, dir := twoSpanLib(t)
	// 第 5 条（索引 4）是一条落在 [d3,d4] 里、却让顺序倒退的记录：d4 之后又来一条 d3。
	s = reopen(t, s, dir, func(t *testing.T, dir string) { appendRaw(t, dir, bar(d3, int64(d3)+5)) })
	var got []int64
	err := s.Walk(d3, d4, func(b tickflow.Bar) bool { got = append(got, b.Ts); return true })
	if !errors.Is(err, errRecordDisorder) {
		t.Fatalf("应报 errRecordDisorder：%v", err)
	}
	// 区间里核过的是索引 2、3 两条；坏的那条（索引 4，也落在区间里）不许交出去。
	if len(got) != 2 {
		t.Errorf("回调 %d 根 %v，应恰好 2 根 —— 坏的那一条落在区间里，而它没核过就不该交出去", len(got), got)
	}
	for _, sp := range s.Coverage() {
		if _, derr := s.DaysWithBars(sp); !errors.Is(derr, tickflow.ErrSpanUnverified) {
			t.Errorf("全库错误之后 %v 不该被置成走查过：%v", sp, derr)
		}
	}
}

// fn 返回 false ⇒ 只停回调，扫描走完、结论照给。
func TestWalkStopEarlyStillConcludes(t *testing.T) {
	t.Run("健康库：停在第一根 ⇒ nil，两段都置走查过", func(t *testing.T) {
		s, _ := twoSpanLib(t)
		calls := 0
		if err := s.Walk(d0, d1, func(tickflow.Bar) bool { calls++; return false }); err != nil {
			t.Fatalf("健康库：%v", err)
		}
		if calls != 1 {
			t.Errorf("fn 返回 false 之后又被调了：共 %d 次", calls)
		}
		for _, sp := range s.Coverage() {
			if _, err := s.DaysWithBars(sp); err != nil {
				t.Errorf("提前停之后 %v 没有结论：%v —— 停回调不许顺带停扫描", sp, err)
			}
		}
	})
	t.Run("第二段多一条：在第一段停下 ⇒ 仍报 bars 对不上", func(t *testing.T) {
		s, dir := twoSpanLib(t)
		s = reopen(t, s, dir, func(t *testing.T, dir string) { appendRaw(t, dir, bar(d4, int64(d4)+1)) })
		calls := 0
		err := s.Walk(d0, d1, func(tickflow.Bar) bool { calls++; return false })
		cov := s.Coverage()
		// ⛔ 报错必须点名【第二段】，而第一段必须置成走查过 ——
		// 只断言「报 errBarsMismatch」的话，一个「停回调就停扫描」的实现会让【第一段】计数不足，
		// 替着满足这条断言（片 B 变异验收 W2 第一轮就是这样绿的）。
		second := "[" + cov[1].From.String() + ", " + cov[1].To.String() + "]"
		if calls != 1 || !errors.Is(err, errBarsMismatch) || !strings.Contains(err.Error(), second) {
			t.Errorf("回调 %d 次、err=%v；应为 1 次且报第二段 %s 的 errBarsMismatch —— 提前停若不扫完，nil 就分不清「核过」与「没核完」", calls, err, second)
		}
		if _, derr := s.DaysWithBars(cov[0]); derr != nil {
			t.Errorf("第一段 %v 应已走查过：%v", cov[0], derr)
		}
		if strings.Contains(err.Error(), "["+cov[0].From.String()+", ") {
			t.Errorf("第一段是好的，报文里却点了它：%v", err)
		}
	})
}

// Walk 不动文件偏移、不改文件；之后的 AppendBars 落在文件尾。
//
// ⚠️ 读数：AppendBars 自己会先 Seek 到尾（store.go）⇒「追加落在尾部」那一格【挡不住】Walk 里动偏移 ——
// 所以另有一格直接断言偏移前后相同（片 B 变异验收里「Walk 里 Seek(0)」只让那一格红）。
func TestWalkLeavesOffsetAndFilesAlone(t *testing.T) {
	s, dir := twoSpanLib(t)
	dat := datPath(t, dir)
	sum := func() [16]byte {
		b, err := os.ReadFile(dat)
		if err != nil {
			t.Fatal(err)
		}
		return md5.Sum(b)
	}
	metaSum := func() [16]byte {
		var out [16]byte
		filepath.Walk(dir, func(p string, fi os.FileInfo, e error) error {
			if e == nil && !fi.IsDir() && strings.HasSuffix(p, ".meta") {
				b, _ := os.ReadFile(p)
				out = md5.Sum(b)
			}
			return e
		})
		return out
	}
	off0, err := s.dat.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if off0 == 0 {
		t.Fatal("前提没成立：Walk 之前偏移就在文件头 ⇒ 「Walk 把偏移挪到头」这一格看不出来")
	}
	datBefore, metaBefore := sum(), metaSum()
	if err := s.Walk(d0, d4, func(tickflow.Bar) bool { return true }); err == nil {
		// [d0,d4] 跨空档，会被拒 —— 换一段合法的再走
		t.Fatal("前提没成立：[d0,d4] 跨空档应被拒")
	}
	if err := s.Walk(d3, d4, func(tickflow.Bar) bool { return true }); err != nil {
		t.Fatal(err)
	}
	off1, _ := s.dat.Seek(0, io.SeekCurrent)
	if off1 != off0 {
		t.Errorf("Walk 前后文件偏移 %d → %d —— Walk 不许动偏移", off0, off1)
	}
	if sum() != datBefore || metaSum() != metaBefore {
		t.Error("Walk 改了 .dat 或 .meta")
	}
	st0, _ := os.Stat(dat)
	extra := bar(d4, int64(d4)+7)
	if err := s.AppendBars([]tickflow.Bar{extra}); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(dat)
	if st1.Size()-st0.Size() != RecordSize {
		t.Fatalf(".dat 长度 %d → %d，应正好多出 %d", st0.Size(), st1.Size(), RecordSize)
	}
	buf := make([]byte, RecordSize)
	if _, err := s.dat.ReadAt(buf, st0.Size()); err != nil {
		t.Fatal(err)
	}
	if b, _ := DecodeBar(buf); b.Ts != extra.Ts || b.TradingDay != extra.TradingDay {
		t.Errorf("文件尾那一条是 %+v，应为刚追加的 %+v", b, extra)
	}
}
