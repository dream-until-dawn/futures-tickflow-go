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

	// ⛔ 这里原来有一个 `Force bool`（「忽略 coverage 强制重拉该段」），
	// 2026-09-12 **删掉了**，而删它的理由比它本身值钱：
	//
	//	它没有任何代码读 ⇒ **设成 true 什么都不会发生，而且不报错**
	//	实测：非测试的**代码行**里读取点 0 处；同一把尺子量
	//	`req.MaxConsecutiveFails` 得 2、`req.From` 得 17 ⇒ 那把尺子会报东西
	//
	// 🔴 它为什么躲过了 doccheck：那道检查守的是「**声明存在**」，而这个声明确实存在。
	// ⇒ 本仓那条「**外形不携带状态**」的新成员，**而这次外形是【一个字段的存在性】**。
	//
	// ⛔ **别把它加回来，除非同一颗提交里就有读它的那一行。**
	// 判据（本仓那条）：**一个被许诺却不生效的开关，比没有这个开关坏** ——
	// 没有它，调用方会去找别的路；有它，调用方以为事情已经办了。
	// ⇒ 它的两个真实用途（交易所修正历史结算价 · 上游抖动被固化成「确认无数据」）
	// 今天走**实测过**的那条路：删掉该周期的 `.dat` 与 `.meta`，再按交易日从早到晚重拉
	// （`git cat-file -p v0.4.1`，底座 f218ae6a）。见 docs/design.md 的 SyncRequest 一节。
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

	// UngatedSource 非空时说：这次同步向源要过数据，而【我们装的限流闸门一次都没被用到】。
	//
	// 🔴 它接住的是一个**假绿**（评审方 2026-09-09 造，我复现读数一致）：
	// 一个「收下 client、原样丢掉、自己造一个裸 client」的 `SourceFactory` ——
	// 对端收到 5 次请求、Bars=5、闸门被问 0 次，**而报告说 Complete()**。
	// ⇒ 设计里那句残余射程（「调用方仍可以无视它」）为真，**而它漏了后半句：
	// 那样做报告不会提。** 堵不住和不留声是两件事。
	UngatedSource []string

	// UngatedOK 说这个源**适不适用**上面那条检查（`Caps.ClientUse == ClientUseHTTP`）。
	//
	// ⛔ **不可省，理由与 `NightAbsentOK` 一模一样**：
	// 「不适用」与「适用且没问题」在 `len(UngatedSource) == 0` 上**不可分辨**。
	// ⇒ 而上一版没有这一格，于是一个不走 HTTP 的源**每一次同步都被诬告**
	//（实测：Ungated 1 条 · Complete()=false，永远）——
	// **而一个长期误报的告警，最终会关掉它自己。**
	UngatedOK bool

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
// ⛔ **而它现在是从 `Incidents()` 算出来的，不再自己列一遍那六格** ——
// 那一步的全部内容是：**让「加一个事实」成为唯一的扩展方式。**
//
//	上一版  加一种痕迹 ⇒ 加字段 ＋ **改这个判断式**（改一个定义）
//	这一版  加一种痕迹 ⇒ 加字段 ＋ **在 Incidents() 里加一行**（加一个事实）
//
// ⇒ 而这正是那条判据自己的形状：**补留声是加一个事实，放宽是改一个定义。**
// 上一版每加一种痕迹都要动一次定义 —— **而定义每被动一次，就有一次被动错的机会。**
func (r SyncReport) Complete() bool { return len(r.Incidents()) == 0 }

// Incidents 交出这次同步留下的【每一条需要人看一眼的痕迹】。
//
// ⛔ **它是那六格的唯一汇合处**，而 `Complete()` 只是 `len(...) == 0`。
// ⇒ 加一种新痕迹时**只动这里**：
// 判断式不用改，`String()` 不用改，测试里那张「每一格都要能单独翻它」的表也不用改形状。
//
// ⚠️ 它比一个 bool 多带一样东西：**是哪几条** ——
// 而那正是 `String()` 上一版只能写「（有留下痕迹，见明细）」的原因。
//
// ⛔ **到期条件（评审方 2026-09-09 要求写下来，我认）**：
// `[]string` 只在「调用方拿它来**看有没有、看是哪几条**」时够用。
// **哪天有人要按类别分支**（`if 是截断 then …`），
// 正确的动作是**给它一个类型**，而不是去 `strings.Contains` ——
// 后者会把⑱ 那一格请回来：**把已经分开的类别，用字符串又粘回去。**
// ⇒ 这一条自己就是自己的有效期：**`Incidents()` 的返回值第一次被拿去做【子串匹配】那天，它到期。**
//
// 🔴 **而这句话【故意不写出那个模式本身】** —— 上一版写了，于是：
//
//	全仓搜那个模式 ⇒ 命中 **1 处**，而那一处**就是定义这条到期的句子自己**
//	⇒ 今天不出事只因为没有人在自动查它；谁把它变成一条 grep 守卫，它当场就红
//
// ⇒ 判据：**一条「出现 X 就到期」的条件，不要在条件里写出 X。**
// 而这与 `elsewhere` 那条守卫栽的第三次是同一格：
// **一个文本检查器的文档，是它自己的语料。**
//
// ⇒ 查法（写成人能执行、而这段文字自己不会命中的形式）：
// 在**非注释的 Go 代码**里，找「拿 `Incidents()` 的结果去做 `strings` 包的子串判断」。
func (r SyncReport) Incidents() []string {
	var out []string
	if note, halted := r.Halt.note(); halted {
		out = append(out, note)
	}
	for _, s := range r.UngatedSource {
		out = append(out, "闸门没被用到："+s)
	}
	for _, s := range r.TruncatedTails {
		out = append(out, "开库时截过残尾："+s)
	}
	for _, s := range r.LegacyMetaDiscarded {
		out = append(out, "旧 meta 已作废重拉："+s)
	}
	for _, s := range r.LegacyMetaUnverified {
		out = append(out, "旧 meta 标记未核并停："+s)
	}
	if r.Misaligned > 0 {
		out = append(out, fmt.Sprintf("%d 根对不上网格", r.Misaligned))
	}
	if len(r.AnomalousDays) > 0 {
		out = append(out, fmt.Sprintf("%d 个交易日的模板与实际矛盾：%v",
			len(r.AnomalousDays), r.AnomalousDays))
	}
	return out
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
	if n := len(r.Incidents()); n > 0 {
		// ⚠️ 报**条数**，不报内容 —— 这一行是给人扫一眼的，明细去读 `Incidents()`。
		// 而上一版只能写「有留下痕迹」：**一个 bool 说不出「几条」。**
		s += fmt.Sprintf("（%d 条痕迹，见 Incidents()）", n)
	}
	return s
}
