package derived

import (
	"errors"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// NewDayVerdict 的三条核验，各自既要有「放行」的格也要有「拒」的格 ——
// 只喂合法输入，拒绝那一支永远不会被走到。
func TestNewDayVerdictRules(t *testing.T) {
	const d = tickflow.TradingDay(20251009)
	cases := []struct {
		name    string
		day     tickflow.TradingDay
		v       Verdict
		r       NoVerdictReason
		wantErr error
	}{
		{"a 档不带原因 ⇒ 放行", d, NightTraded, ReasonNone, nil},
		{"b 档不带原因 ⇒ 放行", d, NightZeroVolume, ReasonNone, nil},
		{"c 档不带原因 ⇒ 放行", d, NightAbsent, ReasonNone, nil},
		{"无结论带挂起 ⇒ 放行", d, NoVerdict, ReasonHeldBack, nil},
		{"无结论带永久洞 ⇒ 放行", d, NoVerdict, ReasonNeverFetched, nil},
		{"无结论带 NoPick ⇒ 放行", d, NoVerdict, ReasonNoPick, nil},

		{"零值档位 ⇒ 拒", d, VerdictUnset, ReasonNone, ErrVerdictUnset},
		{"无结论没原因 ⇒ 拒", d, NoVerdict, ReasonNone, ErrReasonMismatch},
		{"判过了却带原因 ⇒ 拒（另一个方向）", d, NightTraded, ReasonNeverFetched, ErrReasonMismatch},
		{"未知档位 ⇒ 拒", d, Verdict(99), ReasonNone, ErrReasonMismatch},
		{"未知原因 ⇒ 拒", d, NoVerdict, NoVerdictReason(99), ErrReasonMismatch},
		{"交易日不合法 ⇒ 拒", tickflow.TradingDay(0), NightTraded, ReasonNone, ErrDayInvalid},
	}
	for _, c := range cases {
		got, err := NewDayVerdict(c.day, c.v, c.r)
		switch {
		case c.wantErr == nil && err != nil:
			t.Errorf("%s：不该拒，却报 %v", c.name, err)
		case c.wantErr == nil && (got.Day != c.day || got.Verdict != c.v || got.Reason != c.r):
			t.Errorf("%s：放行了，但交出的值 %+v 与输入不一致", c.name, got)
		case c.wantErr != nil && !errors.Is(err, c.wantErr):
			t.Errorf("%s：期望 %v，实得 %v", c.name, c.wantErr, err)
		case c.wantErr != nil && got != (DayVerdict{}):
			t.Errorf("%s：拒了，却同时交出一个非零值 %+v —— 调用方忘了看 err 时会拿到它", c.name, got)
		}
	}
}
