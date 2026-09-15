package segfile

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// coverageChecker 是**逐条核对器**：喂一条记录，最后要每一段的结论。
//
// ⛔ 它存在的理由是片 B 评审（2026-09-14）那一条：**读法可以有两份，核法只许有一份**。
// `VerifyCoverage`（逐条 ReadAt）与 `Walk`（缓冲顺序读）各自带一套「逐条核 ＋ 段计数 ＋ 与 .meta 比」的话，
// 两份迟早漂开 —— 同一个坏库，一个说坏，一个说好。⇒ 两处都调它。
//
// 核三件【全库】的（任何一件不过 ⇒ 全库错误，归给每一段，停）：
//
//	SYN-10 零值 TradingDay（判在最前 —— 更准的诊断必须排在更泛的之前，见 Verify 的长注释）
//	顺序   交易日非降序
//	归属   落在某一段 coverage 里
//
// 再给每一段数 Bars / Days，扫完之后与 .meta 比（B1 / B2）。
type coverageChecker struct {
	cov    []tickflow.Span
	bars   []int
	days   []int
	prevIn []tickflow.TradingDay
	prev   tickflow.TradingDay
	whole  error
}

func newCoverageChecker(cov []tickflow.Span) *coverageChecker {
	return &coverageChecker{
		cov:    cov,
		bars:   make([]int, len(cov)),
		days:   make([]int, len(cov)),
		prevIn: make([]tickflow.TradingDay, len(cov)),
	}
}

// feed 核第 i 条记录。返回 false ⇒ 这一条让全库错误成立（记在 whole 里），调用方应当停止读。
func (c *coverageChecker) feed(i int64, b tickflow.Bar) bool {
	switch {
	case b.TradingDay == 0:
		c.whole = fmt.Errorf("%w: 第 %d 条记录（Ts=%d）的 TradingDay 是零值——"+
			"它来自本版写入口之外（旧版/别的写者/手工造的）；"+
			"这不是文件损坏，别去查长度和截断", errZeroTradingDay, i, b.Ts)
		return false
	case b.TradingDay < c.prev:
		c.whole = fmt.Errorf("%w: 第 %d 条是 %s，而上一条是 %s",
			errRecordDisorder, i, b.TradingDay, c.prev)
		return false
	}
	c.prev = b.TradingDay
	hit := -1
	for j := range c.cov {
		if b.TradingDay >= c.cov[j].From && b.TradingDay <= c.cov[j].To {
			hit = j
			break
		}
	}
	if hit < 0 {
		// 归属那一条：谁的段都不属于 ⇒ 这才是真的损坏（与 Verify 的 default 同义）。
		c.whole = fmt.Errorf("%w: 第 %d 条记录是 %s，而它不落在任何一段 coverage 里",
			errRecordOutside, i, b.TradingDay)
		return false
	}
	if b.TradingDay != c.prevIn[hit] {
		c.days[hit]++
		c.prevIn[hit] = b.TradingDay
	}
	c.bars[hit]++
	return true
}

// result 交出每一段的结论，契约同 VerifyCoverage（全的 · 键是 Coverage() 的 Key() · nil ＝ 通过）。
// 全库错误归给每一段；否则逐段比那两个计数。
func (c *coverageChecker) result() map[tickflow.SpanKey]error {
	out := make(map[tickflow.SpanKey]error, len(c.cov))
	for j, sp := range c.cov {
		switch {
		case c.whole != nil:
			out[sp.Key()] = c.whole
		case c.bars[j] != sp.Bars:
			out[sp.Key()] = fmt.Errorf("%w: 走查数出 %d 条，而 .meta 记的是 %d 条",
				errBarsMismatch, c.bars[j], sp.Bars)
		case c.days[j] != sp.Days:
			out[sp.Key()] = fmt.Errorf("%w: 走查数出 %d 个交易日，而 .meta 记的是 %d 个"+
				"——只比 bars 会让「某天的起点丢了」读成「那天确认没有」",
				errDaysMismatch, c.days[j], sp.Days)
		default:
			out[sp.Key()] = nil
		}
	}
	return out
}

// walkBufSize 是整库扫描的读缓冲。读数（片 B 设计信，2026-09-14，合成库 890,000 根、页缓存热、Windows）：
// 逐条 ReadAt 一遍 2.9–3.3 s，64 KiB 缓冲顺序读＋逐条核 54–60 ms。
const walkBufSize = 64 << 10

