// 天勤（快期）行情网关的探针：握手、深度、已到期合约。
//
// 为什么不并进 tools/probe/probe.py：那个脚本只用 Python 标准库，
// 而这个服务【要求】 permessage-deflate 扩展，标准库里没有 websocket 客户端。
// 与其在探针里手写一个带 deflate 的 websocket，不如单独放一个 Go 程序——
// 反正本库最终就是 Go 的，这段代码也顺带验证了要用的那个库确实能连上。
//
// 用法（需要仓库根的 .env 提供 SHINNY_USER / SHINNY_PASS）：
//
//	cd tools/probe/shinny && go run .
//
// 没有凭证时打印 SKIP 并以 0 退出——没有账户的人不该看到一片红。
//
// 退出码：0 全部符合 docs/probe.md 第六节的记录或已跳过；1 有偏离。
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	authURL = "https://auth.shinnytech.com/auth/realms/shinnytech/protocol/openid-connect/token"
	nsURL   = "https://api.shinnytech.com/ns?stock=false&backtest=false"

	// clientID 是公开的客户端名，不敏感。
	clientID = "shinny_tq"
)

// clientSecret 【刻意不写死在本仓】。
//
// 它是对方 SDK 的客户端身份，不是发给本项目的。contract.md 的缓解措施写着
// 「不设默认值，必须显式提供，让这成为明确的一步」——探针也是本仓的一部分，
// 把值提交进这个【公开仓库】等于让 clone 下来 `go run .` 的人在毫无察觉的情况下
// 用上那个身份，缓解措施就被本仓自己的工具绕过了。
//
// 「可被发现」不等于「可被转发」：值确实公开在 PyPI 的 tqsdk 包里
// （`tqsdk/auth.py` 的 `_request_token`，形如 `client_secret` 那一项），
// 本仓不再做那个转发点。
//
// 照实记：该值【曾】进入本仓公开历史，已做历史重写 + force-push 移除；
// 但 GitHub 对弃置对象保留一段时间，按 SHA 仍可取到，属「大幅降低」非「彻底消除」。
//
// 取值方式：环境变量或仓库根 `.env` 的 SHINNY_CLIENT_SECRET，缺失则 SKIP。
func clientSecret() string {
	if v := os.Getenv("SHINNY_CLIENT_SECRET"); v != "" {
		return v
	}
	return dotenvKey("SHINNY_CLIENT_SECRET")
}

var cst = time.FixedZone("CST", 8*3600)

// docs/probe.md 第六节记录的基线。
var baseline = struct {
	floorDay    string   // 免费档的历史地板
	expiredSyms []string // 必须仍然供得出 1m 的已到期合约
}{
	floorDay:    "2016-01-04",
	expiredSyms: []string{"SHFE.rb1605", "SHFE.rb1801", "SHFE.rb2310", "SHFE.rb2410"},
}

var failed int

// only 由 -only 指定：只跑名字含该子串的探针。
//
// 加它是因为 shinny-trading-day-predicted 是【时间窗口敏感】的：
// 判据只在盘中成立，收盘时跑什么都判不出来。为了补跑那一条而把整套重跑一遍，
// 代价不在耗时，在于**人会因此不跑**。
var only string

func skipped(name string) bool {
	return only != "" && !strings.Contains(name, only)
}

func report(name, status, detail string) {
	if skipped(name) {
		return
	}
	fmt.Printf("[%s] %s\n       %s\n\n", status, name, detail)
	if status == "FAIL" {
		failed++
	}
}

