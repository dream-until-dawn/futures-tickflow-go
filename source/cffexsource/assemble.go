package cffexsource

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 这一片是【组装】：一天的 XML 行 ＋ 那一天的日历 → 一根 tickflow.Bar。
//
// ⛔ **本源与 sinasource 的形状是反的，这决定了接口长什么样：**
//
//	sinasource   一次请求 = 【一个合约】的【全部历史】 ⇒ 一次就覆盖整个区间
//	cffexsource  一次请求 = 【一天】的【全部合约】     ⇒ 要 N 天就得请求 N 次
//
// ⇒ 所以这里的单位是**一天一根**；把区间铺开成一串交易日是拉取层的活，
// 而铺开它的正确工具是 `Calendar.Walk`（它对区间落在覆盖之外会当场炸，不静默少遍历）。

// ErrNotCFFEX 这个源只覆盖中金所。
//
// ⛔ 单列出来不是洁癖，它**支撑着下面那个 Instrument() 的用法**：
//
//	CFFEX  Instrument() 给 "IC2609" —— **正是 XML 里那个形态** ✅
//	CZCE   Instrument() 给 "TA701"  —— 三位年月，别处会出事（sinasource 就栽过）
//
// ⇒ 因为本源只接受 CFFEX，`Instrument()` 在这里才是安全的。
// **把这条守住，那个用法就不会哪天被一个非中金所的 Symbol 悄悄带偏。**
var ErrNotCFFEX = errors.New("cffexsource: 这个源只覆盖中金所（CFFEX）")

// ErrTradingDayMismatch XML 自报的交易日与日历给的那一天不一致。
//
// ⚠️ 这份 XML **自带 `<tradingday>`** —— 那是 sinasource 没有的一手证据。
// 两边对不上时，可能是取错了日期的文件，也可能是日历注入错了；
// **两种都要人看，所以不吞。**
var ErrTradingDayMismatch = errors.New("cffexsource: XML 自报的交易日与日历给的那一天不一致")

// ErrTradingDayFormat 上游那一侧的交易日不是 8 位纯数字。
//
// ⛔ 与 ErrTradingDayMismatch **分开**，因为处置不同：
// 不一致 ⇒ 可能取错了日期的文件，或者日历注入错了；
// **格式变了 ⇒ 是上游改了，而那不是「哪一天」的问题。**
// ⇒ 合成一个的话，错误消息会把读的人指向错误的方向（同⑱ 那一格的教训）。
var ErrTradingDayFormat = errors.New("cffexsource: 上游自报的交易日不是 8 位纯数字")

