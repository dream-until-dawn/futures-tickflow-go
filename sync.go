package tickflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
)

// note 返回这次中止的留声文字，以及**要不要留声**。
//
// ⚠️ 只有 HaltDone 不留声 —— 判据写成「白名单」而不是「黑名单」：
// **黑名单漏掉的那个会静默通过，白名单漏掉的那个会吵。**
func (h HaltReason) note() (string, bool) {
	switch h {
	case HaltDone:
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
}

// Syncer 把一个源同步进一个库。
type Syncer struct {
	cal   Calendar
	store Store
	src   Source
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
	src := cfg.NewSource(c)
	if src == nil {
		return nil, errors.New("tickflow: NewSource 返回了 nil——" +
			"而一个 nil 的 Source 会在第一次调用时 panic，那比现在报错晚得多")
	}
	return &Syncer{cal: cfg.Calendar, store: cfg.Store, src: src}, nil
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
		return rep, nil
	}

	days, err := s.tradingDays(k, from, hi)
	if err != nil {
		return rep, err
	}
	if len(days) == 0 {
		return rep, nil
	}

	chunks := chunkDays(days, s.src.Caps(k).BatchDays)
	halt, ferr := s.fetch(ctx, req, k, chunks, &rep)
	rep.Halt = halt
	if ferr != nil {
		return rep, ferr
	}
	return rep, nil
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
// ⚠️ `BatchDays` 的零值在 `Capabilities.Validate()` 那一侧已经被拒。
// **这里再判一次，而理由不是「以防万一」**：本函数拿到 0 会造出无限循环，
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
	chunks [][]TradingDay, rep *SyncReport) (HaltReason, error) {

	consecutive := 0
	var synced [2]TradingDay

	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			rep.Synced = synced
			return HaltContext, fmt.Errorf("tickflow: 同步被取消: %w", err)
		}
		br := BarRequest{Symbol: req.Symbol, Period: req.Period,
			From: chunk[0], To: chunk[len(chunk)-1]}
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
				return HaltBudget, fmt.Errorf("%w: 连续 %d 次失败（上限 %d），"+
					"停在 %s；最后一次: %v",
					ErrBudgetExhausted, consecutive, req.MaxConsecutiveFails, chunk[0], err)
			}
			continue
		}
		consecutive = 0

		if err := s.store.AppendBars(bars); err != nil {
			rep.Synced = synced
			return HaltBudget, fmt.Errorf("tickflow: 落盘失败，停在 %s: %w", chunk[0], err)
		}
		span := Span{From: chunk[0], To: chunk[len(chunk)-1],
			Bars: len(bars), Days: distinctDays(bars)}
		if err := s.store.CommitSpan(s.cal, k, span, OutcomeComplete); err != nil {
			rep.Synced = synced
			return HaltBudget, fmt.Errorf("tickflow: 扩 coverage 失败，停在 %s: %w", chunk[0], err)
		}

		rep.Bars += len(bars)
		if synced[0] == 0 {
			synced[0] = chunk[0]
		}
		synced[1] = chunk[len(chunk)-1]
	}
	rep.Synced = synced
	return HaltDone, nil
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
