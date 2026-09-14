package main

// v0.6 片一：探针补量（v0.6 之零 H3/H4/H5 与合规 U1）。
//
// ⚠️ 这里的探针都是【点名才跑】（optIn）：默认的 `go run .` 不跑它们 ——
// 全历史翻页要连着拉几十万根，不该在「顺手跑一遍基线」时发生。
// 跑法：`go run . -only shinny-ua` / `-only shinny-page-explore` …
//
// 与 main.go 同一条规矩：本文件【不许 import 本库】（independence_test.go 钉着）——
// 这里断言的是外部世界长什么样，拿本库去判定会让两边一起错。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// optIn：只有 -only 点名（子串匹配）时才跑。
func optIn(name string) bool { return only != "" && strings.Contains(name, only) }

const (
	uaTqsdk = "tqsdk-python 3.10.2"
	// uaSelf 是本库自己的名字。U1（合规）：库若要发布，就不该冒充别人的 SDK —— 先量天勤收不收。
	uaSelf = "futures-tickflow-go/0.6 (+https://github.com/dream-until-dawn/futures-tickflow-go)"
)

// probeUA 量「名称服务」与「行情 websocket」两道门对 User-Agent 的态度。
//
// 三格：tqsdk 的 UA（对照组，必须过 —— 它不过就说明是网络或凭证的问题，另两格的结论作废）·
// 本库自己的 UA · 不设 UA（Go 默认）。
// ⚠️ 射程：只量「这一刻收不收」，不量「条款允不允许」—— 后者是条款问题，探针答不了。
// ⚠️ token 那一步（http.PostForm）今天本来就没设 UA，发的是 Go 默认值；这里不重复量它。
func probeUA(ctx context.Context, tok string) {
	const name = "shinny-ua"
	if !optIn(name) {
		return
	}
	type row struct {
		label, ua  string
		nsStatus   int
		nsMdurl    bool
		wsErr      string
		wsReady    bool
		wsLastID   int64
		wsDuration time.Duration
	}
	rows := []row{{label: "tqsdk（对照组）", ua: uaTqsdk}, {label: "本库自己", ua: uaSelf}, {label: "不设（Go 默认）", ua: ""}}
	for i := range rows {
		r := &rows[i]
		req, _ := http.NewRequestWithContext(ctx, "GET", nsURL, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if r.ua != "" {
			req.Header.Set("User-Agent", r.ua)
		}
		md := ""
		if resp, err := http.DefaultClient.Do(req); err == nil {
			r.nsStatus = resp.StatusCode
			var v struct {
				MdURL string `json:"mdurl"`
			}
			json.NewDecoder(resp.Body).Decode(&v)
			resp.Body.Close()
			md, r.nsMdurl = v.MdURL, v.MdURL != ""
		}
		if md == "" {
			r.wsErr = "名称服务没给 mdurl，websocket 这一格没法量"
			continue
		}
		h := http.Header{"Accept": {"application/json"}, "Authorization": {"Bearer " + tok}}
		if r.ua != "" {
			h.Set("User-Agent", r.ua)
		}
		t0 := time.Now()
		cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		c, _, err := websocket.Dial(cctx, md, &websocket.DialOptions{
			CompressionMode: websocket.CompressionNoContextTakeover, HTTPHeader: h})
		if err != nil {
			r.wsErr = err.Error()
			cancel()
			continue
		}
		c.SetReadLimit(64 << 20)
		send := func(v any) { b, _ := json.Marshal(v); c.Write(cctx, websocket.MessageText, b) }
		send(map[string]any{"aid": "set_chart", "chart_id": "ua", "ins_list": "KQ.m@SHFE.rb",
			"duration": int64(60) * 1e9, "view_width": 5})
		send(map[string]any{"aid": "peek_message"})
		snap := map[string]any{}
		for !r.wsReady {
			_, msg, rerr := c.Read(cctx)
			if rerr != nil {
				r.wsErr = rerr.Error()
				break
			}
			var m struct {
				Aid  string           `json:"aid"`
				Data []map[string]any `json:"data"`
			}
			if json.Unmarshal(msg, &m) == nil && m.Aid == "rtn_data" {
				for _, d := range m.Data {
					merge(snap, d)
				}
				if ch := obj(snap, "charts", "ua"); ch != nil {
					if ready, _ := ch["ready"].(bool); ready {
						if ser := obj(snap, "klines", "KQ.m@SHFE.rb", fmt.Sprintf("%d", int64(60)*1e9)); ser != nil {
							if l, ok := ser["last_id"].(float64); ok {
								r.wsReady, r.wsLastID = true, int64(l)
							}
						}
					}
				}
			}
			send(map[string]any{"aid": "peek_message"})
		}
		r.wsDuration = time.Since(t0)
		c.CloseNow()
		cancel()
	}
	var b strings.Builder
	for _, r := range rows {
		ws := "就绪"
		if !r.wsReady {
			ws = "【未就绪】" + r.wsErr
		}
		fmt.Fprintf(&b, "%-16s 名称服务 HTTP %d mdurl=%v · websocket %s（last_id=%d，%.1fs）\n       ",
			r.label, r.nsStatus, r.nsMdurl, ws, r.wsLastID, r.wsDuration.Seconds())
	}
	st := "PASS"
	if !rows[0].nsMdurl || !rows[0].wsReady {
		st = "FAIL" // 对照组没过：另两格的结论作废
		b.WriteString("⛔ 对照组（tqsdk UA）没过 —— 这一轮读数作废，先查网络与凭证")
	}
	report(name, st, strings.TrimRight(b.String(), " \n"))
}

// probePageExplore 摸清翻页协议的形状（H4）：left_kline_id 窗口下 charts.<id> 回哪些字段、
// 窗口里的 id 范围、以及日线（86400s）的 datetime 标在哪个时刻（H3 的前提）。
// 只印形状，不下结论 —— 结论在后面那条全历史翻页探针里。
func probePageExplore(ctx context.Context, md, tok string) {
	const name = "shinny-page-explore"
	if !optIn(name) {
		return
	}
	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader: http.Header{"User-Agent": {uaTqsdk}, "Accept": {"application/json"},
			"Authorization": {"Bearer " + tok}},
	})
	if err != nil {
		report(name, "FAIL", "连接失败: "+err.Error())
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)
	send := func(v any) { b, _ := json.Marshal(v); c.Write(ctx, websocket.MessageText, b) }
	const sym = "KQ.m@SHFE.rb"
	min, day := int64(60)*1e9, int64(86400)*1e9

	// 1m：从 id 0 起一个 20 根的窗口；日线：最新 8 根
	send(map[string]any{"aid": "set_chart", "chart_id": "pm", "ins_list": sym, "duration": min,
		"view_width": 20, "left_kline_id": 0})
	send(map[string]any{"aid": "set_chart", "chart_id": "pd", "ins_list": sym, "duration": day, "view_width": 8})
	send(map[string]any{"aid": "peek_message"})

	snap := map[string]any{}
	deadline := time.Now().Add(60 * time.Second)
	readyM, readyD := false, false
	for time.Now().Before(deadline) && !(readyM && readyD) {
		_, msg, rerr := c.Read(ctx)
		if rerr != nil {
			report(name, "FAIL", "读失败: "+rerr.Error())
			return
		}
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(msg, &m) == nil && m.Aid == "rtn_data" {
			for _, d := range m.Data {
				merge(snap, d)
			}
			if ch := obj(snap, "charts", "pm"); ch != nil {
				readyM, _ = ch["ready"].(bool)
			}
			if ch := obj(snap, "charts", "pd"); ch != nil {
				readyD, _ = ch["ready"].(bool)
			}
		}
		send(map[string]any{"aid": "peek_message"})
	}

	var b strings.Builder
	chartKeys := func(id string) string {
		ch := obj(snap, "charts", id)
		var ks []string
		for k, v := range ch {
			if k == "state" {
				continue
			}
			ks = append(ks, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(ks)
		return strings.Join(ks, " ")
	}
	serKeys := func(ser map[string]any) string {
		var ks []string
		for k, v := range ser {
			if k == "data" {
				continue
			}
			ks = append(ks, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(ks)
		return strings.Join(ks, " ")
	}
	ids := func(ser map[string]any) (lo, hi int64, n int) {
		lo = -1
		for k := range obj(ser, "data") {
			var id int64
			fmt.Sscan(k, &id)
			if lo < 0 || id < lo {
				lo = id
			}
			if id > hi {
				hi = id
			}
			n++
		}
		return
	}
	fmt.Fprintf(&b, "1m chart pm（left_kline_id=0, view_width=20）ready=%v：%s\n       ", readyM, chartKeys("pm"))
	sm := obj(snap, "klines", sym, fmt.Sprintf("%d", min))
	lo, hi, n := ids(sm)
	fmt.Fprintf(&b, "1m 序列节点：%s\n       1m data 里的 id：%d 个，范围 [%d, %d]\n       ", serKeys(sm), n, lo, hi)
	for _, id := range []int64{lo, hi} {
		if r := obj(obj(sm, "data"), fmt.Sprintf("%d", id)); r != nil {
			ts, _ := r["datetime"].(float64)
			fmt.Fprintf(&b, "  id %d datetime=%s\n       ", id, time.Unix(0, int64(ts)).In(cst).Format("2006-01-02 15:04:05"))
		}
	}
	fmt.Fprintf(&b, "日线 chart pd（view_width=8）ready=%v：%s\n       ", readyD, chartKeys("pd"))
	sd := obj(snap, "klines", sym, fmt.Sprintf("%d", day))
	fmt.Fprintf(&b, "日线序列节点：%s\n       ", serKeys(sd))
	var dids []int64
	for k := range obj(sd, "data") {
		var id int64
		fmt.Sscan(k, &id)
		dids = append(dids, id)
	}
	sort.Slice(dids, func(i, j int) bool { return dids[i] < dids[j] })
	for _, id := range dids {
		r := obj(obj(sd, "data"), fmt.Sprintf("%d", id))
		ts, _ := r["datetime"].(float64)
		var fs []string
		for k := range r {
			fs = append(fs, k)
		}
		sort.Strings(fs)
		fmt.Fprintf(&b, "  日线 id %d datetime=%s（%s）字段=%v open=%v close=%v volume=%v\n       ", id,
			time.Unix(0, int64(ts)).In(cst).Format("2006-01-02 15:04:05 Mon"), time.Unix(0, int64(ts)).UTC().Format("UTC 15:04"),
			fs, r["open"], r["close"], r["volume"])
	}
	st := "PASS"
	if !readyM || !readyD {
		st = "FAIL"
	}
	report(name, st, strings.TrimRight(b.String(), " \n"))
}
