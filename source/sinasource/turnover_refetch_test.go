package sinasource

import (
	"context"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// v0.6.0 发布说明「⚠️ 行为变了的」sina Turnover → NaN 那一条，两句话的实测（片 D 评审 2026-09-15 要求：操作指令要实测，不引别处）：
//
//	并存  老库升级之后，已落盘的旧日子里 Turnover 仍是 0，新拉的日子是 NaN（Sync 对已覆盖的日子不重拉）
//	出路  把 <目录>/1d.dat 与 <目录>/1d.meta 一起删掉，按交易日从早到晚重新同步 ⇒ 全部是 NaN，Complete
//
// 「老库」用 v0.5.0 的组装结果模拟：同一份新浪真实响应组装出来，再把 Turnover 置 0 落盘（v0.5.0 的 AssembleDaily 不填这个字段）。
func TestTurnoverCoexistenceAndRefetchRecipe(t *testing.T) {
	fs := newFixtureServer(t)
	cal := testCal(t)
	dir := t.TempDir()
	sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}
	k := sym.ProductKey()

	// —— 一、造一个 v0.5.0 形状的老库：[0904, 0907]，Turnover 全是 0 ——
	old := rbReq()
	old.From, old.To = 20260904, 20260907
	bars, err := AssembleDaily(rbRows(t), cal, old, nowAfter)
	if err != nil || len(bars) != 2 {
		t.Fatalf("前提：组装 [0904,0907] 应得 2 根：%d 根 %v", len(bars), err)
	}
	for i := range bars {
		bars[i].Turnover = 0
	}
	st, _, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendBars(bars); err != nil {
		t.Fatal(err)
	}
	if err := st.CommitSpan(cal, k, tickflow.Span{From: 20260904, To: 20260907, Bars: 2, Days: 2}, tickflow.OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	st.Close()

	sync := func(t *testing.T, from, to tickflow.TradingDay) (*segfile.Store, tickflow.SyncReport) {
		t.Helper()
		st, _, err := segfile.Open(dir, tickflow.Daily)
		if err != nil {
			t.Fatal(err)
		}
		syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: cal, Store: st, Pacer: pacing.NoPacing(), Timeout: 5 * time.Second,
			NewSource: func(hc *http.Client) tickflow.Source {
				c, err := New(cal, WithBaseURL(fs.URL), WithHTTPClient(hc), WithClock(func() int64 { return nowAfter }))
				if err != nil {
					t.Fatal(err)
				}
				return c
			}})
		if err != nil {
			t.Fatal(err)
		}
		rep, err := syn.Sync(context.Background(), tickflow.SyncRequest{Symbol: sym, Period: tickflow.Daily, From: from, To: to}, nowAfter)
		if err != nil {
			t.Fatalf("Sync [%s,%s]：%v", from, to, err)
		}
		return st, rep
	}
	turnovers := func(t *testing.T, st *segfile.Store, from, to tickflow.TradingDay) map[tickflow.TradingDay]float64 {
		t.Helper()
		out := map[tickflow.TradingDay]float64{}
		if err := st.Walk(from, to, func(b tickflow.Bar) bool { out[b.TradingDay] = b.Turnover; return true }); err != nil {
			t.Fatalf("Walk [%s,%s]：%v", from, to, err)
		}
		return out
	}

	// —— 二、升级后不做处置、往后多同步一天 ⇒ 并存 ——
	st2, rep := sync(t, 20260904, 20260908)
	if rep.Bars != 1 || !rep.Complete() {
		t.Fatalf("前提：升级后同步 [0904,0908] 应只拉 0908 一根且 Complete：Bars=%d Complete=%v Incidents=%q", rep.Bars, rep.Complete(), rep.Incidents())
	}
	got := turnovers(t, st2, 20260904, 20260908)
	if got[20260904] != 0 || got[20260907] != 0 || !math.IsNaN(got[20260908]) {
		t.Errorf("并存那句：旧日子应仍是 0、新日子是 NaN，实得 %v", got)
	}
	st2.Close()

	// —— 三、出路：<目录>/1d.dat 与 <目录>/1d.meta 一起删掉 ⇒ 从早到晚重新同步 ⇒ 全是 NaN ——
	for _, name := range []string{"1d.dat", "1d.meta"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("发布说明写的文件名 %s 在库目录里不存在：%v —— 那句操作指令指错了文件", name, err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	st3, rep := sync(t, 20260904, 20260908)
	if rep.Bars != 3 || !rep.Complete() {
		t.Fatalf("出路那句：删两个文件后重新同步 [0904,0908] 应拉 3 根且 Complete：Bars=%d Complete=%v Incidents=%q", rep.Bars, rep.Complete(), rep.Incidents())
	}
	got = turnovers(t, st3, 20260904, 20260908)
	if len(got) != 3 {
		t.Fatalf("读回 %d 天，应为 3：%v", len(got), got)
	}
	for d, v := range got {
		if !math.IsNaN(v) {
			t.Errorf("出路那句：%s 的 Turnover=%v，删两个文件重拉之后应全是 NaN", d, v)
		}
	}
	st3.Close()
	t.Logf("并存与出路两句都成立：库目录 %s 里的文件名是 1d.dat / 1d.meta", filepath.Base(dir))
}
