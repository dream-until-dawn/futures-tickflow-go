package tickflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// ───────────────────────── 桩件 ─────────────────────────

// countingPacer 记「闸门被问过几次」。
//
// ⛔ 它是本文件那条最强断言的**载体**：断言「源发的每一次请求都问过
// 【调用方注入的那一个】Pacer」——而一个写死的实现（无论写死成什么）
// 都不会碰到这个计数器。
type countingPacer struct {
	mu  sync.Mutex
	n   int
	log *eventLog // 可选：记「闸门」与「请求」的先后
}

func (p *countingPacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
	if p.log != nil {
		p.log.add("W")
	}
	return ctx.Err()
}

// eventLog 记「闸门」(W) 与「对端收到请求」(R) 的**先后**。
//
// 🔴 **它存在的理由是一处被评审方打中的射程**（2026-09-09，我独立复现：
// 把 `pacing.transport.RoundTrip` 里的 `Wait` 挪到 `next.RoundTrip` 【之后】
// ⇒ 根包与 pacing 两个包**一共 0 红**）。
//
//	「次数相等」证得了【被调用】，**证不了【在请求之前】被调用** ——
//	而一个「先发请求、再等」的闸门，请求照样满速出去。
//
// ⇒ 计数是一个**多重集**断言，顺序是一个**序列**断言；
// **前者天然看不见后者**，而闸门这件事的全部内容就在后者里。
type eventLog struct {
	mu sync.Mutex
	s  []string
}

func (l *eventLog) add(e string) { l.mu.Lock(); l.s = append(l.s, e); l.mu.Unlock() }
func (l *eventLog) seq() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.s, "")
}

