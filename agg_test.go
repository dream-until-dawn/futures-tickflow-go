package tickflow_test

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// v0.9 聚合（agg.go）的测试。判对写在 design.md §十五「v0.9 起手」二·甲 / 丙，读数在 probe.md 6.35。
//
// 各格先断言「判别力在场」（输入里真的有那种会让它红的形状），再断言被测的东西 ——
// 判别力不在场时 Fatal，免得一格因为输入太平而恒绿。

var cst = time.FixedZone("CST", 8*3600)

var (
	keyAU = tickflow.ProductKey{Exchange: "SHFE", Product: "au"}
	keyRB = tickflow.ProductKey{Exchange: "SHFE", Product: "rb"}
)

func at(d tickflow.TradingDay, hh, mm int) int64 {
	y, m, dd := d.Split()
	return time.Date(y, time.Month(m), dd, hh, mm, 0, 0, cst).UnixMilli()
}

func hm(ms int64) string { return time.UnixMilli(ms).In(cst).Format("01-02 15:04") }

// ── 合成数据 ──

// synthDay 在一个交易日的全部时段里逐分钟造 1m 根（skip 返回 true 的分钟不造）。价格与量都随分钟序号变，
// 使 O / H / L / C / V 任一字段取错都会让聚合结果变。
func synthDay(d tickflow.Day, skip func(ts int64) bool) []tickflow.Bar {
	var out []tickflow.Bar
	i := 0
	for _, s := range d.Sessions {
		for m := s.Start; m < s.End; m += 60000 {
			i++
			if skip != nil && skip(m) {
				continue
			}
			p := 500 + float64(i%37)*0.25 + float64(i/37)
			out = append(out, tickflow.Bar{
				Ts: m, TsEnd: m + 60000, TradingDay: d.Num,
				Open: p, High: p + 0.5 + float64(i%5)*0.1, Low: p - 0.5 - float64(i%3)*0.1, Close: p + 0.05*float64(i%4),
				Volume: float64(1 + i%7), OpenInterest: float64(1000 + i), Settle: math.NaN(),
			})
		}
	}
	return out
}

func mustDay(t *testing.T, days []tickflow.TradingDay, k tickflow.ProductKey, d tickflow.TradingDay) (tickflow.Day, tickflow.SessionTemplate) {
	t.Helper()
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	day, err := cal.DayOf(k, d)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := cal.Template(k, d)
	if err != nil {
		t.Fatal(err)
	}
	return day, tmpl
}

// 2026-09-03 周四 · 09-04 周五 · 09-07 周一 · 09-08 周二
var synthDays = []tickflow.TradingDay{20260903, 20260904, 20260907, 20260908}

// ── 朴素参考（不调 agg.go / period.go 的任何切分函数） ──

// naiveAxisLabels 逐分钟累计的交易时间轴分组（6.35 的 C4 那份写法）：夜盘块从 0 起；日盘块起点显式设成相位
// （标称夜盘分钟 mod P）；累计满 P 出一根，标签 ＝ 那一分钟的结束时刻；交易日末有余数就冲刷。
func naiveAxisLabels(day tickflow.Day, nightNominal, p int) []int64 {
	var out []int64
	cnt, inDay := 0, false
	var last int64
	for _, s := range day.Sessions {
		h := time.UnixMilli(s.Start).In(cst).Hour()
		if !(h >= 20 || h < 4) && !inDay {
			inDay = true
			cnt = nightNominal % p
		}
		for m := s.Start; m < s.End; m += 60000 {
			cnt++
			last = m + 60000
			if cnt == p {
				out = append(out, last)
				cnt = 0
			}
		}
	}
	if cnt > 0 {
		out = append(out, last)
	}
	return out
}

type ohlcv struct{ o, h, l, c, v float64 }

// naiveSynth 把收盘时刻落在 (prev, label] 的输入合成一根。
func naiveSynth(src []tickflow.Bar, prev, label int64) (ohlcv, int) {
	var r ohlcv
	n := 0
	for _, x := range src {
		if x.TsEnd <= prev || x.TsEnd > label {
			continue
		}
		if n == 0 {
			r = ohlcv{o: x.Open, h: x.High, l: x.Low}
		}
		r.h, r.l, r.c = math.Max(r.h, x.High), math.Min(r.l, x.Low), x.Close
		r.v += x.Volume
		n++
	}
	return r, n
}

func ohlcvOf(b tickflow.Bar) ohlcv { return ohlcv{b.Open, b.High, b.Low, b.Close, b.Volume} }

// ── 零值 ──

