package derived

import (
	"errors"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/continuous"
)

// —— 判据本体的零点：probe.md 6.30 那张读数表 ——
//
// ⛔ 期望值**逐条取自读数表**，不取自跑出来的输出（评审方 2026-09-17 放行判据本体时的第一条）：
//
//	天轴   241 个交易日（2025-09-15…2026-09-11，新浪 RB0 的日期）
//	换月   2025-12-02 rb2601→rb2605 · 2026-04-07 rb2605→rb2610 · 2026-09-01 rb2610→rb2701
//	NoPick 2025-12-01 · 2026-04-02 · 2026-04-03 · 2026-08-28 · 2026-08-31
//	三档   a 230 · b 0 · c 6（2025-10-09 · 2026-01-05 · 2026-02-24 · 2026-04-07 · 2026-05-06 · 2026-06-22）
//	主连无量而别的合约有量  0 天
//
// ⚠️ 输入是**按那张表造出来的合成数据**，不是那次的真数据（那份一次性库 2026-09-17 13:23 已删，6.31 有记）。
// ⇒ 这一格证明的是「判据在一份与 6.30 同形的输入上交出 6.30 那张表」；它**不**重新证明 6.30 的读数本身。

var table630Days = []tickflow.TradingDay{
	20250915, 20250916, 20250917, 20250918, 20250919, 20250922, 20250923, 20250924,
	20250925, 20250926, 20250929, 20250930, 20251009, 20251010, 20251013, 20251014,
	20251015, 20251016, 20251017, 20251020, 20251021, 20251022, 20251023, 20251024,
	20251027, 20251028, 20251029, 20251030, 20251031, 20251103, 20251104, 20251105,
	20251106, 20251107, 20251110, 20251111, 20251112, 20251113, 20251114, 20251117,
	20251118, 20251119, 20251120, 20251121, 20251124, 20251125, 20251126, 20251127,
	20251128, 20251201, 20251202, 20251203, 20251204, 20251205, 20251208, 20251209,
	20251210, 20251211, 20251212, 20251215, 20251216, 20251217, 20251218, 20251219,
	20251222, 20251223, 20251224, 20251225, 20251226, 20251229, 20251230, 20251231,
	20260105, 20260106, 20260107, 20260108, 20260109, 20260112, 20260113, 20260114,
	20260115, 20260116, 20260119, 20260120, 20260121, 20260122, 20260123, 20260126,
	20260127, 20260128, 20260129, 20260130, 20260202, 20260203, 20260204, 20260205,
	20260206, 20260209, 20260210, 20260211, 20260212, 20260213, 20260224, 20260225,
	20260226, 20260227, 20260302, 20260303, 20260304, 20260305, 20260306, 20260309,
	20260310, 20260311, 20260312, 20260313, 20260316, 20260317, 20260318, 20260319,
	20260320, 20260323, 20260324, 20260325, 20260326, 20260327, 20260330, 20260331,
	20260401, 20260402, 20260403, 20260407, 20260408, 20260409, 20260410, 20260413,
	20260414, 20260415, 20260416, 20260417, 20260420, 20260421, 20260422, 20260423,
	20260424, 20260427, 20260428, 20260429, 20260430, 20260506, 20260507, 20260508,
	20260511, 20260512, 20260513, 20260514, 20260515, 20260518, 20260519, 20260520,
	20260521, 20260522, 20260525, 20260526, 20260527, 20260528, 20260529, 20260601,
	20260602, 20260603, 20260604, 20260605, 20260608, 20260609, 20260610, 20260611,
	20260612, 20260615, 20260616, 20260617, 20260618, 20260622, 20260623, 20260624,
	20260625, 20260626, 20260629, 20260630, 20260701, 20260702, 20260703, 20260706,
	20260707, 20260708, 20260709, 20260710, 20260713, 20260714, 20260715, 20260716,
	20260717, 20260720, 20260721, 20260722, 20260723, 20260724, 20260727, 20260728,
	20260729, 20260730, 20260731, 20260803, 20260804, 20260805, 20260806, 20260807,
	20260810, 20260811, 20260812, 20260813, 20260814, 20260817, 20260818, 20260819,
	20260820, 20260821, 20260824, 20260825, 20260826, 20260827, 20260828, 20260831,
	20260901, 20260902, 20260903, 20260904, 20260907, 20260908, 20260909, 20260910,
	20260911,
}

var (
	rb2601 = tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601}
	rb2605 = tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2605}
	rb2610 = tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}
	rb2701 = tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2701}
)