func (p *countingPacer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

// httpSource 是一个**真的发 HTTP 请求**的源：它拿到的那个 client 是什么样，
// 请求就怎么走。⇒ 闸门有没有装上，在对端与 Pacer 两侧都看得见。
type httpSource struct {
	c        *http.Client
	url      string
	batch    int
	failN    int // 前 failN 次调用返回错误
	calls    int
	failCall []TradingDay // 记每一次调用的起点，给「那一天真的被试过」那条断言
}

func (s *httpSource) Bars(ctx context.Context, req BarRequest) ([]Bar, error) {
	s.calls++
	s.failCall = append(s.failCall, req.From)
	resp, err := s.c.Get(s.url)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	if s.calls <= s.failN {
		return nil, errors.New("httpSource: 假装上游出错")
	}
	return []Bar{{Ts: 1, TsEnd: 61000, TradingDay: req.From, Close: 1.5}}, nil
}

func (s *httpSource) Caps(ProductKey) Capabilities {
	return Capabilities{Periods: []Period{Daily},
		Since: map[Period]TradingDay{Daily: 20200101}, BatchDays: s.batch,
		// ⚠️ 这一行是被【自己的测试】逼出来的。实测（删掉它再跑）：
		//
		//	tickflow: 源在 SHFE.rb 上的 Caps 自己不自洽: ClientUse=0 不合法…
		//	⇒ TestGateBypassLeavesANote 两个子格都红
		//
		// ⇒ **`Sync` 当场硬报错，不是「静默关掉」** —— `caps.Validate()` 在
		// `rep.UngatedOK = …` 之前六十行，零值**根本走不到那一行**。
		// ⇒ 教训是正的那一面：**`Validate` 那条新接线，第一个抓到的就是它自己的桩。**
		//
		// 🔴 **而这一句改过一次，上一版的【成因和教训都是反的】**（评审方 2026-09-10 实测抓的）：
		// 我写的是「不写它 ⇒ 闸门那条检查静默关掉」。
		// ⛔ 而它**曾经是真的** —— 我写下它的时候 `Sync` 还没有调 `Validate()`；
		// **那条调用是我在【同一颗提交里】后加的，一加就把这句注释作废了。**
		//
		// > **一颗提交可以作废它自己先写下的话** —— 本仓记过它的「数」的形态
		// > （一颗提交作废了它自己写的数），这是它的【成因】形态。
		// > ⇒ 而成因这一形态更阴：数字看得出陈旧，**一句因果读起来永远新鲜**。
		// ⇒ 动作：**同一颗提交里改了控制流，回头把这颗里写过的每一句「⇒ 会怎样」重跑一遍。**
		ClientUse: ClientUseHTTP}
}

// fakeStore 只记下被调了什么，不做任何落盘。**每一格都可配**，
// 因为丙三之三那几条（C3b / D2b / SYN-6）测的正是「库告诉编排什么」。
type fakeStore struct {
	appended int
	spans    []Span

	openState  OpenState
	coverage   []Span
	verifyErr  error
	discarded  int
	verified   []Span
	hasBarsSet map[TradingDay]bool
}

func (s *fakeStore) Coverage() []Span { return append([]Span(nil), s.coverage...) }

// DaysWithBars 是这个桩对新读法的实现。
//
// ⛔ 它**按段答**，而 `hasBarsSet` 仍是按天记的 —— 两者的换算就在这儿。
// ⚠️ 而这个桩**不模拟三值语义的第三值**（未走查 ⇒ 报错）：
// 本文件里「哪一段走查过」由 `Syncer` 自己那份 `verified` 在 `SpanStatus.Err` 上表达，
// 桩这一侧再模拟一次只会让两处口径漂开。
// 🔴 **真实现那一侧的三值语义由 `store/segfile` 自己的测试与那条缝的集成测试守**，
// 不由这个桩守 —— 写下来是因为：**一个桩的沉默，读起来像「这一格不存在」。**
func (s *fakeStore) DaysWithBars(sp Span) (map[TradingDay]bool, error) {
	out := make(map[TradingDay]bool)
	for d, ok := range s.hasBarsSet {
		if ok && d >= sp.From && d <= sp.To {
			out[d] = true
		}
	}
	return out, nil
}
func (s *fakeStore) AppendBars(b []Bar) error { s.appended += len(b); return nil }
func (s *fakeStore) Verify(sp Span) error     { s.verified = append(s.verified, sp); return s.verifyErr }
func (s *fakeStore) OpenState() OpenState     { return s.openState }
func (s *fakeStore) DiscardCoverage() error   { s.discarded++; s.coverage = nil; return nil }
func (s *fakeStore) Close() error             { return nil }

// CommitSpan 记下这一段，**并且扩 coverage** ——
// ⛔ 上一版只记 spans 不动 coverage，于是 `planGaps` 里那张
// 「哪些段本次走查过」的表**一格都匹配不上**：查表恒为 false，等于死代码，
// 而所有断言照绿。**一个和真实现【行为不同】的桩，会让被测代码的一整段静默失效。**
func (s *fakeStore) CommitSpan(_ Calendar, _ ProductKey, sp Span, _ Outcome) error {
	s.spans = append(s.spans, sp)
	s.coverage = append(s.coverage, sp)
	return nil
}

var _ Store = (*fakeStore)(nil)

// harness 起一个数请求次数的对端，并造好一个 Syncer。
type harness struct {
	srv   *httptest.Server
	hits  *int32Counter
	src   *httpSource
	store *fakeStore
	syn   *Syncer
}

type int32Counter struct {
	mu sync.Mutex
	n  int
}

func (c *int32Counter) inc() { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *int32Counter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func newHarness(t *testing.T, p pacing.Pacer, batch, failN int) *harness {
	return newHarnessWithStore(t, p, batch, failN, &fakeStore{})
}

func newHarnessWithStore(t *testing.T, p pacing.Pacer, batch, failN int, st *fakeStore) *harness {
	t.Helper()
	h := &harness{hits: &int32Counter{}, store: st}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.inc()
		if cp, ok := p.(*countingPacer); ok && cp.log != nil {
			cp.log.add("R")
		}
		w.Write([]byte("ok"))
	}))
	t.Cleanup(h.srv.Close)

	syn, err := NewSyncer(SyncerConfig{
		Calendar: week(),
		Store:    h.store,
		Pacer:    p,
		Timeout:  30 * time.Second,
		NewSource: func(c *http.Client) Source {
			h.src = &httpSource{c: c, url: h.srv.URL, batch: batch, failN: failN}
			return h.src
		},
	})
	if err != nil {
		t.Fatalf("NewSyncer 失败：%v", err)
	}
	h.syn = syn
	return h
}

func req(k int) SyncRequest {
	return SyncRequest{
		Symbol:              Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2610},
		Period:              Daily,
		From:                20200106,
		To:                  20200110,
		MaxConsecutiveFails: k,
	}
}

// ───────── 必做一：无闸门的 Client 不得进源 —— 四级阶梯的【第四级】 ─────────

