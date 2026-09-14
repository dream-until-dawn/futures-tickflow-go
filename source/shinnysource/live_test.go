package shinnysource

import (
	"bufio"
	"context"
	"errors"
	"math"
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

// —— 连真天勤的测试：点名才跑 ——
//
//	TICKFLOW_LIVE=1 go test ./source/shinnysource/ -run Live -v
//
// 凭证从环境变量或仓库根 .env 读（SHINNY_USER / SHINNY_PASS / SHINNY_CLIENT_SECRET），缺一个就 SKIP。
// ⛔ **只报缺了哪几个键名，不打印任何值**；`SHINNY_CLIENT_SECRET` 永不进本仓。
// client_id 用的是天勤 SDK 公开的那个（probe.md 6.1）—— 它不是秘密，但库里不设默认值，所以只在测试里写。
const liveClientID = "shinny_tq"

func liveConfig(t *testing.T, cal tickflow.Calendar) Config {
	t.Helper()
	if os.Getenv("TICKFLOW_LIVE") != "1" {
		t.Skip("连真天勤的测试点名才跑：TICKFLOW_LIVE=1")
	}
	vals := map[string]string{}
	keys := []string{"SHINNY_USER", "SHINNY_PASS", "SHINNY_CLIENT_SECRET"}
	for _, k := range keys {
		vals[k] = os.Getenv(k)
	}
	if f, err := os.Open(filepath.Join("..", "..", ".env")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			ln := strings.TrimSpace(sc.Text())
			if ln == "" || strings.HasPrefix(ln, "#") || !strings.Contains(ln, "=") {
				continue
			}
			k, v, _ := strings.Cut(ln, "=")
			k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
			if _, want := vals[k]; want && vals[k] == "" {
				vals[k] = v
			}
		}
		f.Close()
	}
	var missing []string
	for _, k := range keys {
		if vals[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Skipf("缺 %s（环境变量或仓库根 .env），跳过", strings.Join(missing, " / "))
	}
	return Config{
		User: vals["SHINNY_USER"], Password: vals["SHINNY_PASS"],
		ClientID: liveClientID, ClientSecret: vals["SHINNY_CLIENT_SECRET"],
		Calendar:    cal,
		HTTPClient:  &http.Client{Timeout: 30 * time.Second},
		ReadTimeout: 20 * time.Second,
	}
}

// liveDays 手列：2026-03-06（周五，只作夜盘基准）· 03-09 … 03-13（周一到周五）。三月中旬没有节假日。
// SHFE.rb2605 那时是活跃合约（probe.md 6.20 E1b：03-02 那一窗 2000 根、零成交 0）。
var liveDays = []tickflow.TradingDay{20260306, 20260309, 20260310, 20260311, 20260312, 20260313}

func liveReq() tickflow.BarRequest {
	return tickflow.BarRequest{Symbol: tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2605},
		Period: tickflow.MustIntraday(1), From: 20260309, To: 20260313}
}

func sameBars(a, b []tickflow.Bar) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	eq := func(x, y float64) bool { return x == y || (math.IsNaN(x) && math.IsNaN(y)) }
	for i := range a {
		x, y := a[i], b[i]
		if x.Ts != y.Ts || x.TsEnd != y.TsEnd || x.TradingDay != y.TradingDay || x.Flags != y.Flags ||
			!eq(x.Open, y.Open) || !eq(x.High, y.High) || !eq(x.Low, y.Low) || !eq(x.Close, y.Close) ||
			!eq(x.Volume, y.Volume) || !eq(x.OpenInterest, y.OpenInterest) || !eq(x.Turnover, y.Turnover) || !eq(x.Settle, y.Settle) {
			return i, false
		}
	}
	return 0, true
}

