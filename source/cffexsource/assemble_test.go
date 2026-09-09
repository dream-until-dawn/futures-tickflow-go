package cffexsource

import (
	"errors"
	"math"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

var cst = time.FixedZone("CST", 8*3600)

func at(y, mo, d, h, mi int) int64 {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, cst).UnixMilli()
}

// testDays 手列，**不从 fixture 里抽** —— 拿数据造日历再用它验那份数据是循环论证。
var testDays = []tickflow.TradingDay{20260904, 20260907, 20260908}

func testDay(t *testing.T, k tickflow.ProductKey, num tickflow.TradingDay) tickflow.Day {
	t.Helper()
	cal, err := embedded.New(testDays)
	if err != nil {
		t.Fatalf("造日历：%v", err)
	}
	d, err := cal.DayOf(k, num)
	if err != nil {
		t.Fatalf("取交易日 %s：%v", num, err)
	}
	return d
}

func icSym() tickflow.Symbol {
	return tickflow.Symbol{Exchange: tickflow.CFFEX, Product: "IC", YearMon: 2609}
}

func realRows(t *testing.T) []SettleRow {
	t.Helper()
	rows, err := ParseDaily(read(t, "daily_20260908.xml"))
	if err != nil {
		t.Fatalf("解析 fixture：%v", err)
	}
	return rows
}

var nowAfter = at(2026, 9, 9, 0, 0)

// —— 一、拿真实数据走通，并用契约层的检查器核 ——

func TestAssembleDayOnRealData(t *testing.T) {
	sym := icSym()
	day := testDay(t, sym.ProductKey(), 20260908)
	bar, found, err := AssembleDay(realRows(t), day, sym, nowAfter)
	if err != nil {
		t.Fatalf("组装失败：%v", err)
	}
	if !found {
		t.Fatal("真实数据里有 IC2609，却报 found=false")
	}

	// 契约由测试拿 CheckBars 去核，**不由 AssembleDay 自己核**（同义反复）。
	req := tickflow.BarRequest{Symbol: sym, Period: tickflow.Daily, From: 20260908, To: 20260908}
	if err := tickflow.CheckBars(req, []tickflow.Bar{bar}, nowAfter); err != nil {
		t.Fatalf("组装的产物不满足 Source 契约：%v", err)
	}
	if !bar.Flags.Has(tickflow.FlagSrcExchange) {
		t.Error("没打交易所来源标记 —— 多源共存时「这根是哪来的」事后推不出来")
	}
	if !bar.HasSettle() {
		t.Errorf("IC2609 应当有结算价（本源存在的理由），得到 %v", bar.Settle)
	}
	t.Logf("IC2609 %s 收=%v 结=%v 持仓=%v", bar.TradingDay, bar.Close, bar.Settle, bar.OpenInterest)
}

// TestNoNightSessionForCFFEX 是「Ts 从日历取」那一步的对照事实。
//
// 中金所无夜盘 ⇒ Ts 落在【当天】09:30（v0.2.0 修过的那处），
// 与 sinasource 那边「rb 的周一从上周五 21:00 起」正好相反。
func TestNoNightSessionForCFFEX(t *testing.T) {
	sym := icSym()
	day := testDay(t, sym.ProductKey(), 20260908)
	bar, found, err := AssembleDay(realRows(t), day, sym, nowAfter)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if want := at(2026, 9, 8, 9, 30); bar.Ts != want {
		t.Errorf("Ts = %s，期望当天 09:30（中金所无夜盘）",
			time.UnixMilli(bar.Ts).In(cst).Format("01-02 15:04"))
	}
}

// —— 二、五条不静默 / 不越界 ——

func TestOnlyCFFEX(t *testing.T) {
	sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}
	_, _, err := AssembleDay(realRows(t), testDay(t, icSym().ProductKey(), 20260908), sym, nowAfter)
	if !errors.Is(err, ErrNotCFFEX) {
		t.Fatalf("非中金所应当被拒（Instrument() 的安全前提就靠它），得到 %v", err)
	}
}

func TestMainContinuousRejected(t *testing.T) {
	sym := icSym()
	sym.YearMon = 0
	_, _, err := AssembleDay(realRows(t), testDay(t, sym.ProductKey(), 20260908), sym, nowAfter)
	if err == nil {
		t.Fatal("YearMon=0 应当被拒 —— 这份 XML 里只有具体合约")
	}
}

// TestTradingDayCrossCheck 用的是这份 XML 独有的一手证据。
//
// 它自带 `<tradingday>` —— sinasource 那边没有。两边对不上时不吞。
func TestTradingDayCrossCheck(t *testing.T) {
	sym := icSym()
	// 日历给 09-07，而 XML 里那一行自报 09-08 ⇒ 必须炸
	day := testDay(t, sym.ProductKey(), 20260907)
	_, _, err := AssembleDay(realRows(t), day, sym, nowAfter)
	if !errors.Is(err, ErrTradingDayMismatch) {
		t.Fatalf("XML 自报 20260908 而日历给 20260907，应当报 ErrTradingDayMismatch，得到 %v", err)
	}
}