// guard: AggRule 零值不合法（用户 2026-09-18 裁 U1：不设默认）—— Bounds 与 Aggregate 都报 ErrAggRuleUnset；
// 未知值也报错，但不 Is 那个哨兵（「造了个不存在的值」≠「没选」）。
func TestAggRuleZeroIsRejected(t *testing.T) {
	day, tmpl := mustDay(t, synthDays, keyAU, 20260907)
	bars := synthDay(day, nil)
	p := tickflow.MustIntraday(60)
	// 判别符在前：同一份输入，选了规则就不报错、且产出根 —— 否则下面的「报错」可能只是输入坏了
	for _, r := range []tickflow.AggRule{tickflow.AggTradingAxis, tickflow.AggClockGrid} {
		out, err := tickflow.Aggregate(r, p, tmpl, day, bars)
		if err != nil || len(out) == 0 {
			t.Fatalf("对照失败：%s 在同一份输入上 (err=%v, %d 根)，期望 (nil, >0)", r, err, len(out))
		}
	}
	var zero tickflow.AggRule
	if _, err := zero.Bounds(p, tmpl, day); !errors.Is(err, tickflow.ErrAggRuleUnset) {
		t.Errorf("零值 Bounds：err=%v，应 errors.Is ErrAggRuleUnset", err)
	}
	if out, err := tickflow.Aggregate(zero, p, tmpl, day, bars); !errors.Is(err, tickflow.ErrAggRuleUnset) {
		t.Errorf("零值 Aggregate：(err=%v, %d 根)，应 errors.Is ErrAggRuleUnset —— 零值被当成了某个默认口径", err, len(out))
	}
	_, err := tickflow.AggRule(99).Bounds(p, tmpl, day)
	if err == nil || errors.Is(err, tickflow.ErrAggRuleUnset) {
		t.Errorf("未知值 AggRule(99)：err=%v，应报错且不 Is ErrAggRuleUnset", err)
	}
}

// ── 甲：交易时间轴 vs 新浪（真实数据） ──

var sina635MD5 = map[string]string{
	"AU2612_5m.jsonp":  "57cc36b31d67e75ad5f0e6e8a47359d1",
	"AU2612_15m.jsonp": "73c7abaf64e029efddf5bc90a3e69f7b",
	"AU2612_30m.jsonp": "81bd248df62fe3596abd4579c1de377a",
	"AU2612_60m.jsonp": "c792621bc29d5b1b831229aeb2158f36",
	"AU2612_1d.jsonp":  "5d13944a6fba4d282a43819fbb286592",
	"RB2601_5m.jsonp":  "418dd9408f24acc6fce09514ebe28ab5",
	"RB2601_15m.jsonp": "d527f81e940c931d0ae1276f2c10d946",
	"RB2601_30m.jsonp": "5e207b56f383613f735a81b6508f7513",
	"RB2601_60m.jsonp": "49ffe2e26445f3a966af4723403b581f",
	"RB2601_1d.jsonp":  "8a95199f51f933a5e2e22c55439b652d",
}

// guard: testdata/sina635 是 6.35 那次取数的原样字节 —— 改了或换了，先红（README 那张表）。
func TestSina635FixturesUntouched(t *testing.T) {
	ents, err := os.ReadDir(filepath.Join("testdata", "sina635"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".jsonp") {
			continue
		}
		n++
		want, ok := sina635MD5[e.Name()]
		if !ok {
			t.Errorf("testdata/sina635 多了一份没登记的 %s", e.Name())
			continue
		}
		b, _ := os.ReadFile(filepath.Join("testdata", "sina635", e.Name()))
		sum := md5.Sum(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s md5 %s，登记的是 %s", e.Name(), got, want)
		}
	}
	if n != len(sina635MD5) {
		t.Errorf("testdata/sina635 有 %d 份 .jsonp，登记了 %d 份", n, len(sina635MD5))
	}
}

func sinaRows(t *testing.T, name string) []map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sina635", name))
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(b, []byte("var _=("))
	j := bytes.LastIndex(b, []byte(");"))
	if i < 0 || j <= i {
		t.Fatalf("%s 不是预期的 JSONP", name)
	}
	var r []map[string]string
	if err := json.Unmarshal(b[i+len("var _=("):j], &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func sinaNum(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("%q 不是数", s)
	}
	return v
}

// sinaMinute 读一份新浪分钟线：标签是收盘时刻（自然日）；交易日按「标签 − 1 毫秒」问日历（6.35 第六节）。
// 日历答不了的根（窗口外）丢掉。
func sinaMinute(t *testing.T, cal *embedded.Calendar, k tickflow.ProductKey, sym string, p int) map[tickflow.TradingDay][]tickflow.Bar {
	t.Helper()
	out := map[tickflow.TradingDay][]tickflow.Bar{}
	for _, r := range sinaRows(t, fmt.Sprintf("%s_%dm.jsonp", sym, p)) {
		ts, err := time.ParseInLocation("2006-01-02 15:04:05", r["d"], cst)
		if err != nil {
			t.Fatalf("分钟线时刻 %q：%v", r["d"], err)
		}
		label := ts.UnixMilli()
		d, err := cal.DayAt(k, label-1)
		if err != nil {
			continue
		}
		out[d.Num] = append(out[d.Num], tickflow.Bar{
			Ts: label - int64(p)*60000, TsEnd: label, TradingDay: d.Num,
			Open: sinaNum(t, r["o"]), High: sinaNum(t, r["h"]), Low: sinaNum(t, r["l"]), Close: sinaNum(t, r["c"]),
			Volume: sinaNum(t, r["v"]), OpenInterest: sinaNum(t, r["p"]), Settle: math.NaN(),
		})
	}
	for d := range out {
		bs := out[d]
		sort.Slice(bs, func(i, j int) bool { return bs[i].TsEnd < bs[j].TsEnd })
	}
	return out
}

func sinaDays(t *testing.T, sym string) []tickflow.TradingDay {
	t.Helper()
	var out []tickflow.TradingDay
	for _, r := range sinaRows(t, sym+"_1d.jsonp") {
		d, err := strconv.Atoi(strings.ReplaceAll(r["d"], "-", ""))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tickflow.TradingDay(d))
	}
	return out
}

