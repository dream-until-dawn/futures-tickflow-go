package tickflow

import "fmt"

// 本文件是 v0.3 同步层【丙一】的类型：请求与报告。对应 docs/design.md §七之十一。
//
// ⛔ 这一片**只落类型与接口**，不落编排（`Sync` 本体在丙三）。
// 拆开的理由不是工作量，是**每一颗提交只回答一个问题** —— 那条是评审成本的直接函数。

// SyncRequest 是一次同步请求。
type SyncRequest struct {
	Symbol Symbol

	// Period 是【封口接口】，不是 IntradayPeriod。
	//
	// ⛔ 上一版设计写的是 `IntradayPeriod`，而 `Daily` 是 `CalendarPeriod`
	// （`period.go`）—— **那个字段装不下今天唯一同步得了的周期**，编译期就过不去。
	// 登记：七之零冲突一。
	Period Period

	// From 是起始交易日，必填。
	From TradingDay

	// To 为 0 表示「到最后一个已收盘的交易日」，由 ClipToLastClosed 定（SYN-1）。
	//
	// ⚠️ 这里 0 是**合法的哨兵**，与别处「零值不合法」不冲突：
	// 那些地方 0 是一个**answer**（会被误读成日期/根数），
	// 而这里 0 是一个**question**（「你替我定」）—— 两者的危险方向相反。
	To TradingDay

	// MaxConsecutiveFails 是失败预算：**连续**失败几次就停（丙二）。
	//
	// ⛔ 取【连续】不取【累计】—— 一次抖动与一次宕机，在**第一次失败的时候**是同一件事，
	// 区别只在「后来会不会恢复」，而**「连续 K 次」等到了那个「后来」**；
	// 「累计 K 次」不成立，它把整段历史里散落的抖动加起来当成一次宕机。
	//
	// ⚠️ **不给推荐值** —— 本仓没有量过这个分布（对比：无夜盘那个阈值 2 是十年
	// 2595 个交易日量出来的）。给一个没量过的推荐值，下一个人会把它读成「量过的」，
	// 而 `maxFetchFails = 2` 上已经栽过同族。
	//
	// ⛔ **而这里的零值是【安全的那一侧】，与本仓别处「零值不合法」的理由相反：**
	//
	//	别处的 0  会**静默地做错事**（当成「没有硬顶」「确认没有」…）⇒ 哑 ⇒ 必须拒
	//	这里的 0  = 「一次失败就停」⇒ **吵着停下**，不丢数据 ⇒ 安全
	//
	// ⇒ 判据仍是那条：**给一个取值定级，取它最哑的那个后果。** 这里最哑的后果不存在。
	MaxConsecutiveFails int

	// Force 忽略 coverage 强制重拉该段。
	//
	// 两个正当用途：**交易所修正了历史结算价**，以及
	// **上游抖动被固化成「确认无数据」** —— 后者不重拉就会永久留一个不该存在的空洞。
	Force bool
}

// SyncReport 是一次同步的全部结论。
//
// ⛔ **凡是进了这个结构体的数，都必须是【当前值】**；叙事（「上一版是多少」）只进注释。
// 而「当前」要钉死成**产生它的那一刻** —— 报告在 T 产生、在 T+n 被读，
// 没有那一刻跟着走，**它到第二天就是叙事了**。
type SyncReport struct {
	// Requested 是请求的闭区间。
	Requested [2]TradingDay

	// Covered 是日历【能回答】的闭区间（Calendar.Covers）。
	//
	// ⛔ CoversOK 为 false 时 Covered **不填零值区间**（SYN-4）：
	// 填 [0,0] ⇒ 一个零值区间**读起来仍然像一个区间**。
	Covered  [2]TradingDay
	CoversOK bool

	// Synced 是实际同步到的闭区间。
	Synced [2]TradingDay

	// Bars 是本次落盘的记录条数。
	Bars int

	// Misaligned 是**根数**：边界对不上网格的那些根（SYN-2）。
	Misaligned int

	// AnomalousDays 是【模板与实际矛盾】的**交易日**，不是根数（SYN-5）。
	//
	// ⚠️ 它与 Misaligned **单位不同，而那是有意的**：
	// 一个问「有多少根落在网格外」，一个问「哪几天可疑」。
	// **写在一起，免得下一个人去「统一」它们。**
	AnomalousDays []TradingDay

	// Gaps 是**六类**缺口（SYN-3）。四类改六类的理由见七之零冲突七：
	// 存储侧那两个「答不了」原来一格都没有。
	Gaps []Gap

	// NightAbsentRun 是本次同步里【连续无夜盘的交易日】那一段，给人看的，不下判断。
	NightAbsentRun NightAbsent

	// NightAbsentOK 说这个品种**适不适用**那条检查（标称夜盘是否 > 0）。
	//
	// ⛔ 不可省：「不适用」与「适用但没找到」在 `NightAbsentRun.Days == 0` 上
	// **不可分辨**（同 SYN-4 那个形状）。`CFFEX.IF` 标称夜盘就是 0，
	// 照直报会把它的整段历史都报成「无夜盘」。
	NightAbsentOK bool

	// —— 下面三格是 C3b / D2b 的落声处。——
	//
	// ⛔ 它们此前**在这个结构体的声明里根本不存在**（2026-09-09 实测：各 0 处），
	// 而 §6.1 的 C3b / D2b 明写着「必须进 SyncReport」。
	// ⚠️ 那不是「守卫看不见它」，是**文档要求的字段在文档自己的声明里不存在** ——
	// 两个诊断的症状一模一样（登记表里都是 0），**而处置相反**：前者补登记，后者补声明。

	// TruncatedTails 记「哪些库开的时候截掉了残尾」（C3b）。
	// 截了不留声 ⇒ **与「本来没事」同形**。
	TruncatedTails []string

	// LegacyMetaDiscarded / LegacyMetaUnverified 是 D2b 的两种处置（作废 / 标记）。
	// 判对了却不留声 ⇒ 自愈动作与「本来没事」同形。
	LegacyMetaDiscarded  []string
	LegacyMetaUnverified []string

	// Halt 是【这次同步为什么停下来】。零值 HaltUnknown ⇒ 留声（见 HaltReason）。
	//
	// ⛔ 它进这个结构体、而不是只当循环里的一个局部变量，是评审方 2026-09-09
	// 钉的条件，而理由是一条**具体的路径**：
	// `Sync` 早退时返回 `SyncReport{}` —— 局部变量在那条路上**根本不参与**。
	// ⇒ 落在报告上，那份零值报告才带得上「没有记录理由」这件事。
	Halt HaltReason
}

