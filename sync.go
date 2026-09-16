package tickflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// 本文件是 v0.3 同步层【丙三之二】：`Sync` 本体。对应 docs/design.md §七之十一。
//
// ⛔ 本片**不落** SYN-6 / SYN-10 / C3b / D2b —— 那四条要新的行为（走查、真因、
// 两处留声的接线），在丙三之三。**它们是整条缺席，不是做了一半。**

// SourceFactory 造一个源，而它**收**一个已经装好闸门的 `http.Client`。
//
// ⛔ **为什么是「收」不是「给」** —— 这是丙三冲突一的处置。
// 必做一原本写的是「Syncer 自己造 client 交给源」，而那条路**不存在**：
// `Source` 接口只有 `Bars` / `Caps`，没有任何口子能把一个 client 交进去；
// 而 `WithHTTPClient` 是各实现包的构造选项，**根包 import 不了那些包**（import cycle，实测）。
//
// ⇒ 于是反过来：**Syncer 造 client，调用方造源** ——
// 调用方没法「塞一个 client 进来」，它只能**接**一个。
// 「有没有闸门」于是不再是调用方的一句承诺，**它连说这句话的位置都没有。**
//
// ⚠️ **残余射程**：调用方仍可以在自己的 lambda 里**无视**收到的那个 client、另造一个。
// 而那与「忘了配」不是一回事 —— 它是写在调用方自己代码里、**看得见的一个动作**。
// **堵不住，但它不再是默认值** —— 而必做一防的正是默认值。
type SourceFactory func(*http.Client) Source

// gateCounter 数「经过我们那个闸门的请求」有多少次。
//
// 🔴 **它存在的理由是一处【假绿】**（评审方 2026-09-09 造，我独立复现，读数一致）：
// 一个「收下 client、原样丢掉、自己造一个裸 client」的 `SourceFactory` ——
//
//	对端收到 5 次请求 · Bars=5 · **注入的闸门被问 0 次** · 没有超时
//	而报告 **Complete() == true**，留声 0 条
//
// ⇒ 设计里那句残余射程（「调用方仍可以无视收到的那个 client」）**说的是真的，
// 而它漏了后半句：那样做【报告不会提】。**
// ⇒ 判据：**一个「堵不住」的洞，至少要让它【出声】** ——
// 堵不住和不留声是两件事，而我上一版把它们当成了一件。
type gateCounter struct {
	next http.RoundTripper
	mu   sync.Mutex
	n    int
}

func (g *gateCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
	return g.next.RoundTrip(r)
}

func (g *gateCounter) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

// HaltReason 是同步循环**为什么停下来**。
//
// ⛔ **零值是「不明中止」，而那是【封口】不是疏忽。**
//
//	列举法  「因预算耗尽而中止要留声」 ⇒ 记得写的那些会留声
//	封口法  「出口必须交出一个理由」   ⇒ **忘了写的那个也会留声**
//
// ⇒ 而封口靠的不是自觉：它是 `fetch` 的**返回值** ——
// **Go 要求每一条出口都给出返回值**，所以「加了一条出口却没写理由」写不出来。
//
// ⚠️ 而零值仍然有用，且正是它接住了另一半：
// 一份**没有走过那个循环**的报告（`SyncReport{}`、或 `Sync` 早退时返回的那份）
// 带着 `HaltUnknown` ⇒ **留声** ⇒ `Complete()` 为假。
// **「没跑过」于是落在吵的那一侧，而不是读成「跑完了且干净」。**
type HaltReason int

const (
	// HaltUnknown 没有人记录过为什么停 —— 包括「压根没跑」。留声。
	HaltUnknown HaltReason = iota
	// HaltDone 跑到 `To` 了。**唯一不留声的那个。**
	HaltDone
	// HaltBudget 连续失败次数超过了 `MaxConsecutiveFails`。留声，并且 `Sync` 返回错误。
	//
	// ⛔ **它此前还盖着另外两条出口**（落盘失败 / 扩 coverage 失败），而那是一个缺陷：
	// 实测三条出口在调用方能读到的每一个通道上的取值 ——
	//
	//	通道                                 超预算   落盘失败   扩 coverage 失败
	//	errors.Is(err, ErrBudgetExhausted)   true    **false**  **false**
	//	Halt.String()                        预算耗尽  预算耗尽   预算耗尽      ← 后两个是假话
	//	Incidents() 那句留声                  ——      同上       同上         ← 后两个是假话
	//	盘上根数 / coverage 段数              0 / 0    0 / 0     **1 / 0**
	//
	// 🔴 **两个通道互相矛盾**：`errors.Is` 说「不是预算」，而 `String()` 说「预算耗尽」。
	// ⇒ 读的人按后者行事：**加大预算、稍后重试** —— 而那正是 ≤v0.4.0 上加重损坏的动作。
	HaltBudget
	// HaltContext 调用方取消了。留声。
	HaltContext

	// HaltNoTradingDays 请求区间里一个交易日都没有。**不留声 —— 它是结果，不是异常。**
	//
	// ⚠️ 「没什么可同步」是一个**完整而正确**的回答，而调用方查得到全部依据：
	// `Requested` 在、`Bars=0`、而 `Gaps` 里那一段写着「不是交易日」。
	HaltNoTradingDays

	// HaltOutsideCoverage 请求整段落在日历能回答的范围之外。**同样不留声。**
	//
	// ⛔ 而它**只有在 `Gaps` 被填过之后才配不留声**（评审方 2026-09-10 的裁法，我认）：
	// **只改 `Complete()` 而不加载体，那一步是【放宽】；先加事实再谈总状态，才是【自洽】。**
	HaltOutsideCoverage

	// ——「写失败」这一族：`HaltStoreWrite` 与 `HaltCoverageWrite` ——
	//
	// ⛔ **这两条【不按数值与前面那些相邻】，而那是故意的**：
	// 它们是后加的，**加在末尾**，好让既有取值一个都不动（`HaltNoTradingDays` 与
	// `HaltOutsideCoverage` 因此仍是 4 与 5）。
	//
	// ⚠️ 判据（评审方 2026-09-11 定，我认）：**可见且有界的代价，优先于静默且无界的代价。**
	//
	//	插在中间  读起来成一族（+1），**而既有取值静静地 +2** —— 这种依赖不会在编译期出声
	//	追加末尾  分组被打散（−1），**而任何读这段代码的人当场就看得见**，并能自己判值不值
	//
	// ⇒ 分组这件事写在这儿（注释是读者真正会看的地方），**一分钱数值代价都不用付**。

	// HaltStoreWrite 把这一批记录写进盘时失败了（`Store.AppendBars`）。留声，`Sync` 返回错误。
	//
	// ⚠️ 与 `HaltBudget` 分开，判据不是「它们看起来不同」，是**处置不同**：
	// 超预算 ⇒ 上游在闹脾气，等一等再来是对的；
	// 落盘失败 ⇒ **是本机的问题**（盘满 / 权限 / 文件被占），加大预算一点用都没有。
	HaltStoreWrite

	// HaltCoverageWrite 记录已经写进盘了，**而把这一段登记进 coverage 时失败了**
	// （`Store.CommitSpan`）。留声，`Sync` 返回错误。
	//
	// 🔴 **它和 `HaltStoreWrite` 也必须分开，而理由是那条最贵的**：
	// 这一条留下的是**孤儿记录** —— 盘上有这一批，coverage 里没有（实测 1 根 / 0 段）。
	// ⇒ **直接重跑同一次 Sync，会把同一批记录【再追加一遍】** ——
	// 而那正是 v0.4.1 修的那个毁库动作。
	// ⇒ 所以它的留声必须说「**别直接重试**」，而不是「已同步的部分是完整的」。
	//
	// 📎 本仓那条：**判两个东西该不该共用一个名字，看【处置】分不分岔，不看成因像不像。**
	HaltCoverageWrite

	// HaltAllCovered 请求区间里的交易日**全都已经在 coverage 里**，一天都不必再拉。
	// **不留声 —— 它是结果，不是异常。**
	//
	// ⛔ 它**必须与 `HaltNoTradingDays` 分开**，而判据是本仓那条（处置分不分岔）：
	//
	//	HaltNoTradingDays  请求区间里一天都不交易   ⇒ 换一个区间
	//	HaltAllCovered     交易日有，而**已经拉过** ⇒ 什么都不用做；真想重拉，
	//	                   删掉该周期的 .dat/.meta 再按交易日从早到晚重拉
	//
	// 🔴 两者在调用方那里的**每一个数上都相同**（`Bars=0` · `Gaps` 非空 · 不返回错误）——
	// ⇒ 少了这一档，它们**共用一句话**，而那正是本仓最怕的形状。
	//
	// ⚠️ 而它同样**只有在事实落成之后才配不留声**：`rep.SkippedCovered` 里写着跳过了哪几段。
	// （评审方 2026-09-10 对 `HaltOutsideCoverage` 的裁法，逐字适用。）
	//
	// 📌 加在**末尾**，既有取值一个都不动 —— 见上面那段「可见且有界的代价」。
	HaltAllCovered
)