// TestAbsentIsReportedNotGuessed 钉住那个【缺席】的处置。
func TestAbsentIsReportedNotGuessed(t *testing.T) {
	sym := icSym()
	sym.YearMon = 9912 // 一个不存在的到期月
	day := testDay(t, sym.ProductKey(), 20260908)
	bar, found, err := AssembleDay(realRows(t), day, sym, nowAfter)
	if err != nil {
		t.Fatalf("缺席不是错误（可能未上市/已到期），却报：%v", err)
	}
	if found {
		t.Fatalf("不存在的合约却报 found=true：%+v", bar)
	}
}

func TestUnfinishedIsDropped(t *testing.T) {
	sym := icSym()
	day := testDay(t, sym.ProductKey(), 20260908)
	earlier := at(2026, 9, 8, 15, 0) - 1 // 收盘前一毫秒
	_, found, err := AssembleDay(realRows(t), day, sym, earlier)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if found {
		t.Fatal("那一天还没走完，整根应当被丢掉 —— 未完结的绝不进入本库任何一层")
	}
}

// —— 三、0 → NaN 的映射在这一层，而它买不回已经丢掉的信息 ——

func TestZeroSettleBecomesNaNHere(t *testing.T) {
	sym := icSym()
	day := testDay(t, sym.ProductKey(), 20260908)
	rows := []SettleRow{{
		InstrumentID: "IC2609", TradingDay: "20260908",
		Close: 7743.4, Settle: 0, // XML 里写了 0
	}}
	bar, found, err := AssembleDay(rows, day, sym, nowAfter)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !math.IsNaN(bar.Settle) {
		t.Fatalf("0 应当在这一层映射成 NaN（0 是缺失的伪装，bar.go），得到 %v", bar.Settle)
	}
	if bar.HasSettle() {
		t.Error("HasSettle 应当为 false")
	}
}

// TestBlankAndZeroStillIndistinguishable 钉住登记⑳ 的【射程】，不是钉住它被修好了。
//
// ⛔ 解析层已经把「XML 里那格是空白」和「XML 里写了 0」抹成同一个 0；
// 本层只是给这个 0 一个正确的下游语义（NaN），**并没有把丢掉的信息找回来**。
// ⇒ 这条断言两者在本层的产物**相同** —— 它记录的是缺陷仍在，而不是它没了。
func TestBlankAndZeroStillIndistinguishable(t *testing.T) {
	sym := icSym()
	day := testDay(t, sym.ProductKey(), 20260908)
	mk := func(xml string) tickflow.Bar {
		rows, err := ParseDaily([]byte(`<?xml version="1.0"?><dailydatas><dailydata>` +
			`<instrumentid>IC2609</instrumentid><tradingday>20260908</tradingday>` +
			`<closeprice>7743.4</closeprice><settlementprice>` + xml +
			`</settlementprice></dailydata></dailydatas>`))
		if err != nil {
			t.Fatalf("%v", err)
		}
		b, found, err := AssembleDay(rows, day, sym, nowAfter)
		if err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		return b
	}
	blank, zero := mk(""), mk("0.000")
	if !math.IsNaN(blank.Settle) || !math.IsNaN(zero.Settle) {
		t.Fatalf("两者都该是 NaN：blank=%v zero=%v", blank.Settle, zero.Settle)
	}
	blank.Settle, zero.Settle = 0, 0 // NaN 不能用 == 比，先归一再比其余字段
	if blank != zero {
		t.Fatalf("两者在本层竟然可分辨了 —— 若真修好了，请把登记⑳ 关掉并改这条测试的说明")
	}
}

// TestPreSettleIsDroppedOnPurpose 钉住一件【明确的丢弃】。
//
// ⛔ `SettleRow.PreSettle`（昨结算）**进不了 tickflow.Bar** —— Bar 里没有这个字段。
// 而它不是无用的：下游正是拿它推涨跌停（probe.md）。
// ⇒ 所以这不是「顺手丢了」，是**一个要被看见的缺口**：
// 要用它，得先给 Bar 加字段，那是一次公开类型的变更。**登记㉑。**
func TestPreSettleIsDroppedOnPurpose(t *testing.T) {
	rows := realRows(t)
	withPre := 0
	for _, r := range rows {
		if r.PreSettle != 0 {
			withPre++
		}
	}
	if withPre == 0 {
		t.Fatal("fixture 里一条昨结算都没有 —— 这条断言恒真，先修判据")
	}
	sym := icSym()
	day := testDay(t, sym.ProductKey(), 20260908)
	bar, found, err := AssembleDay(rows, day, sym, nowAfter)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	// Bar 里没有任何一格装得下它 —— 这条测试的作用是让这句话留在代码里。
	_ = bar
	t.Logf("%d 条行带昨结算，而 tickflow.Bar 里没有这个字段 ⇒ 全部丢弃（登记㉑）", withPre)
}
