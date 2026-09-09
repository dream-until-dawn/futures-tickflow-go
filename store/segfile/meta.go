// Package segfile 是 `Store` 的默认实现：定长记录文件 + 一份 `.meta`。
//
// 本文件只做 `.meta` 的编解码与 coverage 的判定，**不碰 `.dat`**。
// 分开的理由写在 docs/design.md §6.1 那张不变量表的覆盖安排里：
// **片一红了必定是格式问题，片二红了才可能是文件层问题。**
// 合在一起，一条红分不清是哪一层。
//
// 本文件覆盖的不变量（编号见 §6.1 那张表）：
//
//	A1a  coverage 各段按 From 升序
//	A1b  coverage 各段互不重叠
//	A1c  coverage 各段的 From/To 必须是交易日
//	A2   相邻性按【交易日】算，不按自然日
//	A3   format 用 *int：「没写」与「写了 0」分得开
//	D1   三种「答不了」互相分得开
//	D2a  format 缺失的判定取决于源的可重放性，不是常数
//	E1a  无法解析时报错，不拿默认值顶上
//	E1b  format 不在已知版本集合内时报错
//	F1   .meta 不得含任何由交易日历派生的字段
package segfile

import (
	"encoding/json"
	"errors"
	"fmt"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// FormatVersion 是本版写出的 `.meta` 版本号。
const FormatVersion = 1

// Meta 是 `.meta` 的线格式。
//
// ⚠️ F1：这里【不得】出现任何由交易日历派生的字段（是不是交易日、时段模板、节假日…）。
// 那是把日历的答案抄进存储，**抄件会过期，而过期的抄件不会报错**。
// 字段集有一条守卫（meta_test.go 的 F1）——它是一道**绊线**，不是语义检查：
// 它只保证「加字段这件事会被看见」，判断新字段是不是日历派生仍然要人做。
type Meta struct {
	// Format 用指针，为的是把「字段没写」与「字段写了 0」分开（A3）。
	// 用 int 的话 0 既是合法版本号又是缺失，而这两者的处置完全不同：
	// 缺失走 D2a（取决于源可不可重放），而一个写下来的 0 是【声明了一个未知版本】。
	Format *int `json:"format"`

	// Coverage 是交易日闭区间的【有序不重叠】列表（A1a / A1b）。
	Coverage []tickflow.Span `json:"coverage"`
}

// —— 内部哨兵。⚠️ 故意【不导出】：`.meta` 层的失败分类还没有进 docs/design.md，
// 而本仓的规矩是文档先行；导出一批文档里没有的公开错误值，就是先斩后奏。
// 测试与本包同包，用得到它们；对外的契约仍然只有根包那两个（ErrSpanUnverified / ErrLegacyMeta）。
// 等 Store 接口那一版再决定哪些该升成公开契约。
var (
	errMetaUnreadable        = errors.New("segfile: .meta 读不懂——报错，不猜")
	errFutureFormat          = errors.New("segfile: .meta 的 format 超出当前已知版本——报错，不猜")
	errUnknownFormat         = errors.New("segfile: .meta 声明了一个本版不认识的 format——报错，不猜")
	errUnordered             = errors.New("segfile: coverage 未按 From 升序")
	errOverlap               = errors.New("segfile: coverage 有重叠段")
	errEndpointNotTradingDay = errors.New("segfile: coverage 的端点不是交易日")
)

// DecodeMeta 把 `.meta` 的字节解成 Meta。
//
//	E1a  解析不了 ⇒ 报错，【不】返回一个空 Meta 顶上
//	E1b  format 不在已知版本集合内 ⇒ 报错，【不】照旧版语义读
//	A3   format 缺失时 Format 为 nil，与「写了 0」分得开；缺失【不是】错误，
//	     它走 D2a（DecideLegacyMeta），因为那个判定需要「源可不可重放」这个本层没有的信息
//
// ⚠️ 判定表原来是「> 已知 ⇒ 报错」，不覆盖「写着 0 或负数」那一支。
// 我实现时撞到它并按第一原则选了「报错不猜」，报给评审方；他的判定是
// **不加行，把那一支收口成全划分**：
//
//	format 不在【已知版本集合】内 ⇒ 报错，不猜
//
// 理由：「> 已知」与「0」不是两条性质，是同一条性质的两个输入
// （第四列一句写得完 ⇒ 按粒度规则停）；而且已知集合将来变成 {1,2} 时这条不用再改。
// ⇒ 落后的是文档那一行，不是实现 —— 下面两支合起来就是「不在集合内」。
// 两个哨兵保留：错误信息更具体是好事。
func DecodeMeta(b []byte) (*Meta, error) {
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", errMetaUnreadable, err)
	}
	if m.Format != nil {
		switch {
		case *m.Format > FormatVersion:
			return nil, fmt.Errorf("%w: format=%d，本版只认到 %d", errFutureFormat, *m.Format, FormatVersion)
		case *m.Format != FormatVersion:
			return nil, fmt.Errorf("%w: format=%d，本版只认 %d", errUnknownFormat, *m.Format, FormatVersion)
		}
	}
	return &m, nil
}

