package continuous

import (
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 内置换月规则 ——
//
// ⛔ 四条都**只看递进来的候选**：谁的量大、谁的持仓大、离到期还有几天。
// 它们一概不问日历、不去取数 —— 那正是本包那条硬约束（见包注释与 noprovider_test.go）。

// ByOpenInterest 选持仓量最大的那个合约。
type ByOpenInterest struct{}

// Pick 实现 RollRule。
func (ByOpenInterest) Pick(_ tickflow.TradingDay, cands []ContractDay) (tickflow.Symbol, bool) {
	return maxBy(cands, func(c ContractDay) float64 { return c.OpenInterest })
}

// ByVolume 选成交量最大的那个合约。
type ByVolume struct{}

// Pick 实现 RollRule。
func (ByVolume) Pick(_ tickflow.TradingDay, cands []ContractDay) (tickflow.Symbol, bool) {
	return maxBy(cands, func(c ContractDay) float64 { return c.Volume })
}

// ByOIAndVolume 只在**两者都最大**时才认它是主力；否则说「这一天没有主力」。
//
// ⚠️ 它的「没有」不是错误，是一个答案：调用方拿到的那一天不产出根，而天轴上仍有这一天。
// ⛔ 而这正是它与前两条的差别 —— **换月点少、且不会在两个合约之间来回跳**，代价是缺口更多。
type ByOIAndVolume struct{}

// Pick 实现 RollRule。
func (ByOIAndVolume) Pick(_ tickflow.TradingDay, cands []ContractDay) (tickflow.Symbol, bool) {
	oi, ok1 := maxBy(cands, func(c ContractDay) float64 { return c.OpenInterest })
	vol, ok2 := maxBy(cands, func(c ContractDay) float64 { return c.Volume })
	if !ok1 || !ok2 || oi != vol {
		return tickflow.Symbol{}, false
	}
	return oi, true
}

// maxBy 取 f 最大的那个合约。并列时取**先出现**的那个 —— 顺序由调用方给，本包不替它定。
func maxBy(cands []ContractDay, f func(ContractDay) float64) (tickflow.Symbol, bool) {
	var best tickflow.Symbol
	var bestV float64
	found := false
	for _, c := range cands {
		if v := f(c); !found || v > bestV {
			best, bestV, found = c.Symbol, v, true
		}
	}
	return best, found
}

// FixedBarDaysBeforeExpiry 在「离到期还有 n 个**库里有根的日子**」时换到下一个合约。
//
// ⛔ **名字里是 BarDays 不是 Days，而那不是拗口**：本包的天轴是「库里实际有根的交易日」，
// 它数的也是那条轴上的日子。库里缺一天 ⇒ 它与「日历交易日」给出**不同的换月日** ——
// 处置分岔 ⇒ 不该共用「交易日」这个名字（design.md §八 第二条不同）。
//
// ⛔ **它要整条天轴，而天轴由 Build 递给它**（`SetAxis`）——
// 🔴 第一版我让它「只累积见过的天」，而「离到期还有几天」问的是**未来**：
// 见过的天里永远没有今天之后的日子 ⇒ 那个数恒为 0 ⇒ 每个合约都被判成「快到期」⇒ 一根都拼不出来。
// **这是写测试时当场撞出来的**，不是想出来的：`TestCountedSpanReachesTheRoll` 第一版报「接缝 0 个」。
// ⇒ 记下来：**一个只看过去的状态机，回答不了一个关于未来的问题** —— 而它不会报错，它会给出一个小的数。
//
// ⚠️ 天轴是**数据**不是提供者：Build 把它已经有的那条轴递过来，规则不去任何地方取数（包注释那条约束仍然成立）。
type FixedBarDaysBeforeExpiry struct {
	// N 是「离到期还有几个有根的日子就换」。
	N int

	axis    []tickflow.TradingDay
	counted CountedSpan
}

// SetAxis 收下本次拼接的天轴（升序）。Build 在开始时调它。
//
// ⚠️ 调用方自己用这条规则时也要先调它 —— 没有天轴，`Pick` 一个候选都留不下（见下）。
func (r *FixedBarDaysBeforeExpiry) SetAxis(days []tickflow.TradingDay) { r.axis = days }

// Pick 实现 RollRule：把「离到期只剩 ≤ N 个有根日子」的合约排除，在剩下的里选持仓最大的。
//
// ⛔ 候选里任何一个的 Expiry 是 `ExpiryUnknown` ⇒ **它判不了这一天**：返回「没有主力」并把
// `Counted` 置空。要区分「真没有主力」与「判不了」，走 `PickErr`。
//
// ⚠️ 这是接口形状带来的妥协，写明白：`RollRule.Pick` 没有 error 返回值，而
// 「不知道到期日」**不该被当成「这个合约不参与换月」**（那是把未知升级成一个肯定的答案）。
func (r *FixedBarDaysBeforeExpiry) Pick(day tickflow.TradingDay, cands []ContractDay) (tickflow.Symbol, bool) {
	sym, ok, err := r.PickErr(day, cands)
	if err != nil {
		return tickflow.Symbol{}, false
	}
	return sym, ok
}

// PickErr 与 Pick 同，而**把「判不了」与「没有主力」分开**：
// 前者返回 error（`ErrExpiryUnknown`），后者返回 ok=false。
func (r *FixedBarDaysBeforeExpiry) PickErr(day tickflow.TradingDay, cands []ContractDay) (tickflow.Symbol, bool, error) {
	for _, c := range cands {
		if c.Expiry == ExpiryUnknown {
			r.counted = CountedSpan{}
			return tickflow.Symbol{}, false, ErrExpiryUnknown
		}
	}
	var live []ContractDay
	for _, c := range cands {
		if _, n := r.remaining(day, c.Expiry); n > r.N {
			live = append(live, c)
		}
	}
	sym, ok := maxBy(live, func(c ContractDay) float64 { return c.OpenInterest })
	if !ok {
		r.counted = CountedSpan{}
		return tickflow.Symbol{}, false, nil
	}
	for _, c := range cands {
		if c.Symbol == sym {
			span, _ := r.remaining(day, c.Expiry)
			r.counted = span
			break
		}
	}
	return sym, true, nil
}

// Counted 实现 CountingRule：最近一次 Pick **为选中的那个合约**数到的那一段。
func (r *FixedBarDaysBeforeExpiry) Counted() CountedSpan { return r.counted }

// remaining 数「天轴上落在 (day, expiry] 里的日子」，并交出那一段本身。
//
// ⚠️ 天轴的**右端取决于库拉到哪里**：库只拉到 expiry 之前 ⇒ 这个数偏小 ⇒ **更早换月**。
// 方向是「早换」不是「漏换」，而它**不会报错** —— 所以那一段要交出去（`CountedSpan`），让下游看得见。
func (r *FixedBarDaysBeforeExpiry) remaining(day, expiry tickflow.TradingDay) (CountedSpan, int) {
	var s CountedSpan
	for _, d := range r.axis {
		if d <= day || d > expiry {
			continue
		}
		if s.Days == 0 {
			s.From = d
		}
		s.To = d
		s.Days++
	}
	return s, s.Days
}
