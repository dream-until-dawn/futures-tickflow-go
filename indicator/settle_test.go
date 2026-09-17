package indicator

import (
	"errors"
	"math"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件盯住「有定义」与「已收敛」的区别。
//
// Warmup() 报的是「算得出一个数」，Settle() 报的是「这个数不再取决于从哪根开始喂」。
// 窗口类指标两者相同；递归类相差一到两个数量级。姊妹仓在补上 Settler 之前，
// 它的 Feed 是按 Warmup 决定预热量的，于是国内口径的 MACD 只预读 4 根，
// 值错了一倍而 Ready() 照报 true（本库的 Feed 在 v0.9，届时照 Settle 预热）。
//
// ⚠️ 与姊妹仓不同的两处（docs/design.md §十五「v0.8 起手」、docs/probe.md 6.33）：
//
//	基线数据   姊妹仓用 500 根 ETH 日线；本库用新浪 RB0 日线的末 1000 根（未复权）
//	measureSettle  多一格「退化：开头价格重复」—— 见 errDegenerateStart

// settleBaseLen 是收敛基线取 RB0 末尾多少根。
// RB0 全量上量出的最大收敛点是 506（RSI14/CN，6.33）⇒ 取 1000 留够余量，又不至于让 O(n²) 的扫描太慢。
const settleBaseLen = 1000

func settleBase(t *testing.T) []tickflow.Bar {
	t.Helper()
	all := loadSinaDaily(t, "daily_RB0.jsonp")
	if len(all) < settleBaseLen {
		t.Fatalf("RB0 只有 %d 根，不够取 %d 根基线", len(all), settleBaseLen)
	}
	return all[len(all)-settleBaseLen:]
}

// errDegenerateStart：measureSettle 找到了逐位相等，但那是【开头价格重复】造成的，不是收敛。
//
// ⛔ 由来（docs/probe.md 6.33 读数第三节，评审方 2026-09-17 独立复现）：
// CN 口径用【首个收盘价】播种。若被丢掉的那一截收盘价全等于紧随其后那一根，
// 从第 1 根喂与从那一根喂的播种值相同，后面整条序列逐位相同 ⇒ 「逐位相等」成立，
// 而值其实仍取决于起点（再多丢一根，播种值就变了）。RB2412 前两根收盘 3889 / 3889 就是这个形状。
//
// ⇒ 判在量法里，不只在数据上断言：只断言「数据没有这个形状」，换一份数据就又漏了。
// ⚠️ 射程：只认【收盘价】重复（EMA / MACD / RSI 的输入）。读 High / Low 的指标（KDJ / CCI）若有别的退化形状，这里不判。
var errDegenerateStart = errors.New("退化：开头价格重复 —— 逐位相等是播种值碰巧不变，不是收敛")

// measureSettle 实测「预读多少根之后，末根的值与从头喂到底【逐位相等】」。
// 不用容差——容差要挑一个数，而挑多少本身就是要论证的东西。
//
//	(n, nil)                  真前缀里第一个逐位相等的预读根数（n < 根数）
//	(-1, nil)                 没有任何真前缀逐位相等 ⇒ 这份数据上没收敛
//	(n, errDegenerateStart)   找到的那个相等是开头价格重复造成的，**不许当收敛点用**
//
// 末根为 NaN（数据比 Warmup 还短）⇒ Fatal，**不折成 -1**：「数据不够长」与「没收敛」是两种状态。
func measureSettle(t *testing.T, mk func() Indicator, cs []tickflow.Bar, field int) (int, error) {
	t.Helper()
	full := Compute(mk(), cs)
	want := full[len(full)-1][field]
	if math.IsNaN(want) {
		t.Fatalf("基线数据不够长，末根仍是 NaN")
	}
	for pre := 1; pre < len(cs); pre++ {
		part := Compute(mk(), cs[len(cs)-pre:])
		if part[len(part)-1][field] != want {
			continue
		}
		dropped := len(cs) - pre
		repeated := true
		for _, c := range cs[:dropped] {
			if c.Close != cs[dropped].Close {
				repeated = false
				break
			}
		}
		if repeated {
			return pre, errDegenerateStart
		}
		return pre, nil
	}
	return -1, nil
}

// TestSettleCoversActualConvergence 是 Settler 的核心契约：
// **Settle() 报的根数必须真的够用。**
//
// 期望值不写死——当场实测出逐位收敛点再比。写死的话，改了 settleEps 或换了
// 基线数据，这条测试要么假绿要么假红。
func TestSettleCoversActualConvergence(t *testing.T) {
	cs := settleBase(t)

	// 【每一路输出都要验】。多输出指标的各路收敛速度未必相同——KDJ 的 J = 3K-2D
	// 会把 K 与 D 的残差放大，只验 .k 就漏掉了。（姊妹仓这条测试最初只验第一路，
	// 是下游报了 kdj.j 的数才补全的，一补就照出个真 bug。）
	for _, c := range []struct {
		name string
		mk   func() Indicator
	}{
		{"MA(20)", func() Indicator { return MA(20) }},
		{"BOLL(20,2)", func() Indicator { return BOLL(20, 2) }},
		{"CCI(20)", func() Indicator { return CCI(20) }},
		{"KDJ(9,3,3)/TV", func() Indicator { return KDJ(9, 3, 3, TV) }},
		{"KDJ(9,3,3)/CN", func() Indicator { return KDJ(9, 3, 3, CN) }},
		{"EMA(20)/TV", func() Indicator { return EMA(20, TV) }},
		{"EMA(20)/CN", func() Indicator { return EMA(20, CN) }},
		{"RSI(14)/TV", func() Indicator { return RSI(14, TV) }},
		{"RSI(14)/CN", func() Indicator { return RSI(14, CN) }},
		{"MACD/TV", func() Indicator { return MACD(12, 26, 9, TV) }},
		{"MACD/CN", func() Indicator { return MACD(12, 26, 9, CN) }},
	} {
		declared := tickflow.IndicatorSettle(c.mk())
		for field, key := range Keys(c.mk()) {
			t.Run(c.name+"/"+key, func(t *testing.T) {
				measured, err := measureSettle(t, c.mk, cs, field)
				if err != nil {
					t.Fatalf("基线上量出 %d 根，但 %v —— 换一段基线", measured, err)
				}
				if measured < 0 {
					t.Fatalf("%d 根之内没收敛，基线不够长", len(cs))
				}
				if declared < measured {
					t.Errorf("Settle() 报 %d，实测要 %d 根才逐位收敛——报少了 %d 根，"+
						"照它预热会预热不足", declared, measured, measured-declared)
				}
				// 也不该离谱地保守：多读几百根 K 线是要花时间和内存的。
				if declared > measured*3+80 {
					t.Errorf("Settle() 报 %d，实测只要 %d——过于保守了", declared, measured)
				}
				t.Logf("Warmup %d / Settle %d / 实测 %d", c.mk().Warmup(), declared, measured)
			})
		}
	}
}

// TestSingleContractDailyDoesNotSettle 钉住 v0.8 起手 甲（docs/probe.md 6.33）：
// 单个合约的整段日线短于递归类指标的收敛根数 ⇒ 值取决于从哪一根开始喂，不会收敛。
//
// ⚠️ 判的是「measureSettle 返回 -1 且不是退化」，不是「根数 < Settle()」：Settle() 是上界，
// 拿上界当门槛会留一段「已经收敛却仍小于 Settle()」的区间（评审方 2026-09-17 指出）。
//
// 对照两格，缺一格这条测试就没有判别力：
//
//	RB0 基线上同一批指标量得出数   ⇒ 量法没坏（坏了的量法也会处处返回 -1）
//	RB2501 上 KDJ/CN 量得出数      ⇒ 这份数据不是「短到什么都收敛不了」
func TestSingleContractDailyDoesNotSettle(t *testing.T) {
	contract := loadSinaDaily(t, "daily_RB2501.jsonp")
	base := settleBase(t)

	recursive := []struct {
		name string
		mk   func() Indicator
	}{
		{"EMA(20)/TV", func() Indicator { return EMA(20, TV) }},
		{"EMA(20)/CN", func() Indicator { return EMA(20, CN) }},
		{"RSI(14)/TV", func() Indicator { return RSI(14, TV) }},
		{"RSI(14)/CN", func() Indicator { return RSI(14, CN) }},
		{"MACD/TV", func() Indicator { return MACD(12, 26, 9, TV) }},
		{"MACD/CN", func() Indicator { return MACD(12, 26, 9, CN) }},
	}
	for _, c := range recursive {
		for field, key := range Keys(c.mk()) {
			got, err := measureSettle(t, c.mk, contract, field)
			if err != nil || got != -1 {
				t.Errorf("%s/%s 在 RB2501（%d 根）上量出 (%d, %v)，期望 (-1, nil) —— 单合约日线上不该收敛",
					c.name, key, len(contract), got, err)
			}
			ctl, err := measureSettle(t, c.mk, base, field)
			if err != nil || ctl < 0 {
				t.Errorf("对照失败：%s/%s 在 RB0 基线上量出 (%d, %v) —— 量法坏了也会处处返回 -1",
					c.name, key, ctl, err)
			}
		}
	}
	kdj := func() Indicator { return KDJ(9, 3, 3, CN) }
	for field, key := range Keys(kdj()) {
		if got, err := measureSettle(t, kdj, contract, field); err != nil || got < 0 {
			t.Errorf("对照失败：KDJ/CN/%s 在 RB2501 上量出 (%d, %v) —— 这份数据短到连 KDJ 都收敛不了，上面的 -1 就没有判别力",
				key, got, err)
		}
	}
}

// TestMeasureSettleFlagsRepeatedStart 钉住 errDegenerateStart 那一格。
//
// 合成的一对只差「第二根收盘」一个数：相同 ⇒ 必须报退化，不许报成收敛点；不同 ⇒ 没收敛（-1）。
// 真实的一格：RB2412（前两根收盘 3889 / 3889）⇒ EMA/CN 与 MACD/CN 三路报退化，RSI/CN 仍是 -1（它按首个涨跌差播种）。
func TestMeasureSettleFlagsRepeatedStart(t *testing.T) {
	ema := func() Indicator { return EMA(20, CN) }

	same := randomWalk(238, 20260917)
	same[1].Close = same[0].Close
	if got, err := measureSettle(t, ema, same, 0); !errors.Is(err, errDegenerateStart) {
		t.Errorf("首两价相同：量出 (%d, %v)，期望报退化 —— 否则会把开头重复当成收敛", got, err)
	}

	diff := randomWalk(238, 20260917)
	diff[1].Close = diff[0].Close + 1
	if got, err := measureSettle(t, ema, diff, 0); err != nil || got != -1 {
		t.Errorf("首两价不同（对照）：量出 (%d, %v)，期望 (-1, nil)", got, err)
	}

	rb2412 := loadSinaDaily(t, "daily_RB2412.jsonp")
	if rb2412[0].Close != rb2412[1].Close {
		t.Fatalf("前提不成立：RB2412 前两根收盘 %v / %v 不相同", rb2412[0].Close, rb2412[1].Close)
	}
	if _, err := measureSettle(t, ema, rb2412, 0); !errors.Is(err, errDegenerateStart) {
		t.Errorf("RB2412 EMA/CN：期望报退化，得到 %v", err)
	}
	macd := func() Indicator { return MACD(12, 26, 9, CN) }
	for field, key := range Keys(macd()) {
		if _, err := measureSettle(t, macd, rb2412, field); !errors.Is(err, errDegenerateStart) {
			t.Errorf("RB2412 MACD/CN/%s：期望报退化，得到 %v", key, err)
		}
	}
	rsi := func() Indicator { return RSI(14, CN) }
	if got, err := measureSettle(t, rsi, rb2412, 0); err != nil || got != -1 {
		t.Errorf("RB2412 RSI/CN：量出 (%d, %v)，期望 (-1, nil) —— 它按首个涨跌差播种，不受首两价相同影响", got, err)
	}
}

// TestWindowedAreBitReproducibleAcrossStarts：窗口类指标在数学上只依赖窗口里那
// n 根，那么从任何位置开始喂，只要窗口填满了，同一根上的值就该【逐位相同】。
//
// 这条在姊妹仓一度不成立：window 的统计量按【物理顺序】遍历环形缓冲，而环的旋转位置
// 取决于已经推入了多少根，于是不同起点的累加顺序不同，浮点结果差最后一位。
// 值只差 1 ULP，却足以在恰好相等的比较上翻面——而且完全不报错。
func TestWindowedAreBitReproducibleAcrossStarts(t *testing.T) {
	cs := settleBase(t)

	for _, c := range []struct {
		name string
		mk   func() Indicator
	}{
		{"MA(20)", func() Indicator { return MA(20) }},
		{"BOLL(20,2)", func() Indicator { return BOLL(20, 2) }},
		{"CCI(20)", func() Indicator { return CCI(20) }},
		{"KDJ(9,3,3)/TV", func() Indicator { return KDJ(9, 3, 3, TV) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			warm := tickflow.IndicatorSettle(c.mk())
			full := Compute(c.mk(), cs)

			// 从若干个不同的起点各喂一遍，比较共同覆盖的那些根。
			// 起点特意取互质的间隔，让环形缓冲的旋转位置各不相同。
			for _, start := range []int{7, 23, 51, 100, 137} {
				part := Compute(c.mk(), cs[start:])
				for i := range part {
					abs := start + i
					if i+1 < warm { // 窗口还没填满，不比
						continue
					}
					for k := range part[i] {
						a, b := full[abs][k], part[i][k]
						if a != b {
							t.Fatalf("起点 %d：第 %d 根第 %d 路 %v != %v（差 %g）——"+
								"窗口类指标的值不该取决于从哪根开始喂",
								start, abs, k, a, b, a-b)
						}
					}
				}
			}
		})
	}
}

// TestWindowedIndicatorsSettleAtWarmup：窗口类指标两者必须相等。
// 窗口滑过去就与更早的数据无关，多报一根都是白读。
func TestWindowedIndicatorsSettleAtWarmup(t *testing.T) {
	for _, ind := range []Indicator{
		MA(20), BOLL(20, 2), CCI(20), KDJ(9, 3, 3, TV),
	} {
		if got, want := tickflow.IndicatorSettle(ind), ind.Warmup(); got != want {
			t.Errorf("%s 的 Settle() = %d，窗口类指标应当等于 Warmup() = %d",
				ind.Name(), got, want)
		}
	}
}

// TestOnlyWarmupIsBadlyInsufficient 把「为什么要有 Settler」量成一个数。
//
// 没有这一条，上面那些测试只说明「Settle() 够用」，说明不了「不用它会怎样」。
func TestOnlyWarmupIsBadlyInsufficient(t *testing.T) {
	cs := settleBase(t)
	mk := func() Indicator { return MACD(12, 26, 9, CN) }

	full := Compute(mk(), cs)
	want := full[len(full)-1][0]

	// 按 Warmup 预热（再乘姊妹仓 Feed 的 slack=2）能拿到的根数。
	warm := mk().Warmup() * 2
	part := Compute(mk(), cs[len(cs)-warm:])
	got := part[len(part)-1][0]
	rel := math.Abs(got-want) / math.Abs(want)

	if rel < 0.5 {
		t.Fatalf("只预读 %d 根时相对误差只有 %.2e——这个测试的前提不成立了，"+
			"要么 Warmup 的语义变了，要么基线数据变得太平缓", warm, rel)
	}
	t.Logf("只按 Warmup 预读 %d 根：MACD.dif = %.4f，收敛值 %.4f，相对误差 %.2f",
		warm, got, want, rel)

	// 按 Settle 预热则应当已经逐位收敛。
	settle := tickflow.IndicatorSettle(mk())
	if settle >= len(cs) {
		t.Skipf("基线只有 %d 根，装不下 Settle() 要的 %d 根", len(cs), settle)
	}
	part = Compute(mk(), cs[len(cs)-settle:])
	if part[len(part)-1][0] != want {
		t.Errorf("按 Settle() 预读 %d 根仍未逐位收敛", settle)
	}
}