// 读数表里的三组日子（期望值与造输入共用 —— 它们就是那张表，不是跑出来的）
var (
	table630NoPick = map[tickflow.TradingDay]bool{20251201: true, 20260402: true, 20260403: true, 20260828: true, 20260831: true}
	table630C      = []tickflow.TradingDay{20251009, 20260105, 20260224, 20260407, 20260506, 20260622}
)

// build630 按 6.30 那张表造输入：四份合约每天都在候选里；主力持仓与成交都最大；NoPick 日让持仓最大与成交最大分属两份。
func build630(t *testing.T) Input {
	t.Helper()
	cSet := map[tickflow.TradingDay]bool{}
	for _, d := range table630C {
		cSet[d] = true
	}
	// 每天的主力（NoPick 日给出「持仓最大」与「成交最大」两份）
	mainOf := func(d tickflow.TradingDay) (oiMax, volMax tickflow.Symbol) {
		switch {
		case d == 20251201:
			return rb2601, rb2605
		case d < 20251202:
			return rb2601, rb2601
		case d == 20260402 || d == 20260403:
			return rb2605, rb2610
		case d < 20260407:
			return rb2605, rb2605
		case d == 20260828 || d == 20260831:
			return rb2610, rb2701
		case d < 20260901:
			return rb2610, rb2610
		default:
			return rb2701, rb2701
		}
	}
	all := []tickflow.Symbol{rb2601, rb2605, rb2610, rb2701}
	var in []continuous.DayBars
	var nights []NightObs
	for _, d := range table630Days {
		oiMax, volMax := mainOf(d)
		db := continuous.DayBars{Day: d, Bars: map[tickflow.Symbol]tickflow.Bar{}}
		for _, s := range all {
			vol, oi := 10.0, 10.0
			if s == oiMax {
				oi = 1000
			}
			if s == volMax {
				vol = 1000
			}
			db.Cands = append(db.Cands, continuous.ContractDay{Symbol: s, Day: d, Volume: vol, OpenInterest: oi, Expiry: continuous.ExpiryUnknown})
			db.Bars[s] = tickflow.Bar{TradingDay: d, Open: 3000, High: 3000, Low: 3000, Close: 3000, Volume: vol, OpenInterest: oi}
			o := NightObs{Day: d, Symbol: s}
			if !cSet[d] { // c 日：整个品种那一夜都没开 ⇒ 每份合约都没有夜盘根
				o.NightBars, o.NightVolume = 120, vol/2
			}
			nights = append(nights, o)
		}
		in = append(in, db)
	}
	c, err := continuous.Build(continuous.ContinuousSpec{Product: "SHFE.rb", Roll: continuous.ByOIAndVolume{}, Adjust: continuous.NoAdjust}, in)
	if err != nil {
		t.Fatalf("造输入时 Build 失败：%v", err)
	}
	return Input{Main: c, Nights: nights}
}