// guard: 甲 —— AggTradingAxis 由新浪 5m 聚合出的 15 / 30 / 60m：标签与新浪逐根相等（48 组），数值与朴素参考逐根相等，
// 齐全日上一根 FlagPartial 都没有（规则三在真实数据上）。新浪自己的高周期数值不当判据（6.35：它自己不自洽）。
func TestAggTradingAxisMatchesSina635(t *testing.T) {
	type ct struct {
		sym string
		key tickflow.ProductKey
	}
	groups, auDays, valueBars := 0, 0, 0
	spanBreak, spanLunch := 0, 0
	var labelBad, valueBad, partial []string
	for _, c := range []ct{{"RB2601", keyRB}, {"AU2612", keyAU}} {
		cal, err := embedded.New(sinaDays(t, c.sym))
		if err != nil {
			t.Fatal(err)
		}
		byP := map[int]map[tickflow.TradingDay][]tickflow.Bar{}
		for _, p := range []int{5, 15, 30, 60} {
			byP[p] = sinaMinute(t, cal, c.key, c.sym, p)
		}
		var dns []tickflow.TradingDay
		for d := range byP[5] {
			dns = append(dns, d)
		}
		sort.Slice(dns, func(i, j int) bool { return dns[i] < dns[j] })
		for _, dn := range dns {
			day, err := cal.DayOf(c.key, dn)
			if err != nil {
				continue
			}
			tmpl, err := cal.Template(c.key, dn)
			if err != nil {
				t.Fatal(err)
			}
			five := byP[5][dn]
			if len(five)*5 != day.Minutes() || len(byP[15][dn]) == 0 || len(byP[30][dn]) == 0 || len(byP[60][dn]) == 0 {
				continue // 不齐（6.35 读数二：窗口起点，与 RB 到期前九天）
			}
			if c.sym == "AU2612" {
				auDays++
			}
			for _, p := range []int{15, 30, 60} {
				groups++
				got, err := tickflow.Aggregate(tickflow.AggTradingAxis, tickflow.MustIntraday(p), tmpl, day, five)
				if err != nil {
					t.Fatalf("%s %s %dm：%v", c.sym, dn, p, err)
				}
				sina := byP[p][dn]
				ok := len(got) == len(sina)
				for i := 0; ok && i < len(got); i++ {
					ok = got[i].TsEnd == sina[i].TsEnd
				}
				if !ok {
					labelBad = append(labelBad, fmt.Sprintf("%s %s %dm（本库 %d 根 · 新浪 %d 根）", c.sym, dn, p, len(got), len(sina)))
				}
				nv := naiveAxisLabels(day, tmpl.NightMinutes(), p)
				prev := int64(math.MinInt64)
				for i, b := range got {
					if b.Flags.Has(tickflow.FlagPartial) {
						partial = append(partial, fmt.Sprintf("%s %s %dm %s", c.sym, dn, p, hm(b.TsEnd)))
					}
					if i >= len(nv) || b.TsEnd != nv[i] {
						valueBad = append(valueBad, fmt.Sprintf("%s %s %dm 第 %d 根标签与朴素参考不同", c.sym, dn, p, i))
						break
					}
					ref, n := naiveSynth(five, prev, nv[i])
					valueBars++
					if n == 0 || ref != ohlcvOf(b) {
						valueBad = append(valueBad, fmt.Sprintf("%s %s %dm %s：本库 %+v · 朴素 %+v", c.sym, dn, p, hm(b.TsEnd), ohlcvOf(b), ref))
					}
					if p == 60 && prev != math.MinInt64 {
						y, mo, dd := time.UnixMilli(b.TsEnd).In(cst).Date()
						if brk := time.Date(y, mo, dd, 10, 20, 0, 0, cst).UnixMilli(); prev < brk && brk < b.TsEnd {
							spanBreak++
						}
						if lunch := time.Date(y, mo, dd, 12, 0, 0, 0, cst).UnixMilli(); prev < lunch && lunch < b.TsEnd {
							spanLunch++
						}
					}
					prev = nv[i]
				}
			}
		}
	}
	// 判别力在前（design.md 甲 ⚠️）：夜盘不整除的品种有齐全日 · 60m 跨小节休息 · 60m 跨午休；以及组数没变
	t.Logf("齐全组 %d（AU 齐全日 %d）· 值比较 %d 根 · 60m 跨 10:15–10:30 %d 根 · 跨午休 %d 根", groups, auDays, valueBars, spanBreak, spanLunch)
	if auDays == 0 || spanBreak == 0 || spanLunch == 0 {
		t.Fatalf("判别力不在场：AU 齐全日 %d · 跨小节休息 %d · 跨午休 %d —— 这一格只剩「整点切分」", auDays, spanBreak, spanLunch)
	}
	if groups != 48 {
		t.Fatalf("齐全组 %d，6.35 读数是 48 —— fixture 或「齐全」判法变了，先查清再改这个数", groups)
	}
	if len(labelBad) > 0 {
		t.Errorf("标签与新浪不等 %d 组：%v", len(labelBad), labelBad)
	}
	if len(valueBad) > 0 {
		t.Errorf("数值与朴素参考不等 %d 处：%v", len(valueBad), valueBad)
	}
	if len(partial) > 0 {
		t.Errorf("齐全日上出现 FlagPartial %d 根（规则三）：%v", len(partial), partial)
	}
}

