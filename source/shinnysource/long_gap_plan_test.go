package shinnysource

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// gapPlan 将真实运行参数与墙钟隔离，先做离线验证再联网。
type gapPlan struct {
	sym                             tickflow.Symbol
	cal                             tickflow.Calendar
	start, begin, drop, resume, end int64
	day                             tickflow.TradingDay
}

func parseGapPlan(env map[string]string, now time.Time) (gapPlan, error) {
	var p gapPlan
	var err error
	p.sym, err = tickflow.ParseSymbol(env["INS"])
	if err != nil {
		return p, err
	}
	var days []tickflow.TradingDay
	for _, s := range strings.Split(env["DAYS"], ",") {
		n, e := strconv.Atoi(s)
		if e != nil {
			return p, fmt.Errorf("DAYS 无效")
		}
		d := tickflow.TradingDay(n)
		y, m, dd := d.Split()
		x := time.Date(y, time.Month(m), dd, 0, 0, 0, 0, tickflow.CST)
		if !d.Valid() || x.Year() != y || int(x.Month()) != m || x.Day() != dd || (len(days) > 0 && d <= days[len(days)-1]) {
			return p, fmt.Errorf("DAYS 必须是真实日期且严格递增")
		}
		days = append(days, d)
	}
	p.cal, err = embedded.New(days)
	if err != nil {
		return p, err
	}
	for k, dst := range map[string]*int64{"START": &p.start, "BEGIN": &p.begin, "DROP": &p.drop, "RESUME": &p.resume, "END": &p.end} {
		x, e := time.Parse(time.RFC3339, env[k])
		if e != nil || x.UnixMilli()%60000 != 0 {
			return p, fmt.Errorf("%s 须为分钟对齐的 RFC3339", k)
		}
		*dst = x.UnixMilli()
	}
	if !(now.UnixMilli() < p.begin && p.start <= p.begin && p.begin < p.drop && p.drop+31*60000 <= p.resume && p.resume < p.end) {
		return p, fmt.Errorf("窗口过期、时序错误或缺口不足 31 分钟")
	}
	local := now.In(tickflow.CST)
	limit := time.Date(local.Year(), local.Month(), local.Day(), 17, 0, 0, 0, tickflow.CST).UnixMilli()
	if value := env["DEADLINE"]; value != "" {
		deadline, e := time.Parse(time.RFC3339, value)
		if e != nil {
			return p, fmt.Errorf("DEADLINE 须为明确授权的 RFC3339 时刻")
		}
		limit = deadline.UnixMilli()
	}
	if p.end+10000 > limit {
		return p, fmt.Errorf("END 超过授权期限（未给 DEADLINE 时为同日17:00）")
	}
	d, err := p.cal.DayAt(p.sym.ProductKey(), p.start)
	if err != nil {
		return p, err
	}
	p.day = d.Num
	for _, s := range d.Sessions {
		if s.Contains(p.start) && p.end < s.End {
			return p, nil
		}
	}
	return p, fmt.Errorf("窗口必须完全位于一段连续交易时段内")
}

// TestGapPlanOfflinePreflight 只验证参数，不读取凭证，不连接行情服务器。
func TestGapPlanOfflinePreflight(t *testing.T) {
	if os.Getenv("TICKFLOW_GAP_PLAN_CHECK") != "1" {
		t.Skip("仅供执行单生成器显式离线预检")
	}
	env := map[string]string{}
	for _, key := range []string{"INS", "DAYS", "START", "BEGIN", "DROP", "RESUME", "END", "DEADLINE"} {
		env[key] = os.Getenv("TICKFLOW_GAP_" + key)
	}
	if _, err := parseGapPlan(env, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestGapPlanValidation(t *testing.T) {
	base := map[string]string{"INS": "SHFE.rb2701", "DAYS": "20261022,20261023", "START": "2026-10-23T13:30:00+08:00", "BEGIN": "2026-10-23T13:31:00+08:00", "DROP": "2026-10-23T13:36:00+08:00", "RESUME": "2026-10-23T14:08:00+08:00", "END": "2026-10-23T14:18:00+08:00"}
	now, _ := time.Parse(time.RFC3339, "2026-10-23T12:00:00+08:00")
	if _, err := parseGapPlan(base, now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"INS": "KQ.m@SHFE.rb", "DAYS": "20260230,20261023", "BEGIN": "2026-10-23T11:00:00+08:00", "DROP": "2026-10-23T13:36:01+08:00", "RESUME": "2026-10-23T13:50:00+08:00", "END": "2026-10-23T17:01:00+08:00", "START": "2026-10-23T11:00:00+08:00"} {
		t.Run(k, func(t *testing.T) {
			e := map[string]string{}
			for key, value := range base {
				e[key] = value
			}
			e[k] = v
			if _, err := parseGapPlan(e, now); err == nil {
				t.Fatal("非法配置被接受")
			}
		})
	}
}

// TestGapExplicitDeadline 只在明确执行单截止内允许准备未来窗口，不放宽过期或短缺口。
func TestGapExplicitDeadline(t *testing.T) {
	env := map[string]string{"INS": "SHFE.rb2701", "DAYS": "20261022,20261023", "START": "2026-10-23T13:30:00+08:00", "BEGIN": "2026-10-23T13:31:00+08:00", "DROP": "2026-10-23T13:36:00+08:00", "RESUME": "2026-10-23T14:08:00+08:00", "END": "2026-10-23T14:18:00+08:00"}
	now, _ := time.Parse(time.RFC3339, "2026-10-22T12:00:00+08:00")
	if _, err := parseGapPlan(env, now); err == nil {
		t.Fatal("没有明确截止不能延长原同日17点保护")
	}
	env["DEADLINE"] = "2026-10-23T15:00:00+08:00"
	if _, err := parseGapPlan(env, now); err != nil {
		t.Fatal(err)
	}
	for _, deadline := range []string{"bad", "2026-10-23T14:18:09+08:00"} {
		env["DEADLINE"] = deadline
		if _, err := parseGapPlan(env, now); err == nil {
			t.Fatal("错误或不足截止被接受")
		}
	}
}