func TestJudgeReproducesTable630(t *testing.T) {
	in := build630(t)

	// 先核输入确实与那张表同形（否则下面的断言在问一份别的输入）
	if len(in.Main.Days) != 241 || len(in.Main.Bars) != 236 || len(in.Main.NoPick) != 5 {
		t.Fatalf("输入不同形：天轴 %d（要 241）· 根 %d（要 236）· NoPick %d（要 5）", len(in.Main.Days), len(in.Main.Bars), len(in.Main.NoPick))
	}
	wantRolls := []struct {
		day      tickflow.TradingDay
		from, to tickflow.Symbol
	}{{20251202, rb2601, rb2605}, {20260407, rb2605, rb2610}, {20260901, rb2610, rb2701}}
	if len(in.Main.Rolls) != len(wantRolls) {
		t.Fatalf("输入不同形：换月 %d 次（要 3）%+v", len(in.Main.Rolls), in.Main.Rolls)
	}
	for i, w := range wantRolls {
		if r := in.Main.Rolls[i]; r.Day != w.day || r.From != w.from || r.To != w.to {
			t.Fatalf("输入不同形：第 %d 次换月 %s %s→%s，要 %s %s→%s", i+1, r.Day, r.From, r.To, w.day, w.from, w.to)
		}
	}

	rep, err := Judge(in)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", rep.Summary())
	// ⛔ 「让蚕食看得见」的那个数本身也要有人守（评审方 K1：分母写成「判过了的天」全绿）：5 / 241 ＝ 2.1%
	if s := rep.Summary(); !strings.Contains(s, "无结论 5 天（2.1%）") {
		t.Errorf("Summary 里的无结论占比不对（要「无结论 5 天（2.1%%）」＝ 5/241）：\n%s", s)
	}

	if len(rep.Days) != 241 || rep.Traded != 230 || rep.ZeroVolume != 0 || rep.Absent != 6 ||
		rep.NoVerdict != 5 || rep.NoPick != 5 || rep.HeldBack != 0 || rep.NeverFetched != 0 || len(rep.OthersHadNight) != 0 {
		t.Errorf("与 6.30 那张表不符：判了 %d（241）· a %d（230）· b %d（0）· c %d（6）· 无结论 %d（5，其中 NoPick %d 要 5、挂起 %d 要 0、永久洞 %d 要 0）· 主连无量而别的合约有量 %d（0）",
			len(rep.Days), rep.Traded, rep.ZeroVolume, rep.Absent, rep.NoVerdict, rep.NoPick, rep.HeldBack, rep.NeverFetched, len(rep.OthersHadNight))
	}
	var gotC []tickflow.TradingDay
	for _, dv := range rep.Days {
		switch {
		case dv.Verdict == NightAbsent:
			gotC = append(gotC, dv.Day)
		case dv.Verdict == NoVerdict && !table630NoPick[dv.Day]:
			t.Errorf("%s 判成无结论（%s），而它不在 6.30 的 NoPick 五天里", dv.Day, dv.Reason)
		case dv.Verdict != NoVerdict && table630NoPick[dv.Day]:
			t.Errorf("%s 是 6.30 的 NoPick 日，却判成 %s", dv.Day, dv.Verdict)
		}
	}
	if len(gotC) != len(table630C) {
		t.Fatalf("c 档 %v，要 %v", gotC, table630C)
	}
	for i := range gotC {
		if gotC[i] != table630C[i] {
			t.Errorf("c 档第 %d 天 %s，要 %s", i+1, gotC[i], table630C[i])
		}
	}
}