// ── 时钟网格（合成数据；U3：没有真实行情对照，标签对的是 probe.md「天勤 = 纯时钟网格」那一节的文字读数） ──

func dayLabels(bs []tickflow.BarBound, from int64, useOpen bool) []string {
	var out []string
	for _, b := range bs {
		if b.Open < from {
			continue
		}
		x := b.Close
		if useOpen {
			x = b.Open
		}
		out = append(out, time.UnixMilli(x).In(cst).Format("15:04"))
	}
	return out
}

// guard: AggClockGrid 的日盘标签（开盘时刻）与 probe.md 记下的天勤读数相同，且 au 与 rb 相同（时钟网格没有相位）；
// 30m 的 10:00 那根只装 15 分钟、10:30 那根装 30 分钟；数值与朴素的按钟点分组逐根相等。
// ⚠️ 射程：标签序列与「装几分钟」来自 probe.md 的文字读数（天勤 60m / 30m / 15m 标签），**没有真实行情数值对照**（U3）。
func TestAggClockGridMatchesShinnyReading(t *testing.T) {
	const d = tickflow.TradingDay(20260907)
	au, auT := mustDay(t, synthDays, keyAU, d)
	rb, rbT := mustDay(t, synthDays, keyRB, d)
	nine := at(d, 9, 0)

	// 判别力在前：交易时间轴上 au 与 rb 的 60m 日盘标签不同（相位），否则「时钟网格 au ＝ rb」判不出任何东西
	ax1, _ := tickflow.AggTradingAxis.Bounds(tickflow.MustIntraday(60), auT, au)
	ax2, _ := tickflow.AggTradingAxis.Bounds(tickflow.MustIntraday(60), rbT, rb)
	if strings.Join(dayLabels(ax1, nine, false), " ") == strings.Join(dayLabels(ax2, nine, false), " ") {
		t.Fatalf("判别力不在场：交易时间轴上 au 与 rb 的 60m 日盘标签相同 %v —— 相位没进来", dayLabels(ax1, nine, false))
	}

	want := map[int]string{
		60: "09:00 10:00 11:00 13:00 14:00",
		30: "09:00 09:30 10:00 10:30 11:00 13:30 14:00 14:30",
		15: "09:00 09:15 09:30 09:45 10:00 10:30 10:45 11:00 11:15 13:30 13:45 14:00 14:15 14:30 14:45",
	}
	for _, p := range []int{60, 30, 15} {
		for _, c := range []struct {
			name string
			day  tickflow.Day
			tmpl tickflow.SessionTemplate
		}{{"au", au, auT}, {"rb", rb, rbT}} {
			bs, err := tickflow.AggClockGrid.Bounds(tickflow.MustIntraday(p), c.tmpl, c.day)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(dayLabels(bs, nine, true), " "); got != want[p] {
				t.Errorf("%s %dm 日盘标签 %q，天勤读数 %q", c.name, p, got, want[p])
			}
			if p == 30 {
				for _, b := range bs {
					lbl := time.UnixMilli(b.Open).In(cst).Format("15:04")
					if b.Open >= nine && (lbl == "10:00" && b.Minutes(c.day) != 15 || lbl == "10:30" && b.Minutes(c.day) != 30) {
						t.Errorf("%s 30m %s 那根装 %d 分钟（天勤读数：10:00 装 15、10:30 装 30）", c.name, lbl, b.Minutes(c.day))
					}
				}
			}
		}
	}

	// 数值：朴素的按钟点分组（北京时间零点对齐，floor），不调 agg.go
	bars := synthDay(au, nil)
	got, err := tickflow.Aggregate(tickflow.AggClockGrid, tickflow.MustIntraday(60), auT, au, bars)
	if err != nil {
		t.Fatal(err)
	}
	type cell struct {
		key int64
		v   ohlcv
		n   int
	}
	var ref []cell
	for _, b := range bars {
		k := time.UnixMilli(b.Ts).In(cst).Truncate(time.Hour).UnixMilli()
		if len(ref) == 0 || ref[len(ref)-1].key != k {
			ref = append(ref, cell{key: k, v: ohlcv{o: b.Open, h: b.High, l: b.Low}})
		}
		r := &ref[len(ref)-1]
		r.v.h, r.v.l, r.v.c = math.Max(r.v.h, b.High), math.Min(r.v.l, b.Low), b.Close
		r.v.v += b.Volume
		r.n++
	}
	if len(got) != len(ref) {
		t.Fatalf("时钟网格 60m 出了 %d 根，朴素分组 %d 根", len(got), len(ref))
	}
	for i := range got {
		if got[i].Ts != ref[i].key || ohlcvOf(got[i]) != ref[i].v || got[i].Flags.Has(tickflow.FlagPartial) {
			t.Errorf("第 %d 根：本库 %s %+v flags=%b · 朴素 %s %+v", i, hm(got[i].Ts), ohlcvOf(got[i]), got[i].Flags, hm(ref[i].key), ref[i].v)
		}
	}
}

