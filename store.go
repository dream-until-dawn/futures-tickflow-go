package tickflow

import (
	"errors"
	"fmt"
)

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

	// DaysWithBars 一次回答【一整段】里哪些交易日有根。
	//
	// ⛔ 它有【三种】答案，而不是两种：有 / 没有 / **答不了**（ErrSpanUnverified）。
	// 「没走查过」不许悄悄变成一个肯定的「确认没有」（B3）。
	// 🔴 而三值里的第三值落在 **error** 上，不落在「返回一个空 map」上 ——
	// 空 map 与「这一段每天都没有根」在调用方那儿长得一模一样。
	//
	// ⛔ **它替换了上一版的 `HasBars(day)`**，理由不是接口美学，是复杂度：
	// 逐日问 ⇒ `O(天数 × 根数)`，整段问 ⇒ `O(根数)`。
	// 全部代价（含**它在哪一族输入上更慢**）写在 `docs/design.md`
	// 「二十·六」那一节，四问＋问五逐条带取法。
	//
	// ⚠️ **调用点被设计钉死**：按 coverage 段调用，且**只对与请求区间相交的段**调用。
	// 🔴 这一条承重：把它挪到循环外面无条件取整段，会让「全新品种/周期的首次同步」
	// （`cov` 为空）从**一次不读**变成**读整个文件** —— 那一族是纯亏。
	// ⇒ 守着它的是 `TestPlanGapsReadsNothingWhenNoCoverage`，不是这段注释。
	//
	// ⚠️ 而 `HasBars(day)` 没有消失：它仍是 `*segfile.Store` 的具体方法，
	// **作为等价性测试的参照实现**。两边都走新代码的话，那条测试是空的。
	DaysWithBars(span Span) (map[TradingDay]bool, error)

	// AppendBars 只把这一批根写进数据文件，**不碰 coverage**（C1 的一半）。
	//
	// ⚠️ 顺序反了，崩溃就会留下「声称拉过而其实没有」—— 最危险的那个方向。
	AppendBars(bars []Bar) error

	// CommitSpan 在数据已经落盘之后扩 coverage。
	//
	// ⛔ out 必须由调用方**说出来**：「问了但没问成」和「问了，确认没有」
	// 在返回值上长得很像，而只有 OutcomeComplete 允许扩 coverage（C2）。
	CommitSpan(cal Calendar, k ProductKey, span Span, out Outcome) error

	// VerifyCoverage 一遍扫描核算 `Coverage()` 的**每一段**：那两个计数与真正枚举出来的
	// 对不对得上（B1 / B2），以及每一条记录**属不属于**某一段。
	//
	// —— 契约三条，缺一条上层就会读错 ——
	//
	//	一、**全的**：`Coverage()` 里每一段都要有一个结论（nil ＝ 通过）。
	//	    ⛔ 漏掉一段 ＝ 违约。而编排拿不到结论时按「本次没走查过」处置 —— 那句话是真的，
	//	    所以违约**不会**变成一个肯定的答案，只会变成一条吵闹的缺口（B3）。
	//	二、**键是 `Coverage()` 返回的那些值**（整个 `Span` 结构体都参与相等）。
	//	    🔴 承重：`planGaps` 查的是同一批值。2026-09-11 的生产缺陷正是两边键不同源 ——
	//	    写入侧用【分块】的 span，读取侧用 `CommitSpan` 并段之后的值，而两处代码
	//	    **都写着 `verified[sp]`**。
	//	三、第二个返回值非 nil ＝ **这一遍跑不起来**（读盘失败）；那时第一个返回值必须是
	//	    **nil map**，不是空 map。
	//	    🔴 理由：`len(m)==0` 对 nil 与空 map 是同一个读数，而 `m == nil` 分得开 ——
	//	    「量不了」和「量出来每段都没问题」必须分得开。
	//
	// ⛔ **它替换了上一版接口里的 `Verify(span)`**，而那不是接口美学：
	// `Verify(span)` 扫整个 `.dat` 却只核算一段 ⇒ 走查 k 段 ＝ k 次整文件扫描
	// （890000 根实测：k=8 时 19.9s vs 单遍 2.5s）。
	// 🔴 而真正的理由是**把走查从「本次碰过什么」上解绑**：SYN-6 的保证原本绑在 `touched` 上，
	// 而丙片改的正是「谁会被碰」—— 绑到**库本身**之后，丙片怎么挑段都影响不到它。
	//
	// ⚠️ 而 `Verify(span)` **没有消失**：它仍是 `*segfile.Store` 的具体方法，
	// 理由有两条，都不是兼容 ——
	// 一、**v0.4.1 的 tag 注解逐字要人跑它**（「对 `Coverage()` 的每一段跑 `store.Verify(span)`」），
	//     **而 tag 改不了**（`tag_selfcheck_meta_test.go` 钉着那一句）；
	// 二、它是新读法的**参照实现** —— 两边都走新代码的话，等价性测试是空的。
	// 📎 与当年 `HasBars(day)` 降级成具体方法是同一个先例，同一个理由。
	VerifyCoverage() (map[Span]error, error)

	// OpenState 报【打开这个库时发现的、需要编排处置的事】（C3b / D2b 要它）。
	//
	// ⛔ 它在接口里，而 `Open` 不在 —— 两者不矛盾：
	// `Open` 是构造（签名里带 dir），而这是构造【留下的事实】，
	// 而那些事实的处置者是编排。
	OpenState() OpenState

	// DiscardCoverage 把 coverage 整个作废，全区间按「没拉过」。
	//
	// ⛔ 它只为 LegacyDiscard 那一支存在，**不是一个通用的清空口** ——
	// 少了它，D2b 会被做成半条：报告里说「已作废」而 coverage 原样留着，
	// **而那正是 D2 存在要挡的那个错**，只是换成由我们自己写进去。
	DiscardCoverage() error

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
	//
	// ⛔ **「瞬时、自动可解」只对「刚写进来、还没轮到走查」那一种来历成立**（2026-09-11 实测）。
	// 它说的是**本次没走查过**，**不表示这一段有问题** —— 详见 `gap.go` 的 `GapStoreUnverified`。
	//
	// ⛔ **上一版这里写的判别符是「走一遍，看缺口动没动」，而它是【循环】的**（实测）：
	// 今天第二次同步会在落盘那一步就中止，**根本走不到走查** ⇒ 缺口逐字不变。
	// **「走一遍」得到的是同一句话，不是新信息。**
	ErrSpanUnverified = errors.New("tickflow/store: 这一段还没走查过——答不了，不是「没有」")

	// ErrSpanVerifyFailed 这一段**走查过了，而它没通过**。
	//
	// ⚠️ 与 `ErrSpanUnverified` 分开，判据不是「它们看起来不同」，是**处置不同**：
	//
	//	ErrSpanUnverified    本次没走查过这一段          ⇒ 它不表示这一段有问题
	//	ErrSpanVerifyFailed  走查过了，而它没通过        ⇒ **别再重跑**，去读包在里面的真因
	//
	// ⛔ **它必须把真因包进来**（`fmt.Errorf("%w: %w", ErrSpanVerifyFailed, 真因)`）：
	// 只包哨兵不包真因，调用方就只剩一个类别名，**再也回不到现场**；
	// 只包真因不包哨兵，调用方**分不出「这一类」与「这一个错」**。⇒ 两头都要。
	ErrSpanVerifyFailed = errors.New("tickflow/store: 这一段走查没通过")

	// ErrLegacyMeta .meta 没有 format 字段（v0.3 之前写的），而这个源不可重放，
	// 所以 coverage 既不能当「已拉过」也不能当「没拉过」。补救：一个显式决定。
	ErrLegacyMeta = errors.New("tickflow/store: meta 版本未知且源不可重放——需要一个显式决定")
)

