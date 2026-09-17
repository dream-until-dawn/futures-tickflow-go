package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/derived"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/continuous"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— 输入：注入的交易日表 · 每份合约的段 · 库 ——
//
// ⚠️ 段是**显式输入**：库里没有合约的上市日（登记㉔），6.27 的段来自 6.26 的普查 ⇒ 工具不自己猜。

// span 是一份合约在本次要看的那一段（来自段文件）。
type span struct {
	sym      tickflow.Symbol
	from, to tickflow.TradingDay
}

// readDays 读注入的交易日表：每行一个 YYYYMMDD，空行与 # 开头的行忽略；必须严格升序、不重复。
func readDays(path string) ([]tickflow.TradingDay, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	var out []tickflow.TradingDay
	for _, ln := range lines {
		d, err := parseDay(ln.text)
		if err != nil {
			return nil, fmt.Errorf("%s:%d：%w", path, ln.no, err)
		}
		if n := len(out); n > 0 && d <= out[n-1] {
			return nil, fmt.Errorf("%s:%d：%s 没有严格晚于上一行的 %s —— 交易日表必须升序且不重复", path, ln.no, d, out[n-1])
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s：一个交易日都没读到", path)
	}
	return out, nil
}

// readSpans 读段文件：每行「合约 段起 段止」，例如 `SHFE.rb2601 20250915 20260210`；合约不许重复。
func readSpans(path string) ([]span, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	var out []span
	seen := map[tickflow.Symbol]bool{}
	for _, ln := range lines {
		f := strings.Fields(ln.text)
		if len(f) != 3 {
			return nil, fmt.Errorf("%s:%d：要三栏「合约 段起 段止」，实得 %d 栏", path, ln.no, len(f))
		}
		sym, err := tickflow.ParseSymbol(f[0])
		if err != nil {
			return nil, fmt.Errorf("%s:%d：%w", path, ln.no, err)
		}
		from, err := parseDay(f[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d：%w", path, ln.no, err)
		}
		to, err := parseDay(f[2])
		if err != nil {
			return nil, fmt.Errorf("%s:%d：%w", path, ln.no, err)
		}
		if from > to {
			return nil, fmt.Errorf("%s:%d：段起 %s 晚于段止 %s", path, ln.no, from, to)
		}
		if seen[sym] {
			return nil, fmt.Errorf("%s:%d：合约 %s 重复", path, ln.no, sym)
		}
		seen[sym] = true
		out = append(out, span{sym: sym, from: from, to: to})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s：一份合约都没读到", path)
	}
	return out, nil
}

type line struct {
	no   int
	text string
}

func readLines(path string) ([]line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []line
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, line{no: n, text: t})
	}
	return out, sc.Err()
}

func parseDay(s string) (tickflow.TradingDay, error) {
	n, err := strconv.Atoi(s)
	if err != nil || len(s) != 8 {
		return 0, fmt.Errorf("「%s」不是 YYYYMMDD", s)
	}
	d := tickflow.TradingDay(n)
	if !d.Valid() {
		return 0, fmt.Errorf("「%s」不是合法的交易日", s)
	}
	return d, nil
}

// dayAgg 是某份合约某个交易日聚出来的那几个数（给 continuous.Build 的候选）。
type dayAgg struct {
	vol, oi float64
	firstTs int64
	last    tickflow.Bar
}

// libraryRead 是从库里读回来的全部东西。
type libraryRead struct {
	agg      map[tickflow.TradingDay]map[tickflow.Symbol]*dayAgg
	nights   []derived.NightObs
	coverage map[tickflow.Symbol][]tickflow.Span
	lastBar  map[tickflow.Symbol]tickflow.TradingDay
	modTime  map[tickflow.Symbol]time.Time // 库文件（.dat）的修改时刻 —— 报文头要印
}