// note 返回这次中止的留声文字，以及**要不要留声**。
//
// ⚠️ 只有 HaltDone 不留声 —— 判据写成「白名单」而不是「黑名单」：
// **黑名单漏掉的那个会静默通过，白名单漏掉的那个会吵。**
func (h HaltReason) note() (string, bool) {
	switch h {
	case HaltDone, HaltNoTradingDays, HaltOutsideCoverage, HaltAllCovered:
		// ⚠️ 这四个都不留声，而**理由不同**，写在一起免得被读成一类：
		//	HaltDone             跑完了
		//	HaltNoTradingDays    没有东西可跑 —— 而 Gaps 里写着「不是交易日」
		//	HaltOutsideCoverage  日历答不了这一段 —— 而 Gaps 里写着「日历答不了」
		//	HaltAllCovered       都拉过了 —— 而 **SkippedCovered 里写着跳过了哪几段**
		// ⇒ 后三个之所以不留声，是因为**那件事已经落成了一个事实**。
		return "", false
	case HaltBudget:
		return "同步因连续失败用尽预算而中止——已同步的部分是完整的，未同步的部分没有被记为「拉过」", true
	case HaltContext:
		return "同步被调用方取消——已同步的部分是完整的", true
	case HaltStoreWrite:
		return "把这一批记录写进盘时失败了——已同步的部分是完整的，这一批没有被记为「拉过」；" +
			"这是本机的问题（盘满／权限／文件被占），加大预算没有用", true
	case HaltCoverageWrite:
		// ⛔ 这一句和上面两句**形状不同，而那是故意的**：
		// 前两句能说「已同步的部分是完整的」，是因为那一批根本没落盘；
		// 而这一条**落盘成功、登记失败** ⇒ 盘上有这一批而 coverage 里没有。
		// 🔴 直接重跑会把同一批**再追加一遍** —— 那正是 v0.4.1 修的那个毁库动作。
		return "记录已经写进盘了，而把这一段登记进 coverage 时失败了——" +
			"盘上有这一批而 coverage 里没有（孤儿记录）。" +
			"⛔ 别直接重跑同一次同步：那会把同一批记录再追加一遍。" +
			"先跑一次 Verify 看这一段的状态——⚠️ 它多半会报「记录的交易日落在本段之外」，" +
			"那说的是这一批孤儿记录，不是文件坏了——再决定怎么办", true
	}
	return "这份报告没有记录它为什么停下来（可能是它压根没有跑过）——" +
		"按【没同步完】处理，别读成「跑完了且干净」", true
}

// String 让它在报告与错误里读得出来。
func (h HaltReason) String() string {
	switch h {
	case HaltUnknown:
		return "未记录"
	case HaltDone:
		return "跑完"
	case HaltBudget:
		return "预算耗尽"
	case HaltStoreWrite:
		return "落盘失败"
	case HaltCoverageWrite:
		return "扩 coverage 失败（盘上已有这一批）"
	case HaltContext:
		return "被取消"
	case HaltNoTradingDays:
		return "区间里没有交易日"
	case HaltOutsideCoverage:
		return "区间在日历覆盖之外"
	case HaltAllCovered:
		return "区间里的交易日已经全部覆盖过"
	}
	return fmt.Sprintf("HaltReason(%d)", int(h))
}

// ErrBudgetExhausted 连续失败次数超过了 `MaxConsecutiveFails`。
//
// ⛔ 它是**一个错误**，不是一份「成功同步了 0 天」的报告（必做二）。
// 报告仍然一并返回 —— 已经落盘的那部分是真的，调用方要看得到。
var ErrBudgetExhausted = errors.New("tickflow: 连续失败用尽预算，同步中止")

// SyncerConfig 是造一个 Syncer 要的全部东西。
//
// ⛔ **它不收 `*http.Client`**，只收 `Pacer` ＋ `NewSource`（必做一）。见 SourceFactory。
type SyncerConfig struct {
	Calendar  Calendar
	Store     Store
	NewSource SourceFactory

	// Pacer 是限流闸门的策略。**必填** —— `pacing` 不设「不限流」的默认值。
	//
	// 真的不想限流，写 `pacing.NoPacing()`：
	// **一个具名的「我不限流」是一个看得见的选择，一个 nil 不是。**
	Pacer pacing.Pacer

	// Timeout 是每一次上游请求的超时。**必填**，零值被拒。
	//
	// 🔴 **这一格是 SourceFactory 顺手拿走的，而第一版把它丢了**
	//（评审方 2026-09-09 问「超时归谁定」，我去量，发现的不是一个空位，是一次静默回退）：
	//
	//	两个源自己的默认   &http.Client{Timeout: 30 * time.Second}
	//	pacing.Client()    &http.Client{Transport: rt}   ← Timeout 零值 = 【没有超时】
	//	WithHTTPClient     c.http = h                    ← 整个替换，不是合并
	//	⇒ 走 SourceFactory 这条路，那 30 秒被【静默拿掉了】
	//
	// ⛔ **而它链到失败预算**：`MaxConsecutiveFails` 数的是【错误】，
	// 而一个挂住的请求不产生错误 —— 它只是不返回。
	// ⇒ **没有超时的话，那个预算有一整类失败接不住**，而那一类恰恰是最长的那种。
	//
	// ⚠️ 不在 `pacing` 那一层补默认值：它明写了「不设超时默认——超时是调用方的事」，
	// 而那个理由仍然成立（**便利构造函数顺手塞默认值 = 替使用者做了一个他不知道的决定**）。
	// ⇒ 决定被搬到了这一层，就在这一层要一个显式的答案。
	//
	// ⛔ **而「每一次上游请求的超时」这句话【字面不成立】** ——
	// 它是**等待＋请求的总预算**（评审方 2026-09-09 提，我独立实测，读数如下）：
	//
	//	对照 A  不限流 + 200ms 超时，连发两次   ⇒ 都成功（服务器够快）
	//	B0      闸门 1s + 200ms（闸门不用等）   ⇒ 1ms 成功（闸门本身不坏）
	//	B1      闸门 1s + 200ms（闸门要等 1s）  ⇒ **200ms 失败，请求根本没发出去**
	//	        err = pacing: 等待被取消：context deadline exceeded
	//
	// ⇒ **落地要求：`Timeout` 必须大于限流间隔**，否则第二次请求起一律超时。
	// ⚠️ 而构造时**判不了**这一条：`Pacer` 接口只有 `Wait(ctx)`，**问不出它的间隔**
	//（评审方核过才提，我复核一致）⇒ 它只能写在这儿，不能变成一条检查。
	// ✅ 而它**不是假绿**：真撞上时 `Halt=预算耗尽`、`Complete()=false` —— 它出声。
	//
	// ⚠️ 附带一格：Go 自己给的错误文本是 `while awaiting headers` ——
	// **它指向上游，而上游从来没有被联系过。** 这一句会把查问题的人带偏。
	//
	// 真的不想要超时，写 `NoTimeout` —— 同 `NoPacing()` / `BatchDaysUnbounded`：
	// **不消灭那个选择，而是逼它变成一个写得出来、看得见的动作。**
	Timeout time.Duration
}

// NoTimeout 是**具名的**「我不要超时」。
//
// ⚠️ 它存在的全部理由是让那个决定看得见：没有它，「不要超时」只能靠传 0 来表达，
// 而那与「忘了填」不可分辨 —— 而这两者最哑的后果差得很远：
// 想好了不要超时的人知道自己在等什么；忘了填的人会看着同步挂住而不知道为什么。
const NoTimeout time.Duration = -1

// Syncer 把一个源同步进一个库。
type Syncer struct {
	cal   Calendar
	store Store
	src   Source

	// gate 数「经过我们那个 http.Client 的请求」。见 gateCounter。
	gate *gateCounter
}

