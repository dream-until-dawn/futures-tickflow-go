package tickflow

import (
	"fmt"
	"strconv"
	"strings"
)

// 六个交易所。
const (
	SHFE  = "SHFE"  // 上海期货交易所
	DCE   = "DCE"   // 大连商品交易所
	CZCE  = "CZCE"  // 郑州商品交易所
	CFFEX = "CFFEX" // 中国金融期货交易所
	INE   = "INE"   // 上海国际能源交易中心
	GFEX  = "GFEX"  // 广州期货交易所
)

// 品种代码的大小写是【交易所规定的】，不是风格问题：
// 上期所 / 大商所 / 上期能源 / 广期所用小写（rb cu m i sc si），
// 郑商所 / 中金所用大写（TA MA IF T）。
// 新浪对大小写不敏感，但交易所与 CTP 敏感。
var upperProduct = map[string]bool{CZCE: true, CFFEX: true}

// czceThreeDigit 记的是「哪个交易所的线格式用三位年月」。
//
// 只有郑商所。它自己写 TA701 指 2027 年 1 月，而 TA701 也可以读成 2017 年 1 月
// ——跨十年就分不清，而本库要覆盖 2009 年至今。
var czceThreeDigit = map[string]bool{CZCE: true}

// Symbol 是一个可交易的期货合约。
//
// 规范形式（String）一律用【四位】年月：CZCE.TA2701。
// 线格式（Native）按交易所原生：郑商所三位 CZCE.TA701，其余四位。
//
// 两种形式都要，是因为上下游要的不是同一个：
//   - 本库内部与落盘用规范形式——四位无歧义，覆盖 17 年历史不会撞；
//   - 下游记账内核 futsim 的键取【线格式】，因为它的 view 包要与 CTP 结构体、
//     DIFF 业务截面字段级同构。
//
// 主连（新浪 RB0、天勤 KQ.m@SHFE.rb）与指数【不是】 Symbol——
// 它们不可交易。混进来的话，「这个代码能不能下单」就没有类型层面的答案了。
type Symbol struct {
	Exchange string
	Product  string
	YearMon  int // 四位，如 2701
}

// String 返回规范形式：CZCE.TA2701。四位年月，无歧义。
func (s Symbol) String() string {
	return fmt.Sprintf("%s.%s%04d", s.Exchange, s.Product, s.YearMon)
}

// Instrument 返回交易所线格式的合约号：郑商所 TA701，其余 rb2701。
func (s Symbol) Instrument() string {
	if czceThreeDigit[s.Exchange] {
		return fmt.Sprintf("%s%03d", s.Product, s.YearMon%1000)
	}
	return fmt.Sprintf("%s%04d", s.Product, s.YearMon)
}

// Native 返回线格式全名：CZCE.TA701 / SHFE.rb2701。给下游与 CTP 用。
func (s Symbol) Native() string { return s.Exchange + "." + s.Instrument() }

// ProductKey 返回时段表的键。注意它【不含年月】——时段按品种给，不按合约。
func (s Symbol) ProductKey() ProductKey {
	return ProductKey{Exchange: s.Exchange, Product: s.Product}
}

// Expiry 返回合约的交割年月（year, month）。
func (s Symbol) Expiry() (year, month int) {
	return 2000 + s.YearMon/100, s.YearMon % 100
}

// ParseSymbol 解析规范形式 `EXCH.product2701`（四位年月）。
func ParseSymbol(s string) (Symbol, error) {
	exch, inst, ok := strings.Cut(s, ".")
	if !ok {
		return Symbol{}, fmt.Errorf("tickflow: 合约代码缺少交易所前缀: %q", s)
	}
	prod, digits, err := splitInstrument(inst)
	if err != nil {
		return Symbol{}, fmt.Errorf("tickflow: %q: %w", s, err)
	}
	if len(digits) != 4 {
		return Symbol{}, fmt.Errorf(
			"tickflow: %q 的年月是 %d 位，规范形式要求四位；三位线格式请用 ParseNative",
			s, len(digits))
	}
	ym, _ := strconv.Atoi(digits)
	return newSymbol(exch, prod, ym)
}