// AssembleDay 把一天的行配上那一天的日历，组装出【一根】日线。
//
// found 为 false 表示**那一天的文件里没有这个合约**。它可能是：
// 合约还没上市、已经到期、或者那天它确实没有数据。
//
//	⛔ **本层分不出这三者** —— 与 sinasource 那边的「丙格」同族（登记⑨）：
//	  「日历说是交易日，而数据里没有这一行」在输出上是一个【缺席】，
//	  而缺席也可能是别的原因。**处置在 Syncer 那一层，本层只如实报 found=false。**
//
//	⛔ **登记㉒（评审方补的，比只写「三合一」有用）：这三者【不对称】。**
//
//	  未上市    ⇒ 上市日之前**永远不该重试**   ← 可由合约的上市日推出来
//	  已到期    ⇒ 到期日之后**永远不该重试**   ← 可由到期日推出来
//	  那天真没有 ⇒ **可能该重试**              ← **只有这一个是真正不可知的**
//
//	⇒ 所以 Syncer 那一片要做的**不是「把三者分开」**，是
//	  **「用上市/到期日把前两者减掉，剩下的才交给⑨ 的按日遍历」**。
//	  ⛔ 【2026-09-10 更正】此处原来把【两个日期】都指向了那一层 ——
//	  **到期日成立，上市日不成立**：全量实测天勤 openmd 的 47 个字段里没有上市日
//	  （docs/probe.md「复核 v5」那一节）。替代出处也在那一节（中金所 XML 逐日列合约，
//	  带对照组），**而它的二分前提未验，所以只写在探针文档里、不进任何接口。**
//
// now 用来判完结，**由调用方给，不在内部取 time.Now()**（同 CheckBars 那条理由）。
func AssembleDay(rows []SettleRow, day tickflow.Day, sym tickflow.Symbol, now int64) (tickflow.Bar, bool, error) {
	var zero tickflow.Bar
	if sym.Exchange != tickflow.CFFEX {
		return zero, false, fmt.Errorf("%w：收到 %s", ErrNotCFFEX, sym)
	}
	if sym.Product == "" || sym.YearMon == 0 {
		return zero, false, fmt.Errorf("cffexsource: Symbol 不完整（%+v）——"+
			"主力连续在这里同样不成立：这份 XML 里只有具体合约", sym)
	}
	if !day.Num.Valid() {
		return zero, false, fmt.Errorf("cffexsource: 交易日 %d 不合法", int32(day.Num))
	}
	if len(day.Sessions) == 0 {
		return zero, false, fmt.Errorf("cffexsource: %s 那天一个时段都没有——"+
			"Ts/TsEnd 无从取值，而留零值上层只能猜", day.Num)
	}

	// CFFEX 上 Instrument() 就是 XML 里那个形态（由 ErrNotCFFEX 守着这个前提）。
	want := sym.Instrument()
	var row *SettleRow
	for i := range rows {
		if rows[i].InstrumentID == want {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return zero, false, nil // 缺席：见函数注释，本层不判它是哪一种
	}

	// 一手交叉核对：XML 自报的交易日 vs 日历给的那一天。
	//
	// ⛔ **两侧【不是同源】**：右边是本仓的 TradingDay，
	// **左边是中金所给的**（fixture 只是它的一份快照，线上还会再拉）。
	// ⇒ 所以这里不做任何「归一化」——**归一化会把上游的变化一并抹掉，
	// 而上游变了正是这道检查要报的事**。
	//
	// 上一版用一个「剥掉所有非数字字符」的比较器，实测它把
	// `2026-09-08 (revised)` 与 `x2026y09z08` 都读成 `20260908` ——
	// **检查比它的用途宽**（评审方 2026-09-09 指出，我复现一致）。
	//
	// ⛔ **登记㉓：这里【不能】写成 `if row.TradingDay != "" { ... }`。**
	// 上一版就是那样，而那个 `!= ""` 是**这道检查的关闭开关，且开关在上游手里**：
	//
	//	实测：TradingDay="" ⇒ **found=true, err=nil，Bar 照样产出** —— 检查整个不做
	//
	// ⚠️ 而同一段注释写着「上游变了正是这道检查要报的事」——
	// **上游把这一格删掉，也是「上游变了」。**
	// ⇒ 空串走 isEightDigits 会失败 ⇒ 落到 ErrTradingDayFormat，这是对的：
	// **少一格和改一格，都该有人看一眼。**
	//
	// （评审方 2026-09-09 发现。他同时点出我上一版为什么会漏：
	// **我量的是这道检查【判得对不对】，没量它【会不会不判】** ——
	// 而两者的输出都是 err == nil。⇒ **看一道检查，先找它的关闭条件，再看它的判断逻辑。**）
	if !isEightDigits(row.TradingDay) {
		return zero, false, fmt.Errorf("%w：%s 自报 %q——"+
			"本仓 fixture 714 条皆 8 位，而那是 1 天快照——独立观测数是 1，不是 714；"+
			"少一格或换个格式都要人看一眼",
			ErrTradingDayFormat, want, row.TradingDay)
	}
	if row.TradingDay != strconv.Itoa(int(day.Num)) {
		return zero, false, fmt.Errorf("%w：%s 自报 %q，而日历给的是 %s",
			ErrTradingDayMismatch, want, row.TradingDay, day.Num)
	}

	ts := day.Sessions[0].Start
	tsEnd := day.Sessions[len(day.Sessions)-1].End
	if tsEnd > now {
		return zero, false, nil // 未完结：整根丢掉，未完结的绝不进入本库任何一层
	}

	return tickflow.Bar{
		Ts:           ts,
		TsEnd:        tsEnd,
		TradingDay:   day.Num,
		Open:         row.Open,
		High:         row.High,
		Low:          row.Low,
		Close:        row.Close,
		Volume:       row.Volume,
		Turnover:     row.Turnover,
		OpenInterest: row.OpenInterest,
		Settle:       settleOrNaN(row.Settle),
		Flags:        tickflow.FlagSrcExchange,
		// ⛔ row.PreSettle（昨结算）**到这里被丢掉** —— tickflow.Bar 里没有这一格，
		// 而下游正是拿它推涨跌停。登记㉑：要用它得先改 Bar，那是公开类型的变更。
		// （记录同时在 TestPreSettleIsDroppedOnPurpose；写在这儿是因为**丢弃发生在这一行**。）
	}, true, nil
}

// settleOrNaN 把 0 映射成 NaN。
//
// ⛔ **这个判断在这一层做，而不是在 parse.go** —— 那里明确写着不做，理由是分层：
// 解析层只报告「XML 里写的是什么」，而**「0 该不该当成缺失」需要语境**。
// 这里就是那个语境：`bar.go` 说得很清楚 ——
// **没有任何品种的结算价会是零，0 只可能是缺失的伪装。**
//
// ⚠️ 而本层**仍然分不出「XML 里写了 0」与「XML 里那一格是空白」**（登记⑳）：
// 解析层已经把两者抹成同一个 0 了。**这一层只是给这个 0 一个正确的下游语义，
// 而不是把已经丢掉的信息找回来。**
func settleOrNaN(v float64) float64 {
	if v == 0 {
		return math.NaN()
	}
	return v
}

// isEightDigits 判据写死成「恰好 8 位、全是数字」。
//
// ⚠️ **不放宽**：放宽等于替上游的格式变化做决定，而那正是上面那道检查要报的事。
func isEightDigits(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < 8; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