// NewSyncer 造一个 Syncer，**并在这里把闸门装上**。
//
// ⛔ 装配顺序是这一格的全部内容：`pacing.Client(p)` 先把闸门装进一个 `http.Client`，
// 然后才把那个 client 交给 `NewSource`。**调用方拿不到「装配之前」的那一刻。**
func NewSyncer(cfg SyncerConfig) (*Syncer, error) {
	var errs []error
	if cfg.Calendar == nil {
		errs = append(errs, errors.New("Calendar 是 nil——没有日历就判不了「哪一天要拉」"))
	}
	if cfg.Store == nil {
		errs = append(errs, errors.New("Store 是 nil——没有库就没有「拉过没有」这件事"))
	}
	if cfg.NewSource == nil {
		errs = append(errs, errors.New("NewSource 是 nil——本类型不接受一个造好的 Source，"+
			"因为那样「它的 http.Client 有没有闸门」就成了调用方的一句承诺"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("tickflow: NewSyncer 的配置立不住: %w", err)
	}
	// ⛔ Pacer 为 nil 时这里报 ErrNoPacer——**不是「默认不限流」**。
	c, err := pacing.Client(cfg.Pacer)
	if err != nil {
		return nil, fmt.Errorf("tickflow: NewSyncer 装不上限流闸门: %w", err)
	}
	switch {
	case cfg.Timeout == NoTimeout:
		// 具名的「我不要超时」⇒ 保持 http.Client 的零值语义。
	case cfg.Timeout > 0:
		c.Timeout = cfg.Timeout
	default:
		return nil, fmt.Errorf("tickflow: NewSyncer 的 Timeout=%v 不合法——"+
			"必须给一个正的超时，或写 NoTimeout；"+
			"0 说不清是「想好了」还是「忘了」，而忘了填的后果是"+
			"【挂住的请求不产生错误，于是失败预算永远不会触发】", cfg.Timeout)
	}
	// ⛔ 计数器装在【交出去之前】—— 交出去之后就没有我们的位置了。
	gate := &gateCounter{next: c.Transport}
	c.Transport = gate

	src := cfg.NewSource(c)
	if src == nil {
		return nil, errors.New("tickflow: NewSource 返回了 nil——" +
			"而一个 nil 的 Source 会在第一次调用时 panic，那比现在报错晚得多")
	}
	if h, ok := src.(CalendarHolder); ok {
		if err := sameCalendar(h.Calendar(), cfg.Calendar); err != nil {
			return nil, fmt.Errorf("tickflow: NewSyncer 的日历与源的日历对不上: %w", err)
		}
	}
	return &Syncer{cal: cfg.Calendar, store: cfg.Store, src: src, gate: gate}, nil
}

func fmtMs(ms int64) string { return time.UnixMilli(ms).In(CST).Format("2006-01-02 15:04") }

// sameCalendar 判源交出的日历与 Syncer 的日历是不是**同一个**。见 CalendarHolder。
func sameCalendar(fromSource, fromConfig Calendar) error {
	if fromSource == nil {
		return errors.New("源交出的日历是 nil——它归交易日用的是什么，从这一层看不见")
	}
	ts, tc := reflect.TypeOf(fromSource), reflect.TypeOf(fromConfig)
	if ts != tc {
		return fmt.Errorf("源用的是 %v，SyncerConfig.Calendar 是 %v——两份日历对「这一根属于哪天」的答案可以不同，而不会报错；"+
			"把同一个日历同时交给源和 Syncer", ts, tc)
	}
	if !ts.Comparable() {
		return fmt.Errorf("日历类型 %v 不可比较，核不了两边是不是同一个——传指针", ts)
	}
	if fromSource != fromConfig {
		return fmt.Errorf("源与 SyncerConfig 各持一份 %v，而不是同一个——内容相同也不行（等价判不了，同一性判得了）；"+
			"把同一个日历同时交给源和 Syncer", ts)
	}
	return nil
}

// Sync 把 `req` 那一段同步进库，并交出一份报告。
//
// ⚠️ **报告在返回错误时也是有内容的**：已经落盘的那部分是真的。
// 而它一定带着一个 `Halt` —— 早退的那些路径带的是 `HaltUnknown`，
// 于是它们的报告 `Complete()` 为假。
//
// —— ⛔ `err` 管什么、不管什么（2026-09-12 裁定，评审方判，我认）——
//
//	err       ＝ **这一次 Sync 动作本身没能完成它该做的事**
//	            （参数立不住 · 源的能力立不住 · 预算耗尽 · 落盘失败 · 扩 coverage 失败 ·
//	              被取消 · Store 违约）
//	库的状态  ＝ **Complete() ＋ Gaps** —— 走查失败在这里，**不在 err**
//
// 🔴 **代价，明码写在这儿**：库已经坏了、而本次同步什么都没做错时，**`err` 为 nil**。
// ⇒ **凡「我要知道这个库好不好」的调用方，判 `err` 不够，必须读 `Complete()`。**
//
// ⚠️ 它是本仓那条老病的一个**留着不治**的实例：`err == nil` 同时意味着
// 「一切都好」与「本次没做错，而库是坏的」——**两种状态共用一个数**，而它们的处置相反。
// 我们不靠消灭这个歧义来解决它（`err` 确实不该管库的历史状态），
// **而是靠把它写在读这个数的人面前**。⇒ 不写，它就是一个静默风险。
//
// 📎 这条裁决的由来值得留着：丙片（挑段跳过已覆盖）之前，一个坏库上的重跑**看起来**会报错——
// 而那个错来自「重拉第一天 ⇒ 交易日倒退」，**是巧合，不是契约**（走查失败从来只进 Gaps）。
// 🔴 ⇒ **一个「一直都在出声」的信号，在你去问它「它凭什么出声」之前，
// 你不知道它是契约还是巧合** —— 而这一次是行为变了**逼**我们去问的。
func (s *Syncer) Sync(ctx context.Context, req SyncRequest, now int64) (SyncReport, error) {
	k := req.Symbol.ProductKey()
	rep := SyncReport{}

	if req.Period == nil {
		return rep, errors.New("tickflow: Sync 的 Period 是 nil——不给周期就没法判一根算不算完结")
	}
	if !req.From.Valid() {
		return rep, fmt.Errorf("tickflow: Sync 的 From=%d 不是一个合法交易日", int32(req.From))
	}

	// —— To == 0 是一个 question，由 ClipToLastClosed 回答（SYN-1）——
	to := req.To
	if to == 0 {
		cf, ct, ok := s.cal.Covers(k)
		if !ok {
			return rep, fmt.Errorf("tickflow: 日历覆盖不到 %s: %w", k, ErrUncovered)
		}
		_ = cf
		last, found, err := ClipToLastClosed(s.cal, k, ct, now)
		if err != nil {
			return rep, fmt.Errorf("tickflow: 定不了末端: %w", err)
		}
		if !found {
			// ⛔ 「一天都还没收盘」不是错误，是一份【没有可同步区间】的报告。
			// 而它仍然带着 HaltUnknown ⇒ Complete() 为假 ⇒ 不会被读成「跑完了」。
			//
			// ⛔ **它【不】跟着另外两条一起具名化，而这是一个写下来的决定**
			//（评审方 2026-09-10 指出，我认）：
			// 另外两条的「没东西可同步」是**日历给的确定答案**，
			// 而这一条是**「现在还答不了，等收盘」** —— 它是一个【时刻】问题，
			// 下一分钟同样的请求可能就有答案了。
			// ⇒ 而这里连 `To` 都定不下来 ⇒ **没有区间可以交给 PlanGaps 分类** ——
			// 也就是说它连「落成一个事实」这一步都做不到，所以它只能留声。
			//
			// ⇒ 判据：**给一族路径统一具名之前，先数清这一族有几条，
			// 并逐条问「它现在的哑，是不是有人故意留的」。**
			//
			// ⚠️ **这一条评审方【没有走到过】**（他自己列进「核不了的」那一栏：
			// 走到它要 `now` 落在收盘之前，而他的四格里没有那一格）。
			// ⇒ 我量了：`TestNotYetClosedKeepsItsDeliberateSilence` 走的就是这条路，
			// 并且带着一条**前提自检**（`Requested[1] != 0 ⇒ Fatal`）——
			// 没有那一句，一条走不到这儿的输入也会让它绿。
			rep.Requested = [2]TradingDay{req.From, 0}
			return rep, nil
		}
		to = last
	}
	if to < req.From {
		return rep, fmt.Errorf("tickflow: From=%s 晚于 To=%s——闭区间下这表示空请求，"+
			"而空请求和「拉过、确认没有」在 coverage 里长得一样", req.From, to)
	}
	rep.Requested = [2]TradingDay{req.From, to}

	// ⛔ **显式 `To` 含一个在 `now` 时还没全部收盘的交易日 ⇒ 报错**（评审方 2026-09-14 造输入复现，我复现，读数一致）。
	//
	// 没有这一格时（v0.5.0 实测，日线与 1m 同形）：
	//
	//	盘中 10:00，显式 To=当天   err=nil · Complete()=true · 当天登记进 coverage（1m：180 根 / 日线：「拉过、确认没有」）
	//	收盘后同一请求再跑         err=nil · Halt=区间里的交易日已经全部覆盖过 · Complete()=true ⇒ **剩下的根永远不再拉**
	//
	// ⇒ 成因：源按约定丢掉未完结的根（约定二），而 `To == 0` 才走 `ClipToLastClosed`，显式 `To` 不裁 ⇒
	// **「源没给」被登记成「这一天就这么多」**；挑段跳过已覆盖（v0.5.0 丙片）又把它从「下次补上」变成「永远不补」；
	// 走查也抓不到（.meta 记的条数与盘上一致）。
	//
	// ⛔ **修在编排，不修在源**：「没收盘的那一天不许登记」对每个周期、每个源都一样，各源各守一遍就会漏一个。
	// ⛔ **报错而不是悄悄裁**：显式 `To` 是调用方说出来的一个日子，把它裁掉等于替他改了请求；
	// 「同步到最后一个已收盘日」这件事本来就有一个具名的问法 —— `To == 0`。
	// ⚠️ 只核日历覆盖之内的日子：覆盖之外答不了「收没收盘」，由下面 Covers 那一格报。
	if day, closesAt, found, err := firstUnclosedDay(s.cal, k, req.From, to, now); err != nil {
		return rep, fmt.Errorf("tickflow: 判不了 [%s, %s] 里有没有还没收盘的交易日: %w", req.From, to, err)
	} else if found {
		return rep, fmt.Errorf("tickflow: To=%s 含一个在 now=%s 时还没收盘的交易日 %s（它最后一段收盘于 %s）——"+
			"源只给已完结的根，这一天会被登记成「就这么多」，而之后的同步会把它当成已覆盖、永远不再拉；"+
			"要「同步到最后一个已收盘的交易日」请传 To=0，或把 To 设为那一天之前",
			to, fmtMs(now), day, fmtMs(closesAt))
	}

	// ⛔ **在依赖 Caps 之前，先让它自证** —— 而这一格是量出来的，不是想出来的：
	// `Capabilities.Validate()` **生产侧零调用点**（2026-09-09 实测：只有两个源
	// 各自的测试在调它）。⇒ 于是本文件里那句「`BatchDays` 的零值在
	// `Capabilities.Validate()` 那一侧已经被拒」**在生产侧是假的** ——
	// 一个第三方 `Source` 实现带着 `BatchDays: 0` 会一路畅通。
	//
	// ⇒ 判据（昨天那条的第三次）：**为一句「从 Y 那里保证」辩护之前，
	// 先 grep 【Y 有没有被调用】** —— 一个零调用点的检查器，
	// 它挡住的东西全在别人的想象里。
	//
	// ⚠️ 而放在这里而不是 `NewSyncer`：`Caps` 是**按品种**问的（登记㉔），
	// 构造时还不知道要同步哪个品种。
	caps := s.src.Caps(k)
	if err := caps.Validate(); err != nil {
		return rep, fmt.Errorf("tickflow: 源在 %s 上的 Caps 自己不自洽: %w", k, err)
	}

	// —— C3b / D2b：打开这个库时发现的事，在这里留声并处置 ——
	// ⛔ 放在拉取【之前】：D2b 的一支要作废 coverage，另一支要停 ——
	// 两者都必须在「按 coverage 决定拉什么」之前发生。
	if err := s.disposeOpenState(req, &rep); err != nil {
		return rep, err
	}

	cf, ct, ok := s.cal.Covers(k)
	rep.CoversOK = ok
	if !ok {
		// SYN-4：CoversOK 为假时 Covered 不填零值区间——一个零值区间读起来仍然像一个区间。
		return rep, fmt.Errorf("tickflow: 日历覆盖不到 %s: %w", k, ErrUncovered)
	}
	rep.Covered = [2]TradingDay{cf, ct}

	// 与日历能回答的那一段求交 —— Walk 的护栏要求两端都在覆盖内。
	from, hi := req.From, to
	if from < cf {
		from = cf
	}
	if hi > ct {
		hi = ct
	}
	if hi < from {
		// 请求整段落在覆盖之外 ⇒ 没有可走的交易日。这是【结果】，不是异常。
		//
		// 🔴 **而上一版只写了这句话，没有把它落成任何事实**：报告里除了 `Halt`
		// 那一位什么也没有（`Gaps` 空、`Bars=0`），而 `Halt` 是零值「未记录」——
		// **一句我们完全知道答案的话，报成了「不知道为什么停」。**
		//
		// ⛔ 而修法**不是手填**：`PlanGaps` 早就答得出来，是这条 `return` 跳过了它。
		// 实测（`week()` 覆盖 0106..0112）：
		//
		//	整段在覆盖之前/之后 ⇒ 1 段【日历答不了】（GapCalendarUnknown）
		//	而那一类的定义里逐字写着「或日期在 Covers 之外」—— 槽位一直都在
		//
		// ⇒ 判据：**一条早退跳过了一个【已经答得出这件事】的函数时，
		// 该补的不是一个事实，是那次调用。** 手填会让同一个判断有两个来源。
		if err := s.planGaps(k, req.From, to, nil, nil, &rep); err != nil {
			return rep, err
		}
		rep.Halt = HaltOutsideCoverage
		return rep, nil
	}

	days, err := s.tradingDays(k, from, hi)
	if err != nil {
		return rep, err
	}
	if len(days) == 0 {
		// 区间在覆盖内，而里面一个交易日都没有（比如只请求了周末）。
		//
		// ⛔ **上一版这里【一句注释都没有】** —— 三条非错误早退里最弱的一条：
		// 另外两条各自写过半句，而这一条**从来没有人说过它的哑是不是故意的**。
		// ⇒ 判据：**哑有三种状态，不是两种** ——
		// 故意的哑 / 声明过是结果的哑 / **从来没人回答过的哑**。
		// 而「问它是不是故意的」，第一步是看**有没有人写过**。
		//
		// ⇒ **而这一条的落款现在有了，它是评审方的**（2026-09-10，原话，一字不改）：
		//
		//	「请求区间里一天都不交易 ⇒ 没有东西被要过，
		//	  『没什么可同步』是一个完整而正确的回答。
		//	  而调用方查得到：Requested 在、Bars=0、Gaps=0。」
		//
		// ⚠️ 引原话而不是转述，是本仓刚立的那条：**引用「某人说过什么」只有那个人能核**
		// ⇒ 这类归属要带原话，否则它是一条永远不会被验的断言，而它看起来像有出处。
		// ⚠️ 而他裁的是「**该**真」，不是「今天已经对」—— 今天这一步（具名化）做完，它才真。
		if err := s.planGaps(k, req.From, to, nil, nil, &rep); err != nil {
			return rep, err
		}
		rep.Halt = HaltNoTradingDays
		return rep, nil
	}

	// —— 丙片：**挑段跳过已经覆盖过的交易日** ——
	//
	// ⛔ 为什么这里不必再跑一次 `VerifyCoverage()`（那会是第二次整库扫描；
	// (iv) 实测 890k 记录一次 2.586s）：`docs/design.md` 那条写着
	// 「`unverified` 的 coverage【不】当作『已拉过』—— 否则语义未知的旧记录会让
	// Syncer 静默跳过（最危险的方向）」，而**那句里的 `unverified` 专指
	// 【不可重放的源 ＋ 旧 .meta】那一种（D2b）** —— 它到不了这一步：
	//
	//	disposeOpenState 的 default 支 ⇒ 填 rep.LegacyMetaUnverified 并 return err
	//	而它的调用点在**日历求交之前**（本函数上方）⇒ Sync 已经停了
	//
	// ⇒ 所以走到这儿的 coverage，要么是本进程刚写的，要么带着 format ⇒ 语义已知。
	// ⚠️ **而语义已知 ≠ 数据还在**（(v)，2026-09-13）：一份带 format 的 .meta 配一个被删掉或截短的 .dat，
	// 上面这段论证照样成立，而跳过会把缺的那些天永远跳过。那一格由 disposeOpenState 里的
	// MissingRecords 检查在更前面拦下（开库时 n < Σbars ⇒ 中止）；它拦不住的是「条数够而内容不对」，那归走查。
	want, skipped := splitCovered(days, s.store.Coverage())
	if len(skipped) > 0 {
		// ⚠️ **跳过必须留下事实**：没有它，「跳过了」与「源什么都没给」在报告上同形
		// （两者都是 `Bars=0`）—— 本仓那条：两种状态不许共用一句话。
		rep.SkippedCovered = append(rep.SkippedCovered,
			fmt.Sprintf("跳过 %d 个已覆盖的交易日（%s..%s 之间），只拉剩下的 %d 个",
				len(skipped), skipped[0], skipped[len(skipped)-1], len(want)))
	}
	if len(want) == 0 && len(skipped) > 0 {
		// 全都拉过了。⚠️ 而**走查与缺口照做** —— 一次「什么都没拉」的同步，
		// 它的报告仍然要回答「这个区间现在完不完整」。
		verified, failed, breach := s.verifyAll(&rep)
		if breach != nil {
			return rep, breach
		}
		if gerr := s.planGaps(k, req.From, to, verified, failed, &rep); gerr != nil {
			return rep, gerr
		}
		rep.Halt = HaltAllCovered
		return rep, nil
	}

	chunks := chunkDays(want, caps.BatchDays)
	aged, err := closedLaterCounter(s.cal, k, want[0], now)
	if err != nil {
		return rep, err
	}
	gateBefore := s.gate.count()
	syncedDays, attempts, halt, ferr := s.fetch(ctx, req, k, chunks, days, aged, &rep)
	rep.Halt = halt

	// ⛔ **闸门有没有被用到** —— 这一格接住的是那个【假绿】：
	// 一个「收下 client 又丢掉」的工厂，请求照发、报告照说 Complete()。
	//
	// ⚠️ 判据故意写得窄：**只在「向源要过数据、而闸门一次都没被用到」时出声。**
	// 少一次不报（源可以自己合并请求），一次不报才报 —— 而 0 是那个可判的边界。
	// ⛔ 而它**不能被抬成 `gate增量 < attempts`**：
	// **Syncer 不可能知道「一次 Bars 该发几个请求」** —— 一次调用发几次随源而变
	// （sina 1 次、cffex N 次），**而那正是当初把闸门放进 RoundTripper 的理由**。
	// ⇒ 那个比较**没有真值**，不是「更严的判据」。（评审方 2026-09-09 提出后撤回。）
	// ⇒ 代价照报：**窄到 `== 0`，挡住的是「完全无视」，挡不住「大部分无视」**
	// （实测 5 次里 1 次过闸 ⇒ 0 条留声 · Complete()=true ⇒ 沉默）。
	//
	// ⛔ **而「适不适用」由【源自己】说，不靠这个 0 去猜**（`Caps.ClientUse`）：
	// 一个不走 HTTP 的源计数恒为 0 ⇒ 上一版**每一次同步都诬告它**，
	// 而**一个长期误报的告警最终会关掉它自己** —— 那会把这一格的全部收益吃掉。
	rep.UngatedOK = caps.ClientUse == ClientUseHTTP
	if rep.UngatedOK && attempts > 0 && s.gate.count() == gateBefore {
		// ⚠️ **措辞只报【读数】，不报【成因】**（评审方 2026-09-09 的判据，我认）：
		// 上一版写「这个源多半没有用交给它的那个 http.Client」——那是一句**成因**，
		// 而一条会误报的留声，成因错的时候比读数错多错一格。
		// ⇒ **读数错是一格，成因错是两格。** 成因留给读的人。
		rep.UngatedSource = append(rep.UngatedSource,
			fmt.Sprintf("向源要过 %d 次数据，而经我们那个 http.Client 的请求是 0 次"+
				"（这个源声明了 ClientUseHTTP）", attempts))
	}

	// ⛔ SYN-6 / 缺口 / 夜盘**在 ferr 非空时也要做** ——
	// 一次中止之后的库比跑完之后的库更需要被走查，而不是更不需要。
	verified, failed, breach := s.verifyAll(&rep)
	if breach != nil {
		// 违约 ＝ 坏了 ⇒ 中止。报告一并返回：已经落盘的那部分是真的。
		return rep, breach
	}
	if gerr := s.planGaps(k, req.From, to, verified, failed, &rep); gerr != nil && ferr == nil {
		ferr = gerr
	}
	s.scanNight(k, syncedDays, &rep)

	if ferr != nil {
		return rep, ferr
	}
	return rep, nil
}

// disposeOpenState 是 C3b 与 D2b：**打开这个库时发现的事，本层处置不了，编排处置。**
func (s *Syncer) disposeOpenState(req SyncRequest, rep *SyncReport) error {
	st := s.store.OpenState()

	// C3b：截了不留声 ⇒ 与「本来没事」同形。
	if st.TruncatedTail > 0 {
		rep.TruncatedTails = append(rep.TruncatedTails,
			fmt.Sprintf("打开库时截掉了 %d 字节残尾（C3a）——"+
				"截断本身已经处理好了，留这一条是为了让它和「本来没事」分得开", st.TruncatedTail))
	}

	// (v)：盘上缺记录而 coverage 还在 ⇒ **在规划之前中止**。
	//
	// ⛔ 为什么中止而不是出声后继续：继续就会进「挑段跳过已覆盖」，缺的那些天被当成拉过而跳过，
	// 走查再报一遍、err=nil —— 那正是 v0.5.0 勘误四丙格的死路，只是多一条痕迹。
	// 而按裁定过的 err 语义（这一次动作本身没做成），**这次同步确实没做成**：它拒绝在缺数据的库上规划。
	// ⚠️ 中止对这个周期的**所有**请求都生效，包括 coverage 之外的新日子 ——
	// 在缺数据的库上往后追加只会让那个缺口更难补（补的时候要从早到晚重拉，追加得越多重拉越多）。这不是误伤。
	// ⛔ 放在 LegacyMeta 之前：LegacyDiscard 那一支会作废 coverage 并全区间重拉，
	// 而盘上那些残余记录还在 ⇒ 先拉早的会撞 ErrOutOfOrder。缺数据的库不该进那一支。
	if st.MissingRecords > 0 {
		cov := s.store.Coverage()
		var claimed int
		for _, sp := range cov {
			claimed += sp.Bars
		}
		rep.MissingRecords = append(rep.MissingRecords,
			fmt.Sprintf("盘上的记录比 coverage 声称的 %d 条少 %d 条 —— 盘上缺数据而 coverage 还在，"+
				"同步会把这些天当成已经拉过而跳过，永远补不回来。"+
				"⛔ 别再重跑，也别只删其中一个文件：把这个周期的 .dat 与 .meta 一起删掉，然后按交易日从早到晚重拉",
				claimed, st.MissingRecords))
		return fmt.Errorf("%w：少 %d 条（coverage 声称 %d 条）", ErrMissingRecords, st.MissingRecords, claimed)
	}

	if !st.LegacyMeta {
		return nil
	}

	// D2b：两种处置都要进报告。而判定要「可不可重放」——见下。
	cov := s.store.Coverage()
	if len(cov) == 0 {
		// ⚠️ 没有 coverage 就没有「语义未知的 coverage」要处置 ——
		// 这一支到不了 D2a。写下来免得下一个人去补一个不会被走到的分支。
		return nil
	}
	dec, err := DecideLegacyMeta(s.replayable(req, cov))
	switch dec {
	case LegacyDiscard:
		if derr := s.store.DiscardCoverage(); derr != nil {
			return fmt.Errorf("tickflow: 旧 .meta 判为作废重拉，而作废失败: %w", derr)
		}
		rep.LegacyMetaDiscarded = append(rep.LegacyMetaDiscarded,
			fmt.Sprintf(".meta 没有 format 且这个源重放得了 %s 起的历史 ⇒ "+
				"已作废 %d 段 coverage，全区间按「没拉过」重拉", cov[0].From, len(cov)))
		return nil
	default:
		rep.LegacyMetaUnverified = append(rep.LegacyMetaUnverified,
			fmt.Sprintf(".meta 没有 format 而这个源重放不了 %s 起的历史 ⇒ "+
				"coverage【不】作废（作废换不来任何东西，而丢掉那些区间不可逆）；"+
				"要一个显式决定（人工确认后删掉该周期的 .dat/.meta 再重拉）", cov[0].From))
		return err
	}
}

// replayable 回答「这个源能不能把【那一段】重新给一遍」。
//
// ⛔ **它不是源的性质，是【源 × 这一段】的性质**（丙三冲突三）：
// 一个源可以对 2024 年可重放、对 2016 年不可重放，而 `Since` 说的正是那条界线。
// ⇒ 设计里原本写「只有编排经 `Caps` 知道『源可不可重放』」，而 `Caps` **没有那个字段** ——
// 那句话被抄了四遍，一处也没被核过。
//
// ⚠️ **而 `Since` 在这里是一个【代理指标】，不是定义**（评审方 2026-09-09 指出，我认）：
//
//	Since 说的是   「这个源最早给得出哪一天」
//	我们要问的是   「那一段现在还能不能重新取回来」
//
// **两者今天大概率同向，而那是一个经验相关，不是等价。** 写下来，
// 免得下一个人把这行推导读成定义 —— 一旦哪个源出现「给得出那一天、但那一段取不回来」
// （下架、改口径、需要付费重放），这一格就要换判据，**而它不会自己报错**。
// ⇒ 换判据的入口就是这个函数：它是【唯一】算 replayable 的地方（本包 grep 可证）。
//
// ⚠️ `Since` 缺失或非法时取 **false**（＝不可重放 ⇒ 要人确认）。判据是那条：
// **给一个取值定级，取它最哑的那个后果。**
// 取 true 最哑的后果是**把一段取不回来的历史作废掉，而那不可逆**；
// 取 false 最哑的后果是**多问一次人**。
func (s *Syncer) replayable(req SyncRequest, cov []Span) bool {
	since, ok := s.src.Caps(req.Symbol.ProductKey()).Since[req.Period]
	if !ok || !since.Valid() {
		return false
	}
	return since <= cov[0].From
}

// verifyAll 是 SYN-6：**结束时走查【全库】——一遍扫描，同时核算 coverage 的每一段。**
//
// ⛔ **上一版走查的是「本次碰过的那些段」，而这一版不看 `touched` 了。**
// 判据不是代价（两者都是一遍整文件扫描），是**保证绑在什么上**：
//
//	绑在 touched 上  ⇒ 丙片改的正是「谁会被碰」⇒ 那个保证跟着缩水，而条款文本一个字不会报警
//	绑在【库】上     ⇒ 丙片怎么挑段都影响不到它
//
// 📎 本仓那条：**一个保证若绑在一个事件上，就问「谁负责让那个事件发生」** ——
// SYN-6 原来绑在「被碰过」上，而那个集合的主人是 `fetch`，不是走查。
//
// —— 📌 被删掉的那个函数留下的读数（代码可以删，读数不能随它消失）——
//
// 上一版有个 `spansTouchedBy(cov, touched)`，回「`cov` 里与任何一块相交的那些段」。
// 它连同五格单元守卫一起删了，而这三条读数要留着 ——
// **否则下一个人重新引入「按 touched 挑段」时，这几轮全要重走**：
//
//	一、`verified` 的键必须与 `planGaps` 查的键**同源**（都来自 `Coverage()`）。
//	    2026-09-11 的生产缺陷正是两边不同源：写入侧用【分块】的 span
//	    `{d1,d1,1,1} {d2,d2,1,1} {d3,d3,1,1}`，而 `CommitSpan` 已把它们并成 `{d1,d3,3,3}`
//	🔴 两组值不相等 ⇒ map 查不到，**而两处代码都写着 `verified[sp]`**。
//	⚠️ 那时的解释写的是「`Span` 是四字段结构体」——**当时为真，而它把注意力引错了地方**：
//	这一格里差的是**区间本身**（`[d1,d1]` vs `[d1,d3]`），不是 `Bars`/`Days`。
//	⇒ 所以 2026-09-12 把键收窄成 `SpanKey`（只有 `[From,To]`）之后，这一格**照旧被抓住**。
//	二、「按 `[From,To]` 找那一块」**修不了它**（双方各打一次突变，两次全绿）：
//	    合并段的 `[From,To]` 本来就不等于任何一块的。
//	三、那条「相交段数 ≤ 分块数 ⇒ 只会更便宜」的不等式，**函数层为假、生产路径为真** ——
//	    函数层反例：`cov=[{08-05,08-05},{08-07,08-07}]` ＋ 一块 `[08-05..08-07]` ⇒ j=2 > k=1；
//	    而 `CommitSpan` 只能向后扩（`ValidateCoverage` 拒 `From < prev.From` 与 `From <= prev.To`）
//	    ⇒ 经由 `Sync` 到不了那个状态。
//	    🔴 **证伪也要说清是在哪一层证伪的** —— 反例的杀伤力越大，越容易忘了给它挂层级。
//
// —— ⚠️ 第二个返回值非 nil 时，这里【什么都不填】，而那是有意的 ——
//
// `VerifyCoverage` 的第二个返回值 ＝「这一遍跑不起来」。那时 `verified` 与 `failed` 都是空的
// ⇒ 每一段落到 `planGaps` 的 `!verified[sp]` ⇒ 报成 `GapStoreVerifyUnrun`
// ＝「**本次没走查过**」。🔴 **那句话在这时是真的** —— 走查确实没跑成。
// ⇒ 这也是 (iv) 之后那一类**唯一的**生产者（另一个是 Store 违约、没给某一段结论）：
// 两条早退（`HaltOutsideCoverage` / `HaltNoTradingDays`）**都产不出它**（2026-09-11 实测，
// 两格都断言过 `Halt`）—— 因为 `classifyTradingDay` 是按【请求区间里的每个交易日】调的，
// 而那两条早退各自消灭了那个前提。
//
// ⚠️ `VerifyUnrun` 那一条痕迹**保留**：它是给人扫一眼的，与类型化的那条不是一回事。
// （2026-09-13 之前它写在 `TruncatedTails`，被 `Incidents()` 贴上「开库时截过残尾」—— 见 SyncReport 那两栏的注释。）
// —— ⛔ 第三个返回值：**`Store` 违约 ＝【坏了】，不是【答不了】** ——
//
// 契约要求 `VerifyCoverage` 是**全的**（`Coverage()` 里每一段都有一个结论）。
// 漏掉一段时，本层**认不出**发生了什么 —— 而本仓那条最硬的规矩正压在这儿：
// **「坏了」不是「答不了」：认不出的一律中止，不折进任何一类缺口。**
//
// ⚠️ 把它折成 `GapStoreVerifyUnrun` 看起来更温和，而那是有害的：
// 调用方会拿到一个**看起来可以照着处置的答案**，而那个处置不存在
// （「再走一遍」对一个不给结论的实现没有用）。
// 📎 与 `classifyTradingDay` 那条兜底同一个处置、同一个理由。
//
// ⚠️ 而「这一遍跑不起来」（`VerifyCoverage` 的第二个返回值）**不是违约**：
// 那时每一段落到「本次没走查过」，**而那句话是真的** —— 见下面那一支。
func (s *Syncer) verifyAll(rep *SyncReport) (map[SpanKey]bool, map[SpanKey]error, error) {
	okSpans := map[SpanKey]bool{}
	failedSpans := map[SpanKey]error{}
	res, err := s.store.VerifyCoverage()
	if err != nil {
		// ⛔ 「跑不起来」不许伪装成「每一段都没问题」：留空 ⇒ 每一段报「本次没走查过」。
		rep.VerifyUnrun = append(rep.VerifyUnrun,
			fmt.Sprintf("这一遍走查没能跑起来：%v", err))
		return okSpans, failedSpans, nil
	}

	// ⛔ 契约一：**全的**。漏掉一段 ＝ 违约 ＝ 坏了 ⇒ 中止（见上面那段注释）。
	for _, sp := range s.store.Coverage() {
		if _, ok := res[sp.Key()]; !ok {
			return nil, nil, fmt.Errorf(
				"tickflow: 这个 Store 的 VerifyCoverage 没有为 [%s, %s] 给出结论"+
					"——契约要求它对 Coverage() 的每一段都给一个（nil 即通过）。"+
					"这是【坏了】，不是【答不了】，所以中止而不是报成一类缺口："+
					"折成缺口会让调用方拿到一个看起来可以照着处置的答案，而那个处置不存在",
				sp.From, sp.To)
		}
	}
	for sp, verr := range res {
		if verr != nil {
			rep.VerifyFailed = append(rep.VerifyFailed,
				fmt.Sprintf("走查 %s..%s 失败：%v", sp.From, sp.To, verr))
			failedSpans[sp] = verr
			continue
		}
		okSpans[sp] = true
	}
	return okSpans, failedSpans, nil
}

// planGaps 把 coverage 翻成七类缺口。
//
// ⚠️ 没被本次走查过的段一律带 ErrSpanUnverified —— 那是 B3 的直接落法：
// **「没走查过」不许悄悄变成一个肯定的「确认没有」。**
func (s *Syncer) planGaps(k ProductKey, from, to TradingDay,
	verified map[SpanKey]bool, failed map[SpanKey]error, rep *SyncReport) error {
	var cov []SpanStatus
	for _, sp := range s.store.Coverage() {
		st := SpanStatus{Span: sp}
		switch {
		case failed[sp.Key()] != nil:
			// ⛔ **顺序要紧**：走查失败的段同时也满足 `!verified[sp]`，
			// 而那两句话不是一回事 ——「走查过而没通过」比「本次没走查」**知道得更多**。
			// ⇒ 先判信息多的那一支。
			//
			// ⚠️ 双 `%w`：**哨兵与真因都要能被 errors.Is 取到** ——
			// 只包哨兵，调用方回不到现场；只包真因，调用方分不出「这一类」。
			st.Err = fmt.Errorf("%w: %w", ErrSpanVerifyFailed, failed[sp.Key()])
		case !verified[sp.Key()]:
			st.Err = ErrSpanUnverified
		}
		cov = append(cov, st)
	}
	gaps, err := PlanGaps(s.cal, k, from, to, cov, s.store.DaysWithBars)
	if err != nil {
		return fmt.Errorf("tickflow: 分类缺口失败: %w", err)
	}
	rep.Gaps = gaps
	return nil
}

// scanNight 填 NightAbsentRun / NightAbsentOK（SYN-7）。
//
// ⚠️ 「不适用」与「适用但没找到」在 `Days == 0` 上不可分辨，所以那个 bool 不可省。
func (s *Syncer) scanNight(k ProductKey, days []TradingDay, rep *SyncReport) {
	if len(days) == 0 {
		return
	}
	run, ok, err := ScanNightAbsent(s.cal, k, days)
	if err != nil {
		return // 夜盘那一格答不了时不报告 —— 它是给人看的提示，不是判断
	}
	rep.NightAbsentRun, rep.NightAbsentOK = run, ok
}

// tradingDays 用 `Walk` 收一段里的交易日。
//
// ⛔ 走 `Walk` 而不是自己逐日调 `DayOf`：**那条护栏就在 Walk 里** ——
// 区间有一端落在 `Covers` 之外时它当场报错，不会安静地少遍历。
func (s *Syncer) tradingDays(k ProductKey, from, to TradingDay) ([]TradingDay, error) {
	var days []TradingDay
	if err := s.cal.Walk(k, from, to, func(d Day) bool {
		days = append(days, d.Num)
		return true
	}); err != nil {
		return nil, fmt.Errorf("tickflow: 遍历 %s 的 %s..%s 失败: %w", k, from, to, err)
	}
	return days, nil
}

// splitCovered 把交易日分成【要拉的】与【已经覆盖过的】两堆。
//
// ⚠️ 判据是**交易日落不落在某一段 coverage 的闭区间里**，而不是「请求区间与某段相交」——
// 后者会把一次**部分重叠**的请求整段跳掉。
// 🔴 本仓那条（(o) 那一格）：**`Coverage()` 给的是并段之后的值**，
// 所以逐日判是唯一不依赖「段怎么并」的写法。
//
// ⛔ 它**不看走查状态**，理由写在调用点上（那一种 unverified 到不了这里）。
//
// ⚠️ 射程：`cov` 按 From 升序且互不重叠（`ValidateCoverage` 保证），
// 而这里**不依赖那个保证** —— 逐段线性判，段数 k 很小（本仓实测 k=8）。
func splitCovered(days []TradingDay, cov []Span) (want, skipped []TradingDay) {
	for _, d := range days {
		covered := false
		for _, sp := range cov {
			if sp.From <= d && d <= sp.To {
				covered = true
				break
			}
		}
		if covered {
			skipped = append(skipped, d)
		} else {
			want = append(want, d)
		}
	}
	return want, skipped
}

// chunkDays 按源的 `BatchDays` 把交易日切成一批批。
//
// ⛔ 切法是**源的性质**，不是调用方的偏好（`Capabilities.BatchDays`）：
// 切成一天一块对 sinasource 是 2671 次「拉全部历史」⇒ **不是浪费，是不可用**；
// 不切对 cffexsource 是一次失败丢 2671 天。
//
// ⚠️ `BatchDays` 的零值由 `Capabilities.Validate()` 拒 —— 而 `Sync` 现在**真的调它**了。
// ⛔ **上一版这句话写的是「那一侧已经被拒」，而它在生产侧是假的**：
// 实测 `Capabilities.Validate()` 当时**零个生产调用点**，只有两个源各自的测试在调。
// ⇒ 一句「别处已经挡住了」，要先 grep 那个「别处」有没有被调用。
//
// **而这里仍然再判一次**，理由不是「以防万一」：本函数拿到 0 会造出无限循环，
// 而那种失败**不报错、只是不返回** —— 比一个错误难查得多。
func chunkDays(days []TradingDay, batch int) [][]TradingDay {
	if len(days) == 0 {
		return nil
	}
	if batch == BatchDaysUnbounded || batch >= len(days) {
		return [][]TradingDay{days}
	}
	if batch < 1 {
		// 到不了这儿（Validate 拒过），但真到了就整段一块——**不要无限循环**。
		return [][]TradingDay{days}
	}
	var out [][]TradingDay
	for i := 0; i < len(days); i += batch {
		j := i + batch
		if j > len(days) {
			j = len(days)
		}
		out = append(out, days[i:j])
	}
	return out
}

// fetch 是同步循环。**它的每一条出口都必须交出一个 HaltReason。**
//
// ⛔ 把理由写成【返回值】而不是一个循环外的变量，是这一格的全部机制：
// **Go 要求每一条出口都给出返回值** ⇒「加了一条出口却忘了写理由」**写不出来**。
// 写成变量的话，一个 `break` 忘了赋值就会静默地走成「跑完了」。
//
// ⚠️ 两条限制，自陈在这儿而不是等人发现：
//
//	一  它挡不住「理由写了，但写错了那一个」—— 那要靠测试逐条驱动，不靠类型。
//	二  **本函数今天短到读得完，所以不给它加 AST 守卫**（守卫本身的维护成本高过它挡住的）。
//	    ⇒ **这个判断在函数长起来的那天要重做。**
func (s *Syncer) fetch(ctx context.Context, req SyncRequest, k ProductKey,
	chunks [][]TradingDay, days []TradingDay, aged func(TradingDay) int, rep *SyncReport) ([]TradingDay, int, HaltReason, error) {

	consecutive := 0
	attempts := 0 // 调用 Bars 的次数，含重试
	var synced [2]TradingDay
	var syncedDays []TradingDay

	// —— 挂起：源一根没给、而还没有理由登记成「拉过确认没有」的那几天（勘误三，2026-09-15）——
	//
	// ⛔ 由来（离线实测，v0.5.0 与 d7f2d9a 读数相同；评审方独立复现）：新浪日线收盘后要过一阵才补上当天那一行。
	// 那段时间里同步，当天整块 0 根 ⇒ 上一版照样把它登记进 coverage ⇒「拉过确认没有」⇒
	// 源补上之后丙片把它当已覆盖跳过、报告全绿 ⇒ **每天收盘后定时同步的用户，每天永久缺一天。**
	//
	// ⇒ 判据：一天源没给根，只有下面两条之一成立才登记 ——
	//
	//	一 更晚的正面证据  它之后有一个交易日**这次拉到了根**（同一合约同一周期）⇒ 源已经出过更晚的数，这一天是真没有
	//	二 年龄上限        它之后已经收盘了 emptyTailGraceDays 个交易日 ⇒ 不再等（用户 2026-09-15 定：重拉要有界）
	//
	// 否则挂起：**不登记，下次同步重拉**（失败方向是多拉，不是丢）。
	//
	// ⛔ 证据**不能只看同一块**：块尾空、下一块有根时，只看本块会把那几天挂起而把下一块照登 ⇒ coverage 留洞 ——
	// 而洞事后补不进去（`CommitSpan` 拒「未按 From 升序」，实测）⇒ 那一天变成永久「没拉过」。
	// ⇒ 挂起的日子**接进下一块**，与它一起登记。
	// ⚠️ 而证据也不会来自「库里更晚处已有的根」：库只往后长，没登记的日子后面不会已有登记（洞除外，而洞补不进去）。
	// ⛔ 挂起只接**紧挨着**的下一块：中间隔着一个已覆盖的交易日（请求的 From 早于已有 coverage）⇒ 丢弃挂起（它们照旧是「没拉过」）——
	// 接过去，登记出去的段会与已有 coverage 重叠，而这一块的根已经落盘 ⇒ 孤儿记录。
	// 📎 失败的块不会隔开挂起：失败重试同一块（勘误四），重试成功时挂起与它仍紧挨；预算用完时走出口、挂起被点名。
	// ⚠️ 挡不住的一格：源的旧响应【中间】缺行（而不是只少末尾几行）⇒ 那一天被更晚的根夹住、登记成「拉过确认没有」。
	// 2026-09-15 探针只观察到末尾一行来回翻；中间缺行是评审方离线构造的（v0.6.0 勘误三）。
	var pending []TradingDay
	idx := make(map[TradingDay]int, len(days))
	for i, d := range days {
		idx[d] = i
	}
	contiguous := func(a, b TradingDay) bool {
		ia, oka := idx[a]
		ib, okb := idx[b]
		return oka && okb && ib == ia+1
	}
	holdBack := func(why string) {
		if len(pending) > 0 {
			rep.HeldBack = append(rep.HeldBack, fmt.Sprintf(
				"源没给 %s..%s 这 %d 个交易日的根，而%s、收盘后也还不到 %d 个交易日——"+
					"没有登记成「拉过确认没有」，下次同步会重拉（它们在 Gaps 里是「没拉过」）",
				pending[0], pending[len(pending)-1], len(pending), why, emptyTailGraceDays))
			pending = nil
		}
	}

	for ci := 0; ci < len(chunks); {
		chunk := chunks[ci]
		if err := ctx.Err(); err != nil {
			holdBack("同步在拉到更晚的根之前被取消了")
			rep.Synced = synced
			return syncedDays, attempts, HaltContext, fmt.Errorf("tickflow: 同步被取消: %w", err)
		}
		br := BarRequest{Symbol: req.Symbol, Period: req.Period,
			From: chunk[0], To: chunk[len(chunk)-1]}
		// attempts 数的是【调用 Bars 的次数】，重试也算一次 —— 它只用在闸门那句「向源要过 N 次数据」上，那句话按次数说才是真的。
		// ⚠️ 它不是「请求了几块」：一块重试一次就是 2。
		attempts++
		bars, err := s.src.Bars(ctx, br)
		if err != nil {
			consecutive++
			// ⛔ 比较符是 `>` 不是 `>=`，而这一格被钉进了测试：
			//
			//	>   ⇒ K=0 时【第一次失败之后】停 ⇒ 那一天真的被试过 ⇒ 吵
			//	>=  ⇒ K=0 时【一次都不试就停】 ⇒ 一份「成功同步了 0 天」的报告 ⇒ 哑
			//
			// ⇒ 「K 的零值是安全的」这句话，真值取决于**这一个符号**。
			if consecutive > req.MaxConsecutiveFails {
				holdBack("同步在拉到更晚的根之前因连续失败中止了")
				rep.Synced = synced
				return syncedDays, attempts, HaltBudget, fmt.Errorf("%w: 连续 %d 次失败（上限 %d），"+
					"停在 %s；最后一次: %v",
					ErrBudgetExhausted, consecutive, req.MaxConsecutiveFails, chunk[0], err)
			}
			// ⛔ **重试同一块，不跳过**（勘误四，2026-09-15；评审方在 d7f2d9a 与 e553d09 上复现，读数相同）。
			//
			// 上一版这里是「跳到下一块」⇒ 后面的块照登 ⇒ coverage 留洞；而洞事后补不进去
			// （CommitSpan 拒「未按 From 升序」、AppendBars 拒「交易日倒退了」）⇒ 这个库此后每次同步
			// 都停在洞那一块报「落盘失败」，**新的交易日永远拉不到**。触发只要 K ≥ 1 ＋ 一次瞬时失败。
			// ⇒ 「连续失败超过上限就停」的字面含义不变：consecutive 数的正是【同一块】的连续失败。
			continue
		}
		consecutive = 0

		if len(pending) > 0 && !contiguous(pending[len(pending)-1], chunk[0]) {
			holdBack("它们与下一块之间隔着已经覆盖过的交易日")
		}
		cand := append(pending, chunk...)
		pending = nil
		// 登记到哪一天：最后一个拉到根的交易日，与最后一个过了年龄上限的交易日，取较晚者。
		// ⚠️ 年龄随日子单调（越早的日子之后收盘的越多）⇒ 满足它的是 cand 的一段前缀。
		cut := -1
		var lastBar TradingDay
		for _, b := range bars {
			if b.TradingDay > lastBar {
				lastBar = b.TradingDay
			}
		}
		for i, d := range cand {
			if (lastBar != 0 && d <= lastBar) || aged(d) >= emptyTailGraceDays {
				cut = i
			}
		}
		pending = append([]TradingDay(nil), cand[cut+1:]...)
		if cut < 0 {
			// 整块挂起：没有根，不落盘，不登记。
			// ⛔ 这里必须前进 ci：循环只在末尾那一句 ci++ 前进（失败重试同一块，勘误四）——
			// 漏掉它 ⇒ 源不给根、consecutive 不涨 ⇒ 同一块无限循环（评审方 2026-09-15 预警）。
			ci++
			continue
		}
		commit := cand[:cut+1]

		if err := s.store.AppendBars(bars); err != nil {
			holdBack("同步在拉到更晚的根之前因落盘失败中止了")
			rep.Synced = synced
			return syncedDays, attempts, HaltStoreWrite, fmt.Errorf("tickflow: 落盘失败，停在 %s: %w", chunk[0], err)
		}
		span := Span{From: commit[0], To: commit[len(commit)-1],
			Bars: len(bars), Days: distinctDays(bars)}
		if err := s.store.CommitSpan(s.cal, k, span, OutcomeComplete); err != nil {
			holdBack("同步在拉到更晚的根之前因扩 coverage 失败中止了")
			rep.Synced = synced
			return syncedDays, attempts, HaltCoverageWrite, fmt.Errorf("tickflow: 扩 coverage 失败，停在 %s: %w", chunk[0], err)
		}

		rep.Bars += len(bars)
		syncedDays = append(syncedDays, commit...)
		if synced[0] == 0 {
			synced[0] = commit[0]
		}
		synced[1] = commit[len(commit)-1]

		// SYN-2 / SYN-5：对不上网格的【根数】与可疑的【交易日】。
		//
		// ⚠️ 只有日内周期有网格 —— `Daily` 是 CalendarPeriod，问它「第几根」没有意义。
		// **不适用与「适用但没找到」在 Misaligned == 0 上不可分辨**，所以这里
		// 用类型断言把「不适用」摘出去，而不是让它悄悄贡献一个 0。
		if ip, isIntraday := req.Period.(IntradayPeriod); isIntraday {
			mis, anom, err := ScanBars(s.cal, k, ip, bars)
			if err == nil {
				rep.Misaligned += mis
				rep.AnomalousDays = append(rep.AnomalousDays, anom...)
			}
		}
		ci++ // 只有这一块落盘并登记之后才往前走
	}
	holdBack("它们之后这次没有拉到根的交易日")
	rep.Synced = synced
	return syncedDays, attempts, HaltDone, nil
}

// emptyTailGraceDays 是挂起的年龄上限：源一根没给的一天，它之后已经收盘了这么多个交易日 ⇒ 登记成「拉过确认没有」，不再等。
//
// ⚠️ 它是一个常数，而这是一个写下来的取舍（用户 2026-09-15 定，选项原文「加一个年龄上限」：尾巴上的一天，
// 如果它收盘后已经过了 N 个交易日（比如 5 个）仍然一根没有，就登记成「确认没有」；重拉最多 N 天，不会一直涨）。
// 没有它，已到期或长期停牌的合约，尾巴上每一天每次同步都要重拉 —— cffexsource 一天一个请求，随到期天数无界增长。
// 代价：源晚于这么多个交易日才补上的那一天会丢。新浪日线的实测读数（出现之后还会再消失）在 docs/release/v0.6.0.md 勘误三。
const emptyTailGraceDays = 5

// closedLaterCounter 交出一个函数：对 d 回答「d 之后、到 now 为止已经收盘了几个交易日」（只数日历覆盖之内的）。
//
// ⚠️ 判「收盘」与 ClipToLastClosed 同口径：最后一段的 End <= now。一次 Walk 从 from 走到覆盖末端。
// ⛔ from 必须不晚于【要拉的第一天】（调用处是 want[0]；挂起的日子都不早于它）：请求区间内部的已收盘交易日也要数进去。只数请求末端之后的话，
// To=0 时末端就是最后一个已收盘日 ⇒ 尾巴年龄永远是 0 ⇒ 年龄上限在这条主路上整个失效（评审方突变 R7，TestEmptyTailAgeCapWithToZero）。
// ⚠️ 只数日历覆盖之内的：now 远在日历末端之后时，尾巴年龄涨不过日历末端 ⇒ 仍挂起、每次重拉（方向是吵；TestAgeCapStopsAtCalendarEnd）。
func closedLaterCounter(cal Calendar, k ProductKey, from TradingDay, now int64) (func(TradingDay) int, error) {
	_, ct, ok := cal.Covers(k)
	var closed []TradingDay
	if ok && from <= ct {
		if err := cal.Walk(k, from, ct, func(d Day) bool {
			if len(d.Sessions) > 0 && d.Sessions[len(d.Sessions)-1].End <= now {
				closed = append(closed, d.Num)
			}
			return true
		}); err != nil {
			return nil, fmt.Errorf("tickflow: 数 %s 起已收盘的交易日失败: %w", from, err)
		}
	}
	return func(d TradingDay) int {
		return len(closed) - sort.Search(len(closed), func(i int) bool { return closed[i] > d })
	}, nil
}

// distinctDays 数这一批根覆盖了几个【交易日】。`Span.Days` 要它。
//
// ⚠️ 它不排序、只数相邻的变化 —— 因为 `Source` 的约定一是「按 Ts 升序」，
// 而升序的 Ts 蕴含非降的 TradingDay。**约定被违反时这个数会偏大**，
// 而那正好会让 `CommitSpan` 的 B1 当场报错 —— **偏向吵的那一侧。**
func distinctDays(bars []Bar) int {
	n := 0
	var prev TradingDay
	for _, b := range bars {
		if b.TradingDay != prev {
			n++
			prev = b.TradingDay
		}
	}
	return n
}
