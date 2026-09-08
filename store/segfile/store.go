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
//
// ⛔ 还有三处【范围边界】，写在这儿免得被读成通用实现：
//
//	一、周期写死成 1m（`1m.dat` / `1m.meta`）。
//	    而 design.md §六 的目录布局里，一个合约目录下会有多个周期（`1m.dat` / `1d.dat`…）。
//	    ⇒ 本类型现在是【一个合约的一个周期】，不是「一个合约目录」。
//	    要支持多周期，Open 得收一个周期参数 —— 那是接口形状的变更，等 Store 接口定型时一起做。
//	二、没有锁。布局里那个 `.lock` 本版一个字都没碰
//	    ⇒ **两个进程同时开同一个目录，本类型不会拦。**
//	    而 B3 那个 `verified` 正是进程内状态：别的进程改了 .meta，这边的走查结论就过期了，
//	    **而它不会知道**。
//	三、Store 接口本身还没定型。design.md 列的是
//	    `Append` / `Merge` / `Iter` / `Range` / `Meta` / `AddCoverage` / `Series` / `Close`，
//	    本类型只实现了落盘格式那几条不变量要用到的部分。
//
// **这三条都不是缺陷，是没做；写下来是为了让「没做」和「做漏了」分得开。**
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
		// ⛔ 读进来的 coverage 也要过 A1a/A1b —— 不变量在【两个入口】都要设卡。
		//
		// 此前它们只在 CommitSpan（写）那一侧执行，于是一份手改的、
		// 或别的版本/工具写的 .meta【进得来】：实测一份乱序 + 重叠 + 端点非交易日的
		// coverage 被 Open 悄悄收下。
		// ⇒ 按本节第一原则（读不懂就报错，不猜）：结构不合法就不收。
		//
		// ⚠️ 而 A1c（端点必须是交易日）**在这里查不了**：它要日历，而 Open 没有。
		// 这是一个【被声明的边界】，不是遗漏：A1c 仍然只在 CommitSpan 那一侧执行。
		// ⇒ 一份端点非交易日的 .meta 仍然进得来，直到有人拿着日历去动它。
		// 要在这里也堵上，得让 Open 收一个 Calendar —— 那是接口形状的变更，
		// 已提给评审方定，本版不擅自改。
		if verr := ValidateCoverage(m.Coverage); verr != nil {
			f.Close()
			return nil, 0, fmt.Errorf("segfile: %s 里的 coverage 结构不合法: %w", dir, verr)
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

// writeMeta 把 `.meta` 写出去 —— **先写临时文件，再 rename 覆盖**。
//
// ⚠️ 为什么不直接 WriteFile（实测，2026-09-09）：
// 直接写时，崩在写一半会留下**半份坏 JSON** ⇒ 下一次 Open 走 E1a（报错不猜）
// ⇒ **整个 store 打不开**。
//
// 而 §6.1 自己写明了安全的失败方向：
// 「先写数据后扩 coverage，崩溃留下的是【拉过却没记】⇒ 重拉一遍，**吵而不丢**」。
// ⇒ rename 之后，崩在写一半留下的是**上一份好的 .meta**：
// coverage 落后于数据，正是那个吵而不丢的方向。**直接写会把它变成硬失败。**
//
// ⛔ 而这一条【没有测试】，说清楚：
// 「rename 是原子的」是文件系统的性质，**单元测试里没法真的崩在中间**。
// 能测的只有「写完之后没有留下临时文件、目标仍然可解」——那两条测的不是原子性。
// ⇒ 它靠的是一个**结构性论证**，不是一次观测。写在这儿，免得下一个人以为它被守着。
func (s *Store) writeMeta() error {
	b, err := EncodeMeta(s.meta)
	if err != nil {
		return err
	}
	final := filepath.Join(s.dir, "1m.meta")
	tmp := final + ".tmp"
	// ⛔ 必须 Sync 之后再 Rename：改名只保证【名字】换了，不保证【内容】已经落盘。
	//
	// 上一版是 os.WriteFile(tmp) → os.Rename，中间没有 Sync ——
	// **而同一个文件里 AppendBars 早就立了这条规矩**（「C1 说的是数据落盘之后，
	// 不是写进页缓存之后」）。⇒ 同一条规矩，给 .dat 立了，没给 .meta 用。
	//
	// ⚠️ 射程说清楚（评审方 2026-09-09 的两句限定，我照收）：
	// 「崩溃后会不会真的留下【已改名而内容为空】的文件」是**文件系统相关**的，
	// 这里不替文件系统下判断；论据是**内部一致性** —— 本仓已为 .dat 立过这条，理由一字不改地适用。
	// 而缺 Sync **不破 C1 的方向**（.dat 已 Sync、coverage 未 Sync ⇒ 丢的是 coverage ⇒ 吵而不丢），
	// 它破的是【这一格自己宣称的目的】。
	//
	// ⛔ 而**目录项的 fsync 没有做**：那是更严的一档，Windows 上拿不到。
	// 写成声明的边界，不假装做了。
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp) // 别把半成品留在目录里冒充别的东西
		return err
	}
	return nil
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
	var prev, prevInSpan tickflow.TradingDay
	for i := int64(0); i < n; i++ {
		if _, err := s.dat.ReadAt(buf, i*RecordSize); err != nil {
			return fmt.Errorf("segfile: 读第 %d 条记录失败: %w", i, err)
		}
		b, err := DecodeBar(buf)
		if err != nil {
			return err
		}
		// 顺序是全库的性质，不是某一段的：先在这一层查。
		if b.TradingDay < prev {
			return fmt.Errorf("%w: 第 %d 条是 %s，而上一条是 %s",
				errRecordDisorder, i, b.TradingDay, prev)
		}
		prev = b.TradingDay

		switch {
		case b.TradingDay >= span.From && b.TradingDay <= span.To:
			// 本段的：数。
			if b.TradingDay != prevInSpan {
				days++
				prevInSpan = b.TradingDay
			}
			bars++
		case s.dayInAnySpan(b.TradingDay):
			// ⛔ 别的段的：**跳过，不是错**。
			//
			// 上一版这里是「不在本段 ⇒ errRecordOutside」，于是**任何多段库都通不过走查**
			// —— 而多段（= 有缺口的序列）正是同步中的常态。
			// 后果比「用不了」重：它把一个【健康的库】报成【损坏】，
			// 而 B1/B2 整套的目的正是「缺席重新只意味着损坏」——那一版把它反了过来：
			// **损坏重新可以只意味着分了两段。**
			//
			// 评审方从 28e37cc 报到 08d0d33，**六个尖端**；而我四次撤回重送都没带上它，
			// 因为每次只按自己新发现的问题改、没复述他的结论。⇒ 那条流程规矩的代价是四轮。
		default:
			// 谁的段都不属于 ⇒ 这才是真的损坏。
			return fmt.Errorf("%w: 第 %d 条记录是 %s，而它不落在任何一段 coverage 里",
				errRecordOutside, i, b.TradingDay)
		}
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

// dayInAnySpan 报告这一天在不在【任何】一段 coverage 里。
//
// Verify 用它把「别的段的记录」与「谁的段都不属于的记录」分开 ——
// 前者跳过，后者才是损坏。
func (s *Store) dayInAnySpan(day tickflow.TradingDay) bool {
	for _, sp := range s.meta.Coverage {
		if day >= sp.From && day <= sp.To {
			return true
		}
	}
	return false
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
		// ⛔ 这里【不能】用 sp.Bars > 0。
		//
		// 那两个计数说的是**这一段整体**对不对，§6.1 明写了它们的边界：
		// **说不出是哪一天丢了**。拿 sp.Bars 去答「这一天有没有」，
		// 等于把一个关于整段的事实当成了一个关于某一天的答案——
		// 一段跨三天、只有第一天有根的 coverage，会对另外两天都答「有」。
		//
		// ⇒ 走一遍，找这一天的记录。**而「找不到」之所以能读成「确认没有」，
		// 靠的是上面那一步：这一段【走查过】。** 没走查过时同样的缺席只意味着损坏。
		return s.dayHasRecords(day)
	}
	return false, nil // 不在任何 coverage 里 ⇒ 没拉过，这不是「确认没有」
}

// dayHasRecords 走一遍 `.dat`，看这一天有没有记录。
//
// ⚠️ 它是 B2 那条判据的同一条：**和一次真正的枚举比，不和一个算出来的期望比。**
func (s *Store) dayHasRecords(day tickflow.TradingDay) (bool, error) {
	st, err := s.dat.Stat()
	if err != nil {
		return false, err
	}
	n := CountRecords(st.Size())
	buf := make([]byte, RecordSize)
	for i := int64(0); i < n; i++ {
		if _, err := s.dat.ReadAt(buf, i*RecordSize); err != nil {
			return false, fmt.Errorf("segfile: 读第 %d 条记录失败: %w", i, err)
		}
		b, err := DecodeBar(buf)
		if err != nil {
			return false, err
		}
		if b.TradingDay == day {
			return true, nil
		}
	}
	return false, nil
}
