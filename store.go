package tickflow

import "errors"

// 本文件对应 docs/design.md §一 的 `store.go`：Store / Iterator 接口所在。
//
// ⚠️ v0.3 甲这一版【只落】`.meta` 侧的契约（Span 与两个「答不了」）。
// 分两步不是为了少写，是因为**落盘格式的不变量要先能被单独测红**——
// 混在接口里，一条红了分不清是格式错还是实现错。
//
// 进度（2026-09-09 现量）：定长文件实现 `store/segfile/` **已完工**；
// **而 Store / Iterator 接口本身仍未定型** —— 这不是漏了，是写下来的决定，
// 原文在 `store/segfile/store.go` 头部那三条「范围边界」的第三条。

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

// Outcome 是上游这一次响应的结果。C2 要它。
//
// ⚠️ 它不是 bool，而且**零值不合法** —— 忘了填的调用方会当场被拒，
// 而不是拿到 false 或 true 里的某一个。
// 「问了但没问成」和「问了，确认没有」在返回值上可以长得很像
// （空数组 / 错误 / 超时后的空响应），所以这件事必须由调用方**说出来**。
//
// ⛔ **它住在根包，而不是住在某个实现里 —— 这不是搬家的便利。**
// 「零值不合法」是**接口契约的一部分**：留在实现包里，
// **别的实现就不受它约束**，而 C2 那条不变量正是靠它成立。
// （`store/segfile` 里保留了同名的类型别名，旧调用方不必改。）
type Outcome int

const (
	// OutcomeComplete 上游【完整成功】。只有它允许扩 coverage。
	OutcomeComplete Outcome = iota + 1
	// OutcomeFailed 出错、超时、或响应不完整。落盘可以，扩 coverage 不行。
	OutcomeFailed
)

// Store 是一个「品种 + 周期」的落盘库。**方法集按 store/segfile 实到的反推**，
// 不照抄姊妹项目（design.md 七之零冲突三：那边八个名字只对上一个 `Close`）。
//
// ⛔ **`Open` 不在接口里**：它是构造，签名里带着 `dir` 这种实现细节。
// 编排收一个**已经打开的** Store，而「怎么开、开在哪」由调用方决定。
// 写下来是因为「接口该不该包含构造」每次都要重 argue 一遍。
//
// ⚠️ **接口只取编排真的会调的那些，不多不少** —— 多一个方法就多一个实现方的负担，
// 而本仓只有一个实现时，「以防万一」加进来的方法**永远不会被第二个实现验证**。
type Store interface {
	// Coverage 返回【交易日闭区间】的有序不重叠列表（A1a / A1b）。
	Coverage() []Span

	// HasBars 回答「这一天在库里有没有根」。
	//
	// ⛔ 它有【三种】答案，而不是两种：有 / 没有 / **答不了**（ErrSpanUnverified）。
	// 「没走查过」不许悄悄变成一个肯定的「确认没有」（B3）。
	HasBars(day TradingDay) (bool, error)

	// AppendBars 只把这一批根写进数据文件，**不碰 coverage**（C1 的一半）。
	//
	// ⚠️ 顺序反了，崩溃就会留下「声称拉过而其实没有」—— 最危险的那个方向。
	AppendBars(bars []Bar) error

	// CommitSpan 在数据已经落盘之后扩 coverage。
	//
	// ⛔ out 必须由调用方**说出来**：「问了但没问成」和「问了，确认没有」
	// 在返回值上长得很像，而只有 OutcomeComplete 允许扩 coverage（C2）。
	CommitSpan(cal Calendar, k ProductKey, span Span, out Outcome) error

	// Verify 走查一段：那两个计数与真正枚举出来的对不对得上（B1 / B2）。
	Verify(span Span) error

	Close() error
}

// —— 两个「答不了」。第三个是日历给的 ErrUncovered，不在这儿。——
//
// ✅ 这两个名字【受 doccheck 保护】。构造验证（2026-09-08，落这个文件时跑的）：
// 把这里的 ErrSpanUnverified 改名 ⇒ doccheck 报
// 「ErrSpanUnverified (value) —— 文档 design.md」并退 1。
//
// ⚠️ 这里原来还带着一个行号（design.md:913）。下一格（加不变量表）在它前面插了
// 50 行，那个行号就成了 963 —— **而下一格正是写下「出处不用行号」那条规矩的那一格。**
// **规矩和它的第一个反例，隔一个提交，在同一条分支上。**
// ⇒ 行号去掉了：这句话本来就不需要行号才成立，名字本身就是锚。
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