// scanRecords 按文件顺序把每一条记录交给 fn（第 i 条、解好的 Bar）；fn 返回 false 就停。
//
// ⛔ **读法只有这一份**（(y)，2026-09-15）：Walk、VerifyCoverage、DaysWithBars 都走它 ——
// 片 B 评审那条「读法两份、核法一份」是过渡形状；(y) 把读法也收成一份，核法仍是 coverageChecker。
// ⚠️ 参照实现**故意不走它**：`Verify(span)` 与 `HasBars`/`dayHasRecords` 仍是逐条 ReadAt ——
// 两边都走新读法的话，等价性测试是空的（equivalence_test.go / verify_coverage_test.go 那两对）。
//
//	长度  取【开始这一刻】的文件大小（Stat）；只用 SectionReader（ReadAt），**不动文件偏移**
//	      ⚠️ 「长度取开始那一刻」**没有测试守着**，理由见 Walk 里那段（片 B 评审补打的变异 R2）
//	错误  Stat 失败 ⇒ statErr 非 nil（调用方据此区分「跑不起来」）；读或解某一条失败 ⇒ 带条号报
//
// scanReaderAt 是 scanRecords 读数据的来源 —— 包内测试缝，生产上恒为 s.dat。
//
// ⛔ 它存在的理由（(y) 评审 2026-09-15，变异 Z4）：DecodeBar 在一条完整记录上从不报错，
// 而长度取开始那一刻 ⇒ 不改代码造不出「读到一半出错」。没有这条缝，
// 「DaysWithBars 读错误时交出半张表、err 为 nil」这个变异全模块 0 红。
// ⚠️ 只替换【读数据】这一处；Stat 仍走 s.dat（长度的来源不变）。参照实现不走它。
var scanReaderAt = func(s *Store) io.ReaderAt { return s.dat }

func (s *Store) scanRecords(fn func(i int64, b tickflow.Bar) bool) (statErr, readErr error) {
	st, err := s.dat.Stat()
	if err != nil {
		return err, nil
	}
	n := CountRecords(st.Size())
	r := bufio.NewReaderSize(io.NewSectionReader(scanReaderAt(s), 0, n*RecordSize), walkBufSize)
	buf := make([]byte, RecordSize)
	for i := int64(0); i < n; i++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("segfile: 读第 %d 条记录失败: %w", i, err)
		}
		b, err := DecodeBar(buf)
		if err != nil {
			return nil, fmt.Errorf("segfile: 解第 %d 条记录失败: %w", i, err)
		}
		if !fn(i, b) {
			return nil, nil
		}
	}
	return nil, nil
}

// errWalkRange Walk 的区间不整个落在某一段 coverage 里。
var errWalkRange = errors.New("segfile: Walk 的区间不整个落在任何一段 coverage 里")