// —— 打开这个库时发现的事，与它们的处置 ——

// OpenState 是【打开这个库时发现的、需要编排处置的事】。
//
// ⛔ 它是一个【方法】而不是 `Open` 的返回值，理由是 `Open` 不在这个接口里
// （构造签名里带着 `dir` 这种实现细节）。而 C3b / D2b 要求编排为这两件事留声，
// 于是编排必须拿得到它们。
//
// ⇒ 让 Store 自己报，而不是让调用方【把这两个数转告】Syncer ——
// **转告是一句承诺，而承诺不可核。**
// 这与丙三之零那三处冲突是同一个形状：**编排被要求为一件事负责，
// 却够不到做那件事所需的东西**；而「让调用方转告」是它们共同的坏解法。
//
// ⚠️ 两格放进一个结构体，是因为它们**同轴**：都在打开的那一刻被发现，
// 且都【本层处置不了】—— 不是因为它们像。
type OpenState struct {
	// TruncatedTail 是打开时被截掉的残尾字节数（C3a）。
	//
	// > 0 ⇒ C3b 要留声。**截了不留声，与「本来没事」同形。**
	TruncatedTail int64

	// LegacyMeta 说这份 `.meta` 没有 `format` 字段（v0.3 之前写的）。
	//
	// ⛔ 它**不是「坏了」，是【要一个决定】** —— 而那个决定取决于
	// 「这个源能不能把那一段重新给一遍」，那要 Caps.Since，本层没有。⇒ 见 DecideLegacyMeta。
	LegacyMeta bool
}