// guard: 时钟网格只收整除 480 分钟的周期（对齐零点未验：北京零点与 UTC 零点差 480 分钟，整除时两者切出同一套格子）——
// 90m / 180m 报错，不替未验的那一问选答案；交易时间轴不受这条限制。
func TestAggClockGridRejectsUnalignedPeriod(t *testing.T) {
	day, tmpl := mustDay(t, synthDays, keyAU, 20260907)
	// 判别力在前：同一天 60m / 120m / 240m 收下（整除 480），交易时间轴 90m 也收下
	for _, p := range []int{60, 120, 240} {
		if _, err := tickflow.AggClockGrid.Bounds(tickflow.MustIntraday(p), tmpl, day); err != nil {
			t.Fatalf("对照失败：时钟网格 %dm（整除 480）报错 %v", p, err)
		}
	}
	if _, err := tickflow.AggTradingAxis.Bounds(tickflow.MustIntraday(90), tmpl, day); err != nil {
		t.Fatalf("对照失败：交易时间轴 90m 报错 %v", err)
	}
	for _, p := range []int{90, 180} {
		if bs, err := tickflow.AggClockGrid.Bounds(tickflow.MustIntraday(p), tmpl, day); err == nil {
			t.Errorf("时钟网格 %dm（不整除 480）没有报错，给出了 %d 格 —— 对齐零点没验，不该替它选", p, len(bs))
		}
	}
}

// ── 丙：停夜盘的那一夜（合成，6.24 / 6.25 那种形状：日历说有夜盘、1m 夜盘一根都没有） ──

// noNight 造一天：日历（embedded）说有夜盘，而 1m 只有日盘的根。
func noNight(t *testing.T) (tickflow.Day, tickflow.SessionTemplate, []tickflow.Bar, int64) {
	t.Helper()
	const d = tickflow.TradingDay(20260907)
	day, tmpl := mustDay(t, synthDays, keyAU, d)
	nightEnd := int64(0)
	for _, s := range day.Sessions {
		if s.End <= at(d, 9, 0) {
			nightEnd = s.End
		}
	}
	if nightEnd == 0 {
		t.Fatal("前提没成立：日历里这一天没有夜盘段 —— 造不出「日历说有、1m 没有」")
	}
	bars := synthDay(day, func(ts int64) bool { return ts < nightEnd })
	return day, tmpl, bars, nightEnd
}

// guard: 丙 规则一 —— 时间界整个落在「日历说有、1m 零根」那一段里的格子不产出根（不造 Volume 0 的空根）。
func TestAggRuleOneEmptyCellYieldsNoBar(t *testing.T) {
	day, tmpl, bars, nightEnd := noNight(t)
	for _, r := range []tickflow.AggRule{tickflow.AggTradingAxis, tickflow.AggClockGrid} {
		p := tickflow.MustIntraday(60)
		bs, _ := r.Bounds(p, tmpl, day)
		// 「整个落在夜盘里」按【交易分钟】判，不按格子边界 —— 判据不依赖格子 Close 怎么取
		//（第一版时钟网格的 Close 不裁，02:00 那格的边界越过了 02:30，按边界判会漏掉它）
		onlyNight := func(open, close int64) bool {
			if close <= nightEnd {
				return true
			}
			return tickflow.BarBound{Open: max(open, nightEnd), Close: close}.Minutes(day) == 0
		}
		inNight := 0
		for _, b := range bs {
			if onlyNight(b.Open, b.Close) {
				inNight++
			}
		}
		// 判别力在前：确实有格子整个落在夜盘里
		if inNight == 0 {
			t.Fatalf("%s：判别力不在场 —— 没有格子整个落在夜盘里", r)
		}
		got, err := tickflow.Aggregate(r, p, tmpl, day, bars)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range got {
			if onlyNight(b.Ts, b.TsEnd) {
				t.Errorf("%s：夜盘里的格子 %s–%s 产出了根（V=%v）—— 规则一：零根格子不出根", r, hm(b.Ts), hm(b.TsEnd), b.Volume)
			}
		}
		if len(got) != len(bs)-inNight {
			t.Errorf("%s：出了 %d 根，期望 %d（格子 %d − 夜盘里的 %d）", r, len(got), len(bs)-inNight, len(bs), inNight)
		}
	}
}

// guard: 丙 规则二 —— 格子里有根、而时间界里有日历说有却没有根的分钟 ⇒ 产出根，带 FlagPartial。
// 形状：au 交易时间轴 60m 的首根日盘格子 ＝ 夜盘尾 02:00–02:30 ＋ 日盘头 09:00–09:30；夜盘没根 ⇒ 只有 30 分钟有根。
// ⚠️ 规则二只对「零成交的分钟也给占位根」的源成立：天勤给（probe.md 6.20 E2）；新浪很可能不给（6.35 五之二，推论）。
func TestAggRuleTwoPartialCellIsFlagged(t *testing.T) {
	day, tmpl, bars, nightEnd := noNight(t)
	got, err := tickflow.Aggregate(tickflow.AggTradingAxis, tickflow.MustIntraday(60), tmpl, day, bars)
	if err != nil {
		t.Fatal(err)
	}
	var straddle *tickflow.Bar
	for i := range got {
		if got[i].Ts < nightEnd && got[i].TsEnd > nightEnd {
			straddle = &got[i]
		}
	}
	// 判别力在前：真有一根横跨夜盘尾与日盘头，且它的时间界里夜盘那部分没有根
	if straddle == nil {
		t.Fatal("判别力不在场：没有横跨夜盘尾与日盘头的根（相位没进来？）")
	}
	bd := tickflow.BarBound{Open: straddle.Ts, Close: straddle.TsEnd}
	nightPart := tickflow.BarBound{Open: straddle.Ts, Close: nightEnd}.Minutes(day)
	if nightPart == 0 || straddle.Volume == 0 {
		t.Fatalf("判别力不在场：横跨那根 %s–%s 夜盘部分 %d 分钟、V=%v", hm(straddle.Ts), hm(straddle.TsEnd), nightPart, straddle.Volume)
	}
	if !straddle.Flags.Has(tickflow.FlagPartial) {
		t.Errorf("横跨那根 %s–%s（格子 %d 分钟，其中夜盘 %d 分钟没有根）没带 FlagPartial —— 规则二：缺失藏进了一根看起来完整的根",
			hm(straddle.Ts), hm(straddle.TsEnd), bd.Minutes(day), nightPart)
	}
}