// dotenvKey 从仓库根的 .env 取一个键；找不到返回空串。
func dotenvKey(key string) string {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(root, ".env"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return ""
}

// dotenv 读凭证：环境变量优先，回落到仓库根的 .env。
func dotenv() (string, string) {
	u, p := os.Getenv("SHINNY_USER"), os.Getenv("SHINNY_PASS")
	if u == "" {
		u = dotenvKey("SHINNY_USER")
	}
	if p == "" {
		p = dotenvKey("SHINNY_PASS")
	}
	return u, p
}

func token(user, pw, secret string) (string, []string, error) {
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"username":      {user},
		"password":      {pw},
	}
	resp, err := http.PostForm(authURL, form)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("HTTP %d（最可能是 client_secret 被轮换了）", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &tr); err != nil {
		return "", nil, err
	}
	seg := strings.Split(tr.AccessToken, ".")[1]
	if n := len(seg) % 4; n != 0 {
		seg += strings.Repeat("=", 4-n)
	}
	raw, _ := base64.URLEncoding.DecodeString(seg)
	var claims struct {
		Grants struct {
			Features []string `json:"features"`
		} `json:"grants"`
	}
	json.Unmarshal(raw, &claims)
	return tr.AccessToken, claims.Grants.Features, nil
}

func mdURL(tok string) (string, error) {
	req, _ := http.NewRequest("GET", nsURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tqsdk-python 3.10.2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		MdURL string `json:"mdurl"`
	}
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &r)
	if r.MdURL == "" {
		return "", fmt.Errorf("名称服务没给 mdurl: HTTP %d %s", resp.StatusCode, b)
	}
	return r.MdURL, nil
}

// merge 是 DIFF 协议的 JSON merge-patch：null 删键，对象递归合并。
func merge(dst, src map[string]any) {
	for k, v := range src {
		if v == nil {
			delete(dst, k)
			continue
		}
		if sm, ok := v.(map[string]any); ok {
			dm, ok2 := dst[k].(map[string]any)
			if !ok2 {
				dm = map[string]any{}
				dst[k] = dm
			}
			merge(dm, sm)
			continue
		}
		dst[k] = v
	}
}

func obj(m map[string]any, path ...string) map[string]any {
	cur := m
	for _, p := range path {
		n, ok := cur[p].(map[string]any)
		if !ok {
			return nil
		}
		cur = n
	}
	return cur
}

type serie struct {
	total    int64
	earliest string
}

// depths 一次连接问多个合约的 1m 序列总根数与最早时刻。
func depths(ctx context.Context, md, tok string, syms []string) (map[string]serie, error) {
	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		// 服务端【要求】这个扩展；不带就是 400 Bad Request，
		// 且响应体只有 "Bad Request"，完全指不到真正的原因。
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader: http.Header{
			"User-Agent":    {"tqsdk-python 3.10.2"},
			"Accept":        {"application/json"},
			"Authorization": {"Bearer " + tok},
		},
	})
	if err != nil {
		return nil, err
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)

	send := func(v any) { b, _ := json.Marshal(v); c.Write(ctx, websocket.MessageText, b) }
	const durNano = int64(60) * 1e9
	durKey := fmt.Sprintf("%d", durNano)
	from, _ := time.ParseInLocation("2006-01-02", "2010-01-01", cst)

	for i, s := range syms {
		send(map[string]any{"aid": "set_chart", "chart_id": fmt.Sprintf("c%d", i),
			"ins_list": s, "duration": durNano, "view_width": 200,
			"focus_datetime": from.UnixNano(), "focus_position": 0})
	}
	send(map[string]any{"aid": "peek_message"})

	snap := map[string]any{}
	out := map[string]serie{}
	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) && len(out) < len(syms) {
		_, msg, err := c.Read(ctx)
		if err != nil {
			break
		}
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(msg, &m) != nil || m.Aid != "rtn_data" {
			send(map[string]any{"aid": "peek_message"})
			continue
		}
		for _, d := range m.Data {
			merge(snap, d)
		}
		for i, s := range syms {
			if _, seen := out[s]; seen {
				continue
			}
			ch := obj(snap, "charts", fmt.Sprintf("c%d", i))
			if ch == nil {
				continue
			}
			if ready, _ := ch["ready"].(bool); !ready {
				continue
			}
			ser := obj(snap, "klines", s, durKey)
			if ser == nil {
				out[s] = serie{total: -1}
				continue
			}
			last, ok := ser["last_id"].(float64)
			if !ok {
				continue
			}
			var mn float64
			n := 0
			for _, v := range obj(ser, "data") {
				r, _ := v.(map[string]any)
				ts, _ := r["datetime"].(float64)
				if ts == 0 {
					continue
				}
				if n == 0 || ts < mn {
					mn = ts
				}
				n++
			}
			e := ""
			if n > 0 {
				e = time.Unix(0, int64(mn)).In(cst).Format("2006-01-02 15:04")
			}
			out[s] = serie{total: int64(last) + 1, earliest: e}
		}
		send(map[string]any{"aid": "peek_message"})
	}
	return out, nil
}