// LegacyDecision 是 `format` 缺失时的处置。
//
// ⛔ **它住在根包，而不是住在某个实现里 —— 这不是搬家的便利**（同 Outcome 那一格）：
// 判定取决于【源的性质】，所以它是**接口契约的一部分**；留在实现包里，
// 别的实现就不受它约束。
//
// ⚠️ 而这一次上移还有一个更硬的理由，是实测出来的（丙三之零）：
// **唯一有资格调它的那一层，结构上调不到它。**
// 判定需要「源可不可重放」⇒ **而那不是 `Caps` 上的一个字段**（丙三冲突三实测）：
// 它由编排从 `Caps.Since` 推出来 —— 且它是【源 × 这一段】的性质，不是源的性质。
// 编排在根包；
// 而根包 import `store/segfile` 是 import cycle。
// ⇒ 它此前「生产侧零调用方」**不是「还没接线」，是「接不上」** ——
// 而那两者处置相反：前者接上就行，后者要动结构。
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

// String 让这个值在报告与错误里读得出来。
func (d LegacyDecision) String() string {
	switch d {
	case LegacyDiscard:
		return "作废重拉"
	case LegacyUnverified:
		return "标记未核并停"
	}
	return fmt.Sprintf("LegacyDecision(%d)", int(d))
}

// DecideLegacyMeta 是 D2a：`format` 缺失时怎么办，**取决于这个源能不能重放**。
//
// ⚠️ 它【不是常数】。这一条是被一次实录失败逼出来的：第一版把它写成
// 「一律作废重拉」，而那个理由错在一个没写出来的前提上——**它假定自愈可用**。
// 源不可重放时，自愈从来就不在选项里。
//
// 不可重放那一支返回 ErrLegacyMeta：它要的是**一个显式决定**（Force 或人工确认），
// 而不是一个自动动作。
//
// ⚠️ **射程改过一次，而旧的那句现在是假的**：它原来写「两种结果都要进
// SyncReport 是 D2b，而 SyncReport 还不存在 ⇒ D2b 不在本版」。
// `SyncReport` 已于丙一落地 ⇒ **那个缺席理由到期了**，D2b 在丙三之三接线。
func DecideLegacyMeta(replayable bool) (LegacyDecision, error) {
	if replayable {
		return LegacyDiscard, nil
	}
	return LegacyUnverified, ErrLegacyMeta
}