// EncodeMeta 把 Meta 写成 `.meta` 的字节。
//
// ⚠️ 写出去的 format 一律取 FormatVersion，**不沿用读进来的那个**：
// 本版能写出来的只有本版的格式。沿用读进来的值，等于用一个旧版本号
// 给一份新版本写的内容背书 —— 而下一个读者会照那个旧语义去解。
func EncodeMeta(m Meta) ([]byte, error) {
	v := FormatVersion
	m.Format = &v
	if m.Coverage == nil {
		m.Coverage = []tickflow.Span{}
	}
	return json.MarshalIndent(m, "", "  ")
}

// ValidateCoverage 检查 A1a（升序）与 A1b（不重叠）。
//
// 这两条【不需要日历】：升序与重叠都是交易日编号上的算术。
// 需要日历的是 A2（相邻性），那在 NormalizeCoverage。
//
// ⚠️ 两条分开返回不同的哨兵，因为它们可以各自单独发生：
// 重叠但有序、不重叠但无序，都是真实存在的坏 .meta。
func ValidateCoverage(spans []tickflow.Span) error {
	for i, s := range spans {
		if s.From > s.To {
			return fmt.Errorf("segfile: coverage[%d] 的 From(%d) 大于 To(%d)", i, s.From, s.To)
		}
		if i == 0 {
			continue
		}
		prev := spans[i-1]
		if s.From < prev.From {
			return fmt.Errorf("%w: coverage[%d].From=%d 小于 coverage[%d].From=%d",
				errUnordered, i, s.From, i-1, prev.From)
		}
		if s.From <= prev.To {
			return fmt.Errorf("%w: coverage[%d] 从 %d 起，而 coverage[%d] 到 %d 为止",
				errOverlap, i, s.From, i-1, prev.To)
		}
	}
	return nil
}

// ValidateEndpoints 检查 A1c：coverage 各段的 From 与 To **必须是交易日**。
//
// ⚠️ 这一条是拆 A1 时【掉在地上】的那半：
// 原来的 A1 是「coverage 是【交易日】闭区间的有序不重叠列表」，
// 拆成 A1a（升序）／ A1b（不重叠）之后，「**是交易日**」没有拿到编号，于是没人测它。
// 它可单独违反（升序、不重叠都成立而端点是周六），而且有实录后果（见 adjacentTradingDays）。
//
// ⚠️ 三种「答不了」在这里要分得开（D1）：
// 那天不是交易日 ⇒ 这是 A1c 违规，是**坏 .meta**；
// 日历覆盖不到   ⇒ 原样抛 ErrUncovered，那是**日历答不了**，不是 .meta 的错。
// 把后者读成前者，会让一份好 .meta 因为换了一份窄日历而被判成损坏。
func ValidateEndpoints(cal tickflow.Calendar, k tickflow.ProductKey, spans []tickflow.Span) error {
	cf, ct, ok := cal.Covers(k)
	for i, s := range spans {
		for _, ep := range []struct {
			name string
			day  tickflow.TradingDay
		}{{"From", s.From}, {"To", s.To}} {
			// ⚠️ 先问【答得了吗】，再问【那天交易吗】——而且这一步在本层自己做，
			// 不依赖实现的 DayOf 去归类。理由是实测出来的：
			//
			//	calendar/embedded 的 DayOf 与 Walk 对同一个日期归类不一致：
			//	窄日历 Covers=[0805,0806] 时，0731 在 DayOf 是 ErrNotTradingDay，
			//	在 Walk 是 ErrUncovered。Walk 比了 Covers，DayOf 只比了 baseFrom 与品种。
			//
			// 那是 calendar/embedded 的既有缺陷（已另案报告），
			// **而把 D1 那条「三种答不了要分得开」建在一个会混淆它们的调用上，
			// 等于把本层的正确性外包给了一个已知会错的地方。**
			if !ok || ep.day < cf || ep.day > ct {
				return fmt.Errorf("segfile: coverage[%d].%s = %s 落在日历覆盖之外"+
					"——这是【答不了】，不是「这一段坏了」: %w", i, ep.name, ep.day, tickflow.ErrUncovered)
			}
			if _, err := cal.DayOf(k, ep.day); err != nil {
				if errors.Is(err, tickflow.ErrNotTradingDay) {
					return fmt.Errorf("%w: coverage[%d].%s = %s 不是交易日",
						errEndpointNotTradingDay, i, ep.name, ep.day)
				}
				return err // 其余原样抛
			}
		}
	}
	return nil
}

