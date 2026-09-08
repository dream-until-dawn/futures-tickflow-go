package segfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// Store 是一个「品种 + 周期」的落盘目录：一份 `.dat` 加一份 `.meta`。
//
// 本文件承的是不变量 B1 B2 B3 C1 C2 C3a（编号见 docs/design.md §6.1 那张表）。
//
// ⚠️ 缺席的：C3b（截断要进 SyncReport）与 D2b（两种结果都进 SyncReport）——
// `SyncReport` 还不存在（tools/doccheck/pending.txt）⇒ 挪到「做 Source 那一版」。
// **它们是整条缺席，不是测了一半。**
type Store struct {
	dir string
	dat *os.File

	meta Meta

	// verified 记「这一段【走查过】没有」。B3 要它：**没走查过的时候，
	// 那两个计数什么也不意味着**，不许回答「拉过，确认没有」。
	//
	// ⚠️ 它是【进程内】状态，不落盘：一份 .meta 被另一个进程改过之后，
	// 上一次走查的结论就不再成立。落盘会让它变成一个会过期的抄件。
	verified map[tickflow.Span]bool
}

// Outcome 是上游这一次响应的结果。C2 要它。
//
// ⚠️ 它不是 bool，而且**零值不合法** —— 忘了填的调用方会当场被拒，
// 而不是拿到 false 或 true 里的某一个。
// 「问了但没问成」和「问了，确认没有」在返回值上可以长得很像
// （空数组 / 错误 / 超时后的空响应），所以这件事必须由调用方**说出来**。
type Outcome int

const (
	// OutcomeComplete 上游【完整成功】。只有它允许扩 coverage。
	OutcomeComplete Outcome = iota + 1
	// OutcomeFailed 出错、超时、或响应不完整。落盘可以，扩 coverage 不行。
	OutcomeFailed
)

var (
	errNotDurable     = errors.New("segfile: coverage 想扩到 .dat 还没有的数据上")
	errIncomplete     = errors.New("segfile: 上游没有完整成功，不许扩 coverage")
	errBarsMismatch   = errors.New("segfile: bars 与走查数出来的对不上")
	errDaysMismatch   = errors.New("segfile: days 与走查数出来的对不上")
	errRecordOutside  = errors.New("segfile: 记录的交易日落在本段之外")
	errRecordDisorder = errors.New("segfile: 记录的交易日不是非降序")
)

// Open 打开一个落盘目录。返回被截掉的残尾字节数（C3a，见 OpenDat）。
func Open(dir string) (s *Store, truncated int64, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, err
	}
	f, truncated, err := OpenDat(filepath.Join(dir, "1m.dat"))
	if err != nil {
		return nil, 0, err
	}
	st := &Store{dir: dir, dat: f, verified: map[tickflow.Span]bool{}}
	b, err := os.ReadFile(filepath.Join(dir, "1m.meta"))
	switch {
	case err == nil:
		m, derr := DecodeMeta(b)
		if derr != nil {
			f.Close()
			return nil, 0, derr
		}
		st.meta = *m
	case os.IsNotExist(err):
		v := FormatVersion
		st.meta = Meta{Format: &v}
	default:
		f.Close()
		return nil, 0, err
	}
	return st, truncated, nil
}

// Close 关掉底下的文件。
func (s *Store) Close() error { return s.dat.Close() }

// Coverage 返回当前的 coverage（拷贝）。
func (s *Store) Coverage() []tickflow.Span {
	return append([]tickflow.Span(nil), s.meta.Coverage...)
}

// AppendBars 只把这一批根写进 `.dat` 并落到盘上，**不碰 coverage**。
//
// ⚠️ 这是 C1 的一半：**coverage 只能在数据落盘之后扩大。**
// 顺序反了，崩溃就会留下「声称拉过而其实没有」——正是最危险的那个方向
// （静默漏数据且不会自愈）。反过来（先写数据后扩 coverage）崩溃留下的是
// 「拉过却没记」⇒ 重拉一遍，**吵而不丢**。
//
// ⇒ 所以本方法与 CommitSpan 是**两个**方法：让「先写后记」成为唯一写得出来的顺序。
func (s *Store) AppendBars(bars []tickflow.Bar) error {
	if len(bars) == 0 {
		return nil
	}
	buf := make([]byte, 0, len(bars)*RecordSize)
	for _, b := range bars {
		r := EncodeBar(b)
		buf = append(buf, r[:]...)
	}
	if _, err := s.dat.Seek(0, os.SEEK_END); err != nil {
		return err
	}
	if _, err := s.dat.Write(buf); err != nil {
		return err
	}
	// ⚠️ 必须 Sync：C1 说的是「数据【落盘】之后」，不是「写进页缓存之后」。
	return s.dat.Sync()
}

