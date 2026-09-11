package tickflow_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— 这一条守的是【一次完全成功的同步不报伪缺口】，以及它是【怎么】做到的 ——
//
// ⛔ 由来（2026-09-11 实测）：上一版这里钉的是**缺陷本身** ——
// 「多块同步会把刚拉好的整段报成『未走查』」。那颗钉子在 (o) 落地时如约响了，
// 于是按它报文里写死的红法二处置：**改这条测试，不是把断言改回去。**
//
// 病在**键**，不在集合：
//
//	写入侧  touched 里是【分块】的 span：{d1,d1,1,1} {d2,d2,1,1} {d3,d3,1,1}
//	读取侧  planGaps 走 Coverage()，而 CommitSpan 已把相邻块并成 {d1,d3,3,3}
//	🔴 Span 是四字段结构体 ⇒ map 键不相等，而两处代码都写着 verified[sp]
//
// ⚠️ 它不是理论缺陷：`source/cffexsource` 的 `BatchDays` 出厂就是 **1**
// ⇒ 那个源上任何跨天同步都中招；`source/sinasource` 是 `BatchDaysUnbounded`（永远一块）
// ⇒ **一个必然中招、一个必然不中招** —— 拿后者试的人什么都看不见。
// 📎 收：**一个缺陷的存活时间，等于「不触发它的那个配置」被用作默认值的时间。**
//
// —— ⛔ 三格标定：把【除块数以外】的变量各钉一次 ——
//
// 第一版对照组只有「三天三块」对「一天一块」——**区间长度跟着块数一起变了**。
// 📎 收：**两端标定不是「一个正例一个反例」，是把每一个你以为无关的变量各钉一次。**
// ⚠️ 而那一格读数当时就在我自己两小时前的探针输出里（「3 天 · BatchDays=30 ⇒ gaps=（空）」），
// 我没把它当成对照组用 ⇒ **「仓里已经记过那一格」最短的版本：「仓」是本轮自己的滚动条。**
//
// —— ⚠️ 第四格钉的是【修法的本体】，不是它的结果 ——
//
// 前三格只说「不报伪缺口」，而**一个什么都不走查的实现也能让它们全绿**。
// ⇒ 第四格喂一个**该红的库**（盘上一条全库坏记录）：
// 走查必须真的发生、必须按【合并段】的身份发生，报文里那个区间就是证据。
// 🔴 少了它，这条测试在「走查被整个关掉」这个方向上是瞎的。

// oneChunkPerDaySource 把 `BatchDays` 压成 1 —— 一天一块。
//
// ⚠️ 它只改这一个开关，**别的能力与 `seamSource` 逐字相同**：
// 前三格的全部判别力都压在「块数」这一个变量上。
type oneChunkPerDaySource struct{ seamSource }

func (s oneChunkPerDaySource) Caps(k tickflow.ProductKey) tickflow.Capabilities {
	c := s.seamSource.Caps(k)
	c.BatchDays = 1
	return c
}

