package shinnyref

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 哨兵 ——
//
// ⚠️ 故意【不导出】：本包对外的失败分类还没有进 docs/contract.md，
// 而本仓的规矩是文档先行。测试与本包同包，用得到它们。
//
// ⛔ 而「不导出」今天有一个后果，写成**当场可求值**的到期条件（评审方 2026-09-10 提）：
//
//	包外调用方【没法 errors.Is 分类】⇒ 「调用方自己按错误分流」这句话**今天只对包内成立**
//	⇒ 到期条件：**在出现第一个包外调用方之前**
//	   求值法：`grep -rl 'refdata/shinnyref' --include=*.go .`
//	   数一数有没有本包之外的包 —— **今天 0**
var (
	errNotFuture   = errors.New("shinnyref: 这一条不是期货合约（class 不是 FUTURE）")
	errMissing     = errors.New("shinnyref: 缺字段——报错，不补")
	errBadOffset   = errors.New("shinnyref: 时段端点解不出来")
	errBadSessions = errors.New("shinnyref: trading_time 的形状不认识")
)

// ClassFuture 是上游给期货合约的 class 值。
//
// ⚠️ 上游全量里有 **8 个 class**（FUTURE_OPTION 199023 · FUTURE_COMBINE 22483 ·
// OPTION 10242 · **FUTURE 7671** · FUTURE_INDEX 88 · FUTURE_CONT 88 · SPOT 21 · INDEX 4）——
// 本包只收 FUTURE 这一种，其余**明确拒绝而不是静默跳过**：
// 一个「跳过了多少条」的计数，读的人分不出「上游没有」和「我漏收了」。
const ClassFuture = "FUTURE"

// entry 是上游一条记录里【本包用得到】的那些字段。
//
// ⛔ 用 `*T` 的那几个是为了把「没写」与「写了零值」分开 —— 本仓在 `.meta` 的
// `format` 上记过同一格（A3）：用值类型的话，`0` 既是合法取值又是缺失，而两者处置不同。
// ⚠️ 而全量实测这四个字段 **7671/7671 恒在、零值 0 条** ——
// 也就是说这一层今天【不会触发】。留着它是因为：**那是今天的读数，不是契约**。
type entry struct {
	Class          string           `json:"class"`
	ExchangeID     *string          `json:"exchange_id"`
	ProductID      *string          `json:"product_id"`
	DeliveryYear   *int             `json:"delivery_year"`
	DeliveryMonth  *int             `json:"delivery_month"`
	VolumeMultiple *int             `json:"volume_multiple"`
	PriceTick      *float64         `json:"price_tick"`
	ExpireDatetime *float64         `json:"expire_datetime"`
	TradingTime    *json.RawMessage `json:"trading_time"`
}