// CommitSpan 扩 coverage。
//
//	C1  先核对数据【真的在盘上】：.dat 的完整记录数必须够得上这一段声称的 bars
//	C2  只有 OutcomeComplete 才允许扩；其余一律拒绝
//	A1a/A1b/A1c/A2  经 NormalizeCoverage 走一遍（升序、不重叠、端点是交易日、按交易日合并）
func (s *Store) CommitSpan(cal tickflow.Calendar, k tickflow.ProductKey,
	span tickflow.Span, out Outcome) error {
	if out != OutcomeComplete {
		return fmt.Errorf("%w: outcome=%d"+
			"——「问了但没问成」和「问了，确认没有」在返回值上长得很像，"+
			"所以这件事必须由调用方说出来", errIncomplete, int(out))
	}
	st, err := s.dat.Stat()
	if err != nil {
		return err
	}
	have := CountRecords(st.Size())
	want := int64(span.Bars)
	for _, sp := range s.meta.Coverage {
		want += int64(sp.Bars)
	}
	if have < want {
		return fmt.Errorf("%w: .dat 里有 %d 条完整记录，而 coverage 加上这一段要 %d 条"+
			"——先落盘，再扩 coverage", errNotDurable, have, want)
	}
	next := append(append([]tickflow.Span(nil), s.meta.Coverage...), span)
	merged, err := NormalizeCoverage(cal, k, next)
	if err != nil {
		return err
	}
	s.meta.Coverage = merged
	return s.writeMeta()
}

func (s *Store) writeMeta() error {
	b, err := EncodeMeta(s.meta)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "1m.meta"), b, 0o644)
}

// Verify 走查一段：**逐条读出来数**，再和 `.meta` 里那两个被写下去的计数比。
//
// ⛔ 判据写死了（B2）：**和一次真正的枚举比，不和一个算出来的期望比。**
// 「文件长度 ÷ 记录长」抓得住少了/多了/残尾，**抓不住「记录都在而读不到」**——
// 而后者恰恰是这一节要防的那一类：**读不到的东西和不存在的东西长得一样。**
//
// B1：**两个计数都要比**。只比 bars 不够——记录一条没少而某天的起点找不到了时，
// bars 仍然相符，于是那一天读成「拉过，确认没有」。days 对不上就报错
// ⇒ **缺席重新只意味着损坏。**
func (s *Store) Verify(span tickflow.Span) error {
	st, err := s.dat.Stat()
	if err != nil {
		return err
	}
	n := CountRecords(st.Size())
	buf := make([]byte, RecordSize)
	bars := 0
	days := 0
	var prev tickflow.TradingDay
	for i := int64(0); i < n; i++ {
		if _, err := s.dat.ReadAt(buf, i*RecordSize); err != nil {
			return fmt.Errorf("segfile: 读第 %d 条记录失败: %w", i, err)
		}
		b, err := DecodeBar(buf)
		if err != nil {
			return err
		}
		if b.TradingDay < span.From || b.TradingDay > span.To {
			return fmt.Errorf("%w: 第 %d 条记录是 %s，而本段是 [%s, %s]",
				errRecordOutside, i, b.TradingDay, span.From, span.To)
		}
		if b.TradingDay < prev {
			return fmt.Errorf("%w: 第 %d 条是 %s，而上一条是 %s",
				errRecordDisorder, i, b.TradingDay, prev)
		}
		if b.TradingDay != prev {
			days++
			prev = b.TradingDay
		}
		bars++
	}
	if bars != span.Bars {
		return fmt.Errorf("%w: 走查数出 %d 条，而 .meta 记的是 %d 条", errBarsMismatch, bars, span.Bars)
	}
	if days != span.Days {
		return fmt.Errorf("%w: 走查数出 %d 个交易日，而 .meta 记的是 %d 个"+
			"——只比 bars 会让「某天的起点丢了」读成「那天确认没有」",
			errDaysMismatch, days, span.Days)
	}
	s.verified[span] = true
	return nil
}

// HasBars 回答「这一天有没有根」。
//
// ⛔ B3：**没走查过的时候，那两个计数什么也不意味着。**
// 那时不许回答「拉过，确认没有」——只能答「答不了」（ErrSpanUnverified）。
// 走查发生在给出「确认没有」这个答案【之前】，不是发现异常之后。
//
// 这是本节第一原则的又一次应用：**缺少验证不能悄悄变成一个肯定的答案。**
// 「没验过」和「验过了，是空的」如果都回答「确认没有」，
// 那么加那两个计数**只是把原来那个洞挪了个位置**。
func (s *Store) HasBars(day tickflow.TradingDay) (bool, error) {
	for _, sp := range s.meta.Coverage {
		if day < sp.From || day > sp.To {
			continue
		}
		if !s.verified[sp] {
			return false, fmt.Errorf("%w: %s 落在 [%s, %s] 里，而这一段还没走查过",
				tickflow.ErrSpanUnverified, day, sp.From, sp.To)
		}
		return sp.Bars > 0, nil
	}
	return false, nil // 不在任何 coverage 里 ⇒ 没拉过，这不是「确认没有」
}