// Walk 按文件顺序把 [from, to] 里的记录逐根交给 fn；**同一遍扫描里把整个库核一遍**（v0.6 片 B）。
//
// 它是库的**读口子**：calendar/derived 要逐根看 Ts 与 Volume，只读每天首末根不够（probe.md 6.13 c″、6.21）。
// ⚠️ **它不在 tickflow.Store 接口里**（用户 2026-09-15 裁定，design.md §十五「第四次裁决」）：只在本类型上。
// ⚠️ **v0.6 里它没有生产消费者**：calendar/derived 于 2026-09-15 挪到 v0.7（design.md §十五「第三次裁决」），
// 而本库 v0.4 那一节记过「读口子不加 ＝ 零调用点」的代价。它已随片 B 合入（先于那次裁决），
// 契约由 store/segfile/walk_test.go 钉着；**第一个消费者是 v0.7 的 derived** —— 那时再核这段语义够不够用。
//
// —— 语义（片 B 评审 2026-09-14 裁定：边扫边核）——
//
//	前置    [from, to] 必须整个落在**某一段**已登记的 coverage 里；否则报错。
//	        段外、或跨过两段之间的空档 ＝「没拉过」，**不是**「没走查过」，两句话分开说。
//	        ⛔ **不要求**本进程先调过 VerifyCoverage —— 保证不绑在「之前某处调过」这件事上。
//	一遍    逐条先核全库三件（零值 TradingDay · 非降序 · 落在某一段），**核过才回调**；
//	        同时给每一段数 Bars/Days，扫完与 .meta 比 —— 核法与 VerifyCoverage 是同一份。
//	停      fn 返回 false ⇒ **只停回调，扫描照样走完、结论照给**。
//	        （提前停若不给结论，nil 就分不清「核过没问题」与「没核完」。）
//	作废    ⛔ **返回非 nil ⇒ 这一次回调出去的每一根都作废**，调用方不得使用。
//	        全库错误在第一条坏记录上中止；计数对不上在扫完后才报。
//	副作用  扫完且某段对上 ⇒ 那一段置「走查过」（同 VerifyCoverage）；不改文件。
//
// ⚠️ **全库任何一处坏，对任何 [from, to] 都报错** —— 哪怕坏的那一段与 [from, to] 不相交。
// 这是「逐条先核全库」的直接后果，与 VerifyCoverage 整库归因同形；
// 代价是「读一段好数据」会被别处的坏数据挡住。**这是设计，不是缺陷。**
//
// ⚠️ **不是并发安全的**：扫描长度取【开始扫描那一刻】的文件大小，扫描期间追加进来的记录不在这一遍里；
// 同一个库一边 AppendBars 一边 Walk 没有任何保证。
// ⚠️ **没有 seek 索引**：每次都从文件头扫到尾（890,000 根约 55 ms，合成库读数见片 B 设计信）。
// 按时间随机定位留给 Feed（v0.9）时再定。
func (s *Store) Walk(from, to tickflow.TradingDay, fn func(tickflow.Bar) bool) error {
	if fn == nil {
		return errors.New("segfile: Walk 的 fn 是 nil")
	}
	if !from.Valid() || !to.Valid() || from > to {
		return fmt.Errorf("segfile: Walk 的区间不合法：[%d, %d]", int32(from), int32(to))
	}
	cov := s.meta.Coverage
	inside := false
	for _, sp := range cov {
		if from >= sp.From && to <= sp.To {
			inside = true
			break
		}
	}
	if !inside {
		// ⛔ 这一句与「没走查过」分开说：区间落在段外是【没拉过】，不是【没核过】。
		return fmt.Errorf("%w: [%s, %s]，而 coverage 是 %v——"+
			"段外（或跨过两段之间的空档）是「没拉过」，不是「没走查过」",
			errWalkRange, from, to, cov)
	}

	// ⛔ 读法在 scanRecords（长度取开始那一刻、不动偏移）。
	// ⚠️ **「长度取开始那一刻」这一条没有测试守着**（片 B 评审 2026-09-14 补打的变异 R2：长度改成 1<<62、读到 EOF ⇒ 全模块一格不红）。
	// 不补的理由：要守它就得在扫描期间追加；而缓冲一次读 64 KiB，小库在第一次回调之前就整个进了缓冲，
	// 追加进来的根不论长度怎么取都读不到 ⇒ 要么造一个大于缓冲的库并在回调里追加（越出上面写明的「并发不安全」射程），
	// 要么就是一个时绿时红的测试。**写在这里，免得下一个人以为有人守着。**
	c := newCoverageChecker(cov)
	deliver := true
	statErr, readErr := s.scanRecords(func(i int64, b tickflow.Bar) bool {
		// ⛔ 先核、再回调：没核过的记录不交出去。
		if !c.feed(i, b) {
			return false
		}
		// ⛔ fn 返回 false 只停回调，扫描照样走完 —— 结论照给。
		if deliver && b.TradingDay >= from && b.TradingDay <= to {
			deliver = fn(b)
		}
		return true
	})
	if statErr != nil {
		return fmt.Errorf("segfile: Walk 取文件大小失败: %w", statErr)
	}
	if readErr != nil {
		return fmt.Errorf("segfile: Walk：%w", readErr)
	}

	res := c.result()
	var errs []error
	for _, sp := range cov {
		if e := res[sp.Key()]; e != nil {
			errs = append(errs, fmt.Errorf("[%s, %s]: %w", sp.From, sp.To, e))
			if c.whole != nil {
				break // 全库错误归给每一段，报一次就够
			}
			continue
		}
		s.verified[sp.Key()] = true
	}
	return errors.Join(errs...)
}