// NormalizeCoverage 把【按交易日相邻】的相邻两段合成一段（A2）。
//
// ⚠️ 判据是交易日，不是自然日：`20200731`（周五）与 `20200803`（周一）之间
// **没有洞**，必须合成一段。按自然日判会在每个周末切一刀——
// 17 年下来约 900 段，而那 900 个「洞」全是假的。
//
// 相邻性问日历，不自己算：本函数不知道哪天是假日。
// 日历答不了（ErrUncovered）就原样把错误抛出去——**不猜**。
func NormalizeCoverage(cal tickflow.Calendar, k tickflow.ProductKey, spans []tickflow.Span) ([]tickflow.Span, error) {
	if err := ValidateCoverage(spans); err != nil {
		return nil, err
	}
	if err := ValidateEndpoints(cal, k, spans); err != nil {
		return nil, err
	}
	if len(spans) < 2 {
		return append([]tickflow.Span(nil), spans...), nil
	}
	out := []tickflow.Span{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		adj, err := adjacentTradingDays(cal, k, last.To, s.From)
		if err != nil {
			return nil, err
		}
		if adj {
			last.To = s.To
			last.Bars += s.Bars
			last.Days += s.Days
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// adjacentTradingDays 回答「b 是不是 a 之后的下一个交易日」。
//
// 判据：走 [a,b]，走出来的交易日集合**恰好是 {a, b}**。
//
// ⛔ 上一版的判据是「[a,b] 里恰好有 2 个交易日」，而那个推理
// **只在 a 与 b 都是交易日时才成立**——那个前提当时没有任何地方保证。
// 评审方 2026-09-08 实测出的后果（我已复现）：
//
//	日历 …0806 0807 0810…      两段 [0805,0806] 与 [0808,0810]（0808 是周六）
//	走 [0806,0808] ⇒ {0806, 0807} 也是 2 个 ⇒ 判成相邻 ⇒ 合并成 [0805,0810]
//	⇒ **coverage 凭空声称覆盖了 20200807，而两段谁都没声称过它。**
//
// 那正是 §6.1 点名的最危险方向：**声称拉过而其实没有（静默漏数据且不会自愈）**。
// 而它不需要崩溃、不需要竞态，**只需要一个 From 不是交易日的 .meta**。
//
// ⇒ 改成比对端点本身：first == a && last == b。
// 它顺带也挡住「a 不是交易日」那一支（那时 first != a）。
// ⚠️ 而根上的洞由 A1c 堵（端点必须是交易日）——这里是第二道，
// **两道都要**：A1c 管「别让坏 .meta 进来」，这里管「就算进来了也别把它读成合并」。
func adjacentTradingDays(cal tickflow.Calendar, k tickflow.ProductKey, a, b tickflow.TradingDay) (bool, error) {
	if b <= a {
		return false, nil
	}
	n := 0
	var first, last tickflow.TradingDay
	err := cal.Walk(k, a, b, func(d tickflow.Day) bool {
		n++
		if n == 1 {
			first = d.Num
		}
		last = d.Num
		return n <= 2 // 数到第三个就可以停
	})
	if err != nil {
		return false, err
	}
	return n == 2 && first == a && last == b, nil
}

// —— D2a 的判定已【上移到根包】。这里只留别名，旧调用方不必改。——
//
// ⛔ 上移的理由不是整理，是实测出来的一处结构错位（design.md 丙三之零冲突二）：
// 判定需要「源可不可重放」⇒ 只有编排经 `Caps` 知道；编排在根包；
// **而根包 import 本包是 import cycle。**
// ⇒ 它此前「生产侧零调用方」**不是「还没接线」，是「接不上」** ——
// 而那两者处置相反：前者接上就行，后者要动结构。
//
// ⇒ 判据：**量出「零调用方」之后，要再问一句「那个调用者【能不能】调到它」** ——
// 否则会把一处结构错位记成一件待办，而待办会被排期，错位不会自己好。
//
// ⚠️ 与 `Outcome` 那一格同样的处置、同样的理由：它是**契约的一部分**，
// 留在实现包里，别的实现就不受它约束。
type LegacyDecision = tickflow.LegacyDecision

const (
	// LegacyDiscard 源可重放 ⇒ coverage 作废，全区间按「没拉过」，重拉。
	LegacyDiscard = tickflow.LegacyDiscard
	// LegacyUnverified 源不可重放 ⇒ coverage【不】作废，整份标 unverified，报告并停。
	LegacyUnverified = tickflow.LegacyUnverified
)

// DecideLegacyMeta 是 D2a。实现在根包，这里只转发。
//
// ⚠️ 写成【转发函数】而不是 `var DecideLegacyMeta = tickflow.DecideLegacyMeta`：
// 后者是一个变量，**谁都可以在运行期把它换掉** —— 而这是一条判定规则，
// 不是一个可配置项。
func DecideLegacyMeta(replayable bool) (LegacyDecision, error) {
	return tickflow.DecideLegacyMeta(replayable)
}