// minuteLabels 拉一个合约某周期的 K 线，返回 自然日 -> [时刻标签]。
// 标签取的是 datetime 字段本身，即天勤的【开盘时刻】。
// minuteLabels 保持原签名不动——别的探针的基线是照它跑出来的，
// 改窗口就等于悄悄换了那些基线的取样条件。
func minuteLabels(ctx context.Context, md, tok, sym string, min int) (map[string][]string, error) {
	return minuteLabelsN(ctx, md, tok, sym, min, 300, 20)
}

// minuteLabelsN 同上，但窗口宽度与回溯天数可调。
//
// 拆出来是因为 300 根对 rb 恰好够一个完整日盘，对 ag（每日 555 根）就不够——
// 于是「窗口里没有完整的日盘」，那看起来像数据的问题，其实是取数窗口的问题。
// **取不到和不存在长得一样**，所以宁可让窗口成为一个显式参数。
func minuteLabelsN(ctx context.Context, md, tok, sym string, min, width, daysBack int) (map[string][]string, error) {
	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader: http.Header{
			"User-Agent":    {"tqsdk-python 3.10.2"},
			"Accept":        {"application/json"},
			"Authorization": {"Bearer " + tok},
		},
	})
	if err != nil {
		return nil, err
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)

	send := func(v any) { b, _ := json.Marshal(v); c.Write(ctx, websocket.MessageText, b) }
	dur := int64(min) * 60 * 1e9
	dk := fmt.Sprintf("%d", dur)
	req := map[string]any{"aid": "set_chart", "chart_id": "g", "ins_list": sym,
		"duration": dur, "view_width": width}
	// daysBack <= 0 表示【锚在末端】：不给 focus_datetime，服务端给最新的 width 根。
	//
	// 锚在起点（给 focus_datetime）时，窗口的【末端】落在哪取决于品种每天多少根——
	// ag 每天 555 根，2000 根只够 3.6 天，末端就断在某个交易日的中间。
	// 而断在中间这件事**在输出里看不见**，于是它被读成了「那天的时段只有这么长」。
	if daysBack > 0 {
		from := time.Now().AddDate(0, 0, -daysBack)
		req["focus_datetime"] = from.UnixNano()
		req["focus_position"] = 0
	}
	send(req)
	send(map[string]any{"aid": "peek_message"})

	snap := map[string]any{}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := c.Read(ctx)
		if err != nil {
			return nil, err
		}
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(msg, &m) != nil || m.Aid != "rtn_data" {
			send(map[string]any{"aid": "peek_message"})
			continue
		}
		for _, d := range m.Data {
			merge(snap, d)
		}
		ch := obj(snap, "charts", "g")
		ser := obj(snap, "klines", sym, dk)
		if ch == nil || ser == nil {
			send(map[string]any{"aid": "peek_message"})
			continue
		}
		if ready, _ := ch["ready"].(bool); !ready {
			send(map[string]any{"aid": "peek_message"})
			continue
		}
		if _, ok := ser["last_id"].(float64); !ok {
			send(map[string]any{"aid": "peek_message"})
			continue
		}
		out := map[string][]string{}
		for _, v := range obj(ser, "data") {
			r, _ := v.(map[string]any)
			ts, _ := r["datetime"].(float64)
			if ts == 0 {
				continue
			}
			t := time.Unix(0, int64(ts)).In(cst)
			d := t.Format("2006-01-02")
			out[d] = append(out[d], t.Format("15:04"))
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s 超时未就绪", sym)
}