// DecodeContract 把上游一条记录解成 Contract。
//
// ⛔ **不做任何补全**：缺一个字段就报错。本仓在 refdata 那一节记过这条的由来 ——
// 「真正的静默丢发生在【自作聪明的修补】上，不在解析器上」。
func DecodeContract(raw []byte) (Contract, error) {
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Contract{}, fmt.Errorf("shinnyref: 这一条读不懂: %w", err)
	}
	if e.Class != ClassFuture {
		return Contract{}, fmt.Errorf("%w: class=%q", errNotFuture, e.Class)
	}
	// ⛔ 用**有序的 slice**，不用 map ——
	// 第一版用 map 遍历，而 Go 的 map 顺序是随机的：
	// 同一份输入（`{"class":"FUTURE"}`）跑 40 次 ⇒ **8 种报文**（评审方量的，我复现）。
	// 🔴 它不产生错答案（每一条都为真），**而它让同一个失败不可复现** ——
	// 而本仓把读数当证据用，一个报文随机的错误贴进信里时**对不上账**。
	// ⚠️ 而我原来的测试是「逐个改键名」所以是确定的 ——
	// **确定性来自测试的构造，不来自被测方**（这一句是这一格真正的教训）。
	//
	// ⇒ 而这里更进一步：**一次报出【全部】缺的**，不是报第一个。
	// 报第一个的话，调用方要试 N 次才知道缺了 N 个。
	var missing []string
	for _, f := range []struct {
		name string
		ok   bool
	}{
		{"exchange_id", e.ExchangeID != nil},
		{"product_id", e.ProductID != nil},
		{"delivery_year", e.DeliveryYear != nil},
		{"delivery_month", e.DeliveryMonth != nil},
		{"volume_multiple", e.VolumeMultiple != nil},
		{"price_tick", e.PriceTick != nil},
		{"expire_datetime", e.ExpireDatetime != nil},
		{"trading_time", e.TradingTime != nil},
	} {
		if !f.ok {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return Contract{}, fmt.Errorf("%w: %s", errMissing, strings.Join(missing, " "))
	}

	sym, err := symbolOf(*e.ExchangeID, *e.ProductID, *e.DeliveryYear, *e.DeliveryMonth)
	if err != nil {
		return Contract{}, err
	}
	ses, err := decodeSessions(*e.TradingTime)
	if err != nil {
		return Contract{}, err
	}
	return Contract{
		Symbol:     sym,
		Multiplier: *e.VolumeMultiple,
		PriceTick:  *e.PriceTick,
		// 上游给的是 unix【秒】（带小数）；本仓的时刻单位是毫秒（同 Bar.Ts）。
		ExpireTs: int64(*e.ExpireDatetime * 1000),
		Sessions: ses,
	}, nil
}

// symbolOf 从【交割年月】拼出合约，**不从合约代码里解**。
//
// 🔴 理由是一条会咬人的歧义：郑商所的线格式是**三位**年月（`ZC002`），
// 而三位跨十年会撞 —— 本仓在 `ProductKey` 那儿就写着：
// 「refdata（合约规格，按合约，**键里必须含四位年月否则跨十年会撞**）」。
// ⇒ 而上游**自己带 delivery_year / delivery_month** ⇒ **不必猜年代**，
// 也就不必给 `ParseNative` 编一个 `asOf`。
func symbolOf(exch, prod string, year, month int) (tickflow.Symbol, error) {
	if month < 1 || month > 12 {
		return tickflow.Symbol{}, fmt.Errorf("shinnyref: delivery_month=%d 不是 1..12", month)
	}
	// ⛔ 上界下界都卡在 **2000..2099**，而这不是「像不像一个年份」——
	// 是 **`tickflow.Symbol` 只表达得了这一段**：它只带两位年，
	// `Symbol.Expiry()` ＝ `2000 + YearMon/100` ⇒ 出了这一段就**静默折叠**。
	//
	// 实测（评审方 2026-09-10 构造，我逐格复现，读数逐位一致）：
	//
	//	1999-12 ⇒ Expiry() 给 **2099-12**   1990-01 ⇒ **2090-01**
	//	2100-01 ⇒ **2000-01**              2101-03 ⇒ **2001-03**   2999-12 ⇒ **2099-12**
	//
	// 🔴 五个值全部**被放行且产生静默错值** —— 而 `newSymbol` 也接不住（它只校月份）。
	// ⚠️ 可达性今天是 **0**（上游的 delivery_year 都在 2015–2035）——
	// **而判它必改的是【方向】不是可达性**：它落在「静默」那一侧。
	if year < 2000 || year > 2099 {
		return tickflow.Symbol{}, fmt.Errorf(
			"shinnyref: delivery_year=%d 超出 2000..2099 —— "+
				"本仓的 tickflow.Symbol 只带两位年（Expiry() ＝ 2000 + YearMon/100），"+
				"1999 会被折成 2099、2100 会被折成 2000 ⇒ 这里拒绝，不折", year)
	}
	return tickflow.ParseSymbol(fmt.Sprintf("%s.%s%02d%02d", exch, prod, year%100, month))
}