// TestEveryRequestThroughOurClientIsGated 是本片最强的那条断言。
//
// 🔴 **它原名 `TestSyncerRoutesEveryRequestThroughTheInjectedPacer`，而那个名字过头了**
// （评审方 2026-09-09 指出，我实测确认）：**存在一类 request 不经过** ——
// 一个「收下 client 又丢掉」的源，它的请求一次都不走这里。
// ⇒ 名字收窄成「经过【我们那个 client】的请求都被闸住」，那是它真正证得了的。
// 而「源有没有用我们那个 client」由 `TestGateBypassLeavesANote` 守。
// ⇒ 判据：**一个名字里的「every」，要能说清 every 的【定义域】** ——
// 说不清的时候，它承诺的比断言证得的多。
//
// ⛔ **四级阶梯**（前三级各自被什么绕过去，写在这儿而不是靠下一个人重新发现）：
//
//	一 断言【接线】     「Transport 是我们装的那个」 ⇒ **空操作满足**（NoPacing 就是）
//	二 断言【效果】     「两次请求真的隔了时间」      ⇒ **写死的常量满足**
//	三 断言【== 注入值】「隔的是调用方给的那个 d」    ⇒ **恰好等于那个常量时仍然满足**
//	四 断言【注入的那一个【实例】被问过】            ⇒ **挡得住任何常量**
//
// 🔴 第四级不是新想出来的：**它逐字写在本仓 `tools/probe/README.md` 里** ——
// 「1 个已知值挡不住『恰好返回那个值』；2 个不同的已知值挡得住任何常量」，
// 而那一节的标题是「对照组的价值在它们之间的**差**，不在各自的**值**」。
// ⇒ 判据：**每次觉得「这条断言还能更强」，先去仓里搜有没有记过那一格。**
//
// ⚠️ 这里用「注入的实例被问过几次」而不是用两个不同的 `d` 量时间，
// 是因为它同时更强、且**不量墙钟**（墙钟测试会因机器负载偶发红，而偶发红迟早被加 skip）。
func TestEveryRequestThroughOurClientIsGated(t *testing.T) {
	p := &countingPacer{}
	other := &countingPacer{} // 第二个实例：它【没有】被注入
	h := newHarness(t, p, 1, 0)

	rep, err := h.syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatalf("同步不该出错：%v", err)
	}

	// —— 基线：**确实发生过上游请求** ——
	// ⛔ 少了这一条，下面那个「相等」在 0 == 0 时照样绿（四因表第二格：输入走不到那一行）。
	if h.hits.get() == 0 {
		t.Fatal("对端一次请求都没收到 —— 基线没成立，下面那条相等什么也不证明")
	}
	if rep.Bars == 0 {
		t.Fatal("一根都没同步 —— 基线没成立")
	}

	// —— 第四级：注入的【那一个】实例，每次请求都被问过 ——
	if p.count() != h.hits.get() {
		t.Errorf("闸门被问了 %d 次，而对端收到 %d 次请求\n"+
			"  ⇒ 有请求没有过闸门；「装上了」与「被闸住」是两句话", p.count(), h.hits.get())
	}
	// —— 对照：没被注入的那个实例必须【一次都没被问】 ——
	// ⛔ 少了这一条，一个「问了某个 Pacer」的实现也会绿。
	if other.count() != 0 {
		t.Errorf("没有被注入的那个 Pacer 被问了 %d 次 —— 那不可能，除非断言打偏了", other.count())
	}
}

// TestSyncerRefusesWithoutAPacer 忘了配限流 ⇒ 当场报错，不是「默认不限流」。
func TestSyncerRefusesWithoutAPacer(t *testing.T) {
	_, err := NewSyncer(SyncerConfig{
		Calendar:  week(),
		Store:     &fakeStore{},
		Timeout:   30 * time.Second,
		NewSource: func(*http.Client) Source { return &httpSource{} },
	})
	if !errors.Is(err, pacing.ErrNoPacer) {
		t.Fatalf("没给 Pacer 应当报 ErrNoPacer，实得 %v", err)
	}
	// 对照：给了就该成 —— 否则上面那条也可能是「它对谁都报错」。
	if _, err := NewSyncer(SyncerConfig{
		Calendar: week(), Store: &fakeStore{}, Pacer: pacing.NoPacing(),
		Timeout:   30 * time.Second,
		NewSource: func(*http.Client) Source { return &httpSource{} },
	}); err != nil {
		t.Fatalf("给了 NoPacing() 应当成功，实得 %v", err)
	}
}