// readLibrary 逐份合约打开「<库根>/<合约>」，按 coverage 逐段 Walk 回来：聚日（给 Build）＋ 夜盘观测（给 Judge）。
//
// ⛔ 只读：只调 Open / Coverage / Walk / Close；**不调任何写方法**（跑前跑后库文件 md5 不变，有一格测试断言）。
// ⚠️ 只取 [from, to] 窗口内的日子。
func readLibrary(root string, spans []span, from, to tickflow.TradingDay) (libraryRead, error) {
	lr := libraryRead{
		agg:      map[tickflow.TradingDay]map[tickflow.Symbol]*dayAgg{},
		coverage: map[tickflow.Symbol][]tickflow.Span{},
		lastBar:  map[tickflow.Symbol]tickflow.TradingDay{},
		modTime:  map[tickflow.Symbol]time.Time{},
	}
	for _, sp := range spans {
		dir := filepath.Join(root, sp.sym.String())
		if _, err := os.Stat(dir); err != nil {
			return lr, fmt.Errorf("%s：库目录 %s 打不开：%w", sp.sym, dir, err)
		}
		// ⛔ segfile.Open **不是只读的**（store/segfile/dat.go OpenDat）：O_RDWR|O_CREATE 打开 .dat，
		// 缺文件就新建、有残尾就截断 —— 工具在坏库上会先改库、再报错。
		// ⇒ 打开之前先自己核这两件，核不过就停，**不去碰它**（修一份残尾库是同步层的事，不是只读工具的事）。
		dat := filepath.Join(dir, "1m.dat")
		fi, err := os.Stat(dat)
		if err != nil {
			return lr, fmt.Errorf("%s：%s 不存在或读不了（打开它会新建文件 —— 工具只读，停）：%w", sp.sym, dat, err)
		}
		if _, ragged := segfile.TailCheck(fi.Size()); ragged != 0 {
			return lr, fmt.Errorf("%s：%s 有 %d 字节残尾（不足一条记录）—— 打开它会截断文件，工具只读，停；残尾交给同步层处置", sp.sym, dat, ragged)
		}
		lr.modTime[sp.sym] = fi.ModTime()
		st, trunc, err := segfile.Open(dir, tickflow.MustIntraday(1))
		if err != nil {
			return lr, fmt.Errorf("%s：开库失败：%w", sp.sym, err)
		}
		if trunc != 0 { // 上面已核过残尾；走到这里说明两次 Stat 之间文件变了（有人在写）
			st.Close()
			return lr, fmt.Errorf("%s：开库时截断了 %d 字节 —— 上面核的时候还没有残尾，库在工具运行时被改了，停", sp.sym, trunc)
		}
		cov := st.Coverage()
		lr.coverage[sp.sym] = cov
		var bars []tickflow.Bar
		for _, c := range cov {
			a, b := c.From, c.To
			if a < from {
				a = from
			}
			if b > to {
				b = to
			}
			if a > b {
				continue
			}
			// 整段 Walk、在回调里按窗口过滤：Walk 要求区间整个落在一段 coverage 里，整段最稳；段与窗口不相交的已在上面跳过
			err := st.Walk(c.From, c.To, func(bar tickflow.Bar) bool {
				if bar.TradingDay < from || bar.TradingDay > to {
					return true
				}
				bars = append(bars, bar)
				if bar.TradingDay > lr.lastBar[sp.sym] {
					lr.lastBar[sp.sym] = bar.TradingDay
				}
				m := lr.agg[bar.TradingDay]
				if m == nil {
					m = map[tickflow.Symbol]*dayAgg{}
					lr.agg[bar.TradingDay] = m
				}
				g := m[sp.sym]
				if g == nil {
					g = &dayAgg{firstTs: bar.Ts}
					m[sp.sym] = g
				}
				g.last = bar
				g.vol += bar.Volume
				g.oi = bar.OpenInterest
				return true
			})
			if err != nil {
				st.Close()
				return lr, fmt.Errorf("%s：Walk %s..%s 失败：%w", sp.sym, c.From, c.To, err)
			}
		}
		st.Close()
		lr.nights = append(lr.nights, derived.NightObsOf(sp.sym, bars)...)
	}
	return lr, nil
}

// buildMain 把聚好的日子拼成品种级主连（ByOIAndVolume ＋ 未复权，design.md 读数计划第②样）。
func buildMain(product string, agg map[tickflow.TradingDay]map[tickflow.Symbol]*dayAgg) (continuous.Continuous, error) {
	days := make([]tickflow.TradingDay, 0, len(agg))
	for d := range agg {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	in := make([]continuous.DayBars, 0, len(days))
	for _, d := range days {
		db := continuous.DayBars{Day: d, Bars: map[tickflow.Symbol]tickflow.Bar{}}
		for sym, g := range agg[d] {
			db.Cands = append(db.Cands, continuous.ContractDay{Symbol: sym, Day: d, Volume: g.vol, OpenInterest: g.oi, Expiry: continuous.ExpiryUnknown})
			bar := g.last
			bar.Ts = g.firstTs
			bar.Volume = g.vol
			db.Bars[sym] = bar
		}
		sort.Slice(db.Cands, func(i, j int) bool { return db.Cands[i].Symbol.YearMon < db.Cands[j].Symbol.YearMon })
		in = append(in, db)
	}
	return continuous.Build(continuous.ContinuousSpec{Product: product, Roll: continuous.ByOIAndVolume{}, Adjust: continuous.NoAdjust}, in)
}

// flattenBase 替 base 答完 [from, to] 这一段：注入表里落在窗口内的每个交易日，Night ＝ 首段起点落在 20:00 之后或 04:00 之前。
//
// ⚠️ 与 derived.NightObsOf 同一把尺子（开盘时刻按 CST 墙钟判时段）。
func flattenBase(k tickflow.ProductKey, days []tickflow.TradingDay, from, to tickflow.TradingDay) ([]derived.BaseDay, error) {
	cal, err := embedded.New(days)
	if err != nil {
		return nil, fmt.Errorf("用注入的交易日表造 base 日历失败：%w", err)
	}
	var out []derived.BaseDay
	for _, d := range days {
		if d < from || d > to {
			continue
		}
		day, err := cal.DayOf(k, d)
		if err != nil {
			return nil, fmt.Errorf("base 答不出 %s %s：%w", k, d, err)
		}
		night := false
		if len(day.Sessions) > 0 {
			h := time.UnixMilli(day.Sessions[0].Start).In(tickflow.CST).Hour()
			night = h >= 20 || h < 4
		}
		out = append(out, derived.BaseDay{Day: d, Night: night})
	}
	return out, nil
}
