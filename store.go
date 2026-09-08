package tickflow

import "errors"

// 本文件对应 docs/design.md §一 的 `store.go`：Store / Iterator 接口所在。
//
// ⚠️ v0.3 甲这一版【只落】`.meta` 侧的契约（Span 与两个「答不了」），
// Store / Iterator 接口本身、以及定长文件实现（store/segfile/）在后续几件里。
// 分两步不是为了少写，是因为**落盘格式的不变量要先能被单独测红**——
// 混在接口里，一条红了分不清是格式错还是实现错。

// Span 是一段【交易日闭区间】，外加两个【被写下去的计数】。
//
// ⚠️ 这两个计数不是统计信息，是【正面记号】：
// 没有它们，「拉过确认没有」就只能靠「.dat 里没有它的根」来表示——
// 那是一个【缺席】，而缺席也可能是残尾、截断、索引损坏、一个 bug。
type Span struct {
	From TradingDay `json:"from"`
	To   TradingDay `json:"to"` // 闭区间；相邻性按【交易日】算，不按自然日

	// Bars 是写这一段时【实际落盘的记录条数】。
	Bars int `json:"bars"`

	// Days 是这一段里【至少有一根的交易日】条数。
	//
	// 只有 Bars 不够：记录一条没少而【某一天的起点找不到了】（日→偏移的映射
	// 损坏、首条记录时间戳被写坏）时，Bars 仍然相符，于是那一天读成
	// 「拉过，确认没有」—— 上一版那个自信的错答案原样回来。
	// Days 对不上就报错 ⇒ **缺席重新只意味着损坏。**
	Days int `json:"days"`
}

// —— 两个「答不了」。第三个是日历给的 ErrUncovered，不在这儿。——
//
// ✅ 这两个名字【受 doccheck 保护】。构造验证（2026-09-08，落这个文件时跑的）：
// 把这里的 ErrSpanUnverified 改名 ⇒ doccheck 报
// 「ErrSpanUnverified (value) —— 文档 design.md:913」并退 1。
//
// ⚠️ 这一行原本写的是相反的话。docs/design.md §6.1 有一段说「var 不受保护」，
// 我落这个文件时把它【原样抄了过来】——而那段当天晚些时候就过期了
// （scanBlock 补了 inValue 一路）。抄的时候它已经假了。
// ⇒ **一句带着「构造验证」字样的话，会让下一个人跳过验证。我就是那个下一个人。**
// 那段 §6.1 已一并改成「已补上」，并在那里记了这次传染。
var (
	// ErrSpanUnverified 这一段的 bars/days 还没走查过，所以现在【答不了】
	// 「这一天是不是确认没有」。补救：走一遍（第二档）。瞬时，自动可解。
	ErrSpanUnverified = errors.New("tickflow/store: 这一段还没走查过——答不了，不是「没有」")

	// ErrLegacyMeta .meta 没有 format 字段（v0.3 之前写的），而这个源不可重放，
	// 所以 coverage 既不能当「已拉过」也不能当「没拉过」。补救：一个显式决定。
	ErrLegacyMeta = errors.New("tickflow/store: meta 版本未知且源不可重放——需要一个显式决定")
)