// Complete 报「这一次同步有没有留下【需要人看一眼】的痕迹」。
//
// ⛔ **它是一个方法，不是一个字段 —— 而那不是风格。**
//
//	存一个派生字段  ⇒ 它有了【和明细分叉】的能力，我们只能"保证"不让它分叉
//	写成一个方法    ⇒ **它没有存储位，也就没有分叉的能力**
//
// ⇒ 同 `GapKind` 那一格选「类型化常量」而不是「抽名字比字符串」：
// **能让求值期消掉的错误，不要留给一条断言。**
//
// ⚠️ **边界（必须一起读）**：报告要被序列化、被别的进程读 ⇒
// **跨出去那一刻它终究会变成一个副本**。准确的说法是：
// **在【同一份内存表示】里派生值不该有存储位；
// 跨进程那一份的正确性由【序列化那一步】负责，不由字段负责。**
// 而「序列化那一步」是一个**位置**，所以它可以被守：
// 一条测试 —— 反序列化回来的 `Complete()` 必须等于序列化前的。
//
// ⛔ **它【故意不看 Gaps】，而这一格要写死：**
//
//	缺口不是异常，是**结果** —— 一次正常的同步本来就会报出「不是交易日」「拉过确认没有」
//	而把六类折成一个 bool，正是⑱ 那一格：**第五类「走一遍就行」与第六类「必须问人」
//	处置完全不同，合成一位就把刚分开的两者又粘回去了**
//	⇒ 要按缺口判断，读 Gaps；本方法只回答【过程有没有留下痕迹】
//
// ⛔ **而「没同步完」现在【是】一种痕迹（丙三之二）** —— 它经 `Halt` 进来，
// **不是**靠比较 `Synced` 与 `Requested`。两条路的差别要写死：
//
//	比 Synced vs Requested  请求超出 Covers() 时 Synced 天然更短 ——
//	                        那是【结果】不是异常 ⇒ 一比就产生假警报
//	读 Halt                 它记的是【为什么停】—— 与「范围天然更短」分得开
//
// ⇒ 一般式（评审方 2026-09-09）：**当一个「总状态」漏报了某件事，
// 先问那件事有没有【留声】—— 补留声比放宽总状态安全：
// 留声是加一个事实，放宽是改一个定义。**
func (r SyncReport) Complete() bool {
	_, halted := r.Halt.note()
	return !halted &&
		len(r.TruncatedTails) == 0 &&
		len(r.LegacyMetaDiscarded) == 0 &&
		len(r.LegacyMetaUnverified) == 0 &&
		r.Misaligned == 0 &&
		len(r.AnomalousDays) == 0
}

func (r SyncReport) String() string {
	s := fmt.Sprintf("请求 %s..%s", r.Requested[0], r.Requested[1])
	if r.CoversOK {
		s += fmt.Sprintf("，日历覆盖 %s..%s", r.Covered[0], r.Covered[1])
	} else {
		s += "，日历整个答不了这个品种"
	}
	s += fmt.Sprintf("，同步 %s..%s，%d 根，%d 段缺口",
		r.Synced[0], r.Synced[1], r.Bars, len(r.Gaps))
	if note, halted := r.Halt.note(); halted {
		s += "，停因：" + r.Halt.String()
		_ = note
	}
	if !r.Complete() {
		s += "（有留下痕迹，见明细）"
	}
	return s
}