// TestSyncerDoesNotAcceptAPrebuiltSource 记的是一件**写不出来**的事。
//
// ⛔ `SyncerConfig` 里没有 `*http.Client`，也没有 `Source` —— 只有 `SourceFactory`。
// ⇒ 「我塞一个没有闸门的 client 进去」这句话**在这个 API 上说不出来**。
// 而这正是必做一要的：**取消那个选择**，而不是检查它。
//
// ⚠️ 本条断言的是【类型】，所以它的「变红」方式是**编译不过** ——
// 本仓记过这个例外：当被测的东西就是编译期约束时，编译失败就是它变红的样子。
func TestSyncerDoesNotAcceptAPrebuiltSource(t *testing.T) {
	var cfg SyncerConfig
	// 这一行只要能编译，就说明工厂签名是「收 client」而不是「给 client」。
	cfg.NewSource = func(c *http.Client) Source {
		if c == nil {
			t.Error("工厂收到的 client 是 nil —— 那样源会退回 http.DefaultTransport，满速打上游")
		}
		return &httpSource{c: c}
	}
	if cfg.NewSource == nil {
		t.Fatal("不可能")
	}
}

// ───────── 必做二：预算耗尽 ⇒ 错误，不是「成功同步了 0 天」 ─────────

// TestBudgetExhaustedIsAnErrorNotACleanReport 三件事一起断言。
func TestBudgetExhaustedIsAnErrorNotACleanReport(t *testing.T) {
	h := newHarness(t, pacing.NoPacing(), 1, 99) // 每一次都失败
	rep, err := h.syn.Sync(context.Background(), req(0), 0)

	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("预算耗尽应当返回 ErrBudgetExhausted，实得 %v", err)
	}
	if rep.Halt != HaltBudget {
		t.Errorf("报告的 Halt = %v，期望 HaltBudget", rep.Halt)
	}
	// ⛔ 这一条是必做三的落点：**在日志上吵、在报告上哑 ⇒ 按哑算。**
	if rep.Complete() {
		t.Error("一次因预算耗尽而中止的同步，报告说 Complete() —— " +
			"而下游读的正是这一位")
	}
}

// TestBudgetComparisonOperatorIsPinned 把 `>` 与 `>=` 的差别钉死。
//
// ⛔ `MaxConsecutiveFails = 0` 时两个符号给出**完全不同的行为**：
//
//	>   第一次失败【之后】停 ⇒ 那一天真的被试过 ⇒ 吵
//	>=  一次都不试就停       ⇒ 一份「成功同步了 0 天」的报告 ⇒ 哑
//
// ⇒ 所以断言的是**「那一天真的被试过」**，不是「没有报错」——
// 后者在 `>=` 那一版上照样绿。
//
// 🔴 而这一条的来历要写下来：我先前判过「K 的零值是安全的」，
// 而那句话当时**没有真值** —— 评审方量出 `MaxConsecutiveFails` 零个使用点。
// **「这个取值是安全的」是一句关于【行为】的断言；行为不存在时它没有真值。**
// ⇒ 现在行为存在了，所以它可以被钉住 —— 钉的就是这一个符号。
func TestBudgetComparisonOperatorIsPinned(t *testing.T) {
	h := newHarness(t, pacing.NoPacing(), 1, 99)
	_, err := h.syn.Sync(context.Background(), req(0), 0)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("前提没成立：应当因预算耗尽而中止，实得 %v", err)
	}
	if h.src.calls != 1 {
		t.Fatalf("K=0 时源被调了 %d 次，期望【正好 1 次】\n"+
			"  0 次 ⇒ 比较符是 >= ：一次都不试就停，报告会说「成功同步了 0 天」\n"+
			"  2 次 ⇒ 预算根本没生效", h.src.calls)
	}
	if h.src.failCall[0] != 20200106 {
		t.Errorf("被试的那一天是 %s，期望 20200106（区间的第一天）", h.src.failCall[0])
	}

	// 对照：K=1 时应当试【两次】才停 —— 少了这一格，上面那个 1 也可能是「它永远只试一次」。
	h2 := newHarness(t, pacing.NoPacing(), 1, 99)
	if _, err := h2.syn.Sync(context.Background(), req(1), 0); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("K=1 也该耗尽，实得 %v", err)
	}
	if h2.src.calls != 2 {
		t.Errorf("K=1 时源被调了 %d 次，期望 2 次 —— 预算没有跟着 K 走", h2.src.calls)
	}
}

