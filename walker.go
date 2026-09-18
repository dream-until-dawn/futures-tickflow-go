package tickflow

import "errors"

// BarWalker 是逐根读库的口子：Feed（v0.9）从这里读根。
//
// ⛔ 它是**消费方定义的窄接口**，不是 Store 的一部分（用户 2026-09-18 裁，design.md §十五「v0.9 起手」U4 乙）：
// Store 接口一个字不动，别人自己写的 Store 实现不补这两个方法也照样编译；想喂 Feed 就实现它们。
// ⇒ 「能被 Sync 写的库」（Store）与「能喂 Feed 的库」（BarWalker）是两个集合。本库的 *segfile.Store 两个都是
// （segfile 包里有编译期断言钉着）。
//
// —— Walk 的契约（从 (*segfile.Store).Walk 的语义提上来；v0.7「还没定」甲 ⚠️ 那一句要求进接口前先写成契约）——
//
//	前置    [from, to] 必须整个落在 Coverage() 的**某一段**里；否则返回的错误 errors.Is(err, ErrWalkOutsideCoverage)。
//	        段外、或跨过两段之间的空档 ＝「没拉过」，不是「没核过」—— 两种状态不许共用一个判断：
//	        **数据坏了（先核没过）的错误不许 Is 这个哨兵**。消费方据此分岔：没拉过 ⇒ 提示去同步；坏了 ⇒ 停
//	        （评审方 2026-09-18 指出：只写「报错里分得开」而不给哨兵，这条在消费方那一侧不可检，只能比字符串）
//	顺序    按交易日（其次按时刻）升序回调
//	先核    实现要在回调之前核过它交出去的数据（segfile 是「逐条先核全库」）；
//	        ⚠️ 这意味着库里**任何一处**坏，对任何 [from, to] 都可以报错 —— 哪怕坏的那一段与 [from, to] 不相交
//	停      fn 返回 false ⇒ 只停回调，结论照给（nil 就是「核过、没问题」）
//	作废    返回非 nil ⇒ 这一次回调出去的**每一根**都作废，调用方不得使用
//	代价    实现可以没有 seek 索引（segfile 每次从文件头扫到尾）⇒ 调用方不得假设代价与区间长度成正比
//	并发    不要求并发安全；一边写一边 Walk 没有保证
//
// Coverage 返回已登记的 coverage（调用方可以改它，不影响库）。
type BarWalker interface {
	Coverage() []Span
	Walk(from, to TradingDay, fn func(Bar) bool) error
}

// ErrWalkOutsideCoverage：Walk 的区间不整个落在某一段 coverage 里 ＝「没拉过」（不是「坏了」）。
// BarWalker 的实现在前置不满足时返回的错误要 errors.Is 它；先核没过（数据坏了）的错误不许 Is 它。
var ErrWalkOutsideCoverage = errors.New("tickflow: Walk 的区间不整个落在任何一段 coverage 里（没拉过，不是坏了）")
