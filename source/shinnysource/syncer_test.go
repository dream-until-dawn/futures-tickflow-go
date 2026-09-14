package shinnysource

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// 经【真的 NewSyncer】跑一次 Sync —— 探针 6.19 是形状复刻（gateCounter 没导出、探针不 import 本库），
// 这一格补上「经真闸门」那一半。

func newSyncerFor(t *testing.T, fs *fakeServer, cal tickflow.Calendar, factory func(cfg Config) tickflow.SourceFactory) (*tickflow.Syncer, error) {
	t.Helper()
	st, _, err := segfile.Open(t.TempDir(), tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar:  cal,
		Store:     st,
		NewSource: factory(fs.config(cal, farFuture)),
		Pacer:     pacing.NoPacing(),
		Timeout:   5 * time.Second,
	})
}

// honest 照设计的用法：把递进来的 client 交给源。
func honest(cfg Config) tickflow.SourceFactory {
	return func(hc *http.Client) tickflow.Source {
		cfg.HTTPClient = hc
		c, err := New(cfg)
		if err != nil {
			panic(err)
		}
		return c
	}
}

// bypass 收下 client 又丢掉，自己用一个裸 client —— 闸门那一格要抓的正是它。
func bypass(cfg Config) tickflow.SourceFactory {
	return func(*http.Client) tickflow.Source {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
		c, err := New(cfg)
		if err != nil {
			panic(err)
		}
		return c
	}
}

func TestSyncThroughRealGateCountsTheHandshake(t *testing.T) {
	cal := testCalendar(t)
	cells := []struct {
		name        string
		factory     func(Config) tickflow.SourceFactory
		wantUngated bool
	}{
		{"源用递进来的 client ⇒ 闸门计数涨 ⇒ 不留声", honest, false},
		// 反标定：这一格必须留声。它不留声的话，上一格的「不留声」可能只是闸门那条检查没生效。
		{"源丢掉递进来的 client ⇒ 留声（必须红的那一格）", bypass, true},
	}
	for _, ce := range cells {
		fs := newFakeServer(t)
		fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260908)
		s, err := newSyncerFor(t, fs, cal, ce.factory)
		if err != nil {
			t.Fatalf("%s：NewSyncer：%v", ce.name, err)
		}
		rep, err := s.Sync(context.Background(), tickflow.SyncRequest{
			Symbol: tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601},
			Period: tickflow.MustIntraday(1), From: 20260904, To: 20260907,
		}, farFuture)
		if err != nil {
			t.Fatalf("%s：Sync：%v", ce.name, err)
		}
		// 前提：真的向源要过数据、而且拿到了 —— 否则「没留声」在 0 次请求上照样成立。
		if rep.Bars != 690 {
			t.Fatalf("%s：前提不成立：Bars=%d，应为 2 × 345 = 690", ce.name, rep.Bars)
		}
		if !rep.UngatedOK {
			t.Errorf("%s：UngatedOK=false —— 源声明了 ClientUseHTTP，闸门那条检查应当适用", ce.name)
		}
		if got := len(rep.UngatedSource) > 0; got != ce.wantUngated {
			t.Errorf("%s：UngatedSource=%q，应%s留声", ce.name, rep.UngatedSource, map[bool]string{true: "", false: "不"}[ce.wantUngated])
		}
		// ⛔ 第二次 Sync：token 与 mdurl 已缓存，经闸门的只剩握手。
		// 只看第一次的话，取 token 那两个请求就把计数填上了，握手走不走闸门看不出来（变异验收 M2 当场存活过）。
		rep2, err := s.Sync(context.Background(), tickflow.SyncRequest{
			Symbol: tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601},
			Period: tickflow.MustIntraday(1), From: 20260908, To: 20260908,
		}, farFuture)
		if err != nil {
			t.Fatalf("%s：第二次 Sync：%v", ce.name, err)
		}
		if rep2.Bars != 345 {
			t.Fatalf("%s：第二次前提不成立：Bars=%d，应为 345（往后多一天；库只收非降序追加）", ce.name, rep2.Bars)
		}
		if got := len(rep2.UngatedSource) > 0; got != ce.wantUngated {
			t.Errorf("%s：第二次（token 已缓存）UngatedSource=%q，应%s留声", ce.name, rep2.UngatedSource, map[bool]string{true: "", false: "不"}[ce.wantUngated])
		}
	}
}

func TestNewSyncerRejectsASecondCalendar(t *testing.T) {
	cal := testCalendar(t)
	other := testCalendar(t) // 内容相同、实例不同
	fs := newFakeServer(t)
	cells := []struct {
		name    string
		srcCal  tickflow.Calendar
		wantErr bool
	}{
		{"同一个日历 ⇒ 造得出来（标定格）", cal, false},
		{"内容相同的另一份 ⇒ 拒", other, true},
	}
	for _, ce := range cells {
		_, err := newSyncerFor(t, fs, cal, func(cfg Config) tickflow.SourceFactory {
			cfg.Calendar = ce.srcCal
			return honest(cfg)
		})
		if (err != nil) != ce.wantErr {
			t.Errorf("%s：err=%v", ce.name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "同一个") {
			t.Errorf("%s：报文应说「同一个」：%v", ce.name, err)
		}
	}
}
