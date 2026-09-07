package tickflow

import (
	"strings"
	"testing"
)

// TestNativeVsCanonical 规范形式一律四位；线格式只有郑商所是三位。
//
// 这是本库与下游记账内核 futsim 的接口约定：内部与落盘用 String()，
// 递给下游用 Native()。两者混用会让郑商所合约的键对不上，
// 而【键对不上不报错】——查不到持仓而已。
func TestNativeVsCanonical(t *testing.T) {
	cases := []struct {
		exch, prod string
		ym         int
		canonical  string
		native     string
	}{
		{CZCE, "TA", 2701, "CZCE.TA2701", "CZCE.TA701"},
		{CZCE, "MA", 2609, "CZCE.MA2609", "CZCE.MA609"},
		{SHFE, "rb", 2701, "SHFE.rb2701", "SHFE.rb2701"},
		{DCE, "m", 2701, "DCE.m2701", "DCE.m2701"},
		{CFFEX, "IF", 2609, "CFFEX.IF2609", "CFFEX.IF2609"},
		{INE, "sc", 2611, "INE.sc2611", "INE.sc2611"},
		{GFEX, "si", 2611, "GFEX.si2611", "GFEX.si2611"},
	}
	for _, c := range cases {
		s, err := newSymbol(c.exch, c.prod, c.ym)
		if err != nil {
			t.Fatalf("%s.%s%d: %v", c.exch, c.prod, c.ym, err)
		}
		if got := s.String(); got != c.canonical {
			t.Errorf("String() = %q, want %q", got, c.canonical)
		}
		if got := s.Native(); got != c.native {
			t.Errorf("Native() = %q, want %q", got, c.native)
		}
	}
}

// TestProductCaseIsExchangeRule 品种大小写是交易所规定的，不是风格。
// 上期/大商/上期能源/广期所小写，郑商/中金大写。
func TestProductCaseIsExchangeRule(t *testing.T) {
	cases := []struct{ exch, in, want string }{
		{SHFE, "RB", "rb"}, {SHFE, "rb", "rb"},
		{DCE, "M", "m"},
		{GFEX, "SI", "si"},
		{INE, "SC", "sc"},
		{CZCE, "ta", "TA"}, {CZCE, "TA", "TA"},
		{CFFEX, "if", "IF"},
	}
	for _, c := range cases {
		s, err := newSymbol(c.exch, c.in, 2701)
		if err != nil {
			t.Fatal(err)
		}
		if s.Product != c.want {
			t.Errorf("%s 的 %q 应规范成 %q，得到 %q", c.exch, c.in, c.want, s.Product)
		}
	}
}

// TestParseNativeAnchorsOnDataTradingDay 是这一组里最要紧的一条。
//
// 三位码 TA701 既可以是 2017-01 也可以是 2027-01。判据是
// 「相对【该数据自身的交易日】取最近的将来」——**不是相对「现在」**。
//
// 所以【同一个三位码配不同的 asOf，必须给出不同的四位码】。
// 一个用「现在」当锚的实现会让这条测试的两行给出相同结果——
// 而那个错误的后果是：同一段历史数据在不同年份跑出不同结果，
// 且跑一年才会发现。
func TestParseNativeAnchorsOnDataTradingDay(t *testing.T) {
	cases := []struct {
		instID string
		asOf   TradingDay
		want   string
	}{
		{"TA701", 20260907, "CZCE.TA2701"}, // 2026 年看 701 → 2027-01
		{"TA701", 20160907, "CZCE.TA1701"}, // 2016 年看 701 → 2017-01
		{"TA609", 20260101, "CZCE.TA2609"}, // 同年未来月份 → 本年
		{"TA609", 20160101, "CZCE.TA1609"},
		{"MA509", 20250301, "CZCE.MA2509"}, // 同年同月之后
	}
	seen := map[string]bool{}
	for _, c := range cases {
		s, err := ParseNative(CZCE, c.instID, c.asOf)
		if err != nil {
			t.Fatalf("ParseNative(%q, %d): %v", c.instID, c.asOf, err)
		}
		if got := s.String(); got != c.want {
			t.Errorf("ParseNative(%q, asOf=%d) = %q, want %q",
				c.instID, c.asOf, got, c.want)
		}
		seen[c.instID+"|"+s.String()] = true
	}
	// 反向断言：同一个 TA701 在两个年代必须解成【不同】的合约
	a, _ := ParseNative(CZCE, "TA701", 20260907)
	b, _ := ParseNative(CZCE, "TA701", 20160907)
	if a.String() == b.String() {
		t.Fatalf("同一个三位码在 2026 与 2016 解出了同一个合约（%s）——"+
			"说明锚用的不是【数据自身的交易日】", a)
	}
}