// dayLabels 取某个合约在最后一个【日盘完整】自然日上、该周期的日盘标签。
//
// 「日盘完整」用 15:00 之后仍有 09:00–15:00 区间的根来判；天勤按开盘时刻标注，
// 所以日盘最后一根是 14:xx 而不是 15:00，这里改用「至少 4 根落在日盘区间」。
func dayLabels(m map[string][]string) (string, []string) {
	days := make([]string, 0, len(m))
	for d := range m {
		days = append(days, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for _, d := range days {
		var day []string
		for _, t := range m[d] {
			if t >= "09:00" && t <= "15:00" {
				day = append(day, t)
			}
		}
		sort.Strings(day)
		if len(day) >= 4 {
			return d, day
		}
	}
	return "", nil
}

// probeGridIsClockGrid 钉住本轮最大的一条结论：
// **天勤用纯时钟网格、按开盘时刻标注，没有「相位」这个概念。**
//
// 判据是两条同时成立：
//
//   - 沪银（标称夜盘 330 分，对 60 余 30）与螺纹（120 分，整除）的 60m
//     日盘标签【完全相同】——若天勤改成交易时间轴，这两个会错开 30 分钟；
//   - 标签是 09:00 而不是 09:30/10:00 之类——即按【开盘时刻】标注。
//
// 这条 FAIL 就说明 design 第二节那个架构决策（自己从 1m 聚合、
// 两套聚合规则、默认未定）的前提变了。**它是本轮唯一一条会改变
// 全部高周期 K 线内容的结论，所以必须有东西盯着。**
func probeGridIsClockGrid(ctx context.Context, md, tok string) {
	want := []string{"09:00", "10:00", "11:00", "13:00", "14:00"}
	got := map[string][]string{}
	var when string
	for _, sym := range []string{"SHFE.ag2612", "SHFE.rb2701"} {
		m, err := minuteLabels(ctx, md, tok, sym, 60)
		if err != nil {
			report("shinny-grid-clock", "FAIL", sym+" 拉取失败: "+err.Error())
			return
		}
		d, lab := dayLabels(m)
		when, got[sym] = d, lab
	}
	ag, rb := got["SHFE.ag2612"], got["SHFE.rb2701"]
	same := len(ag) == len(rb)
	if same {
		for i := range ag {
			if ag[i] != rb[i] {
				same = false
				break
			}
		}
	}
	isClock := len(ag) == len(want)
	if isClock {
		for i := range ag {
			if ag[i] != want[i] {
				isClock = false
				break
			}
		}
	}
	st := "PASS"
	if !same || !isClock {
		st = "FAIL"
	}
	report("shinny-grid-clock", st, fmt.Sprintf(
		"%s  ag2612 60m 日盘=%v\n       %s  rb2701 60m 日盘=%v\n"+
			"       两者必须【相同】(实为 %v) 且等于时钟网格 %v (实为 %v)\n"+
			"       —— 天勤按开盘时刻标注、无相位；新浪是 09:30 10:45 13:45 14:45 15:00，两套不同",
		when, ag, when, rb, same, want, isClock))
}

// probeGfexNoNight 从 1m 数据反推 GFEX 的【实际】交易时段。
//
// 背景：openmd 目录 334 MiB，GFEX 从 40% 之后才出现，本仓分析用的两份快照
// 只覆盖前 5%——于是「GFEX 有没有夜盘」一度被判成「要先拉完目录才能答」。
// 那是路径选错：**一根 1m 存在 ⇔ 那一分钟在交易**，分钟数据直接就能答，
// 而 calendar/derived 本来就是照这个思路设计的。
//
// 判据两条，缺一不可：
//   - GFEX 三个品种（si/lc/ps）**没有**任何 20:00 之后 / 04:00 之前的 1m；
//   - **对照组** SHFE.rb **必须有**——否则「没检测到夜盘」可能只是方法失效。
//
// 对照组就是把那条最便宜的证伪检查焊死在流程里，让人没法跳过。
// 「没检测到」和「不存在」是两回事，而它们看起来一模一样：
// token 过期、symbol 拼错、时区算错，都会安安静静地给出「没有夜盘」。
func probeGfexNoNight(ctx context.Context, md, tok string) {
	hasNight := func(sym string) (bool, int, error) {
		m, err := minuteLabels(ctx, md, tok, sym, 1)
		if err != nil {
			return false, 0, err
		}
		n := 0
		night := false
		for _, ts := range m {
			for _, t := range ts {
				n++
				if t >= "20:00" || t < "04:00" {
					night = true
				}
			}
		}
		return night, n, nil
	}

	var b strings.Builder
	bad := 0
	for _, sym := range []string{"KQ.m@GFEX.si", "KQ.m@GFEX.lc", "KQ.m@GFEX.ps"} {
		night, n, err := hasNight(sym)
		if err != nil {
			fmt.Fprintf(&b, "%-16s 失败 %v\n       ", sym, err)
			bad++
			continue
		}
		if night {
			bad++
		}
		fmt.Fprintf(&b, "%-16s %4d 根 1m  夜盘=%v（期望 false）\n       ", sym, n, night)
	}
	// 对照组：方法本身能不能看见夜盘
	night, n, err := hasNight("KQ.m@SHFE.rb")
	if err != nil || !night {
		bad++
	}
	fmt.Fprintf(&b, "对照 KQ.m@SHFE.rb  %4d 根 1m  夜盘=%v（期望 true——"+
		"它为 false 说明是方法失效，不是 GFEX 真没夜盘）", n, night)

	st := "PASS"
	if bad > 0 {
		st = "FAIL"
	}
	report("shinny-gfex-no-night", st, b.String())
}

// probeNightGap 量「有夜盘的品种，连续多少个交易日【没有】夜盘」。
//
// 起因：`docs/design.md` §七 五 用「连续 N 个交易日 actual == 0」判模板过期，
// 而 N 写着**未验**，注解是「长假最长多少个连续交易日无夜盘，要从历史数据量」。
//
// # 口径（先定义被数的东西，再挑量法）
//
//	交易日 T     该自然日上日盘（09:00–15:00）至少 3 根
//	T 的夜盘     落在【上一个交易日那个自然日】20:00–次日 04:00 里的根
//	             —— 中国期货夜盘 21:00 开，属于【下一个】交易日
//	actual == 0  那个区间一根都没有
//
// # 为什么用 60m 而不是 1m
//
//	rb 每天约 465 根 1m ⇒ 一窗 2000 根只盖 4.3 天 ⇒ 十年要八百多次请求
//	60m 每天约 6.9 根   ⇒ 一窗盖约 288 天 ⇒ 十年十七次
//
// 而 60m 能不能用，是先验过的：天勤的 60m **是时钟对齐的**，
// 夜盘就标在 `21:00` / `22:00`（新浪不是，见 `probe.md` 坑三之四）。
//
// ⚠️ **射程**：只量 `KQ.m@SHFE.rb` 一个品种，**是下界不是全市场**。
// 别的品种夜盘时长不同，停夜盘的安排也可能不同。
func probeNightGap(ctx context.Context, md, tok string) {
	const sym = "KQ.m@SHFE.rb"
	all := map[string][]string{}
	fails := 0
	// 从十年多以前往回走，每步 240 天，窗口 2000 根（约 288 天）⇒ 有重叠，不留缝。
	for back := 3900; back >= 0; back -= 240 {
		m, err := minuteLabelsN(ctx, md, tok, sym, 60, 2000, back)
		if err != nil {
			fails++
			continue
		}
		for d, ts := range m {
			all[d] = append(all[d], ts...)
		}
	}
	if fails > 2 {
		report("shinny-night-gap", "FAIL",
			fmt.Sprintf("十七窗里有 %d 窗取数失败 —— 缺口会把「无夜盘」造出来，不报结论", fails))
		return
	}

	dates := make([]string, 0, len(all))
	for d := range all {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	hasDay := func(d string) bool {
		n := 0
		for _, t := range all[d] {
			if t >= "09:00" && t < "15:00" {
				n++
			}
		}
		return n >= 3
	}
	hasNight := func(d string) bool {
		for _, t := range all[d] {
			if t >= "20:00" || t < "04:00" {
				return true
			}
		}
		return false
	}

	var tdays []string
	for _, d := range dates {
		if hasDay(d) {
			tdays = append(tdays, d)
		}
	}
	// 对照组焊在里面：交易日太少 ⇒ 是取数塌了，不是市场变了。
	if len(tdays) < 2000 {
		report("shinny-night-gap", "FAIL",
			fmt.Sprintf("只认出 %d 个交易日（期望 2000+）—— 判据或取数有问题，不报结论", len(tdays)))
		return
	}

	longest, longFrom, longTo := 0, "", ""
	runs := map[int]int{} // 段长 -> 段数
	cur, curFrom := 0, ""
	closeRun := func(to string) {
		if cur > 0 {
			runs[cur]++
			if cur > longest {
				longest, longFrom, longTo = cur, curFrom, to
			}
		}
		cur = 0
	}
	for i := 1; i < len(tdays); i++ {
		if !hasNight(tdays[i-1]) {
			if cur == 0 {
				curFrom = tdays[i]
			}
			cur++
		} else {
			closeRun(tdays[i-1])
		}
	}
	closeRun(tdays[len(tdays)-1])

	lens := make([]int, 0, len(runs))
	for k := range runs {
		lens = append(lens, k)
	}
	sort.Ints(lens)
	var dist strings.Builder
	for _, k := range lens {
		fmt.Fprintf(&dist, "%d个交易日×%d段 ", k, runs[k])
	}

	// 基线：2026-09-09 实测。变了就该有人来看一眼 —— 这不是一条永远绿的断言。
	const wantLongest, wantFrom, wantTo = 64, "2020-02-03", "2020-05-06"
	st := "PASS"
	note := ""
	if longest != wantLongest || longFrom != wantFrom || longTo != wantTo {
		st = "FAIL"
		note = fmt.Sprintf("\n       ⚠️ 与基线不符（基线 %d 个交易日 %s…%s，2026-09-09 实测）——"+
			"要么数据变了，要么又发生了一次停夜盘，去看一眼",
			wantLongest, wantFrom, wantTo)
	}
	report("shinny-night-gap", st, fmt.Sprintf(
		"交易日 %d 个（%s…%s）；无夜盘的连续段分布：%s\n"+
			"       最长 = %d 个交易日（%s … %s）—— 那是 2020 年的政策性停夜盘，不是长假%s",
		len(tdays), tdays[0], tdays[len(tdays)-1], dist.String(),
		longest, longFrom, longTo, note))
}

func main() {
	flag.StringVar(&only, "only", "", "只跑名字含该子串的探针，如 -only trading-day")
	flag.Parse()

	fmt.Println("天勤行情网关探针 — 基线见 docs/probe.md 第六节")
	if only != "" {
		fmt.Printf("（-only %q：其余探针跳过）\n", only)
	}
	fmt.Println()

	user, pw := dotenv()
	secret := clientSecret()
	if user == "" || pw == "" || secret == "" {
		missing := []string{}
		if user == "" {
			missing = append(missing, "SHINNY_USER")
		}
		if pw == "" {
			missing = append(missing, "SHINNY_PASS")
		}
		if secret == "" {
			missing = append(missing, "SHINNY_CLIENT_SECRET")
		}
		report("shinny-ws", "SKIP",
			"缺少 "+strings.Join(missing, " / ")+"（环境变量或仓库根 .env），跳过。\n"+
				"       SHINNY_CLIENT_SECRET 的取值见 docs/probe.md 6.1——本仓刻意不提交它")
		return
	}

	tok, feats, err := token(user, pw, secret)
	if err != nil {
		report("shinny-auth", "FAIL", "取 token 失败: "+err.Error())
		os.Exit(1)
	}
	hasFutr := false
	for _, f := range feats {
		if f == "futr" {
			hasFutr = true
		}
	}
	sort.Strings(feats)
	st := "PASS"
	if !hasFutr {
		st = "FAIL"
	}
	report("shinny-auth", st, fmt.Sprintf("futr=%v  features=%v", hasFutr, feats))

	md, err := mdURL(tok)
	if err != nil {
		report("shinny-ns", "FAIL", err.Error())
		os.Exit(1)
	}
	report("shinny-ns", "PASS", "mdurl="+md+"（不能写死域名，必须问名称服务）")

	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Second)
	defer cancel()

	// 关键一条：【已到期】合约还供不供分钟线。
	//
	// 主连 KQ.m@ 有 89 万根，但它未复权（见 6.5），按本库设计只作对账；
	// 自建主连的真值是合约级序列，而那需要一长串早已退市的合约。
	// 这一条不成立的话，「分钟级硬缺口已解决」就不成立——
	// 缺口只是从「新浪 1023 根」挪到了「天勤只给在市合约」。
	syms := append([]string{"KQ.m@SHFE.rb"}, baseline.expiredSyms...)
	var res map[string]serie
	if !skipped("shinny-depth") {
		res, err = depths(ctx, md, tok, syms)
		if err != nil {
			report("shinny-depth", "FAIL", "连接或拉取失败: "+err.Error())
			os.Exit(1)
		}
	}

	// 分子与分母必须同源。
	//
	// 原来用一个 bad 在【5 个】symbol（含主连 KQ.m@）上累加，却减在
	// len(expiredSyms)=【4】上：主连超时就会打印「已到期合约 3/4」，
	// 五个全挂会打印「-1/4」——而这正是决定 D2 的那个数字。
	expired := make(map[string]bool, len(baseline.expiredSyms))
	for _, s := range baseline.expiredSyms {
		expired[s] = true
	}
	var b strings.Builder
	expiredBad, otherBad := 0, 0
	countBad := func(s string) {
		if expired[s] {
			expiredBad++
		} else {
			otherBad++
		}
	}
	for _, s := range syms {
		r, ok := res[s]
		if !ok {
			fmt.Fprintf(&b, "%-16s 超时未就绪\n       ", s)
			countBad(s)
			continue
		}
		if r.total <= 0 {
			fmt.Fprintf(&b, "%-16s 【无序列】\n       ", s)
			countBad(s)
			continue
		}
		fmt.Fprintf(&b, "%-16s %7d 根  最早 %s\n       ", s, r.total, r.earliest)
	}
	st = "PASS"
	if expiredBad > 0 || otherBad > 0 {
		st = "FAIL"
	}
	report("shinny-depth", st, strings.TrimRight(b.String(), " \n")+
		fmt.Sprintf("\n       已到期合约 %d/%d 有 1m 历史；地板线记录为 %s",
			len(baseline.expiredSyms)-expiredBad, len(baseline.expiredSyms), baseline.floorDay))

	if !skipped("shinny-grid-clock") {
		probeGridIsClockGrid(ctx, md, tok)
	}
	if !skipped("shinny-gfex-no-night") {
		probeGfexNoNight(ctx, md, tok)
	}
	if !skipped("shinny-night-gap") {
		probeNightGap(ctx, md, tok)
	}
	if !skipped("shinny-trading-day-predicted") {
		probeTradingDayPredicted(ctx, md, tok)
	}
	if !skipped("shinny-suspended-night-span") {
		probeSuspendedNightSpan(ctx, md, tok)
	}
	if !skipped("shinny-embedded-template") {
		probeEmbeddedTemplateMatchesMeasured(ctx, md, tok)
	}

	if failed > 0 {
		fmt.Printf("%d 条偏离记录 → 先判断是上游变了还是记录错了，再更新 docs/probe.md\n", failed)
		os.Exit(1)
	}
	if only != "" {
		fmt.Printf("含 %q 的探针与记录一致；**其余未跑，不代表通过**\n", only)
		return
	}
	fmt.Println("全部与 docs/probe.md 第六节的记录一致")
}
