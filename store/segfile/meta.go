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
//	A2   相邻性按【交易日】算，不按自然日
//	A3   format 用 *int：「没写」与「写了 0」分得开
//	D1   三种「答不了」互相分得开
//	D2a  format 缺失的判定取决于源的可重放性，不是常数
//	E1a  无法解析时报错，不拿默认值顶上
//	E1b  format 大于当前已知版本时报错
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
	errMetaUnreadable = errors.New("segfile: .meta 读不懂——报错，不猜")
	errFutureFormat   = errors.New("segfile: .meta 的 format 超出当前已知版本——报错，不猜")
	errUnknownFormat  = errors.New("segfile: .meta 声明了一个本版不认识的 format——报错，不猜")
	errUnordered      = errors.New("segfile: coverage 未按 From 升序")
	errOverlap        = errors.New("segfile: coverage 有重叠段")
)

// DecodeMeta 把 `.meta` 的字节解成 Meta。
//
//	E1a  解析不了 ⇒ 报错，【不】返回一个空 Meta 顶上
//	E1b  format 大于 FormatVersion ⇒ 报错，【不】照旧版语义读
//	A3   format 缺失时 Format 为 nil，与「写了 0」分得开；缺失【不是】错误，
//	     它走 D2a（DecideLegacyMeta），因为那个判定需要「源可不可重放」这个本层没有的信息
//
// ⚠️ 一处【设计里没写】的情形，我在这里选了「报错不猜」并标出来：
// §6.1 那张四行判定表覆盖的是「读不到/非法」「缺失」「> 已知」「== 1」，
// **没有覆盖「声明了一个 <= 已知但不等于已知的版本」**（例如写着 0 或负数）。
// 按本节第一原则（缺少验证不能悄悄变成一个肯定的答案），这里也报错。
// 已作为设计缺口报给评审方；若判定改了，这一行跟着改。
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
// 判据来自日历的 Walk：走 [a,b]，中间不该有第三个交易日。
func adjacentTradingDays(cal tickflow.Calendar, k tickflow.ProductKey, a, b tickflow.TradingDay) (bool, error) {
	if b <= a {
		return false, nil
	}
	n := 0
	err := cal.Walk(k, a, b, func(tickflow.Day) bool {
		n++
		return n <= 2 // 只要数到第三个就可以停
	})
	if err != nil {
		return false, err
	}
	return n == 2, nil
}

// LegacyDecision 是 `format` 缺失时的处置。
type LegacyDecision int

const (
	// LegacyDiscard 源可重放 ⇒ coverage 作废，全区间按「没拉过」，重拉。
	// 吵，但会收敛；而重拉能把语义未知的旧记录换成语义已知的新记录。
	LegacyDiscard LegacyDecision = iota + 1

	// LegacyUnverified 源不可重放 ⇒ coverage【不】作废，整份标 unverified，报告并停。
	// 作废在这里换不来任何东西：重拉取不回那段，只会永远重试，
	// 而丢掉那些区间是不可逆的。
	LegacyUnverified
)

// DecideLegacyMeta 是 D2a：`format` 缺失时怎么办，**取决于这个源能不能重放**。
//
// ⚠️ 它【不是常数】。这一条是被一次实录失败逼出来的：第一版把它写成
// 「一律作废重拉」，而那个理由错在一个没写出来的前提上——**它假定自愈可用**。
// 源不可重放时，自愈从来就不在选项里。
//
// 不可重放那一支返回根包的 ErrLegacyMeta：它要的是**一个显式决定**（Force 或人工确认），
// 而不是一个自动动作。
//
// ⚠️ 射程：本函数只给出「判定」。**两种结果都要进 SyncReport** 是 D2b，
// 而 SyncReport 还不存在（见 tools/doccheck/pending.txt）⇒ D2b 不在本版，
// 挪到「做 Source 那一版」。**本版没有把 D2 测成半条：D2a 整条测完，D2b 整条缺席。**
func DecideLegacyMeta(replayable bool) (LegacyDecision, error) {
	if replayable {
		return LegacyDiscard, nil
	}
	return LegacyUnverified, tickflow.ErrLegacyMeta
}