// TestParseNativeRejectsMissingAnchor 没有有效 asOf 时必须报错，而不是猜。
//
// 「猜一个」在这里是最坏的选择：它会成功、会给出一个合理的合约号、
// 而且只在跨十年的历史上才错。
func TestParseNativeRejectsMissingAnchor(t *testing.T) {
	_, err := ParseNative(CZCE, "TA701", 0)
	if err == nil {
		t.Fatal("asOf 无效时应当报错")
	}
	if !strings.Contains(err.Error(), "asOf") {
		t.Errorf("错误信息应点出 asOf，得到 %v", err)
	}
	// 四位码不需要锚
	if _, err := ParseNative(SHFE, "rb2701", 0); err != nil {
		t.Errorf("四位码不该要求 asOf: %v", err)
	}
}

// TestThreeDigitOnlyCZCE 只有郑商所是三位线格式。别处出现三位要报错，
// 而不是当成某种简写去猜。
func TestThreeDigitOnlyCZCE(t *testing.T) {
	if _, err := ParseNative(SHFE, "rb701", 20260907); err == nil {
		t.Error("上期所的三位码应当报错")
	}
}

// TestParseSymbolRequiresFourDigits 规范形式必须四位，三位要给出可操作的提示。
func TestParseSymbolRequiresFourDigits(t *testing.T) {
	_, err := ParseSymbol("CZCE.TA701")
	if err == nil {
		t.Fatal("规范形式收到三位应当报错")
	}
	if !strings.Contains(err.Error(), "ParseNative") {
		t.Errorf("错误信息应指向 ParseNative，得到 %v", err)
	}
	if _, err := ParseSymbol("CZCE.TA2701"); err != nil {
		t.Errorf("四位应当接受: %v", err)
	}
}

// TestProductKeyHasNoYearMonth 时段表的键【不含年月】。
//
// 这一点与 refdata（按合约，键必须含四位年月，否则跨十年会撞）相反。
// 同一个「跨十年会撞」的警告，对 calendar 不成立、对 refdata 成立，
// 差别就在键里有没有年月——所以这里显式钉住。
func TestProductKeyHasNoYearMonth(t *testing.T) {
	a, _ := ParseSymbol("CZCE.TA2701")
	b, _ := ParseSymbol("CZCE.TA1701") // 相隔十年的同品种合约
	if a.ProductKey() != b.ProductKey() {
		t.Errorf("同品种不同年月应当共用一个 ProductKey：%v vs %v",
			a.ProductKey(), b.ProductKey())
	}
	if k := a.ProductKey(); k.String() != "CZCE.TA" {
		t.Errorf("ProductKey 不该含年月，得到 %q", k.String())
	}
}

func TestSymbolRejects(t *testing.T) {
	if _, err := newSymbol("XXXX", "rb", 2701); err == nil {
		t.Error("未知交易所应当报错")
	}
	if _, err := newSymbol(SHFE, "rb", 2713); err == nil {
		t.Error("月份 13 应当报错")
	}
	if _, err := ParseSymbol("rb2701"); err == nil {
		t.Error("缺交易所前缀应当报错")
	}
}

func TestTradingDayIsNamedType(t *testing.T) {
	d := TradingDay(20260907)
	if !d.Valid() {
		t.Error("20260907 应当有效")
	}
	if y, m, day := d.Split(); y != 2026 || m != 9 || day != 7 {
		t.Errorf("Split 得到 %d-%d-%d", y, m, day)
	}
	if s := d.String(); s != "2026-09-07" {
		t.Errorf("String 得到 %q", s)
	}
	for _, bad := range []TradingDay{0, 20261301, 20260932, 1899} {
		if bad.Valid() {
			t.Errorf("%d 应当无效", int32(bad))
		}
	}
}
