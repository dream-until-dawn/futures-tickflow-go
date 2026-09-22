package shinnysource

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// v0.10 P-e 读数（probe.md 6.40）：拿封存的推送帧回放 Assemble 的截止，数「收进来之后又变了」的根 —— 旧规则 vs 新规则。
//
//	TICKFLOW_REPLAY_TAIL=<文件，多个用分号隔开> go test -run '^TestAssembleCutoffReplay$' -v ./source/shinnysource/
//
// 没给文件 ⇒ Skip。标定在 TestCutoffCountCalibration（常驻）。

// cutRule 在「一窗 rows、本机时刻 now」下截出收下的根。
type cutRule func(rows []Row, now int64) ([]tickflow.Bar, error)

// oldCut 是 P-e 之前的规则（只看 TsEnd ≤ now），留作对照。
func oldCut(cal tickflow.Calendar, k tickflow.ProductKey) cutRule {
	return func(rows []Row, now int64) ([]tickflow.Bar, error) {
		var out []tickflow.Bar
		for _, r := range rows {
			b, err := rowBar(r, cal, k)
			if err != nil {
				return nil, err
			}
			if b.TsEnd > now {
				break
			}
			out = append(out, b)
		}
		return out, nil
	}
}

// newCut 就是 Assemble 本身（请求区间放宽到日历覆盖的全部）。
func newCut(cal tickflow.Calendar, req tickflow.BarRequest) cutRule {
	return func(rows []Row, now int64) ([]tickflow.Bar, error) { return Assemble(rows, cal, req, now) }
}

// cutoffCount：每两帧之间「下一帧到来前 1 ms」那一刻模拟一次 Sync，按 rule 截一次；
// 收下的某根的值 ≠ 它在回放里最后一次看见的值 ⇒ 记一根（只记第一次错收的时刻 − TsEnd）。
func cutoffCount(frames []replayFrame, ins string, rule cutRule) (map[int64]int64, int, error) {
	// 第一遍：每个 id 最后一次看见的值
	last := map[int64]Row{}
	snaps := []map[int64]Row{} // 每帧之后的快照（只留 K 线行）
	snap := map[string]any{}
	for _, f := range frames {
		dec := json.NewDecoder(bytes.NewReader(f.Raw))
		dec.UseNumber()
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if err := dec.Decode(&m); err != nil {
			return nil, 0, err
		}
		if m.Aid == "rtn_data" {
			for _, d := range m.Data {
				merge(snap, d)
			}
		}
		cur := map[int64]Row{}
		for k, v := range obj(snap, "klines", ins, minuteKey, "data") {
			id, err := strconv.ParseInt(k, 10, 64)
			b, ok := v.(map[string]any)
			if err != nil || !ok {
				continue
			}
			row, err := parseRow(id, b)
			if err != nil {
				return nil, 0, err
			}
			cur[id] = row
			last[id] = row
		}
		snaps = append(snaps, cur)
	}
	bad := map[int64]int64{}
	sims := 0
	for i := 0; i+1 < len(frames); i++ {
		now := frames[i+1].Recv - 1
		cur := snaps[i]
		rows := make([]Row, 0, len(cur))
		for _, r := range cur {
			rows = append(rows, r)
		}
		sort.Slice(rows, func(a, b int) bool { return rows[a].ID < rows[b].ID })
		got, err := rule(rows, now)
		if err != nil {
			return nil, 0, err
		}
		sims++
		byTs := map[int64]Row{}
		for _, r := range rows {
			byTs[r.Datetime/1e6] = r
		}
		for _, b := range got {
			r := byTs[b.Ts]
			if _, seen := bad[r.ID]; !seen && !sameRow(r, last[r.ID]) {
				bad[r.ID] = now - b.TsEnd
			}
		}
	}
	return bad, sims, nil
}