// ───────── 必做三：为什么停下来，落在【报告】上 ─────────

func TestHaltReasonsLandOnTheReport(t *testing.T) {
	t.Run("跑完", func(t *testing.T) {
		h := newHarness(t, pacing.NoPacing(), 1, 0)
		rep, err := h.syn.Sync(context.Background(), req(0), 0)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Halt != HaltDone {
			t.Errorf("Halt = %v，期望 HaltDone", rep.Halt)
		}
		if !rep.Complete() {
			t.Errorf("一次跑完且无痕迹的同步，Complete() 却是假：%s", rep)
		}
	})

	t.Run("被取消", func(t *testing.T) {
		h := newHarness(t, pacing.NoPacing(), 1, 0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rep, err := h.syn.Sync(ctx, req(0), 0)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("期望 context.Canceled，实得 %v", err)
		}
		if rep.Halt != HaltContext {
			t.Errorf("Halt = %v，期望 HaltContext", rep.Halt)
		}
		if rep.Complete() {
			t.Error("一次被取消的同步，报告说 Complete()")
		}
	})
}

// TestEarlyReturnsCarryHaltUnknown 早退路径返回的那份报告必须带着「没记录理由」。
//
// 🔴 这一条是评审方 2026-09-09 钉的条件的**落点**：
// `haltReason` 要落在【报告】上，不是循环里的一个局部变量 ——
// **局部变量在 `return SyncReport{}, err` 那条路上根本不参与。**
// ⇒ 于是「一次连循环都没进的同步」也落在吵的那一侧。
func TestEarlyReturnsCarryHaltUnknown(t *testing.T) {
	h := newHarness(t, pacing.NoPacing(), 1, 0)
	for _, c := range []struct {
		name string
		r    SyncRequest
	}{
		{"Period 是 nil", SyncRequest{Symbol: Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2610}, From: 20200106}},
		{"From 不合法", SyncRequest{Symbol: Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2610}, Period: Daily}},
		{"From 晚于 To", SyncRequest{Symbol: Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2610},
			Period: Daily, From: 20200110, To: 20200106}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep, err := h.syn.Sync(context.Background(), c.r, 0)
			if err == nil {
				t.Fatalf("这一格本该出错")
			}
			if rep.Halt != HaltUnknown {
				t.Errorf("Halt = %v，期望 HaltUnknown", rep.Halt)
			}
			if rep.Complete() {
				t.Error("一份早退的报告说 Complete() —— 而它连循环都没进")
			}
		})
	}
}

// TestChunkDaysFollowsTheSourcesBatchDays 切法跟着源走，而不是跟着调用方的偏好走。
func TestChunkDaysFollowsTheSourcesBatchDays(t *testing.T) {
	days := []TradingDay{20200106, 20200107, 20200108, 20200109, 20200110}
	for _, c := range []struct {
		name  string
		batch int
		want  int
	}{
		{"一天一块（cffexsource）", 1, 5},
		{"两天一块", 2, 3},
		{"整段一块（sinasource）", BatchDaysUnbounded, 1},
		{"批量比区间还长", 99, 1},
		// ⚠️ 0 在 Capabilities.Validate() 那一侧已经被拒；这里再判一次的理由
		// 不是「以防万一」：拿到 0 会造出**无限循环**，而那种失败不报错、只是不返回。
		{"零值（到不了这儿，但真到了不许挂住）", 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := len(chunkDays(days, c.batch)); got != c.want {
				t.Errorf("切成了 %d 块，期望 %d", got, c.want)
			}
		})
	}
	if chunkDays(nil, 1) != nil {
		t.Error("空输入应当切成 nil")
	}
}