// 一窗拉完 vs 一窗 300 根翻着拉：结果必须逐根相同。
// ⛔ 这一格不靠日历真值：它比的是【同一个源的两种翻法】。
// ⚠️ 第一版窄窗取 1000、前提写「> 2000 根」—— 而五天只有 5 × 345 = 1725 根 ⇒ 前提不成立、读数作废（2026-09-14 第一次跑）。
func TestLivePagingIsWidthIndependent(t *testing.T) {
	cal, err := embedded.New(liveDays)
	if err != nil {
		t.Fatal(err)
	}
	cfg := liveConfig(t, cal)
	run := func(w int) []tickflow.Bar {
		old := pageWidth
		pageWidth = w
		defer func() { pageWidth = old }()
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		bars, err := c.Bars(context.Background(), liveReq())
		if err != nil {
			t.Fatalf("窗宽 %d：%v", w, err)
		}
		per := map[tickflow.TradingDay]int{}
		zero := 0
		for _, b := range bars {
			per[b.TradingDay]++
			if b.Volume == 0 {
				zero++
			}
		}
		t.Logf("窗宽 %d：%d 根 · 每日 %v · 零成交 %d 根 · 用时 %v · UA %q", w, len(bars), per, zero, time.Since(t0).Round(time.Millisecond), DefaultUserAgent)
		if err := tickflow.CheckBars(liveReq(), bars, time.Now().UnixMilli()); err != nil {
			t.Errorf("窗宽 %d：CheckBars：%v", w, err)
		}
		return bars
	}
	const narrowWidth = 300
	wide, narrow := run(10000), run(narrowWidth)
	// 前提：要真的翻过页 —— 根数不到两窗时，窄窗那一次和宽窗是同一件事。
	if len(wide) <= 2*narrowWidth {
		t.Fatalf("前提不成立：只有 %d 根，窗宽 %d 翻不到两窗以上", len(wide), narrowWidth)
	}
	if i, ok := sameBars(wide, narrow); !ok {
		t.Errorf("两种翻法结果不同：宽 %d 根 · 窄 %d 根 · 第一处不同在第 %d 根", len(wide), len(narrow), i)
	}
}

// 经真 NewSyncer：闸门那一格（probe.md 6.19 只量了形状复刻）。
func TestLiveSyncThroughRealGate(t *testing.T) {
	cal, err := embedded.New(liveDays)
	if err != nil {
		t.Fatal(err)
	}
	cfg := liveConfig(t, cal)
	st, _, err := segfile.Open(t.TempDir(), tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar: cal, Store: st, Pacer: pacing.NoPacing(), Timeout: 30 * time.Second,
		NewSource: func(hc *http.Client) tickflow.Source {
			cfg.HTTPClient = hc
			c, err := New(cfg)
			if err != nil {
				panic(err)
			}
			return c
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := liveReq()
	rep, err := syn.Sync(context.Background(), tickflow.SyncRequest{Symbol: r.Symbol, Period: r.Period, From: r.From, To: r.To}, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("Sync：%v", err)
	}
	t.Logf("Bars=%d · Halt=%v · UngatedOK=%v · UngatedSource=%q · Complete=%v · Incidents=%q",
		rep.Bars, rep.Halt, rep.UngatedOK, rep.UngatedSource, rep.Complete(), rep.Incidents())
	if rep.Bars == 0 {
		t.Fatal("前提不成立：一根都没拉到，闸门那一格什么也不证明")
	}
	if !rep.UngatedOK || len(rep.UngatedSource) != 0 {
		t.Errorf("经真闸门：UngatedOK=%v UngatedSource=%q，应为 true / 空", rep.UngatedOK, rep.UngatedSource)
	}
}

// 不存在的合约：报 ErrNotReady，不是空切片（probe.md 6.20 E3）。
func TestLiveUnknownContractIsNotReady(t *testing.T) {
	cal, err := embedded.New(liveDays)
	if err != nil {
		t.Fatal(err)
	}
	cfg := liveConfig(t, cal)
	cfg.ReadTimeout = 5 * time.Second
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := liveReq()
	req.Symbol.YearMon = 9901
	t0 := time.Now()
	bars, err := c.Bars(context.Background(), req)
	t.Logf("用时 %v · 错误 %v", time.Since(t0).Round(time.Millisecond), err)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("SHFE.rb9901 应报 ErrNotReady：bars=%d err=%v", len(bars), err)
	}
}
