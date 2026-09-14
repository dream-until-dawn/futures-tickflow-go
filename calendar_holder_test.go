package tickflow

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// 本文件是 CalendarHolder 那一格：源交出的日历与 SyncerConfig.Calendar 不是同一个 ⇒ NewSyncer 拒。

type holderSource struct {
	noHTTPSource
	cal Calendar
}

func (s *holderSource) Calendar() Calendar { return s.cal }

// uncomparableCal 是一个值类型、带切片的日历 —— 用 == 比会 panic 的那一种。
type uncomparableCal struct {
	Calendar
	days []TradingDay
}

func TestNewSyncerChecksCalendarIdentity(t *testing.T) {
	base := week()
	uncmp := uncomparableCal{Calendar: base, days: []TradingDay{1}}
	cells := []struct {
		name    string
		cfgCal  Calendar
		src     Source
		wantErr string // 空 ⇒ 不许报错
	}{
		{"同一个 ⇒ 造得出来（标定格）", base, &holderSource{noHTTPSource{ClientUseHTTP}, base}, ""},
		{"不实现 CalendarHolder 的源 ⇒ 不核（射程）", base, &noHTTPSource{ClientUseHTTP}, ""},
		{"内容相同的另一份 ⇒ 拒", base, &holderSource{noHTTPSource{ClientUseHTTP}, week()}, "同一个"},
		{"源交出 nil ⇒ 拒", base, &holderSource{noHTTPSource{ClientUseHTTP}, nil}, "nil"},
		{"类型不同 ⇒ 拒", base, &holderSource{noHTTPSource{ClientUseHTTP}, uncmp}, "两份日历"},
		{"类型不可比较 ⇒ 拒而不 panic", uncmp, &holderSource{noHTTPSource{ClientUseHTTP}, uncmp}, "传指针"},
	}
	for _, ce := range cells {
		t.Run(ce.name, func(t *testing.T) {
			_, err := NewSyncer(SyncerConfig{
				Calendar: ce.cfgCal, Store: &fakeStore{}, Pacer: pacing.NoPacing(),
				Timeout:   30 * time.Second,
				NewSource: func(*http.Client) Source { return ce.src },
			})
			switch {
			case ce.wantErr == "" && err != nil:
				t.Errorf("不该报错：%v", err)
			case ce.wantErr != "" && (err == nil || !strings.Contains(err.Error(), ce.wantErr)):
				t.Errorf("应报含 %q 的错，得到 %v", ce.wantErr, err)
			}
		})
	}
}
