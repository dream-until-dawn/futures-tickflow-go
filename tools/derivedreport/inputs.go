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
		// ⛔ .dat 有记录而 .meta 不在 ⇒ 孤儿记录（盘上有根、没有记账）—— 评审方 L2：
		// 原来会被当成「一段 coverage 都没有」⇒ 全是尾部未登记 ⇒ 退出 3、处置「再同步一次」；
		// 而同步层面对的是一份记录已经在盘上的「新库」，照着做是错的 ⇒ 结构层不过，停。
		if _, err := os.Stat(filepath.Join(dir, "1m.meta")); os.IsNotExist(err) && fi.Size() > 0 {
			return lr, fmt.Errorf("%s：%s 有 %d 条记录而 1m.meta 不存在 —— 孤儿记录（盘上有根、没有记账），不是「还没同步」，停", sp.sym, dat, fi.Size()/segfile.RecordSize)
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
		// ⛔ Open 量到的两件，显式核（评审方 L1：原来工具从不看 OpenState）：
		//	LegacyMeta      .meta 没有 format —— v0.3 之前写的、语义未知的 coverage（D2a 要人决定）⇒ 不许当成「拉过」
		//	MissingRecords  coverage 声称的根比盘上多 ⇒ 数据缺了而账还在 —— 原来靠 Walk 碰巧红出来，这里写成显式的核
		ostate := st.OpenState()
		if ostate.LegacyMeta {
			st.Close()
			return lr, fmt.Errorf("%s：1m.meta 是旧 .meta（没有 format 字段）—— 那份 coverage 的语义未知，不许当成「拉过」，停；交给同步层（D2a）处置", sp.sym)
		}
		if ostate.MissingRecords > 0 {
			st.Close()
			return lr, fmt.Errorf("%s：coverage 声称的根比盘上少了 %d 条 —— 数据缺了而账还在，停；交给同步层处置", sp.sym, ostate.MissingRecords)
		}
		cov := st.Coverage()
		lr.coverage[sp.sym] = cov
		var bars []tickflow.Bar
		for _, c := range cov {
			// 每一段都整段 Walk、在回调里按窗口过滤：Walk 要求区间整个落在一段 coverage 里；
			// ⛔ 与窗口不相交的段也要走 —— 「最后一根」要按全部根算（见回调里那句）
			// ⚠️ 代价（评审方 2026-09-17）：segfile.Walk 没有 seek 索引、每次从文件头扫到尾 ⇒ 扫描次数 ＝ 段数 × 整个文件。
			//    今天每份合约通常只有一段，无所谓；段多了会线性变慢 —— 别以为这里只走了窗口内的段
			err := st.Walk(c.From, c.To, func(bar tickflow.Bar) bool {
				// ⛔ 最后一根按【全部根】算，不按窗口：到期放行要拿它去对「最后交易日」——
				// 窗口若整个落在到期之后，按窗口算它就是 0，到期合约的空档全被当成洞（整天无结论）
				if bar.TradingDay > lr.lastBar[sp.sym] {
					lr.lastBar[sp.sym] = bar.TradingDay
				}
				if bar.TradingDay < from || bar.TradingDay > to {
					return true
				}
				bars = append(bars, bar)
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
// nonight 非空 ⇒ 原样作为 embedded.NoNightAfter 注入（公告日期 X；X 的下一个交易日没有夜盘段）。
//
// ⚠️ 与 derived.NightObsOf 同一把尺子（开盘时刻按 CST 墙钟判时段）。
func flattenBase(k tickflow.ProductKey, days, nonight []tickflow.TradingDay, from, to tickflow.TradingDay) ([]derived.BaseDay, error) {
	var opts []embedded.Option
	if len(nonight) > 0 {
		opts = append(opts, embedded.NoNightAfter(nonight...))
	}
	cal, err := embedded.New(days, opts...)
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