// guard: 一次完全成功的多块同步不报伪「未走查」，而走查真的按【合并段】的身份发生。
func TestSuccessfulMultiChunkSyncReportsNoFalseGap(t *testing.T) {
	const (
		d1 = tickflow.TradingDay(20200805)
		d2 = tickflow.TradingDay(20200806)
		d3 = tickflow.TradingDay(20200807)
	)

	// run 跑一次同步。zeroRec 为真时，先往 .dat 里塞一条【全库】坏记录。
	run := func(t *testing.T, days []tickflow.TradingDay, oneDayBatch, zeroRec bool) (tickflow.SyncReport, []tickflow.Span) {
		t.Helper()
		cal, err := embedded.New(days)
		if err != nil {
			t.Fatalf("造日历失败：%v", err)
		}
		dir := t.TempDir()
		if zeroRec {
			seed, _, oerr := segfile.Open(dir, tickflow.Daily)
			if oerr != nil {
				t.Fatalf("开库失败：%v", oerr)
			}
			if cerr := seed.Close(); cerr != nil {
				t.Fatalf("关库失败：%v", cerr)
			}
			writeZeroRecord(t, dir)
		}
		store, truncated, err := segfile.Open(dir, tickflow.Daily)
		if err != nil {
			t.Fatalf("开库失败：%v", err)
		}
		// ⛔ 前提自检：坏记录留住了（`OpenDat` 会砍半截记录）／没塞时不该有残尾。
		if truncated != 0 {
			t.Fatalf("前提没成立：开库报了 %d 字节残尾 ⇒ 读数作废", truncated)
		}
		t.Cleanup(func() {
			if cerr := store.Close(); cerr != nil {
				t.Errorf("关库失败：%v", cerr)
			}
		})
		give := map[tickflow.TradingDay]bool{}
		for _, d := range days {
			give[d] = true
		}
		base := seamSource{give: give}
		var src tickflow.Source = base
		if oneDayBatch {
			src = oneChunkPerDaySource{base}
		}
		syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
			Calendar:  cal,
			Store:     store,
			NewSource: func(*http.Client) tickflow.Source { return src },
			Pacer:     pacing.NoPacing(),
			Timeout:   5 * time.Second,
		})
		if err != nil {
			t.Fatalf("造 Syncer 失败：%v", err)
		}
		rep, serr := syn.Sync(context.Background(), tickflow.SyncRequest{
			Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
			Period: tickflow.Daily,
			From:   days[0], To: days[len(days)-1],
		}, dayStartMs(20200901))
		if serr != nil {
			t.Fatalf("同步出错：%v", serr)
		}
		// ⛔ 前提自检：这一跑必须是【完全成功】的，且落盘条数对得上。
		// 少了它，下面每一格都可能在量「halt 之后的报告」，而那是另一个缺陷。
		if rep.Halt != tickflow.HaltDone {
			t.Fatalf("前提没成立：Halt=%v（要「跑完」）⇒ 读数作废", rep.Halt)
		}
		if rep.Bars != len(days) {
			t.Fatalf("前提没成立：落盘 %d 条，期望 %d 条 ⇒ 读数作废", rep.Bars, len(days))
		}
		return rep, store.Coverage()
	}

	fmtGaps := func(rep tickflow.SyncReport) string {
		var out []string
		for _, g := range rep.Gaps {
			out = append(out, g.From.String()+".."+g.To.String()+"="+g.Kind.String())
		}
		if len(out) == 0 {
			return "（空）"
		}
		return strings.Join(out, " ")
	}
	noGap := func(t *testing.T, rep tickflow.SyncReport, cov []tickflow.Span, why string) {
		t.Helper()
		if len(rep.Gaps) != 0 {
			t.Fatalf("%s，而它报了缺口：%s\n  coverage=%v", why, fmtGaps(rep), cov)
		}
	}

	t.Run("标定甲 同样三天而只有一块", func(t *testing.T) {
		rep, cov := run(t, []tickflow.TradingDay{d1, d2, d3}, false, false)
		noGap(t, rep, cov, "三天一块、源把三天都给了")
	})

	t.Run("标定乙 同样BatchDays而只有一天", func(t *testing.T) {
		rep, cov := run(t, []tickflow.TradingDay{d1}, true, false)
		noGap(t, rep, cov, "BatchDays=1 而只有一天（仍是一块）")
	})

	t.Run("被测 三天三块_并成一段而不报伪缺口", func(t *testing.T) {
		rep, cov := run(t, []tickflow.TradingDay{d1, d2, d3}, true, false)
		// ⛔ 前提自检：三块真的并成了一段 —— 否则键不会分岔，这一格量的是别的东西。
		if len(cov) != 1 {
			t.Fatalf("前提没成立：期望并成 1 段，实得 %v ⇒ 读数作废", cov)
		}
		if len(rep.Gaps) != 0 {
			t.Fatalf("多块同步报出了缺口：%s\n"+
				"  ⇒ 这是那个键分岔缺陷回来了：verified 的键是【分块】的 span，\n"+
				"     而 planGaps 查的是 CommitSpan 并段之后的那个值。\n"+
				"  ⇒ 修法在 sync.go 的 coverageTouchedBy：走查【与 touched 相交的 Coverage() 段】，\n"+
				"     而不是 touched 里那些值本身。\n"+
				"  coverage=%v", fmtGaps(rep), cov)
		}
	})

	t.Run("第四格 走查真的按合并段的身份发生了", func(t *testing.T) {
		// 盘上先有一条零值记录 ⇒ 任何一段的走查都过不了。
		rep, cov := run(t, []tickflow.TradingDay{d1, d2, d3}, true, true)
		if len(cov) != 1 {
			t.Fatalf("前提没成立：期望并成 1 段，实得 %v ⇒ 读数作废", cov)
		}
		// ⛔ 前提自检：这个库确实走查不过 —— 否则下面两句在量一个健康库。
		if verr := cov[0].From; verr == 0 {
			t.Fatal("前提没成立：coverage 的 From 是零值 ⇒ 构造作废")
		}

		if len(rep.Gaps) == 0 {
			t.Fatalf("盘上有一条全库坏记录，而它一段缺口都没报 ——\n" +
				"  ⇒ 走查多半根本没发生：一个什么都不走查的实现，会让上面三格全绿。")
		}
		for _, g := range rep.Gaps {
			if g.Kind != tickflow.GapStoreVerifyFailed {
				t.Errorf("缺口 [%s,%s] 报成了 %v，期望「走查没通过」。\n"+
					"  ⇒ 若是「本次没走查」：走查没落在这一段上，那正是这条测试要挡的。",
					g.From, g.To, g.Kind)
			}
		}

		// —— 要害：报文里那个区间必须是【合并段】的，不是任何一块的 ——
		want := cov[0].From.String() + ".." + cov[0].To.String()
		joined := strings.Join(rep.TruncatedTails, " | ")
		if !strings.Contains(joined, want) {
			t.Errorf("走查失败的痕迹里没有合并段那个区间 %s：\n  %s\n"+
				"  ⇒ 那说明走查落在【分块】的身份上，而 planGaps 查的是合并段 ⇒ 键又分岔了。",
				want, joined)
		}
		if strings.Contains(joined, d2.String()+".."+d2.String()) {
			t.Errorf("走查失败的痕迹里出现了【单块】区间 %s..%s：\n  %s\n"+
				"  ⇒ 走查仍然按块在跑。", d2, d2, joined)
		}
	})
}