// 洞排在 NoPick 前面；同一天两种洞 ⇒ 记永久洞；不在天轴上的洞也要逐天交出来。
func TestJudgeHolesComeFirstAndAreNamed(t *testing.T) {
	in := build630(t)
	in.Holes = []Hole{
		{Day: 20251009, Reason: ReasonHeldBack},     // 本来是 c ⇒ 洞优先
		{Day: 20251201, Reason: ReasonHeldBack},     // 本来是 NoPick ⇒ 洞优先
		{Day: 20260105, Reason: ReasonHeldBack},     // 同一天两种洞 ⇒ 永久洞（挂起在前）
		{Day: 20260105, Reason: ReasonNeverFetched}, //
		{Day: 20260224, Reason: ReasonNeverFetched}, // 同一天两种洞 ⇒ 永久洞（永久洞在前）
		{Day: 20260224, Reason: ReasonHeldBack},     // ⛔ 只有这一种顺序，「后来者胜」才会判错（评审方式突变 J4 第一次落在等价输入上）
		{Day: 20260101, Reason: ReasonNeverFetched}, // 不在天轴上（元旦）⇒ 仍要交出来
	}
	rep, err := Judge(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[tickflow.TradingDay]NoVerdictReason{
		20251009: ReasonHeldBack,
		20251201: ReasonHeldBack,
		20260105: ReasonNeverFetched,
		20260224: ReasonNeverFetched,
		20260101: ReasonNeverFetched,
	}
	got := map[tickflow.TradingDay]DayVerdict{}
	for _, dv := range rep.Days {
		got[dv.Day] = dv
	}
	for d, r := range want {
		dv, ok := got[d]
		if !ok {
			t.Errorf("%s 有洞，却没被交出来 —— 不许跳过不提", d)
			continue
		}
		if dv.Verdict != NoVerdict || dv.Reason != r {
			t.Errorf("%s：得 %s/%s，要 无结论/%s", d, dv.Verdict, dv.Reason, r)
		}
	}
	if len(rep.Days) != 242 || rep.HeldBack != 2 || rep.NeverFetched != 3 || rep.NoPick != 4 || rep.Absent != 3 {
		t.Errorf("报数：判了 %d（242）· 挂起 %d（2）· 永久洞 %d（3）· NoPick %d（4）· c %d（3）",
			len(rep.Days), rep.HeldBack, rep.NeverFetched, rep.NoPick, rep.Absent)
	}
	s := rep.Summary()
	// 占比：9 / 242 ＝ 3.7% —— 与零点那格（5/241）分母不同，分母写错时至少一格会红
	for _, frag := range []string{"无结论 9 天（3.7%）", "库侧挂起 2", "永久洞 3", "规则没给出主力 4", "无结论 2026-01-01：库侧没拉过（永久洞）"} {
		if !strings.Contains(s, frag) {
			t.Errorf("Summary 里缺「%s」：\n%s", frag, s)
		}
	}
}

// ⛔ 主力那一天没有观测 ⇒ 报错，不许判成 c。
func TestJudgeRefusesToReadMissingObsAsAbsent(t *testing.T) {
	in := build630(t)
	var kept []NightObs
	for _, o := range in.Nights {
		if !(o.Day == 20251010 && o.Symbol == rb2601) {
			kept = append(kept, o)
		}
	}
	in.Nights = kept
	if _, err := Judge(in); !errors.Is(err, ErrNightObsMissing) {
		t.Fatalf("期望 ErrNightObsMissing，实得 %v", err)
	}
	// 缺的是非主力合约那一天的观测 ⇒ 不影响判（主力有观测即可）
	in = build630(t)
	kept = nil
	for _, o := range in.Nights {
		if !(o.Day == 20251010 && o.Symbol == rb2605) {
			kept = append(kept, o)
		}
	}
	in.Nights = kept
	if _, err := Judge(in); err != nil {
		t.Fatalf("缺的是非主力合约的观测，不该报错：%v", err)
	}
}

func TestJudgeRejectsBadInput(t *testing.T) {
	in := build630(t)
	in.Holes = []Hole{{Day: 20251010, Reason: ReasonNoPick}}
	if _, err := Judge(in); !errors.Is(err, ErrHoleReason) {
		t.Errorf("洞的原因写成 NoPick：期望 ErrHoleReason，实得 %v", err)
	}
	in.Holes = []Hole{{Day: 20251010, Reason: ReasonNone}}
	if _, err := Judge(in); !errors.Is(err, ErrHoleReason) {
		t.Errorf("洞没写原因：期望 ErrHoleReason，实得 %v", err)
	}
	in = build630(t)
	in.Nights = append(in.Nights, in.Nights[0])
	if _, err := Judge(in); !errors.Is(err, ErrNightObsDuplicate) {
		t.Errorf("重复观测：期望 ErrNightObsDuplicate，实得 %v", err)
	}
}

// 主连无量而别的合约有量 ⇒ 仍判 b/c，但点名。
func TestJudgeNamesOthersHadNight(t *testing.T) {
	in := build630(t)
	for i, o := range in.Nights {
		if o.Day == 20251010 && o.Symbol == rb2601 { // 主力那一夜有根而量 0
			in.Nights[i].NightVolume = 0
		}
	}
	rep, err := Judge(in)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ZeroVolume != 1 || len(rep.OthersHadNight) != 1 || rep.OthersHadNight[0] != 20251010 {
		t.Errorf("b %d（要 1）· 主连无量而别的合约有量 %v（要 [2025-10-10]）", rep.ZeroVolume, rep.OthersHadNight)
	}
}

// NightObsOf 的时刻边界：20:00 算夜盘、19:59 不算、03:59 算、04:00 不算；没有夜盘根的日子也交一条。
func TestNightObsOfBoundaries(t *testing.T) {
	at := func(s string) int64 {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, tickflow.CST)
		if err != nil {
			t.Fatal(err)
		}
		return tm.UnixMilli()
	}
	bars := []tickflow.Bar{
		{TradingDay: 20260310, Ts: at("2026-03-09 19:59"), Volume: 1},
		{TradingDay: 20260310, Ts: at("2026-03-09 20:00"), Volume: 2},
		{TradingDay: 20260310, Ts: at("2026-03-10 03:59"), Volume: 4},
		{TradingDay: 20260310, Ts: at("2026-03-10 04:00"), Volume: 8},
		{TradingDay: 20260310, Ts: at("2026-03-10 09:00"), Volume: 16},
		{TradingDay: 20260311, Ts: at("2026-03-11 09:00"), Volume: 32}, // 只有日盘
	}
	got := NightObsOf(rb2605, bars)
	if len(got) != 2 {
		t.Fatalf("交出 %d 条，要 2（没有夜盘根的日子也要一条）：%+v", len(got), got)
	}
	if got[0].Day != 20260310 || got[0].NightBars != 2 || got[0].NightVolume != 6 {
		t.Errorf("03-10：%+v，要 2 根 · 量 6（20:00 与 03:59 两根）", got[0])
	}
	if got[1].Day != 20260311 || got[1].NightBars != 0 || got[1].NightVolume != 0 || got[1].Symbol != rb2605 {
		t.Errorf("03-11：%+v，要 0 根 · 量 0", got[1])
	}
}