// decodeSessions 解 trading_time，**保住 night 的三态**。
func decodeSessions(raw json.RawMessage) (Sessions, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return Sessions{}, fmt.Errorf("%w: %v", errBadSessions, err)
	}
	dayRaw, ok := m["day"]
	if !ok {
		return Sessions{}, fmt.Errorf("%w: 没有 day", errBadSessions)
	}
	day, err := decodeRanges(dayRaw)
	if err != nil {
		return Sessions{}, err
	}

	// ⛔ 三态就在这三行里，而它们**必须靠「键在不在」分开** ——
	// 用 `len(ranges) == 0` 判的话，NightUnset 与 NightEmpty 会挤成同一个读数。
	nightRaw, present := m["night"]
	if !present {
		return Sessions{Day: day, Night: Night{State: NightUnset}}, nil
	}
	night, err := decodeRanges(nightRaw)
	if err != nil {
		return Sessions{}, err
	}
	if len(night) == 0 {
		return Sessions{Day: day, Night: Night{State: NightEmpty}}, nil
	}
	return Sessions{Day: day, Night: Night{State: NightRanges, Ranges: night}}, nil
}

func decodeRanges(raw json.RawMessage) ([]Range, error) {
	var pairs [][]string
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadSessions, err)
	}
	out := make([]Range, 0, len(pairs))
	for _, p := range pairs {
		if len(p) != 2 {
			return nil, fmt.Errorf("%w: 一个时段有 %d 个端点，要 2 个", errBadSessions, len(p))
		}
		a, err := ParseOffset(p[0])
		if err != nil {
			return nil, err
		}
		b, err := ParseOffset(p[1])
		if err != nil {
			return nil, err
		}
		if b <= a {
			return nil, fmt.Errorf("%w: 时段 %q~%q 的右端不在左端之后", errBadSessions, p[0], p[1])
		}
		out = append(out, Range{Start: a, End: b})
	}
	return out, nil
}

// ParseOffset 把上游那种 `"HH:MM:SS"` 解成【自交易日名义起点起的偏移】。
//
// ⛔ **小时可以 ≥ 24**（实测：`"25:00:00"` ＝次日 01:00、`"26:30:00"` ＝次日 02:30）——
// 所以它**不能**走 `time.Parse`：
//
//	time.Parse("15:04:05", "25:00:00") ⇒ **报错 hour out of range**
//
// 🔴 而那个报错拦住的**不是坏数据，是一个它表达不了的正确值**：
// `time.Parse` 的目标类型是【时刻】，而这个值是【偏移】。
// ⇒ 判据：**当一个「吵」的检查拦住了正确的输入，先问它检查的是不是【另一个类型】** ——
// 而不是先想「怎么把输入改成它能收的样子」（那会把 25:00 折成 01:00，**丢掉「次日」**）。
func ParseOffset(s string) (time.Duration, error) {
	f := strings.Split(s, ":")
	if len(f) != 3 {
		return 0, fmt.Errorf("%w: %q 不是 HH:MM:SS", errBadOffset, s)
	}
	var v [3]int
	for i, part := range f {
		n, err := strconv.Atoi(part)
		if err != nil || len(part) != 2 || n < 0 {
			return 0, fmt.Errorf("%w: %q 的第 %d 段是 %q", errBadOffset, s, i+1, part)
		}
		v[i] = n
	}
	// ⚠️ 小时【不设上界】—— 设了就等于回到 time.Parse 那一格。
	// 分与秒仍然要 < 60：那两个是真的进位，不是偏移。
	if v[1] > 59 || v[2] > 59 {
		return 0, fmt.Errorf("%w: %q 的分或秒 ≥ 60", errBadOffset, s)
	}
	return time.Duration(v[0])*time.Hour +
		time.Duration(v[1])*time.Minute +
		time.Duration(v[2])*time.Second, nil
}