// writeZeroRecord 往库目录的 .dat 末尾塞一条 TradingDay 为零的记录。
//
// ⚠️ 写入口（`AppendBars`）拒收零值（SRC-7），所以只能绕过它直接写 ——
// **而那正是走查存在的理由**：写入口守得住未来，守不住已经在盘上的东西。
func writeZeroRecord(t *testing.T, dir string) {
	t.Helper()
	var dat string
	if werr := filepath.Walk(dir, func(p string, fi os.FileInfo, e error) error {
		if e == nil && !fi.IsDir() && strings.HasSuffix(p, ".dat") {
			dat = p
		}
		return e
	}); werr != nil {
		t.Fatalf("走一遍库目录失败：%v", werr)
	}
	if dat == "" {
		t.Fatal("没找到 .dat —— 构造作废")
	}
	rec := segfile.EncodeBar(tickflow.Bar{Ts: 1, TsEnd: 2, TradingDay: 0,
		Open: 1, High: 1, Low: 1, Close: 1, Volume: 1})
	f, err := os.OpenFile(dat, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 .dat 失败：%v", err)
	}
	if _, err := f.Write(rec[:]); err != nil {
		f.Close()
		t.Fatalf("写零值记录失败：%v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关 .dat 失败：%v", err)
	}
}