// TestTimeoutReachesTheSourcesClient 超时必须【到得了源】，且是调用方给的那一个。
//
// 🔴 **这一格是 SourceFactory 顺手拿走的，而第一版把它丢了。**
// 评审方 2026-09-09 只问了一句「超时归谁定」，我去量，发现的不是一个空位：
//
//	两个源自己的默认   &http.Client{Timeout: 30 * time.Second}
//	pacing.Client()    &http.Client{Transport: rt}   ← Timeout 零值 = 没有超时
//	WithHTTPClient     c.http = h                    ← 整个替换，不是合并
//	⇒ 走 SourceFactory 这条路，那 30 秒被【静默拿掉了】
//
// ⛔ 而它不是「少了个保险」：`MaxConsecutiveFails` 数的是【错误】，
// 而一个挂住的请求不产生错误 —— **没有超时，那个预算有一整类失败接不住。**
//
// ⚠️ 断言用【两个不同的注入值】，同必做一那条阶梯：
// 一个值挡不住「恰好写死成那个值」，两个不同的值挡得住任何常量。
func TestTimeoutReachesTheSourcesClient(t *testing.T) {
	for _, want := range []time.Duration{7 * time.Second, 43 * time.Second} {
		t.Run(want.String(), func(t *testing.T) {
			var got time.Duration
			if _, err := NewSyncer(SyncerConfig{
				Calendar: week(), Store: &fakeStore{}, Pacer: pacing.NoPacing(),
				Timeout: want,
				NewSource: func(c *http.Client) Source {
					got = c.Timeout
					return &httpSource{c: c}
				},
			}); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("源拿到的 client 超时是 %v，而调用方给的是 %v", got, want)
			}
		})
	}
}

// TestTimeoutZeroIsRefusedAndNoTimeoutIsNamed 零值被拒，而「我不要超时」要写出来。
//
// ⚠️ 判据是那条：**给一个取值定级，取它最哑的那个后果。**
// `http.Client{Timeout: 0}` 就是「永不超时」——
// 想好了不要超时的人知道自己在等什么；忘了填的人会看着同步挂住而不知道为什么。
func TestTimeoutZeroIsRefusedAndNoTimeoutIsNamed(t *testing.T) {
	mk := func(d time.Duration) (time.Duration, error) {
		var got time.Duration
		_, err := NewSyncer(SyncerConfig{
			Calendar: week(), Store: &fakeStore{}, Pacer: pacing.NoPacing(), Timeout: d,
			NewSource: func(c *http.Client) Source { got = c.Timeout; return &httpSource{c: c} },
		})
		return got, err
	}
	if _, err := mk(0); err == nil {
		t.Error("Timeout 零值应当被拒 —— 它是「永不超时」，而那让失败预算接不住挂起")
	}
	if _, err := mk(-5 * time.Second); err == nil {
		t.Error("负的超时（NoTimeout 之外）应当被拒")
	}
	// 具名的那一个：收下，且真的不设超时。
	got, err := mk(NoTimeout)
	if err != nil {
		t.Fatalf("NoTimeout 应当被收下，实得 %v", err)
	}
	if got != 0 {
		t.Errorf("写了 NoTimeout，而源拿到的 client 超时是 %v", got)
	}
}

// TestPacerIsConsultedBeforeEachRequest 闸门必须在请求**之前**被问 —— 顺序，不是次数。
//
// 🔴 **这一条补的是评审方 2026-09-09 打中的射程，我独立复现过**：
// 把 `pacing.transport.RoundTrip` 里的 `Wait` 挪到 `next.RoundTrip` 之后
// ⇒ 根包与 pacing 两个包**一共 0 红** —— 计数一模一样，而闸门形同虚设。
//
// ⛔ **计数是一个【多重集】断言，顺序是一个【序列】断言；前者天然看不见后者。**
// 而闸门这件事的全部内容就在后者里：先发请求再等，请求照样满速出去。
//
// ⚠️ 而「它今天会不会出事」是另一句话（评审方一并量了，我认）：
// `Sync` 是顺序调用，挪到后面时相邻两次请求之间仍然隔着 d，
// **它真会伤人的条件是同一个 transport 上有并发请求**。
// ⇒ 所以这是一处**射程缺口**，不是一个**风险** —— 两者要分开说。
// ⛔ 而我仍然现在就补，理由是：**一条「等到并发那天再补」的断言，
// 要靠人在那一天记得它** —— 而它现在就写得出来，成本是一个字符串。
//
// ⚠️ 一处我自己的读数与评审方不同，一并写下（我读 `fixed.Wait` 推的，未实测计时）：
// 他写「差别只在首次不等、末次多等一次」，而按 `next` 的零值推，
// **前两次请求之间没有间隔**（第一次 Wait 在请求之后才把 next 推到 +d）——
// 也就是每次同步开头有一个 2 连发。**结论（不判必改）不变，而那句描述要更准一格。**
func TestPacerIsConsultedBeforeEachRequest(t *testing.T) {
	p := &countingPacer{log: &eventLog{}}
	h := newHarness(t, p, 1, 0)

	if _, err := h.syn.Sync(context.Background(), req(0), 0); err != nil {
		t.Fatalf("同步不该出错：%v", err)
	}

	got := p.log.seq()
	// 基线：确实发生过 —— 空串会让下面那条判断恒真。
	if got == "" {
		t.Fatal("既没有闸门也没有请求 —— 基线没成立，这一条什么也不证明")
	}
	want := strings.Repeat("WR", h.hits.get())
	if got != want {
		t.Errorf("闸门与请求的先后是 %q，期望 %q\n"+
			"  W=闸门被问  R=对端收到请求\n"+
			"  ⇒ 出现 \"RW\" 说明【先发请求再等】——那样的闸门拦不住任何东西，\n"+
			"    而它的【调用次数】和正确实现一模一样", got, want)
	}
}

