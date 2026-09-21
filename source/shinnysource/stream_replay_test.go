package shinnysource

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// v0.10 P-b1 读数：把落盘的推送帧（shinny-live-tail 的 JSONL，仓库外的封存文件）整条喂给 liveCore，离线回放。
//
//	TICKFLOW_REPLAY_TAIL=<文件，多个用分号隔开> go test -run TestLiveReplaySealed -v ./source/shinnysource/
//
// 没给文件 ⇒ Skip（与 live_test.go 同一种门控：读数不是常驻测试）。本机时刻按帧里记下的走，帧与帧之间每 250 ms tick 一次。
func TestLiveReplaySealed(t *testing.T) {
	paths := os.Getenv("TICKFLOW_REPLAY_TAIL")
	if paths == "" {
		t.Skip("没给 TICKFLOW_REPLAY_TAIL（封存文件在仓库外）")
	}
	cal, err := embedded.New([]tickflow.TradingDay{20260917, 20260918, 20260921, 20260922})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range strings.Split(paths, ";") {
		replayOne(t, cal, path)
	}
}

type replayFrame struct {
	Recv int64           `json:"recv_ms"`
	Raw  json.RawMessage `json:"raw"`
}

type replayGot struct {
	b    tickflow.Bar
	when int64 // 交出时的本机时刻
}

func replayOne(t *testing.T, cal tickflow.Calendar, path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var frames []replayFrame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	for sc.Scan() {
		var x replayFrame
		if err := json.Unmarshal(sc.Bytes(), &x); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, x)
	}
	if err := sc.Err(); err != nil || len(frames) == 0 {
		t.Fatalf("读 %s：%d 帧 · %v", path, len(frames), err)
	}
	// 起始格：连上那一分钟起的第一个交易分钟（view_width 带来的历史不交）。P-b2 起起始格必须是交易分钟（L12：
	// 真用时由 Feed 给「最后一根的下一格」）—— 08:59 / 20:59 连上的，起始格是 09:00 / 21:00，推送窗口自带它 ⇒ 不补。
	startAt := frames[0].Recv / 60000 * 60000
	for i := 0; !onSession(cal, startAt) && i < 24*60; i++ {
		startAt += 60000
	}
	c := newLiveCore(cal, liveSym, startAt, LiveOptions{})
	c.ins = "KQ.m@SHFE.rb" // 封存的是主连那一路（6.36 / 6.37 取的是 KQ.m）
	var out []replayGot
	var stop error
	take := func(bs []tickflow.Bar, now int64) {
		for _, b := range bs {
			out = append(out, replayGot{b, now})
		}
	}
	step := func(now int64, bs []tickflow.Bar, err error) bool {
		take(bs, now)
		stop = err
		return err == nil
	}
	now := frames[0].Recv
	ok := true
	for _, x := range frames {
		for ; ok && now+250 < x.Recv; now += 250 {
			bs, err := c.tick(now + 250)
			ok = step(now+250, bs, err)
		}
		if !ok {
			break
		}
		now = x.Recv
		bs, err := c.feed(x.Recv, x.Raw)
		if ok = step(x.Recv, bs, err); !ok {
			break
		}
	}
	for t2 := now + 250; ok && t2 <= now+10000; t2 += 250 {
		bs, err := c.tick(t2)
		ok = step(t2, bs, err)
	}
	// 交出时刻距收盘（本机时刻 − TsEnd）；时段末根单列；交出的值 vs 回放结束时快照里的最终值（判据一：交出后不再变）
	final := map[int64]Row{}
	for _, r := range c.rows {
		final[r.Datetime/1e6] = r
	}
	var lag, segLag []int64
	changed := 0
	for i, g := range out {
		d := g.when - g.b.TsEnd
		if end, _ := c.isSegmentEnd(g.b); end {
			segLag = append(segLag, d)
		} else {
			lag = append(lag, d)
		}
		if r, ok := final[g.b.Ts]; ok && (r.Close != g.b.Close || r.Volume != g.b.Volume || r.High != g.b.High || r.Low != g.b.Low || r.Open != g.b.Open) {
			changed++
		}
		if i > 0 && g.b.Ts <= out[i-1].b.Ts {
			t.Errorf("交出的顺序乱了：%s 之后是 %s", fmtTs(out[i-1].b.Ts), fmtTs(g.b.Ts))
		}
	}
	w, lo := c.warnings()
	name := path[strings.LastIndexAny(path, `\/`)+1:]
	first, last := "—", "—"
	if len(out) > 0 {
		first, last = fmtTs(out[0].b.Ts), fmtTs(out[len(out)-1].b.Ts)
	}
	_, _, need := c.needBackfill()
	t.Logf("%s：帧 %d · 起始格 %s（要补齐 %v）· 交出 %d 根（首 %s · 末 %s）· 停在 %v", name, len(frames), fmtTs(startAt), need, len(out), first, last, stop)
	t.Logf("  非末根 交出时刻 − 收盘（本机，毫秒）%s", distMs(lag))
	t.Logf("  时段末根 交出时刻 − 收盘（本机，毫秒）%v", segLag)
	t.Logf("  交出的值与回放结束时的最终值不同的根 %d · 偏慢告警 %d 次（最新低端 %d）", changed, w, lo)
}

func onSession(cal tickflow.Calendar, ts int64) bool {
	d, err := cal.DayAt(liveSym.ProductKey(), ts)
	if err != nil {
		return false
	}
	for _, s := range d.Sessions {
		if s.Contains(ts) {
			return true
		}
	}
	return false
}

func distMs(xs []int64) string {
	if len(xs) == 0 {
		return "（无）"
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	p := func(q float64) int64 { return s[max(int(math.Ceil(q*float64(len(s)))), 1)-1] }
	f := strconv.FormatInt
	return "n=" + strconv.Itoa(len(s)) + " · 最小 " + f(s[0], 10) + " · 中位 " + f(p(0.5), 10) + " · 95 分位 " + f(p(0.95), 10) + " · 最大 " + f(s[len(s)-1], 10)
}
