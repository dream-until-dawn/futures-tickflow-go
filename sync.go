package tickflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
)

// note 返回这次中止的留声文字，以及**要不要留声**。
//
// ⚠️ 只有 HaltDone 不留声 —— 判据写成「白名单」而不是「黑名单」：
// **黑名单漏掉的那个会静默通过，白名单漏掉的那个会吵。**
func (h HaltReason) note() (string, bool) {
	switch h {
	case HaltDone, HaltNoTradingDays, HaltOutsideCoverage:
		// ⚠️ 这三个都不留声，而**理由不同**，写在一起免得被读成一类：
		//	HaltDone             跑完了
		//	HaltNoTradingDays    没有东西可跑 —— 而 Gaps 里写着「不是交易日」
		//	HaltOutsideCoverage  日历答不了这一段 —— 而 Gaps 里写着「日历答不了」
		// ⇒ 后两个之所以不留声，是因为**那件事已经落成了一个事实**（在 Gaps 里）。
		return "", false
	case HaltBudget:
		return "同步因连续失败用尽预算而中止——已同步的部分是完整的，未同步的部分没有被记为「拉过」", true
	case HaltContext:
		return "同步被调用方取消——已同步的部分是完整的", true
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
	case HaltContext:
		return "被取消"
	case HaltNoTradingDays:
		return "区间里没有交易日"
	case HaltOutsideCoverage:
		return "区间在日历覆盖之外"
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
	return &Syncer{cal: cfg.Calendar, store: cfg.Store, src: src, gate: gate}, nil
}

// Sync 把 `req` 那一段同步进库，并交出一份报告。
//
// ⚠️ **报告在返回错误时也是有内容的**：已经落盘的那部分是真的。
// 而它一定带着一个 `Halt` —— 早退的那些路径带的是 `HaltUnknown`，
// 于是它们的报告 `Complete()` 为假。
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
		if err := s.planGaps(k, req.From, to, nil, &rep); err != nil {
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
		if err := s.planGaps(k, req.From, to, nil, &rep); err != nil {
			return rep, err
		}
		rep.Halt = HaltNoTradingDays
		return rep, nil
	}

	chunks := chunkDays(days, caps.BatchDays)
	gateBefore := s.gate.count()
	touched, syncedDays, attempts, halt, ferr := s.fetch(ctx, req, k, chunks, &rep)
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
	verified := s.verifyTouched(touched, &rep)
	if gerr := s.planGaps(k, req.From, to, verified, &rep); gerr != nil && ferr == nil {
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
				"要一个显式决定（Force 或人工确认）", cov[0].From))
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

// verifyTouched 是 SYN-6：**结束时走查本次碰过的段。**
//
// ⛔ 不走查 ⇒ 冷序列的损坏**发现时间没有上界**：一段写下去之后没有人再读它，
// 坏了也要等到有人来取那段数据的那一天才知道 —— 而那可能是几个月后。
//
// 返回「走查过且没问题」的那些段，给缺口分类当输入（B3：**没走查过的时候，
// 那两个计数什么也不意味着**）。
func (s *Syncer) verifyTouched(touched []Span, rep *SyncReport) map[Span]bool {
	okSpans := map[Span]bool{}
	for _, sp := range touched {
		if err := s.store.Verify(sp); err != nil {
			rep.TruncatedTails = append(rep.TruncatedTails,
				fmt.Sprintf("走查 %s..%s 失败：%v", sp.From, sp.To, err))
			continue
		}
		okSpans[sp] = true
	}
	return okSpans
}

// planGaps 把 coverage 翻成六类缺口。
//
// ⚠️ 没被本次走查过的段一律带 ErrSpanUnverified —— 那是 B3 的直接落法：
// **「没走查过」不许悄悄变成一个肯定的「确认没有」。**
func (s *Syncer) planGaps(k ProductKey, from, to TradingDay, verified map[Span]bool, rep *SyncReport) error {
	var cov []SpanStatus
	for _, sp := range s.store.Coverage() {
		st := SpanStatus{Span: sp}
		if !verified[sp] {
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
	chunks [][]TradingDay, rep *SyncReport) ([]Span, []TradingDay, int, HaltReason, error) {

	consecutive := 0
	attempts := 0
	var synced [2]TradingDay
	var touched []Span
	var syncedDays []TradingDay

	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			rep.Synced = synced
			return touched, syncedDays, attempts, HaltContext, fmt.Errorf("tickflow: 同步被取消: %w", err)
		}
		br := BarRequest{Symbol: req.Symbol, Period: req.Period,
			From: chunk[0], To: chunk[len(chunk)-1]}
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
				rep.Synced = synced
				return touched, syncedDays, attempts, HaltBudget, fmt.Errorf("%w: 连续 %d 次失败（上限 %d），"+
					"停在 %s；最后一次: %v",
					ErrBudgetExhausted, consecutive, req.MaxConsecutiveFails, chunk[0], err)
			}
			continue
		}
		consecutive = 0

		if err := s.store.AppendBars(bars); err != nil {
			rep.Synced = synced
			return touched, syncedDays, attempts, HaltBudget, fmt.Errorf("tickflow: 落盘失败，停在 %s: %w", chunk[0], err)
		}
		span := Span{From: chunk[0], To: chunk[len(chunk)-1],
			Bars: len(bars), Days: distinctDays(bars)}
		if err := s.store.CommitSpan(s.cal, k, span, OutcomeComplete); err != nil {
			rep.Synced = synced
			return touched, syncedDays, attempts, HaltBudget, fmt.Errorf("tickflow: 扩 coverage 失败，停在 %s: %w", chunk[0], err)
		}

		rep.Bars += len(bars)
		touched = append(touched, span)
		syncedDays = append(syncedDays, chunk...)
		if synced[0] == 0 {
			synced[0] = chunk[0]
		}
		synced[1] = chunk[len(chunk)-1]

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
	}
	rep.Synced = synced
	return touched, syncedDays, attempts, HaltDone, nil
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