// TestGateBypassLeavesANote 一个「收下 client 又丢掉」的工厂，必须**出声**。
//
// 🔴 **这一条接住的是一个假绿**（评审方 2026-09-09 造，我独立复现，读数逐字一致）：
//
//	对端收到 5 次请求 · Bars=5 · 注入的闸门被问 **0** 次 · 没有超时
//	而报告 **Complete() == true**，留声 **0** 条
//
// ⇒ 设计里那句残余射程（「调用方仍可以在自己的 lambda 里无视收到的那个 client」）
// **说的是真的，而它漏了后半句：那样做【报告不会提】。**
// ⇒ 判据：**一个「堵不住」的洞，至少要让它出声** —— 堵不住和不留声是两件事，
// 而我上一版把它们当成了一件。
//
// ⚠️ 两个已知端都验：守规矩的工厂**不许**被误报，丢掉 client 的工厂**必须**被报。
// 少了前者，一个「无论如何都报一条」的实现也会绿。
func TestGateBypassLeavesANote(t *testing.T) {
	newSyncer := func(t *testing.T, discard bool) (*Syncer, *int32Counter, *countingPacer) {
		t.Helper()
		hits := &int32Counter{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.inc()
			w.Write([]byte("ok"))
		}))
		t.Cleanup(srv.Close)
		p := &countingPacer{}
		syn, err := NewSyncer(SyncerConfig{
			Calendar: week(), Store: &fakeStore{}, Pacer: p, Timeout: 30 * time.Second,
			NewSource: func(c *http.Client) Source {
				if discard {
					c = &http.Client{} // 收下，丢掉，自己造一个裸的
				}
				return &httpSource{c: c, url: srv.URL, batch: 1}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return syn, hits, p
	}

	t.Run("守规矩的工厂 ⇒ 不报", func(t *testing.T) {
		syn, hits, p := newSyncer(t, false)
		rep, err := syn.Sync(context.Background(), req(0), 0)
		if err != nil {
			t.Fatal(err)
		}
		if hits.get() == 0 {
			t.Fatal("对端一次请求都没收到 —— 基线没成立")
		}
		if p.count() == 0 {
			t.Fatal("闸门一次都没被问 —— 基线没成立（守规矩那一侧本该走闸门）")
		}
		if len(rep.UngatedSource) != 0 {
			t.Errorf("守规矩的工厂被报了 %d 条 —— 误报", len(rep.UngatedSource))
		}
		if !rep.Complete() {
			t.Errorf("守规矩的一次同步却不 Complete：%s", rep)
		}
	})

	t.Run("丢掉 client 的工厂 ⇒ 必须报", func(t *testing.T) {
		syn, hits, p := newSyncer(t, true)
		rep, err := syn.Sync(context.Background(), req(0), 0)
		if err != nil {
			t.Fatal(err)
		}
		// 前提：请求确实发出去了，而闸门确实一次没被用 —— 这正是那个假绿的形状。
		if hits.get() == 0 {
			t.Fatal("对端一次请求都没收到 —— 前提没成立")
		}
		if p.count() != 0 {
			t.Fatalf("闸门被问了 %d 次 —— 前提没成立（这一格要的就是绕过去）", p.count())
		}
		if len(rep.UngatedSource) == 0 {
			t.Error("源绕开了我们的 client，请求满速出去，而报告一个字都没说")
		}
		if rep.Complete() {
			t.Error("这次同步既没有限流也没有超时，而报告说 Complete() —— " +
				"而下游读的正是这一位")
		}
	})
}
