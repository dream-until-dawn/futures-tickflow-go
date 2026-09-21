package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// synthTail 造一段推送帧：T0 之后 n 根 1m，每根在开盘后 0.5 秒出现、收盘前 1 秒改一次；
// late 里的 id 在收盘后 2 秒（下一根已在收盘后 0.5 秒出现）又改一次；resend 里的 id 在收盘前 0.5 秒原样重发一次。
func synthTail(t0 int64, n int, late, resend map[int64]bool) []tailFrame {
	durKey := strconv.FormatInt(tailDur, 10)
	frame := func(id int64, fields map[string]any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
			"data": map[string]any{strconv.FormatInt(id, 10): fields}}}}}}})
		return b
	}
	var out []tailFrame
	add := func(at int64, raw json.RawMessage) { out = append(out, tailFrame{RecvMs: at, Raw: raw}) }
	for k := int64(0); k < int64(n); k++ {
		open := t0 + k*60000
		full := map[string]any{"datetime": float64(open) * 1e6, "open": 100.0, "high": 101.0, "low": 99.0, "close": 100.0, "volume": 1.0, "close_oi": 5.0, "open_oi": 5.0}
		add(open+500, frame(k, full))
		add(open+59000, frame(k, map[string]any{"close": 100.5, "volume": 2.0}))
		if resend[k] {
			add(open+59500, frame(k, map[string]any{"close": 100.5}))
		}
		if late[k] {
			add(open+60000+2000, frame(k, map[string]any{"close": 100.7, "volume": 3.0}))
		}
	}
	// 按时刻排好（late 那帧落在下一根出现之后）
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].RecvMs < out[j-1].RecvMs; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// guard: 离线分析的计数 —— 收盘后又改（且在下一根出现之后）⇒ A1 / A2 各 1；原样重发只进「重发未变」、不进 A2（评审方第 2 条）。
func TestLiveAnalyzeCounts(t *testing.T) {
	const t0 = int64(1789000000000)
	fr := synthTail(t0, 40, map[int64]bool{5: true}, map[int64]bool{7: true})
	r := analyzeTail(fr)
	if r.bars != 40 || r.a1Pos != 1 || r.a2Pos != 1 || r.resends != 1 {
		t.Fatalf("根 %d · A1>0 %d · A2>0 %d · 重发未变 %d，期望 40 · 1 · 1 · 1", r.bars, r.a1Pos, r.a2Pos, r.resends)
	}
	if got := maxOf(r.a1); got != 2000 {
		t.Errorf("A1 最大 %d，期望 2000（收盘后 2 秒）", got)
	}
	if got := maxOf(r.a2); got != 1500 {
		t.Errorf("A2 最大 %d，期望 1500（下一根出现后 1.5 秒）", got)
	}
	// 落盘再读回来（readFrames）结论不变
	path := t.TempDir() + "/tail.jsonl"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range fr {
		b, _ := json.Marshal(x)
		f.Write(append(b, '\n'))
	}
	f.Close()
	back, err := readFrames(path)
	if err != nil || len(back) != len(fr) {
		t.Fatalf("读回 %d 帧（写了 %d）err=%v", len(back), len(fr), err)
	}
	if rb := analyzeTail(back); rb.a1Pos != r.a1Pos || rb.a2Pos != r.a2Pos || rb.resends != r.resends || rb.bars != r.bars {
		t.Errorf("读回之后结论变了：%+v vs %+v", rb, r)
	}
	// 对照：不造晚改、不造重发 ⇒ 全 0
	c := analyzeTail(synthTail(t0, 40, nil, nil))
	if c.a1Pos != 0 || c.a2Pos != 0 || c.resends != 0 {
		t.Errorf("对照：A1>0 %d · A2>0 %d · 重发 %d，应都为 0", c.a1Pos, c.a2Pos, c.resends)
	}
}