// guard: 丙 规则三 —— 时间界里日历说有的分钟全都有根 ⇒ 不带 FlagPartial（标记不从别的格子漏过来）；
// 对照：同一天夜盘齐全时，横跨那根也不带（规则二管的是「缺分钟」，不是「跨时段」）。
func TestAggRuleThreeCompleteCellIsNotFlagged(t *testing.T) {
	day, tmpl, bars, nightEnd := noNight(t)
	got, err := tickflow.Aggregate(tickflow.AggTradingAxis, tickflow.MustIntraday(60), tmpl, day, bars)
	if err != nil {
		t.Fatal(err)
	}
	whole, partial := 0, 0
	for _, b := range got {
		if b.Ts >= nightEnd {
			whole++
			if b.Flags.Has(tickflow.FlagPartial) {
				t.Errorf("整个落在日盘、底层完整的 %s–%s 带了 FlagPartial —— 规则三", hm(b.Ts), hm(b.TsEnd))
			}
		} else if b.Flags.Has(tickflow.FlagPartial) {
			partial++
		}
	}
	// 判别力：同一天里真有一根带 FlagPartial（否则「其余不带」可能只是这套实现从来不标）
	if whole == 0 || partial == 0 {
		t.Fatalf("判别力不在场：日盘完整格子 %d 根 · 带 FlagPartial 的 %d 根", whole, partial)
	}
	full, err := tickflow.Aggregate(tickflow.AggTradingAxis, tickflow.MustIntraday(60), tmpl, day, synthDay(day, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range full {
		if b.Flags.Has(tickflow.FlagPartial) {
			t.Errorf("夜盘齐全时 %s–%s 带了 FlagPartial —— 跨时段本身不是缺失", hm(b.Ts), hm(b.TsEnd))
		}
		if !b.Flags.Has(tickflow.FlagAggregated) {
			t.Errorf("%s 没带 FlagAggregated", hm(b.TsEnd))
		}
	}
}

// ── 周五夜盘跨零点到周一（合成，AU 形状） ──

// guard: 周一那个交易日的夜盘在上周五晚上、跨零点到周六 02:30；交易时间轴 60m 的首根日盘格子横跨周末
// （周六 02:00 → 周一 09:30），装满 60 分钟、不带 FlagPartial、交易日是周一；时钟网格下周六 00:00 / 01:00 / 02:00 三格
// 也都归周一，02:00 那格只装夜盘那 30 分钟、不带周一的根。
func TestAggFridayNightIntoMonday(t *testing.T) {
	const mon = tickflow.TradingDay(20260907)
	day, tmpl := mustDay(t, synthDays, keyAU, mon)
	fri21, sat00, sat02, sat0230, mon09, mon0930 := at(20260904, 21, 0), at(20260905, 0, 0), at(20260905, 2, 0), at(20260905, 2, 30), at(mon, 9, 0), at(mon, 9, 30)
	// 判别力在前：日历真把周五晚上的夜盘给了周一，且夜盘跨过了周六零点
	if len(day.Sessions) == 0 || day.Sessions[0].Start != fri21 {
		t.Fatalf("判别力不在场：周一的首段不是周五 21:00（%v）", day.Sessions)
	}
	crosses := false
	for _, s := range day.Sessions {
		if s.Start < sat00 && s.End > sat00 {
			crosses = true
		}
	}
	if !crosses {
		t.Fatal("判别力不在场：夜盘没有跨过周六零点")
	}
	bars := synthDay(day, nil)

	ax, err := tickflow.Aggregate(tickflow.AggTradingAxis, tickflow.MustIntraday(60), tmpl, day, bars)
	if err != nil {
		t.Fatal(err)
	}
	var wk *tickflow.Bar
	for i := range ax {
		if ax[i].TradingDay != mon {
			t.Errorf("交易时间轴第 %d 根交易日 %s，应为周一 %s", i, ax[i].TradingDay, mon)
		}
		if ax[i].Ts == sat02 {
			wk = &ax[i]
		}
	}
	if wk == nil {
		t.Fatalf("交易时间轴没有从周六 02:00 起的那根（横跨周末）")
	}
	ref, n := naiveSynth(bars, sat02, mon0930)
	if wk.TsEnd != mon0930 || n != 60 || ohlcvOf(*wk) != ref || wk.Flags.Has(tickflow.FlagPartial) {
		t.Errorf("横跨周末那根：%s–%s %+v flags=%b · 期望 %s–%s，60 根合成 %+v（实数 %d）、不带 FlagPartial",
			hm(wk.Ts), hm(wk.TsEnd), ohlcvOf(*wk), wk.Flags, hm(sat02), hm(mon0930), ref, n)
	}

	cg, err := tickflow.Aggregate(tickflow.AggClockGrid, tickflow.MustIntraday(60), tmpl, day, bars)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]tickflow.Bar{}
	for _, b := range cg {
		if b.TradingDay != mon {
			t.Errorf("时钟网格 %s 交易日 %s，应为周一", hm(b.Ts), b.TradingDay)
		}
		seen[b.Ts] = b
	}
	for _, k := range []int64{sat00, at(20260905, 1, 0), sat02, mon09} {
		if _, ok := seen[k]; !ok {
			t.Errorf("时钟网格缺 %s 那一格", hm(k))
		}
	}
	if b := seen[sat02]; b.Volume != func() float64 { r, _ := naiveSynth(bars, sat02, sat0230); return r.v }() || b.Flags.Has(tickflow.FlagPartial) {
		t.Errorf("时钟网格周六 02:00 那格 V=%v flags=%b，应只装 02:00–02:30 那 30 根、不带 FlagPartial", b.Volume, b.Flags)
	}
}

// ── 评审方 2026-09-18 对 c22ef24 补的五格（突变 R2 / R3 / R4 / R7 / R8 当时没红）与时钟网格 Close ──

func findBound(bs []tickflow.BarBound, open int64) (tickflow.BarBound, bool) {
	for _, b := range bs {
		if b.Open == open {
			return b, true
		}
	}
	return tickflow.BarBound{}, false
}

// guard: 时钟网格的 Close ＝ 格子里最后一个交易分钟的结束时刻（按日历时段，不按数据）——
// 60m 11:00 那格收 11:30、夜盘 02:00 那格收 02:30、10:00 那格仍收 11:00（对照）；30m 10:00 那格收 10:15；
// 11:15–11:30 缺根时 11:00 那根的 TsEnd 仍是 11:30（带 FlagPartial）。
func TestAggClockGridCloseIsLastTradingMinute(t *testing.T) {
	const d = tickflow.TradingDay(20260907)
	day, tmpl := mustDay(t, synthDays, keyAU, d)
	sat := tickflow.TradingDay(20260905)
	b60, err := tickflow.AggClockGrid.Bounds(tickflow.MustIntraday(60), tmpl, day)
	if err != nil {
		t.Fatal(err)
	}
	b30, err := tickflow.AggClockGrid.Bounds(tickflow.MustIntraday(30), tmpl, day)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		bs          []tickflow.BarBound
		step        int64 // 格子长度（毫秒）
		open, close int64
	}{
		{"60m 10:00（对照：格子终点就是交易终点）", b60, 3600000, at(d, 10, 0), at(d, 11, 0)},
		{"60m 11:00", b60, 3600000, at(d, 11, 0), at(d, 11, 30)},
		{"60m 夜盘 02:00", b60, 3600000, at(sat, 2, 0), at(sat, 2, 30)},
		{"30m 10:00", b30, 1800000, at(d, 10, 0), at(d, 10, 15)},
	}
	// 判别力在前：后三格的期望收盘确实早于格子终点（否则「裁没裁」判不出来）；对照格两者相等
	for i, c := range cases {
		if cut := c.close < c.open+c.step; cut != (i > 0) {
			t.Fatalf("判别力不在场：%s 期望收盘 %s、格子终点 %s", c.name, hm(c.close), hm(c.open+c.step))
		}
	}
	for _, c := range cases {
		b, ok := findBound(c.bs, c.open)
		if !ok {
			t.Errorf("%s：没有这一格", c.name)
			continue
		}
		if b.Close != c.close {
			t.Errorf("%s：Close %s，应为 %s（格子里最后一个交易分钟的结束时刻）", c.name, hm(b.Close), hm(c.close))
		}
	}
	// 按日历、不按数据：11:15–11:30 那 15 根不给，11:00 那根的 TsEnd 仍是 11:30
	bars := synthDay(day, func(ts int64) bool { return ts >= at(d, 11, 15) && ts < at(d, 11, 30) })
	got, err := tickflow.Aggregate(tickflow.AggClockGrid, tickflow.MustIntraday(60), tmpl, day, bars)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range got {
		if b.Ts == at(d, 11, 0) {
			found = true
			if b.TsEnd != at(d, 11, 30) || !b.Flags.Has(tickflow.FlagPartial) {
				t.Errorf("11:00 那根缺尾 15 分钟：TsEnd %s flags=%b，应为 11:30 且带 FlagPartial —— TsEnd 不许跟着数据走", hm(b.TsEnd), b.Flags)
			}
		}
	}
	if !found {
		t.Error("缺尾那天没有 11:00 那根")
	}
}

