package tickflow

// Indicator 是一个流式技术指标。
//
// 接口定义在根包，而实现（MA / EMA / MACD / KDJ / RSI / CCI / BOLL）在子包
// indicator 里。这么分是因为 Feed（v0.9）要在根包消费指标，而 indicator 包要用 Bar——
// 定义放在任何一边都会形成循环引用。indicator.Indicator 是本类型的别名，
// 两边写哪个都一样。
//
// 自姊妹项目 okx-tickflow-go v1.4.2 移植，签名只差一处：Update 吃 Bar，不吃 Candle
// （用户 2026-09-17 定：自定义指标由此能读 Bar.OpenInterest；内置七个只读 High / Low / Close）。
//
// 外部实现只要满足这个接口，就能和内置指标一样被消费（indicator.Compute 今天就能用；Feed 在 v0.9）。
type Indicator interface {
	// Name 是指标在视图里的键名，如 "ma20"、"macd"。
	Name() string

	// Fields 是多输出指标的字段名，如 MACD 的 ["dif","dea","hist"]。
	// 单输出指标返回 nil。
	Fields() []string

	// Warmup 是【出第一个有效值】所需的 K 线根数。
	//
	// 含义是「值从此有定义」，**不是**「值已收敛」。这两件事对窗口类指标是一回事，
	// 对递归类（EMA / MACD / RSI）能差两个数量级：MACD(12,26,9) 国内口径的
	// Warmup() 报 1，而只喂 4 根时 dif 的相对误差是 **1.01**——值错了一倍（姊妹仓在 ETH 日线上的实测）。
	//
	// 要「已收敛」那个数，用 Settler / IndicatorSettle。v0.9 的 Feed 自动预热与
	// View.Ready 按后者判。
	Warmup() int

	// Update 喂入一根【已完结】的 K 线，返回本根对应的指标值。
	//
	// 尚未 warmup 完时返回 NaN——不是 nil，也不是 0。返回的切片由指标复用，
	// 调用方不得把它留到下一次 Update 之后。
	//
	// 返回的长度必须恒等于 IndicatorKeys 的长度，也就是
	// max(1, len(Fields()))。（v0.9 的 Feed 会在第一次推进时校验这一条；本版没有消费方校验它。）
	Update(b Bar) []float64

	// Reset 清空全部内部状态，回到刚构造出来的样子。
	Reset()
}

// Settler 是 Indicator 的【可选】扩展：报告值需要多少根才【收敛】。
//
// Warmup 报的是「值从此有定义」，Settle 报的是「值与从哪根开始喂基本无关」。
// 对窗口类指标两者相同，且是逐位的；对递归类（EMA / MACD / RSI / 国内口径的 KDJ）相差一到两个数量级。
//
// ⛔ 契约（docs/probe.md 6.34；与姊妹仓不同）：Settle() 是**按递推系数**算出的、播种残差相对衰减到
// settleEps（1e-15）以下所需的根数。**它不保证逐位相等**：只预读 Settle() 根时，末根与从头喂到底
// 相差几个 ULP 量级（RB0 上实测最大相对误差 8.0e-16，测试断言 ≤ 1e-14）。
// 逐位稳定点跟着数据的数值与舍入走，任何只由系数算出的数都封不住它 —— 下表 RSI 那两行就是：
//
//	指标                 Warmup()  Settle()   ETH 日线 500 根（首个逐位相等）   RB0 末 1000 根（逐位稳定点）
//	MA(20) / BOLL / CCI       20       20        20                               20
//	KDJ(9,3,3) 国内口径         9      183        95 / 95 / 84                     101 / 108 / 108
//	EMA(20) TV / 国内口径     20 / 1  366 / 347   320 / 337                        325 / 324
//	RSI(14) TV / 国内口径     15 / 2  482 / 469   452 / 458                        508 / 525  ← 稳定点 > Settle()
//	MACD(12,26,9) TV          34      639        442 / 470 / 470                  439 / 480 / 480
//	MACD(12,26,9) 国内口径      1      606        421 / 428 / 428                  433 / 479 / 479
//	（多输出按 dif/dea/hist、k/d/j 分路列。ETH 一列是姊妹仓的量法「首个逐位相等」，它不单调、会低估；
//	 RB0 一列是「从此一直逐位相等」的稳定点，来自 6.34）
//
// 差距有多要紧：只预读 4 根时 MACD 的 dif 相对误差是 **1.01**——值错了一倍，
// 而 Warmup() 说它「有定义」。预读 150 根降到 1.8e-6，300 根降到 3.3e-10（姊妹仓 ETH 实测）。
//
// ⛔ 期货合约只活约一年：rb 单个合约日线 236–243 根（6.33）⇒ 在单个合约的整段日线上，
// 递归类指标的值取决于从哪一根开始喂，不会收敛。要收敛的值，喂主连。
//
// 对预热（v0.9）：几个 ULP 的差对任何用途都无意义 ⇒ 照 Settle() 读就够，不必为逐位相等多读。
//
// v0.9 的 Feed 用它决定自动预热要往前多读多少，也用它判断 View.Ready。不实现这个接口
// 的指标退回用 Warmup()——对窗口类指标那是对的；写递归类指标时请实现它。
type Settler interface {
	// Settle 返回值收敛所需的根数。应当 >= Warmup()。
	Settle() int
}

// IndicatorSettle 返回指标的收敛根数：实现了 Settler 就用它，否则退回 Warmup()。
func IndicatorSettle(ind Indicator) int {
	if s, ok := ind.(Settler); ok {
		if n := s.Settle(); n > ind.Warmup() {
			return n
		}
	}
	return ind.Warmup()
}

// IndicatorKeys 返回指标在视图里的全部键名。
//
// 单输出为 Name()，多输出为 Name()+"."+字段名，如 "macd.dif"。
func IndicatorKeys(ind Indicator) []string {
	f := ind.Fields()
	if len(f) == 0 {
		return []string{ind.Name()}
	}
	out := make([]string, len(f))
	for i, k := range f {
		out[i] = ind.Name() + "." + k
	}
	return out
}