// guard: 标定 —— 在一段干净的帧里造一次「收盘后 5 秒又改」，A1 / A2 必须各多报一根（6.36 四）。
func TestLiveAnalyzeCalibration(t *testing.T) {
	const t0 = int64(1789000000000)
	fr := synthTail(t0, 40, nil, nil)
	base := analyzeTail(fr)
	if !base.calibOK {
		t.Fatal("标定目标找不到")
	}
	id := base.calibID
	c := analyzeTail(calibrate(fr, id, t0+id*60000+60000))
	if c.a1Pos != base.a1Pos+1 || c.a2Pos != base.a2Pos+1 {
		t.Errorf("造帧后 A1>0 %d · A2>0 %d，应比基线（%d · %d）各多 1", c.a1Pos, c.a2Pos, base.a1Pos, base.a2Pos)
	}
}

// guard: 边界 —— livetail.go 里发出去的 aid 只有行情类三个（不碰交易 / 下单 / 资金）。
func TestLiveTailSendsOnlyMarketAids(t *testing.T) {
	src, err := os.ReadFile("livetail.go")
	if err != nil {
		t.Fatal(err)
	}
	allow := map[string]bool{}
	for _, a := range liveTailAids {
		allow[a] = true
	}
	allow["rtn_data"] = true // 只在解析与造帧里出现（收到的帧），不是发出去的
	ms := regexp.MustCompile(`"aid":\s*"([a-z_]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(ms) < 3 {
		t.Fatalf("只找到 %d 处 aid —— 筛子多半坏了", len(ms))
	}
	for _, m := range ms {
		if !allow[m[1]] {
			t.Errorf("livetail.go 里出现了不在白名单的 aid %q", m[1])
		}
	}
}

// skewed 模拟本机时钟比服务器慢 skew 毫秒（2026-09-18 夜盘那种形状）：synthTail 的时间轴当【服务器】时刻
// （K 线的 datetime 就在这条轴上），每帧补一个 quote.datetime ＝ 服务器时刻，而本机收到时刻 ＝ 服务器时刻 − skew。
// ⚠️ 第一版只改了 quote.datetime、没挪本机收到时刻 ⇒ K 线 datetime 与本机时刻仍在同一条轴上，「本机慢」根本没造出来（这格当场红在前提上）。
func skewed(frames []tailFrame, skew int64) []tailFrame {
	out := make([]tailFrame, len(frames))
	for i, fr := range frames {
		var m map[string]any
		json.Unmarshal(fr.Raw, &m)
		srv := time.UnixMilli(fr.RecvMs).In(cst).Format("2006-01-02 15:04:05.000000")
		data := m["data"].([]any)
		data = append(data, map[string]any{"quotes": map[string]any{tailSym: map[string]any{"datetime": srv}}})
		m["data"] = data
		raw, _ := json.Marshal(m)
		out[i] = tailFrame{RecvMs: fr.RecvMs - skew, Raw: raw}
	}
	return out
}

// guard: 以服务器时刻为准（用户 2026-09-18 裁：不动本机系统设置）—— 本机慢 2.4 秒时，本机版 A1 把「收盘后 2 秒又改」掩盖成负数，
// 服务器时刻版 A1s 照样量出 +2 秒；A2（只比本机时刻）不受影响。对照：本机不慢时两版一致。
func TestLiveAnalyzeServerTimeSurvivesSlowLocalClock(t *testing.T) {
	const t0 = int64(1789000000000)
	base := synthTail(t0, 40, map[int64]bool{5: true}, nil)
	even := analyzeTail(skewed(base, 0))
	if maxOf(even.a1) != 2000 || maxOf(even.a1s) != 2000 {
		t.Fatalf("对照失败：本机不慢时 A1 最大 %d · A1s 最大 %d，应都为 2000", maxOf(even.a1), maxOf(even.a1s))
	}
	slow := analyzeTail(skewed(base, 2400))
	if maxOf(slow.a1) >= 0 || slow.a1Pos != 0 {
		t.Fatalf("前提没成立：本机慢 2.4 秒时本机版 A1 最大 %d、>0 的根 %d —— 应被掩盖成负数、0 根", maxOf(slow.a1), slow.a1Pos)
	}
	if maxOf(slow.a1s) != 2000 || slow.a1sPos != 1 {
		t.Errorf("服务器时刻版 A1s 最大 %d、>0 的根 %d，应为 2000 与 1（不受本机时钟影响）", maxOf(slow.a1s), slow.a1sPos)
	}
	if slow.a2Pos != even.a2Pos || maxOf(slow.a2) != maxOf(even.a2) {
		t.Errorf("A2 受了本机时钟影响：慢 %d/%d · 不慢 %d/%d", slow.a2Pos, maxOf(slow.a2), even.a2Pos, maxOf(even.a2))
	}
}

// guard: -tail-out 落在仓库里就拒绝（原始帧不进仓）—— 判定按绝对路径前缀，大小写不敏感（Windows）。
func TestLiveTailRefusesOutputInsideRepo(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	inside := filepath.Join(root, "tools", "probe", "shinny", "scratchpad", "x.jsonl")
	outside := filepath.Join(os.TempDir(), "x.jsonl")
	if !outputInsideRepo(inside) {
		t.Errorf("%s 在仓库里，而没被认出来", inside)
	}
	if !outputInsideRepo(strings.ToUpper(inside)) {
		t.Errorf("大写的仓库内路径没被认出来（Windows 路径大小写不敏感）")
	}
	if outputInsideRepo(outside) {
		t.Errorf("%s 在仓库外，却被当成仓库内", outside)
	}
}

// guard: 乙的空档只算交易时段内（6.36 写的「按日历说在交易时段过滤」；2026-09-18 夜盘工具第一版没过滤，41 秒的开盘前等待被当成空档）——
// 开盘前 90 秒来一帧上一段遗留的根 ⇒ 不分时段的 B1 ＝ 90 秒；时段内的 B1s ＝ 合成数据自己的 58.5 秒（每根开盘后 0.5 秒出现、59 秒再改）。
// ⚠️ 第一版只提前 41 秒：它比时段内本来就有的 58.5 秒短，不是最大值 ⇒ 过滤与否分不开（这格当场红在断言上）。
func TestLiveAnalyzeGapOnlyInsideSession(t *testing.T) {
	const t0 = int64(1789000000000)
	durKey := strconv.FormatInt(tailDur, 10)
	// 上一段的根（十小时前，早已收盘）——真实取数里 20:59 连上时推来的就是这种
	old := map[string]any{"datetime": float64(t0-10*3600000) * 1e6, "open": 1.0, "high": 1.0, "low": 1.0, "close": 1.0, "volume": 1.0, "close_oi": 1.0, "open_oi": 1.0}
	raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
		"data": map[string]any{"-600": old}}}}}}})
	frames := append([]tailFrame{{RecvMs: t0 - 89500, Raw: raw}}, synthTail(t0, 40, nil, nil)...)
	r := analyzeTail(skewed(frames, 0))
	if r.b1 != 90000 {
		t.Fatalf("前提没成立：不分时段的 B1 %d，应为 90000（开盘前那段等待）", r.b1)
	}
	if r.b1s != 58500 {
		t.Errorf("时段内 B1s %d，应为 58500（不含开盘前那 90 秒）", r.b1s)
	}
}

// synthDaySession 造 rb 一个日盘（09:00–10:15 · 10:30–11:30 · 13:30–15:00，2026-09-21 +0800）的 1m 推送帧，
// 每根开盘后 0.5 秒出现、收盘前 1 秒改一次；时间轴当服务器时刻，本机收到 ＝ 服务器 − skew（经 skewed）。
func synthDaySession(skew int64) []tailFrame {
	durKey := strconv.FormatInt(tailDur, 10)
	day := time.Date(2026, 9, 21, 0, 0, 0, 0, cst)
	segs := [][2]int{{9*60 + 0, 10*60 + 15}, {10*60 + 30, 11*60 + 30}, {13*60 + 30, 15 * 60}}
	var out []tailFrame
	id := int64(1000)
	for _, sg := range segs {
		for m := sg[0]; m < sg[1]; m++ {
			open := day.Add(time.Duration(m) * time.Minute).UnixMilli()
			full := map[string]any{"datetime": float64(open) * 1e6, "open": 100.0, "high": 101.0, "low": 99.0, "close": 100.0, "volume": 1.0, "close_oi": 5.0, "open_oi": 5.0}
			for _, x := range []struct {
				at int64
				f  map[string]any
			}{{open + 500, full}, {open + 59000, map[string]any{"close": 100.5, "volume": 2.0}}} {
				raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
					"data": map[string]any{strconv.FormatInt(id, 10): x.f}}}}}}})
				out = append(out, tailFrame{RecvMs: x.at, Raw: raw})
			}
			id++
		}
	}
	return skewed(out, skew)
}

// guard: 6.37 的三处时段末根按 C 认出（10:15 · 11:30 · 15:00），前两处的下一根出现、15:00 那处没有；
// 本机慢 2.4 秒时 E_loc ＝ −1000 − 2400（收盘前 1 秒最后一次改、再减本机慢的那 2.4 秒）、E_srv ＝ −1000。
func TestLiveSegmentEnds(t *testing.T) {
	r := analyzeTail(synthDaySession(2400))
	if len(r.segs) != 3 {
		t.Fatalf("认出 %d 处时段末根，应为 3", len(r.segs))
	}
	for i, want := range []string{"10:15", "11:30", "15:00"} {
		sg := r.segs[i]
		if sg.label != want || sg.hasNext != (want != "15:00") || sg.eSrv != -1000 || sg.eLoc != -3400 || sg.post != 0 {
			t.Errorf("第 %d 处：%+v，应为 %s · 下一根出现 %v · E_srv −1000 · E_loc −3400 · post 0", i+1, sg, want, want != "15:00")
		}
	}
	if len(r.d) == 0 || maxOf(r.d) != -2400 || minOf(r.d) != -2400 {
		t.Errorf("D 应全为 −2400（本机慢 2.4 秒），得 n=%d 最小 %d 最大 %d", len(r.d), minOf(r.d), maxOf(r.d))
	}
}

// guard: D 排除旧报价（6.37 事先写死）—— 开盘前一帧带着上一时段的 quote.datetime ⇒ 不进 D、计入被排除；对照：不加这一帧时被排除 0。
func TestLiveDExcludesStaleQuote(t *testing.T) {
	fr := synthDaySession(0)
	base := analyzeTail(fr)
	if base.dStale != 0 {
		t.Fatalf("对照失败：干净的合成日被排除了 %d 帧", base.dStale)
	}
	stale := time.Date(2026, 9, 18, 23, 0, 0, 0, cst).Format("2006-01-02 15:04:05.000000")
	raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"quotes": map[string]any{tailSym: map[string]any{"datetime": stale}}}}})
	pre := time.Date(2026, 9, 21, 8, 59, 30, 0, cst).UnixMilli()
	r := analyzeTail(insertFrame(fr, tailFrame{RecvMs: pre, Raw: raw}))
	if r.dStale != 1 || len(r.d) != len(base.d) {
		t.Errorf("旧报价：排除 %d 帧、D %d 条（对照 %d 条），应排除 1、D 条数不变", r.dStale, len(r.d), len(base.d))
	}
}

// guard: Post 的边界是「晚于 C」不是「不早于 C」（评审方补，9/18 夜最常见的形状）——
// 第 2 处末根（11:30）一次改动带 quote.datetime 恰好 ＝ C ⇒ Post 不计、为 0；对照：同一处再加一次 C ＋ 1 ms 的改动 ⇒ Post ＝ 1。
func TestLivePostExcludesExactlyAtClose(t *testing.T) {
	durKey := strconv.FormatInt(tailDur, 10)
	fr := synthDaySession(0)
	base := analyzeTail(fr)
	if len(base.segs) != 3 || base.segs[1].label != "11:30" || base.segs[1].post != 0 {
		t.Fatalf("前提没成立：%+v", base.segs)
	}
	sg := base.segs[1]
	change := func(frames []tailFrame, at int64, close float64) []tailFrame {
		raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
			"quotes": map[string]any{tailSym: map[string]any{"datetime": time.UnixMilli(at).In(cst).Format("2006-01-02 15:04:05.000000")}},
			"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
				"data": map[string]any{strconv.FormatInt(sg.id, 10): map[string]any{"close": close}}}}}}}})
		return insertFrame(frames, tailFrame{RecvMs: at, Raw: raw})
	}
	at := change(fr, sg.c, 101.0)
	postOf := func(frames []tailFrame) int {
		for _, x := range analyzeTail(frames).segs {
			if x.id == sg.id {
				return x.post
			}
		}
		t.Fatalf("末根 id %d 没认出", sg.id)
		return -1
	}
	if got := postOf(at); got != 0 {
		t.Errorf("quote.datetime 恰好 ＝ C 的改动：Post %d，应为 0（C 本身不算「之后」）", got)
	}
	if got := postOf(change(at, sg.c+1, 101.5)); got != 1 {
		t.Errorf("对照：再加一次 C ＋ 1 ms 的改动后 Post %d，应为 1", got)
	}
}

// power637In 造一份判别力其余三条都满足的读数（三处末根在、前两处下一根出现、D 1000 帧），只让 bars 与 A2 的条数变。
func power637In(bars, a2 int) tailResult {
	return tailResult{
		bars: bars,
		a2:   make([]int64, a2),
		d:    make([]int64, 1000),
		segs: []segEnd{{label: "10:15", hasNext: true}, {label: "11:30", hasNext: true}, {label: "15:00"}},
	}
}

// guard: 6.37 判别力的样本门槛是「A2 的根 ≥ 100」（2026-09-21 分析之前改，评审方裁）——
// 99 ⇒ 缺且只缺这一条；100 ⇒ 在场。
func TestPower637A2Threshold(t *testing.T) {
	miss := power637(power637In(225, 99))
	if len(miss) != 1 || !strings.Contains(miss[0], "判据一复核") {
		t.Errorf("A2 99 根：缺 %q，应只缺「判据一复核」一条", miss)
	}
	if miss := power637(power637In(225, 100)); len(miss) != 0 {
		t.Errorf("A2 100 根：缺 %q，应在场", miss)
	}
}

// guard: 旧门槛「根数 ≥ 300」已删 —— rb 日盘一根不漏也只有 225 根（synthDaySession 量出来就是 225），
// 它不许再让一个满日盘作废。
func TestPower637FullDayNotVoidedByBarCount(t *testing.T) {
	r := analyzeTail(synthDaySession(0))
	if r.bars != 225 {
		t.Fatalf("前提没成立：合成满日盘 bars %d，应为 225", r.bars)
	}
	if miss := power637(power637In(r.bars, r.bars-1)); len(miss) != 0 {
		t.Errorf("满日盘（bars %d · A2 %d）：缺 %q，应在场", r.bars, r.bars-1, miss)
	}
}

// withPreOpenResend 在合成日盘上加一帧：下一时段开盘前 150 ms（13:29:59.850），整根原值重发上一时段末根（11:29 开盘那根，id 1134）。
// 这是 6.37 那天真实数据里的形状（11:30 那根在 13:29:59.850 被整根重发），合成数据原先没有。
func withPreOpenResend(fr []tailFrame, skew int64) []tailFrame {
	durKey := strconv.FormatInt(tailDur, 10)
	day := time.Date(2026, 9, 21, 0, 0, 0, 0, cst)
	open := day.Add(11*time.Hour + 29*time.Minute).UnixMilli()
	full := map[string]any{"datetime": float64(open) * 1e6, "open": 100.0, "high": 101.0, "low": 99.0, "close": 100.5, "volume": 2.0, "close_oi": 5.0, "open_oi": 5.0}
	raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
		"data": map[string]any{strconv.FormatInt(1000+75+59, 10): full}}}}}}})
	at := day.Add(13*time.Hour+29*time.Minute+59*time.Second+850*time.Millisecond).UnixMilli() - skew
	return insertFrame(fr, tailFrame{RecvMs: at, Raw: raw})
}

// guard: 6.36 标定挑目标（评审方裁「甲」）—— 第一根、第二根自己就 A1 > 0（6.37 那天第一根就是这样）⇒ 跳过它们，挑 id 2，且标定过；
// 对照：干净的帧 ⇒ 挑 id 1（id 0 开盘早于第一帧，只看到了半截）。旧挑法（第一根下一根出现过的）在前一种上挑 id 0、A1 加不上去。
func TestCalib636SkipsBarsAlreadyLate(t *testing.T) {
	const t0 = int64(1789000000000)
	for _, c := range []struct {
		late   map[int64]bool
		wantID int64
	}{{map[int64]bool{0: true, 1: true}, 2}, {nil, 1}} {
		fr := synthTail(t0, 40, c.late, nil)
		id, _, found := calibTarget(fr)
		if !found || id != c.wantID {
			t.Errorf("晚改 %v：挑中 id %d（找到 %v），应为 %d", c.late, id, found, c.wantID)
		}
		if found, ok, why := calib636(fr, analyzeTail(fr)); !found || !ok {
			t.Errorf("晚改 %v：6.36 标定没过：%s", c.late, why)
		}
	}
}

// guard: calibTarget 的 A2、A1s 两个条件各有一种它们独自挡住的形状（本机慢 2.4 秒，skewed 给每帧带上服务器时刻）——
//
//	A2：id 0 一次改动的服务器时刻是 C − 0.1 秒（没晚于收盘），却在下一根出现之后 0.3 秒才送到（网络慢了这一帧）
//	    ⇒ A1 − 1.6 秒 ≤ 0、A1s − 0.1 秒 ≤ 0，只有 A2 ＋0.3 秒 > 0 ⇒ 挑 id 1
//	    （本机偏差恒定时 A2 > 0 必带着 A1s > 0，只有「这一帧晚到」才让 A2 单独 > 0）
//	A1s：id 0 在服务器 C ＋ 0.3 秒一改 ⇒ 本机 C − 2.1 秒收到：A1、A2 都 ≤ 0，A1s ＋0.3 秒 > 0 ⇒ 挑 id 1
func TestCalib636TargetNeedsA2AndA1sRoom(t *testing.T) {
	const t0, skew = int64(1789000000000), int64(2400)
	durKey := strconv.FormatInt(tailDur, 10)
	c0 := t0 + 60000
	slow, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
		"quotes": map[string]any{tailSym: map[string]any{"datetime": time.UnixMilli(c0 - 100).In(cst).Format("2006-01-02 15:04:05.000000")}},
		"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{"data": map[string]any{"0": map[string]any{"close": 100.6}}}}}}}})
	fr := insertFrame(skewed(synthTail(t0, 40, nil, nil), skew), tailFrame{RecvMs: c0 + 500 - skew + 300, Raw: slow})
	if b := analyzeTail(fr); b.a2Pos != 1 || b.a1Pos != 0 || b.a1sPos != 0 {
		t.Fatalf("A2 那格前提没成立：A1>0 %d · A2>0 %d · A1s>0 %d，应为 0 · 1 · 0", b.a1Pos, b.a2Pos, b.a1sPos)
	}
	if id, _, found := calibTarget(fr); !found || id != 1 {
		t.Errorf("A2 那格：挑中 id %d（找到 %v），应为 1", id, found)
	}
	if found, ok, why := calib636(fr, analyzeTail(fr)); !found || !ok {
		t.Errorf("A2 那格：6.36 标定没过：%s", why)
	}
	raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
		"quotes": map[string]any{tailSym: map[string]any{"datetime": time.UnixMilli(c0 + 300).In(cst).Format("2006-01-02 15:04:05.000000")}},
		"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{"data": map[string]any{"0": map[string]any{"close": 100.6}}}}}}}})
	fr2 := insertFrame(skewed(synthTail(t0, 40, nil, nil), skew), tailFrame{RecvMs: c0 + 300 - skew, Raw: raw})
	if id, _, found := calibTarget(fr2); !found || id != 1 {
		t.Errorf("A1s 那格：挑中 id %d（找到 %v），应为 1", id, found)
	}
	if found, ok, why := calib636(fr2, analyzeTail(fr2)); !found || !ok {
		t.Errorf("A1s 那格：6.36 标定没过：%s", why)
	}
}

// guard: calibTarget 的 A1 条件独自挡住的形状 —— 6.37 那天的真形状：id 1 在收盘后 0.2 秒又改了一次，而下一根 0.5 秒才出现
// ⇒ A1 ＋0.2 秒 > 0、A2 − 0.3 秒 ≤ 0（A2 挡不住它）⇒ 跳过，挑 id 2。
func TestCalib636TargetNeedsA1Room(t *testing.T) {
	const t0 = int64(1789000000000)
	durKey := strconv.FormatInt(tailDur, 10)
	c1 := t0 + 2*60000
	raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
		"data": map[string]any{"1": map[string]any{"close": 100.6}}}}}}}})
	fr := insertFrame(synthTail(t0, 40, nil, nil), tailFrame{RecvMs: c1 + 200, Raw: raw})
	base := analyzeTail(fr)
	if base.a1Pos != 1 || base.a2Pos != 0 {
		t.Fatalf("前提没成立：A1>0 %d · A2>0 %d，应为 1 · 0", base.a1Pos, base.a2Pos)
	}
	if id, _, found := calibTarget(fr); !found || id != 2 {
		t.Errorf("挑中 id %d（找到 %v），应为 2", id, found)
	}
	if found, ok, why := calib636(fr, base); !found || !ok {
		t.Errorf("6.36 标定没过：%s", why)
	}
}

// guard: 6.36 那格的造帧也是持续的 —— 目标那根收盘很久之后被整根原值重发一次 ⇒ 真改动仍只 ＋1（不持续则 ＋2：造一次、冲回一次）。
func TestCalib636SurvivesLaterResendOfTarget(t *testing.T) {
	const t0 = int64(1789000000000)
	fr := synthTail(t0, 40, nil, nil)
	id, _, found := calibTarget(fr)
	if !found {
		t.Fatal("标定目标找不到")
	}
	durKey := strconv.FormatInt(tailDur, 10)
	open := t0 + id*60000
	full := map[string]any{"datetime": float64(open) * 1e6, "open": 100.0, "high": 101.0, "low": 99.0, "close": 100.5, "volume": 2.0, "close_oi": 5.0, "open_oi": 5.0}
	raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
		"data": map[string]any{strconv.FormatInt(id, 10): full}}}}}}})
	fr = insertFrame(fr, tailFrame{RecvMs: open + 10*60000, Raw: raw})
	base := analyzeTail(fr)
	if base.changes != analyzeTail(synthTail(t0, 40, nil, nil)).changes {
		t.Fatalf("前提没成立：那次整根原值重发被算成了真改动")
	}
	if found, ok, why := calib636(fr, base); !found || !ok {
		t.Errorf("6.36 标定没过：%s", why)
	}
}

// guard: 两格标定造出来的值是持续的（评审方裁「乙」）—— 下一时段开盘前整根原值重发上一时段末根，不许把造出来的值冲回去。
// 基线：那次重发算「重发未变」、post 仍 0；标定：6.37 那格 post 只 ＋1、E_loc 是 C ＋ 3 秒（不是被拉到 13:29:59）。
func TestCalib637SurvivesPreOpenResend(t *testing.T) {
	const skew = 2400
	plain := analyzeTail(synthDaySession(skew))
	fr := withPreOpenResend(synthDaySession(skew), skew)
	base := analyzeTail(fr)
	if base.resends != plain.resends+1 || base.segs[1].post != 0 {
		t.Fatalf("前提没成立：重发未变 %d（对照 %d）· 11:30 那根 post %d，应为对照 ＋1 · 0", base.resends, plain.resends, base.segs[1].post)
	}
	if ok, why := calibrate637(fr, base); !ok {
		t.Errorf("6.37 标定没过：%s", why)
	}
	if found, ok, why := calib636(fr, base); !found || !ok {
		t.Errorf("6.36 标定没过：%s", why)
	}
}

// guard: persist 只改 at 之后、带这根 close 的帧；at 之前的、别的根的、null 帧都不动。
func TestPersistRewritesOnlyLaterCloseOfThatBar(t *testing.T) {
	const t0 = int64(1789000000000)
	fr := synthTail(t0, 5, nil, nil)
	const id = int64(2)
	at := t0 + id*60000 + 60000 // 这根收盘时刻
	out := persist(fr, id, at, 1e9)
	changed := 0
	for i := range fr {
		if string(fr[i].Raw) != string(out[i].Raw) {
			changed++
			if fr[i].RecvMs <= at {
				t.Errorf("at 之前的帧被改了：%s", fr[i].Raw)
			}
		}
	}
	if changed != 0 {
		t.Errorf("synthTail 里 id %d 在收盘之后不再出现，应一帧不改，改了 %d 帧", id, changed)
	}
	// 在它之后补一帧原值重发、一帧别的根、一帧 null ⇒ 只有第一帧被改
	durKey := strconv.FormatInt(tailDur, 10)
	mk := func(bid int64, v any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
			"data": map[string]any{strconv.FormatInt(bid, 10): v}}}}}}})
		return b
	}
	extra := []tailFrame{{RecvMs: at + 1000, Raw: mk(id, map[string]any{"close": 100.5})}, {RecvMs: at + 2000, Raw: mk(id+1, map[string]any{"close": 100.5})}, {RecvMs: at + 3000, Raw: mk(id, nil)}}
	fr2 := fr
	for _, x := range extra {
		fr2 = insertFrame(fr2, x)
	}
	out2 := persist(fr2, id, at, 1e9)
	var diff []int64
	for i := range fr2 {
		if string(fr2[i].Raw) != string(out2[i].Raw) {
			diff = append(diff, fr2[i].RecvMs-at)
		}
	}
	if len(diff) != 1 || diff[0] != 1000 {
		t.Errorf("被改的帧（相对 at 的毫秒）%v，应只有 [1000]", diff)
	}
}

// guard: 6.37 的标定本身能过（第 2 处末根 C＋3 秒又改 ⇒ post ＋1、E_loc 变大；旧报价不进 D）。
func TestLiveCalibrate637(t *testing.T) {
	fr := synthDaySession(2400)
	ok, why := calibrate637(fr, analyzeTail(fr))
	if !ok {
		t.Errorf("6.37 标定没过：%s", why)
	}
}

// guard: G 的公式（6.37 三，事先写死）：2 × ceil_sec( max(0, E_loc) ＋ max(0, 最大 D) ＋ |最小 D| )。
func TestSegGraceFormula(t *testing.T) {
	cases := []struct {
		eLoc int64
		d    []int64
		want int64
	}{
		{-3400, []int64{-2600, -2300}, 2 * 3}, // 本机慢：E_loc 取 0，最大 D 取 0，|最小 D| 2.6 秒 ⇒ 3 秒 ×2
		{1500, []int64{-2600, 400}, 2 * 5},    // 1.5 ＋ 0.4 ＋ 2.6 ＝ 4.5 ⇒ 5 秒 ×2
		{0, []int64{0}, 0},
	}
	for _, c := range cases {
		if got := segGrace(c.eLoc, c.d); got != c.want {
			t.Errorf("segGrace(%d, %v) ＝ %d，应为 %d", c.eLoc, c.d, got, c.want)
		}
	}
}