// guard: Turnover 求和、OpenInterest 取末根（6.35 的对照只比 OHLCV，新浪 Turnover 是 NaN、OI 不在比较里 ⇒ 这两条原来没人守）。
func TestAggTurnoverSumsAndOITakesLast(t *testing.T) {
	day, tmpl := mustDay(t, synthDays, keyAU, 20260907)
	bars := synthDay(day, nil)
	for i := range bars {
		bars[i].Turnover = float64(1000 + 37*i%101) // 有限、各根不同
	}
	for _, r := range []tickflow.AggRule{tickflow.AggTradingAxis, tickflow.AggClockGrid} {
		got, err := tickflow.Aggregate(r, tickflow.MustIntraday(60), tmpl, day, bars)
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, b := range got {
			var in []tickflow.Bar
			for _, x := range bars {
				if x.Ts >= b.Ts && x.TsEnd <= b.TsEnd {
					in = append(in, x)
				}
			}
			// 判别力：格子里至少两根，且首末 OI 不同（否则「取末根」与「取首根」分不开）
			if len(in) < 2 || in[0].OpenInterest == in[len(in)-1].OpenInterest {
				continue
			}
			checked++
			sum := 0.0
			for _, x := range in {
				sum += x.Turnover
			}
			if b.Turnover != sum {
				t.Errorf("%s %s：Turnover %v，应为 %d 根之和 %v", r, hm(b.Ts), b.Turnover, len(in), sum)
			}
			if b.OpenInterest != in[len(in)-1].OpenInterest {
				t.Errorf("%s %s：OpenInterest %v，应取末根 %v（首根 %v）", r, hm(b.Ts), b.OpenInterest, in[len(in)-1].OpenInterest, in[0].OpenInterest)
			}
		}
		if checked == 0 {
			t.Fatalf("%s：判别力不在场 —— 没有一格装了两根以上且首末 OI 不同", r)
		}
	}
}