// ParseNative 解析交易所线格式，三位年月按 asOf 展开成四位。
//
// asOf 必须是【该数据自身所属的交易日】，不是「现在」。
//
// 这一条很要紧：三位码 TA701 既可以是 2017-01 也可以是 2027-01，
// 而判据是「相对 asOf 取最近的将来」。若用「现在」当锚，
// **同一段历史数据在不同年份跑出不同结果**——那是个跑一年才会发现的静默错误。
//
// 展开规则是【推定】：郑商所没有文档说明跨十年怎么办。
func ParseNative(exch, instID string, asOf TradingDay) (Symbol, error) {
	prod, digits, err := splitInstrument(instID)
	if err != nil {
		return Symbol{}, fmt.Errorf("tickflow: %s.%s: %w", exch, instID, err)
	}
	switch len(digits) {
	case 4:
		ym, _ := strconv.Atoi(digits)
		return newSymbol(exch, prod, ym)
	case 3:
		if !czceThreeDigit[exch] {
			return Symbol{}, fmt.Errorf(
				"tickflow: %s.%s 用了三位年月，但只有郑商所是三位线格式", exch, instID)
		}
		if !asOf.Valid() {
			return Symbol{}, fmt.Errorf(
				"tickflow: 展开三位年月 %q 需要一个有效的 asOf 交易日（收到 %d）"+
					"——锚必须是数据自身的交易日，不能是「现在」", instID, int32(asOf))
		}
		ym, err := expandThreeDigit(digits, asOf)
		if err != nil {
			return Symbol{}, fmt.Errorf("tickflow: %s.%s: %w", exch, instID, err)
		}
		return newSymbol(exch, prod, ym)
	default:
		return Symbol{}, fmt.Errorf(
			"tickflow: %s.%s 的年月是 %d 位，只支持三位或四位", exch, instID, len(digits))
	}
}

// expandThreeDigit 把 YMM 展开成 YYMM：取【不早于 asOf 所在年】的、
// 年份个位等于 Y 的最近一年。
func expandThreeDigit(digits string, asOf TradingDay) (int, error) {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, fmt.Errorf("年月不是数字: %q", digits)
	}
	yDigit, month := n/100, n%100
	if month < 1 || month > 12 {
		return 0, fmt.Errorf("月份越界: %d", month)
	}
	baseYear, _, _ := asOf.Split()
	for y := baseYear; y < baseYear+10; y++ {
		if y%10 != yDigit {
			continue
		}
		// 同年但月份已过，说明它指的是下一个十年那一档
		if y == baseYear {
			_, asOfMonth, _ := asOf.Split()
			if month < asOfMonth {
				continue
			}
		}
		return (y%100)*100 + month, nil
	}
	return 0, fmt.Errorf("无法在 %d 之后十年内展开 %q", baseYear, digits)
}

func newSymbol(exch, prod string, ym int) (Symbol, error) {
	switch exch {
	case SHFE, DCE, CZCE, CFFEX, INE, GFEX:
	default:
		return Symbol{}, fmt.Errorf("tickflow: 未知交易所 %q", exch)
	}
	if prod == "" {
		return Symbol{}, fmt.Errorf("tickflow: 品种代码为空")
	}
	if m := ym % 100; m < 1 || m > 12 {
		return Symbol{}, fmt.Errorf("tickflow: 月份越界: %d", m)
	}
	if upperProduct[exch] {
		prod = strings.ToUpper(prod)
	} else {
		prod = strings.ToLower(prod)
	}
	return Symbol{Exchange: exch, Product: prod, YearMon: ym}, nil
}

// splitInstrument 把 rb2701 拆成 ("rb", "2701")。
func splitInstrument(inst string) (prod, digits string, err error) {
	i := len(inst)
	for i > 0 && inst[i-1] >= '0' && inst[i-1] <= '9' {
		i--
	}
	prod, digits = inst[:i], inst[i:]
	if prod == "" || digits == "" {
		return "", "", fmt.Errorf("合约号 %q 不是「品种+年月」的形状", inst)
	}
	return prod, digits, nil
}
