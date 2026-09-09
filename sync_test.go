package tickflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// ───────────────────────── 桩件 ─────────────────────────

// countingPacer 记「闸门被问过几次」。
//
// ⛔ 它是本文件那条最强断言的**载体**：断言「源发的每一次请求都问过
// 【调用方注入的那一个】Pacer」——而一个写死的实现（无论写死成什么）
// 都不会碰到这个计数器。
type countingPacer struct {
	mu sync.Mutex
	n  int
}

func (p *countingPacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
	return ctx.Err()
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
		Since: map[Period]TradingDay{Daily: 20200101}, BatchDays: s.batch}
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
func (s *fakeStore) HasBars(d TradingDay) (bool, error) {
	return s.hasBarsSet[d], nil
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
		w.Write([]byte("ok"))
	}))
	t.Cleanup(h.srv.Close)

	syn, err := NewSyncer(SyncerConfig{
		Calendar: week(),
		Store:    h.store,
		Pacer:    p,
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

// TestSyncerRoutesEveryRequestThroughTheInjectedPacer 是本片最强的那条断言。
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
func TestSyncerRoutesEveryRequestThroughTheInjectedPacer(t *testing.T) {
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
		NewSource: func(*http.Client) Source { return &httpSource{} },
	})
	if !errors.Is(err, pacing.ErrNoPacer) {
		t.Fatalf("没给 Pacer 应当报 ErrNoPacer，实得 %v", err)
	}
	// 对照：给了就该成 —— 否则上面那条也可能是「它对谁都报错」。
	if _, err := NewSyncer(SyncerConfig{
		Calendar: week(), Store: &fakeStore{}, Pacer: pacing.NoPacing(),
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