// guard: 输入自带的 FlagPartial 往上传 —— 格子本身装满（规则二不触发）、而其中一根输入带 FlagPartial ⇒ 输出带。
func TestAggInputPartialPropagates(t *testing.T) {
	const d = tickflow.TradingDay(20260907)
	day, tmpl := mustDay(t, synthDays, keyAU, d)
	bars := synthDay(day, nil)
	target := at(d, 13, 45) // 日盘里一个装满的格子中间
	p := tickflow.MustIntraday(60)
	cellOf := func(out []tickflow.Bar) *tickflow.Bar {
		for i := range out {
			if out[i].Ts <= target && target < out[i].TsEnd {
				return &out[i]
			}
		}
		return nil
	}
	// 对照在前：不打标时那一格不带 FlagPartial（格子装满，规则二不触发）
	clean, err := tickflow.Aggregate(tickflow.AggTradingAxis, p, tmpl, day, bars)
	if err != nil {
		t.Fatal(err)
	}
	if c := cellOf(clean); c == nil || c.Flags.Has(tickflow.FlagPartial) {
		t.Fatalf("对照失败：干净输入上那一格 %v", c)
	}
	marked := 0
	for i := range bars {
		if bars[i].Ts == target {
			bars[i].Flags |= tickflow.FlagPartial
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("前提没成立：打标了 %d 根", marked)
	}
	got, err := tickflow.Aggregate(tickflow.AggTradingAxis, p, tmpl, day, bars)
	if err != nil {
		t.Fatal(err)
	}
	if c := cellOf(got); c == nil || !c.Flags.Has(tickflow.FlagPartial) {
		t.Errorf("输入里一根带 FlagPartial，而装它的那一格 %v 没带 —— 不完整没往上传", c)
	}
}

// guard: 输入重叠或乱序 ⇒ Aggregate 报错（「不满足就报错，不猜」）。
func TestAggRejectsOverlapOrDisorder(t *testing.T) {
	day, tmpl := mustDay(t, synthDays, keyAU, 20260907)
	bars := synthDay(day, nil)
	p := tickflow.MustIntraday(60)
	// 对照在前：原样输入不报错
	if _, err := tickflow.Aggregate(tickflow.AggTradingAxis, p, tmpl, day, bars); err != nil {
		t.Fatalf("对照失败：干净输入报错 %v", err)
	}
	dup := append(append(append([]tickflow.Bar{}, bars[:100]...), bars[99]), bars[100:]...) // 第 99 根重复一次 ⇒ 重叠
	swap := append([]tickflow.Bar{}, bars...)
	swap[200], swap[201] = swap[201], swap[200] // 相邻两根对调 ⇒ 乱序
	for name, in := range map[string][]tickflow.Bar{"重叠": dup, "乱序": swap} {
		if out, err := tickflow.Aggregate(tickflow.AggTradingAxis, p, tmpl, day, in); err == nil {
			t.Errorf("%s的输入没有报错，出了 %d 根", name, len(out))
		}
	}
}

// guard: 输入里有一根的 TradingDay 不是 d ⇒ Aggregate 报错（「不满足就报错，不猜」）。
func TestAggRejectsForeignTradingDay(t *testing.T) {
	day, tmpl := mustDay(t, synthDays, keyAU, 20260907)
	bars := synthDay(day, nil)
	p := tickflow.MustIntraday(60)
	if _, err := tickflow.Aggregate(tickflow.AggTradingAxis, p, tmpl, day, bars); err != nil {
		t.Fatalf("对照失败：干净输入报错 %v", err)
	}
	bars[150].TradingDay = 20260908
	if out, err := tickflow.Aggregate(tickflow.AggTradingAxis, p, tmpl, day, bars); err == nil {
		t.Errorf("有一根属于 20260908，而 Aggregate(20260907) 没有报错，出了 %d 根", len(out))
	}
}
