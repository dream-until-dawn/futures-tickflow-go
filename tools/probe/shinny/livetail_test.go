package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"testing"
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