// guard: 6.40 的计数先标定 ——
// 甲 k 在 TsEnd ＋ 500 ms 又改、k＋1 在 TsEnd ＋ 1500 ms 才出现 ⇒ 旧 1 根、新 0 根；
// 乙 k＋1 在 TsEnd ＋ 200 ms 出现、k 在 TsEnd ＋ 500 ms 又改（判据一的反例）⇒ 旧 1 根、新 1 根（计数对新规则也会响）。
func TestCutoffCountCalibration(t *testing.T) {
	cal := testCalendar(t)
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	end := m + 60000
	req := tickflow.BarRequest{Symbol: liveSym, Period: tickflow.MustIntraday(1), From: 20260903, To: 20260908}
	f := func(recv int64, raw []byte) replayFrame { return replayFrame{Recv: recv, Raw: raw} }
	cases := []struct {
		name     string
		frames   []replayFrame
		old, new int
	}{
		{"甲 下一根出现之前又改", []replayFrame{
			f(m+100, kFrame(100, m, 3000, "")),
			f(end+500, kFrame(100, m, 3001, "")),
			f(end+1500, kFrame(101, end, 3002, "")),
			f(end+10000, qFrame(end+10000)),
		}, 1, 0},
		{"乙 下一根出现之后又改", []replayFrame{
			f(m+100, kFrame(100, m, 3000, "")),
			f(end+200, kFrame(101, end, 3002, "")),
			f(end+500, kFrame(100, m, 3001, "")),
			f(end+10000, qFrame(end+10000)),
		}, 1, 1},
	}
	for _, c := range cases {
		o, _, err := cutoffCount(c.frames, liveSym.Native(), oldCut(cal, liveSym.ProductKey()))
		if err != nil {
			t.Fatal(err)
		}
		n, _, err := cutoffCount(c.frames, liveSym.Native(), newCut(cal, req))
		if err != nil {
			t.Fatal(err)
		}
		if len(o) != c.old || len(n) != c.new {
			t.Errorf("%s：旧 %d 根 %v · 新 %d 根 %v；应为旧 %d · 新 %d", c.name, len(o), o, len(n), n, c.old, c.new)
		}
	}
}

func TestAssembleCutoffReplay(t *testing.T) {
	paths := os.Getenv("TICKFLOW_REPLAY_TAIL")
	if paths == "" {
		t.Skip("没给 TICKFLOW_REPLAY_TAIL（封存文件在仓库外）")
	}
	cal, err := embedded.New([]tickflow.TradingDay{20260917, 20260918, 20260921, 20260922})
	if err != nil {
		t.Fatal(err)
	}
	const ins = "KQ.m@SHFE.rb" // 封存的是主连那一路
	req := tickflow.BarRequest{Symbol: liveSym, Period: tickflow.MustIntraday(1), From: 20260918, To: 20260922}
	for _, path := range strings.Split(paths, ";") {
		frames := readReplay(t, path)
		name := path[strings.LastIndexAny(path, `\/`)+1:]
		for _, x := range []struct {
			label string
			rule  cutRule
		}{{"旧规则（TsEnd ≤ now）", oldCut(cal, liveSym.ProductKey())}, {"新规则（Assemble）", newCut(cal, req)}} {
			bad, sims, err := cutoffCount(frames, ins, x.rule)
			if err != nil {
				t.Fatalf("%s %s：%v", name, x.label, err)
			}
			var ids []int64
			for id := range bad {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
			var show []string
			for _, id := range ids {
				show = append(show, "id "+strconv.FormatInt(id, 10)+" 第一次错收在收盘后 "+strconv.FormatInt(bad[id], 10)+" ms")
			}
			t.Logf("%s · %s：模拟 Sync %d 次 · 收进来又变了 %d 根 %v", name, x.label, sims, len(bad), show)
		}
	}
}

// readReplay 读一份 shinny-live-tail 的 JSONL（与 stream_replay_test.go 同一种格式）。
func readReplay(t *testing.T, path string) []replayFrame {
	t.Helper()
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
	return frames
}
