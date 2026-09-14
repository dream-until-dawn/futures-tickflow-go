package sinasource

import (
	"net/http"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// 本源实现 tickflow.CalendarHolder ⇒ NewSyncer 拒「源一份、Syncer 一份」的日历（v0.6 起，片二评审 2026-09-14 要的）。
// ⛔ 断言落在【经真 NewSyncer 的行为】上，不只是「方法存在」：方法存在而 NewSyncer 不去问，这一格照样开着。
func TestNewSyncerRejectsASecondCalendarForSina(t *testing.T) {
	days := []tickflow.TradingDay{20260907, 20260908}
	mkCal := func() tickflow.Calendar {
		c, err := embedded.New(days)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cal, other := mkCal(), mkCal() // 内容相同、实例不同
	cells := []struct {
		name    string
		srcCal  tickflow.Calendar
		wantErr bool
	}{
		{"同一个日历 ⇒ 造得出来（标定格）", cal, false},
		{"内容相同的另一份 ⇒ 拒", other, true},
	}
	for _, ce := range cells {
		st, _, err := segfile.Open(t.TempDir(), tickflow.Daily)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tickflow.NewSyncer(tickflow.SyncerConfig{
			Calendar: cal, Store: st, Pacer: pacing.NoPacing(), Timeout: 5 * time.Second,
			NewSource: func(hc *http.Client) tickflow.Source {
				c, err := New(ce.srcCal, WithHTTPClient(hc))
				if err != nil {
					t.Fatal(err)
				}
				return c
			},
		})
		st.Close()
		if (err != nil) != ce.wantErr {
			t.Errorf("%s：err=%v", ce.name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "同一个") {
			t.Errorf("%s：报文应说「同一个」：%v", ce.name, err)
		}
	}
}
