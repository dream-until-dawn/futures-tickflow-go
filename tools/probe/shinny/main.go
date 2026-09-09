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

// symsFlag 由 -syms 指定：临时覆盖 night-hours 的品种表（逗号分隔）。
var symsFlag string
var winFlag string
var gridFlag int

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
//	rb 每天 345 根 1m ⇒ 一窗 2000 根盖 5.8 天 ⇒ 十年约 450 次请求
//	60m 每天 7 根     ⇒ 一窗盖约 280 个交易日 ⇒ 十年十七次
//
// ⚠️ 345 的出处是本仓自己的 `nominalDayMinutes`（`345 = 225 + 120`）。
// **上一版这里写的是 465，而那个数没有出处** —— 而它所在的正是「为什么用 60m」
// 这个【方法选择的前提】。⇒ 一个没有出处的数，不该出现在一个前提里。
//
// 而 60m 能不能用，是先验过的：天勤的 60m **是时钟对齐的**，
// 夜盘就标在 `21:00` / `22:00`（新浪不是，见 `probe.md` 坑三之四）。
//
// ⚠️ **射程**：只量 `KQ.m@SHFE.rb` 一个品种，**是下界不是全市场**。
// 别的品种夜盘时长不同，停夜盘的安排也可能不同。
func probeNightGap(ctx context.Context, md, tok string) {
	// 默认 rb；`-syms` 可以换（取第一个）。换品种的意义不在于「多量几个」——
	// 这条判据对**夜盘跨零点**的品种不成立，而下面那道拦截正是为此而设。
	// 不让它可换，那道拦截就永远没被行使过，而**没被行使过的拦截和没有拦截差不多**。
	sym := "KQ.m@SHFE.rb"
	if symsFlag != "" {
		sym = strings.Split(symsFlag, ",")[0]
	}
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
	// ⛔ **这个早退不是冗余，它承担的是【次序】** —— 别把它删掉换成后面那次 `enoughDays`。
	//
	// 评审方 2026-09-09 提过「删掉早退、统一走 enoughDays」，理由是那个阈值写了两处。
	// **问题是真的，而那个解法会引入一个更坏的失败**：本函数在早退与 `enoughDays`
	// 之间还有一道**跨零点拦截**（`crossed > len(all)/10`），它自己会 `report` 并 `return`。
	//
	//	取数大面积失败 ⇒ `all` 稀疏 ⇒ 那两个数都来自同一份稀疏数据
	//	⇒ 拦截可能先响，而它报的是「**这个品种的夜盘跨零点**」
	//	⇒ **「取不到」被报成「跨零点」** —— 本仓那条「取不到和不存在长得一样」的又一个形状，
	//	  而且是我们亲手造的。
	//
	// ⛔ **而这个早退把那个风险【压小】了，没有【消除】它 ——
	// 这一句是评审方 2026-09-09 修正我的，原来我写得太满。**
	//
	//	早退只在 `fails > maxFetchFails`（今天是 2）时拦 ⇒ **失败 1~2 窗时照样往下走**
	//	十年 17 窗 ⇒ 丢 2 窗最多缺 **11.8%** 的天数
	//	而拦截算的是 `crossed / len(all)` —— **分母是【取到的天数】，不是【应该有的天数】**
	//	⇒ 最坏情况（丢掉的全是不跨零点的日子）比值升 **1/(1-0.118) = 1.134 倍**
	//
	//	实测 rb 今天 **2.9%**（阈值 10%）⇒ 最坏 3.3%，离阈值还远。
	//	⚠️ **但余量是【按品种】的，不是普适的**：真实跨零点比例落在 **8.8%~10%** 的品种，
	//	  丢 2 窗就足以被推过阈值 ⇒ **误报成「跨零点」**。今天没有这样的品种，
	//	  而这句话是给「哪天有」准备的。
	//
	// ⛔ **而上面只是【一侧】。另一侧方向相反，而且对 `rb` 是【可达】的**
	// （评审方 2026-09-09 补的，我把它量到了比他更紧的结论）：
	//
	//	丢掉的窗里**没有**跨零点日 ⇒ 只有分母小 ⇒ 比值**虚高** ⇒ **误报**成「跨零点」（上面那一侧）
	//	丢掉的窗**正好装着**跨零点日 ⇒ 分子也小 ⇒ 比值**虚低** ⇒ **该拦不拦**
	//	  ⇒ 一个【真的跨零点】的品种通过拦截，探针接着去判基线 ——
	//	    **而那条基线的判据对跨零点品种本来就不成立，那正是这道拦截存在的全部理由。**
	//
	// ⚠️ **这一侧对 `rb` 不是假设，而且只需要丢【一窗】**（实测 + 算术，2026-09-09）：
	//
	//	rb 的跨零点日全部挤在 **2016-01-06…2016-04-29**（77 个交易日，探针自己印的）
	//	取数循环 `back := 3900; back >= 0; back -= 240`，`focus_position = 0`
	//	⇒ 窗口从锚点**往后**铺；`back` 越大越老
	//	  窗 1 起点 **2016-01-05**；窗 2 起点 **2016-09-01**（在那段结束后 125 天）
	//	⇒ **整个跨零点区间只被【窗 1】一个窗覆盖。**
	//	⇒ 丢掉窗 1（`fails = 1`，远在 `maxFetchFails = 2` 之内）⇒ `crossed ≈ 0`
	//	  ⇒ **拦截静默，基线照跑。**
	//
	//	⚠️ 评审方推的是「那一两窗」，**实测是一窗 —— 而允许量是它的两倍。**
	//
	// ⛔ **而上面整段有一个【到期日】，因为窗口锚在 `time.Now()` 上 —— 它每天往后滑一天。**
	//（评审方 2026-09-09 发现，我复算时把日期各修正了一天：他给的是「最后可见日」，
	// 不是「消失日」。）
	//
	//	判据：一天 `d` 还看得见 ⟺ `d >= 今天 − 3900`（`focus_position = 0` ⇒ 窗口从锚点往后铺）
	//
	//	今天（2026-09-09）窗 1 起点 **2016-01-05**，而区间首日是 **2016-01-06**
	//	⇒ **余量 1 天。**
	//	区间首日 最后可见 **2026-09-10** ⇒ **2026-09-11 起看不见**
	//	区间末日 最后可见 **2027-01-02** ⇒ **2027-01-03 起看不见**
	//
	// ⇒ 三个后果，都有日期：
	//
	//	一、`probe.md` 6.9 那条读数（rb 跨零点 2.9% / 77 个交易日 / 2016-01-06…2016-04-29）
	//	  **从 2026-09-11 起逐日失效，2027-01-03 之后归零。**
	//	二、**上面那句「区间只被窗 1 覆盖 ⇒ 丢一窗就够」，同一天起不再成立** ——
	//	  区间会从窗 1 的左边界开始滑出去。
	//	三、⛔ **这一条我写错过，已撤回，原文与反证并排留着：**
	//
	//	  原文：「【漏拦】那一侧不再需要取数失败这个巧合 —— `crossed` 自己降到 0
	//	        ⇒ 拦截自己变哑 ⇒ 从需要巧合的场景变成一个有日期的必然。」
	//
	//	  **不成立**（评审方 2026-09-09 自己提出、我核过）：`night-gap` 的判据坏在
	//	  「跨零点品种的 00:00–04:00 那几根属于**前一晚**开的那一场」——
	//	  **它要求窗口里存在跨零点日**。而 2027-01-03 之后 `rb` 在窗口里
	//	  **真的没有**跨零点日了（今天的夜盘 21:00–23:00，不过零点）
	//	  ⇒ **判据在那个窗口上是成立的 ⇒ 拦截静默是【正确行为】，不是漏拦。**
	//
	//	  ⇒ **把「变哑」直接读成「变错」是这条的错法。**
	//	    滑过去之后**行为反而更干净**（窗口里不再有判据不适用的那一段）。
	//
	//	⇒ **真正的残留不是行为，是【读数与它的理由脱钩】：**
	//	  文档还挂着「跨零点 2.9%，离 10% 还远」，而**那个 2.9% 已经不是当下的读数**；
	//	  读它的人会拿它去推理，而它通过拦截是因为**证据没了**，不是因为余量。
	//
	//	  ⇒ 所以「写下有效期」不只是够用，**它是对症的那一个**：
	//	    要修的是**读数的时效**，不是行为。
	//
	// ⛔ **这是本仓那条老病的新形状，而且更隐蔽：**
	//
	//	**射程写成位置** ⇒ 位置至少是**静止**的
	//	**射程锚在 `time.Now()`** ⇒ 它**每天都在动**，而读数带着一个**没写下来的到期日**
	//
	// ⚠️ 同一机制也悬在 `probeNightGap` 的基线上（`wantLongest = 64`，
	// 2020-02-03…2020-05-06）：那段的起点 2020-02-03 **最后可见 2030-10-08**。
	// 离得远，**而机制是同一个**。
	//
	// ⇒ 真正的修法是把 `daysBack` 换成一个**绝对起点**（例如「自 2016-01-04 起」），
	//   否则每一条基于窗口的读数都带着一个**自己会走的射程**。
	//   ⚠️ 那要重测所有窗口相关的基线 ⇒ **单独一格，不在这里。这里先把到期日写下来。**
	//
	// ⇒ **只声明一个方向的射程，比不声明更容易被信**（本仓在 `DayOf` 那一格吃过同样的亏：
	//   注释只说了「长假前后【多】给夜盘段」，而 `i > 0` 制造的是「左端【少】给一段」）。
	//
	// ⚠️ **两侧都只算过，一次都没构造过。** 构造它要伪造取数失败，成本高于收益 ——
	// **所以这一格【有意停在算术上】，而这句话本身就是它的射程。**
	//
	// ⇒ **真正的成因不是早退拦不拦，是那个分母。** 修分母是另一格（要知道「应该有多少天」，
	//   而那需要日历 —— 正是本仓还没有的东西）。
	//
	// ⇒ 所以次序保留，而**两处实现那个真问题用共用常量解**（见 maxFetchFails）。
	// ⚠️ 而这个决定**不依赖上面那条因果**：共用常量本身就修好了「一个阈值两处写」，
	// 并且比删早退多保住一样东西 —— **那条更贴近现场的错误信息**（「取数失败 N 窗」）。
	//
	//	**一个修法如果需要一条未经实测的因果才成立，它是脆的。这个不需要。**
	if fails > maxFetchFails {
		report("shinny-night-gap", "FAIL",
			fmt.Sprintf("十七窗里有 %d 窗取数失败（上限 %d）—— 缺口会把「无夜盘」造出来，不报结论",
				fails, maxFetchFails))
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

	// 夜盘【跨零点】的品种，这个判据不成立：自然日 D 上的 00:00–01:00 那几根属于
	// D 自己的夜盘（前一晚 21:00 开的），于是即使 D 晚上那一场没开，D 仍然「有夜盘的根」
	// ⇒ 缺口被漏掉，而且**安静地**漏掉。换 ag/au/cu/zn/sc 来跑就是这种情况。
	crossed := 0
	for _, ts := range all {
		for _, t := range ts {
			if t >= "00:00" && t < "04:00" {
				crossed++
				break
			}
		}
	}
	// ⚠️ 阈值取 10% 是有余量的，而**余量必须看得见**。
	// 但只印比例还不够 —— 评审方 2026-09-09 指出，**那是用错了统计量**：
	//
	//	rb 的 3% **不是散布在十年里的，是挤在一个连续的四个月里**
	//	⇒ 在那四个月【内部】，这条判据不是「3% 不准」，是 **100% 不可用**
	//
	//	**一个按比例设的阈值，分不开「3% 散布在十年」和「3% 挤在一个季度」——
	//	而后者是完全不同的风险：前者是噪声，后者是一段【整体失效的区间】。**
	//
	// ⇒ 所以真正的保障从来不是这个 10%（它只是让 rb 通过），
	//   而是「那四个月里的假期被手工逐个排查过」。
	//   ⇒ 两个数一起印：**全局比例**，和**最长连续跨零点区间**——
	//     后者才是这条判据真正的失效尺度。
	//
	//	**一个没写明它是什么统计量的余量，印出来也还是没有余量。**
	crossedPct := 100.0 * float64(crossed) / float64(len(all))
	// ⛔ 第一版这里量的是「最长【连续】跨零点自然日」，而那个数是 **5** ——
	// 不是因为窗口只有 5 天，是因为**周的节律**：周日不开夜盘 ⇒ 周一没有零点后的根
	// ⇒ 每周必断一次。**那个统计量被一个和失效毫无关系的周期支配了。**
	//
	//	⇒ 要量的是「这条判据【不可用的那段区间】有多长」，
	//	  而不是「跨零点的日子有没有挨在一起」。挨不挨在一起由周末决定。
	//
	// ⇒ 改量【首末跨零点日之间的跨度】里有多少个交易日 —— 那才是失效区间的长度。
	crossFrom, crossTo := "", ""
	for _, d := range dates {
		for _, t := range all[d] {
			if t >= "00:00" && t < "04:00" {
				if crossFrom == "" {
					crossFrom = d
				}
				crossTo = d
				break
			}
		}
	}
	crossSpan := 0 // 在 tdays 建好之后再填（见下）
	if crossed > len(all)/10 {
		// ⚠️ 把 `fails` 一起印出来（评审方 2026-09-09 提的）：
		// **分母是「取到的天数」，而它取决于几窗成功。**
		// 读的人得知道这个比例是在几窗失败的情况下算出来的，否则他没法判断这条结论有多硬。
		report("shinny-night-gap", "FAIL", fmt.Sprintf(
			"这个品种的夜盘【跨零点】（%d/%d 个自然日有 00:00–04:00 的根；取数失败 %d 窗）——"+
				"本判据对它不成立，会安静地漏掉缺口，不报结论。\n"+
				"       ⚠️ 分母是【取到的】天数：失败窗越多，这个比例越虚高（见早退那一段的算术）。\n"+
				"       只有夜盘收在零点之前的品种才能用这条（见 probe.md 6.9 的射程）",
			crossed, len(all), fails))
		return
	}

	var tdays []string
	for _, d := range dates {
		if hasDay(d) {
			tdays = append(tdays, d)
		}
	}
	// 失效区间的跨度：首末跨零点日之间有多少个【交易日】。
	for _, d := range tdays {
		if crossFrom != "" && d >= crossFrom && d <= crossTo {
			crossSpan++
		}
	}

	// 对照组焊在里面：交易日不够 ⇒ 是取数塌了，不是市场变了。
	//
	// ⛔ 这里原来也是 `len(tdays) < 2000` —— 和另外两条探针同一个错。
	// **而我上一趟改那两条时【漏了这一处】，因为我扫的是字面串 `len(all) < 2000`，
	// 而这里的变量叫 `tdays`。**（评审方 2026-09-09 读这条分支时点出来的。）
	// ⇒ 判据又一次写成了「它现在长什么样」，而不是性质 —— 本仓反复撞的那个形状，
	//   这次撞在**我自己修那个形状的那一趟里**。
	sortedT := append([]string{}, tdays...)
	sort.Strings(sortedT)
	if ok, why := enoughDays(sortedT, fails); !ok {
		report("shinny-night-gap", "FAIL",
			fmt.Sprintf("取数不足：%s —— 判据或取数有问题，不报结论", why))
		return
	}

	// 内置时段表自 2020-05-06 生效（calendar/embedded 的 baseFrom）。
	// 这一行量的是：**本库的目标深度里，有多少落在日历【答得了】的那一侧。**
	// 它不是这条探针的主结论，但它是一个别处拿不到的数：
	// 目标深度由数据源决定（2016-01-04），而覆盖由内置表决定（2020-05-06）——
	// **两者之间那一段，Walk 一律给 ErrUncovered。**
	covered := 0
	for _, d := range tdays {
		if d >= "2020-05-06" {
			covered++
		}
	}
	// 顺带算两个【假如】：把生效起点往前挪到这两个日子，覆盖面各是多少。
	// 这两个日子不是随便挑的，它们是 6.10 量出来的最后两次时段变更：
	//   2019-12-11  最后一次变更（CZCE）之后 ⇒ 那之后的时段全都等于今天
	//   2016-05-03  SHFE 那次变更之后 ⇒ 再往前就要第二段模板了
	// **把「能往前挪」变成一个数，而不是一句「原则上可以」。**
	var wouldCover [2]int
	for i, from := range [2]string{"2019-12-11", "2016-05-03"} {
		for _, d := range tdays {
			if d >= from {
				wouldCover[i]++
			}
		}
	}

	longest, longFrom, longTo := 0, "", ""
	runs := map[int]int{} // 段长 -> 段数
	// byYear 把每一段的起始交易日按年归拢。
	// **聚合数分辨不了「检出漏了」和「期望算错了」，而日期能**（评审方 2026-09-09 要的）：
	// 整条 SYN-7 的措辞靠「2..63 是空的」这个形状，而一个漏掉缺口的检出方法，
	// 同样可能漏掉那些 2 天的段。
	byYear := map[string][]string{}
	cur, curFrom := 0, ""
	closeRun := func(to string) {
		if cur > 0 {
			runs[cur]++
			tag := curFrom
			if cur > 1 {
				tag = fmt.Sprintf("%s×%d", curFrom, cur)
			}
			byYear[curFrom[:4]] = append(byYear[curFrom[:4]], tag)
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
	ys := make([]string, 0, len(byYear))
	for y := range byYear {
		ys = append(ys, y)
	}
	sort.Strings(ys)
	var perYear strings.Builder
	for _, y := range ys {
		sort.Strings(byYear[y])
		fmt.Fprintf(&perYear, "\n       %s（%d 段）%s", y, len(byYear[y]),
			strings.Join(byYear[y], " "))
	}

	// 基线：2026-09-09 实测。变了就该有人来看一眼 —— 这不是一条永远绿的断言。
	const wantLongest, wantFrom, wantTo = 64, "2020-02-03", "2020-05-06"
	st := "PASS"
	note := ""
	// 基线只对默认品种成立；-syms 换过就不判（假红教人忽略真红）。
	//
	// ⛔ **而「不判」那一支必须报 SKIP，不能报 PASS**（评审方 2026-09-09 抓到）：
	//
	//	默认（rb）              拦截过 ⇒ 基线断言跑了      ⇒ PASS/FAIL **有意义**
	//	-syms <跨零点品种>      拦截 FAIL 早退             ⇒ 有意义
	//	-syms <非跨零点品种>    拦截过、基线断言**被跳过** ⇒ 原来报 PASS，**而什么都没断言**
	//
	// ⇒ 判据是本仓自己写过的那条，只是换个方向用：
	//
	//	**没被行使过的拦截，和没有拦截差不多。**
	//	⇒ **一个没有断言在跑的 PASS，和没跑差不多 —— 而它比没跑更贵：
	//	  它进报告，读起来像通过。**
	//
	// ⚠️ 「换了品种就不判基线」这个决定本身是对的，没有动它。**改的只是那一支的标签。**
	// 而这个改动真正买到的东西是：**改完之后，本函数的 `PASS` 只剩一个含义**
	//（此前它混着「断言过了」和「压根没断言」两种）。
	if symsFlag != "" {
		st = "SKIP"
		note = "\n       ⚠️ 【换过品种（-syms），基线断言未运行】 —— " +
			"本行的结论只是「取到的数长这样」，【不是「与基线相符」】。" +
			"要判基线请去掉 -syms。"
	} else if longest != wantLongest || longFrom != wantFrom || longTo != wantTo {
		st = "FAIL"
		note = fmt.Sprintf("\n       ⚠️ 与基线不符（基线 %d 个交易日 %s…%s，2026-09-09 实测）——"+
			"要么数据变了，要么又发生了一次停夜盘，去看一眼",
			wantLongest, wantFrom, wantTo)
	}
	report("shinny-night-gap", st, fmt.Sprintf(
		"交易日 %d 个（%s…%s），其中内置表覆盖得到的（2020-05-06 起）%d 个 = %.0f%%；\n"+
			"       假如起点挪到 2019-12-11 ⇒ %d 个 = %.0f%%；挪到 2016-05-03 ⇒ %d 个 = %.0f%%\n"+
			"       跨零点的自然日 %.1f%%（阈值 10%%），而它们【挤在 %s…%s 之间，跨 %d 个交易日】 —— 那一段里这条判据【不可用】，不是「不准」\n"+
			"       无夜盘的连续段分布：%s\n"+
			"       最长 = %d 个交易日（%s … %s）—— 那是 2020 年的政策性停夜盘，不是长假%s"+
			"\n       ── 每一段的起始交易日，按年（给人看的，不参与判定）──%s",
		len(tdays), tdays[0], tdays[len(tdays)-1],
		covered, 100.0*float64(covered)/float64(len(tdays)),
		wouldCover[0], 100.0*float64(wouldCover[0])/float64(len(tdays)),
		wouldCover[1], 100.0*float64(wouldCover[1])/float64(len(tdays)),
		crossedPct, crossFrom, crossTo, crossSpan, dist.String(),
		longest, longFrom, longTo, note, perYear.String()))
}

// probeNightHours 量「夜盘每天开到几点」，按年列出来。
//
// 碰两条「未验」：
//
//	design.md   `实际 > 标称` 那一侧未验（实测只覆盖 `实际 ≤ 标称`）
//	contract.md 交易时段的历史变更表 —— 变更时点未测
//
// 判据：把每个自然日的夜盘标签取**最大**的那个（60m 网格，夜盘标 21:00/22:00/…），
// 按年归拢成集合。**集合变了 = 时段变过**，而变的那一年是时点的上界（精度只到年）。
//
// 选 `ag`（白银）与 `cu`（铜）：它们的夜盘比 `rb` 长，**改动才有地方显形**；
// `rb` 作对照 —— 它十年不该变。
//
// ⚠️ **射程**：60m 网格只能给到【小时】，`02:30` 这种半点收盘它看不出来，
// 只会显示成最后一根 `02:00`。**要精确到分钟得用 1m，而那是八百多次请求。**
// ⇒ 本探针回答的是「有没有变、大约哪一年」，**不是「几点几分」**。
// atoi 把 "HH" 转成数字；非法输入返回 0（调用方只喂两位数字）。
func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func probeNightHours(ctx context.Context, md, tok string) {
	// 默认只三个品种：这条探针每个品种要 17 次连接，**加品种就是加分钟**，
	// 而一条跑起来嫌慢的探针，下场是没人跑。
	// 要建「时段历史变更表」时用 -syms 临时铺开，结果记进 probe.md，别改默认值。
	syms := []string{"KQ.m@SHFE.ag", "KQ.m@SHFE.cu", "KQ.m@SHFE.rb"}
	if symsFlag != "" {
		syms = strings.Split(symsFlag, ",")
	}
	var b strings.Builder
	bad := 0
	for si, sym := range syms {
		all := map[string][]string{}
		fails := 0
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
		gotDays := make([]string, 0, len(all))
		for d := range all {
			gotDays = append(gotDays, d)
		}
		sort.Strings(gotDays)
		if ok, why := enoughDays(gotDays, fails); !ok {
			fmt.Fprintf(&b, "%-14s 取数不足：%s —— 不报结论\n       ", sym, why)
			bad++
			continue
		}
		// 年 -> 该年出现过的夜盘收盘标签集合
		byYear := map[string]map[string]bool{}
		for d, ts := range all {
			y := d[:4]
			last := ""
			for _, t := range ts {
				if t >= "20:00" || t < "04:00" {
					// 夜盘跨零点：把 00:00–03:59 排在 20:00–23:59 之后
					key := t
					if t < "04:00" {
						// ⛔ 跨零点要把【小时】加 24，不能只换前缀：
						// 原来写 "24"+t[2:]，于是 01:00 与 02:00 都变成 "24:00"
						// —— 「夜盘到底开到几点」被这一行悄悄抹平，而输出看着正常。
						key = fmt.Sprintf("%02d%s", 24+atoi(t[:2]), t[2:])
					}
					if key > last {
						last = key
					}
				}
			}
			if last == "" {
				continue
			}
			if byYear[y] == nil {
				byYear[y] = map[string]bool{}
			}
			byYear[y][last] = true
		}
		// 变了的话，把【最后一个旧形态的自然日】和【第一个新形态的自然日】找出来 ——
		// 「哪一年变的」不够用：contract.md 那条未验问的是**变更时点**。
		type dl struct {
			d, last string
		}
		var seq []dl
		for d, ts := range all {
			last := ""
			for _, t := range ts {
				if t >= "20:00" || t < "04:00" {
					key := t
					if t < "04:00" {
						// ⛔ 跨零点要把【小时】加 24，不能只换前缀：
						// 原来写 "24"+t[2:]，于是 01:00 与 02:00 都变成 "24:00"
						// —— 「夜盘到底开到几点」被这一行悄悄抹平，而输出看着正常。
						key = fmt.Sprintf("%02d%s", 24+atoi(t[:2]), t[2:])
					}
					if key > last {
						last = key
					}
				}
			}
			if last != "" {
				seq = append(seq, dl{d, last})
			}
		}
		sort.Slice(seq, func(a, c int) bool { return seq[a].d < seq[c].d })
		var edges []string
		// 用「前 20 天的最大收盘」当形态，避开单日缺根造成的抖动
		windowMax := func(i, n int) string {
			m := ""
			for j := i; j > i-n && j >= 0; j-- {
				if seq[j].last > m {
					m = seq[j].last
				}
			}
			return m
		}
		for i := 20; i+20 < len(seq); i++ {
			before, after := windowMax(i, 20), windowMax(i+20, 20)
			if before != after && len(edges) < 6 {
				// 20 日窗只把变更【括】在一个区间里。再往里收一次，收到【日】：
				// 旧形态的最后一天 = 最后一个 last == before 的日子；
				// 新形态的第一天   = 它之后第一个 last == after 的日子。
				lastOld, firstNew := "", ""
				for j := i; j <= i+20 && j < len(seq); j++ {
					if seq[j].last == before {
						lastOld = seq[j].d
					}
				}
				for j := i; j <= i+20 && j < len(seq); j++ {
					if seq[j].d > lastOld && seq[j].last == after {
						firstNew = seq[j].d
						break
					}
				}
				edges = append(edges, fmt.Sprintf(
					"旧形态最后一天 %s（最晚 %s）→ 新形态第一天 %s（最晚 %s）",
					lastOld, before, firstNew, after))
				i += 20
			}
		}
		years := make([]string, 0, len(byYear))
		for y := range byYear {
			years = append(years, y)
		}
		sort.Strings(years)
		fmt.Fprintf(&b, "%-14s ", sym)
		prev := ""
		for _, y := range years {
			ks := make([]string, 0, len(byYear[y]))
			for k := range byYear[y] {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			cur := strings.Join(ks, ",")
			mark := ""
			if prev != "" && cur != prev {
				mark = " ⇐变"
			}
			fmt.Fprintf(&b, "\n                 %s %s%s", y, cur, mark)
			prev = cur
		}
		if len(edges) > 0 {
			fmt.Fprintf(&b, "\n                 【变更时点】：%s", strings.Join(edges, " ／ "))
		}
		// 基线（2026-09-09 实测）：只有 rb 变过一次，而且就那一次。
		// 没有断言的话，这个探针只是一份报告 —— **报告不会因为世界变了而红。**
		//
		// ⚠️ 基线只对【默认品种表】成立。用 -syms 铺开去建表时不判 ——
		// 否则每次探索都会看见一个假红，而**假红教人忽略真红**。
		if symsFlag != "" {
			continue
		}
		wantEdges := 0
		if sym == "KQ.m@SHFE.rb" {
			wantEdges = 1
			if len(edges) == 1 && !strings.Contains(edges[0], "2016-04-29") {
				bad++
				fmt.Fprintf(&b, "\n                 ⚠️ 变更时点与基线不符（基线：旧形态最后一天 2016-04-29"+
					"→ 新形态第一天 2016-05-03）")
			}
		}
		if len(edges) != wantEdges {
			bad++
			fmt.Fprintf(&b, "\n                 ⚠️ 检出 %d 处变更，基线是 %d 处 —— "+
				"要么数据变了，要么交易所又改了时段，去看一眼", len(edges), wantEdges)
		}

		if si < len(syms)-1 {
			fmt.Fprintf(&b, "\n       ")
		}
	}
	// ⛔ **没有断言在跑的那一次，不能叫 PASS** —— 与 `probeNightGap` / `probeDaySegments`
	// 同一条。本函数的跳过条件是循环里那句 `if symsFlag != "" { continue }`。
	//
	// ⚠️ 这一处是**第三个**实例，而它是靠一次「按骨架扫」找出来的（2026-09-09）：
	//
	//	只扫骨架 `st := "PASS"`            ⇒ 全仓 **7** 处（多）
	//	凭印象点名                          ⇒ **2** 处（少）
	//	骨架 ＋「函数体里有跳过断言的条件」 ⇒ **3** 处 ← 真数
	//
	//	⇒ **一个模式类缺陷，扫它的骨架会多收，凭印象点名会漏收；
	//	  判据要【两半齐全】才数得对。**
	//	  另外四处（`probeGridIsClockGrid` / `probeGfexNoNight` /
	//	  `probeTradingDayPredicted` / `main`）没有跳过条件 —— **它们的 PASS 是对的，别一起改。**
	// ⛔ 用 `switch` 而不是两条顺序的 `if`：**让「FAIL 压过 SKIP」由【结构】保证，
	// 而不是由【语句顺序】保证**（评审方 2026-09-09 提的，我认）。
	//
	// 上一版是 `if symsFlag != ""{SKIP}` 后面跟 `if bad > 0{FAIL}` —— 行为对，
	// 而它对得**只因为后者写在后面**。哪天有人把「先判失败」挪到前面（那是个很自然的重构），
	// **SKIP 会静悄悄压过 FAIL，而不会有任何东西红** ——
	// 因为**这段是探针代码，不进 `go test`**，它当时的全部保护就是行末那句注释。
	//
	//	⇒ **一条不再需要解释的规则，才算真的落地。**
	//	  （同一形状本仓已经吃过一次：`high_water.txt` 的来历行「谁在前」决定链检查红不红。）
	st := "PASS"
	switch {
	case bad > 0:
		st = "FAIL"
	case symsFlag != "":
		st = "SKIP"
		fmt.Fprintf(&b, "\n       ⚠️ 【换过品种（-syms），基线断言未运行】 —— "+
			"本行的结论只是「取到的数长这样」，【不是「与基线相符」】。要判基线请去掉 -syms。")
	}
	report("shinny-night-hours", st, b.String())
}

// probeDaySegments 量「**日盘**的分段结构有没有变过」，按年列出来。
//
// 碰的那条「未验」写在 calendar/embedded/embedded.go 里：
//
//	6.10 那张变更表给的是【边界日期】，不是【模板】。它测的是「最晚一根」
//	⇒ 能重建**夜盘尾端**，**而它对【日盘分段】一言未发**。
//	「日盘这十年没变过」是一个**没有被那次测量覆盖的前提**。
//
// 判据：把每个自然日落在 [04:00, 20:00) 的标签集合当作「日盘形态」，
// 取 20 日窗的**并集**当作那一段时间的形态；**并集变了 = 日盘分段变过**。
//
// ⛔ 为什么是【并集】而不是逐日比：这条量的失效方式是**少根**（那一格没成交、
// 窗口断在中间、主连换月），而少根只会让集合**变小，永远不会变大**。
// 并集对「少根」免疫，对「多出一段 / 少掉一段」不免疫 —— 而后者正是要测的东西。
//
// ⛔ **网格必须比 60m 细，这一条是量出来的、不是想出来的**（2026-09-09）：
//
//	60m 日盘 = [09:00 10:00 11:00 13:00 14:00]        ← 上午休息不产生任何标签差异
//	15m 日盘 = [09:00 09:15 09:30 09:45 10:00 __ 10:30 10:45 11:00 11:15
//	            13:30 13:45 14:00 14:15 14:30 14:45]  ← **10:15 的缺席就是那道休息**
//
// 日盘有一道 **10:15–10:30 的上午休息**。**用 60m 去测日盘分段，
// 等于拿一把看不见那道缝的尺子去量那道缝** —— 它会一直报「没变」，而且永远是对的。
//
// ⚠️ **射程**：15m 看得见 15 分钟的边界，看不见 10:20 这种。
// 要更细得用 5m（rb 每日 ~68 根，width 10000 ⇒ 一窗 ~147 天 ⇒ 十年 ~27 窗，**做得起**）。
// 今天不做的理由是：**已知的分段边界全部落在 :15 / :30 上**，5m 只对未知的更细变更有用。
// ⇒ 本探针答的是「按 15 分钟看，日盘分段变没变」，**不是「按分钟看」**。
//
// ⚠️ **view_width 的上限也是量出来的**：10000 可以，12000 **直接断连**
// （`failed to read frame header: EOF`），**不是一个干净的报错**。
// ⇒ 这正是本文件反复写的那条：**取不到和不存在长得一样。**
// 超上限的 width 会走进 `fails++` 那一支，读起来像「这一段没有数据」。
//
// ⚠️ 顺带一条读数，留给以后：夜盘那条探针用的是 width=2000（一窗 88 天，十年 17 窗）。
// **上限是 10000（一窗 439 天）—— 同样的覆盖 4 窗就够。** 那条分支现在冻着，不动它。
// ⛔ **一个「十一年全同」的结果，长得和「测坏了」一模一样。**
// 所以这条探针有三个对照组，结果记在 docs/probe.md 6.12：
//
//	A 形态灵敏度   CFFEX.IF ⇒ 10:15 在它那里是【有】的（中金所没有上午休息）
//	               ⇒ 判据整个压在这一个标签上，而它在两个真实品种上给出相反答案
//	B 时间灵敏度   -win night 指向夜盘 ⇒ 检出 rb 那次变更，落点与 6.10 用 60m
//	               独立定死的 2016-04-29 → 2016-05-03 一致
//	C 断言会不会红 默认表换成 CFFEX.T ⇒ FAIL，三个分支全响；
//	               基线里加上 10:15 ⇒ FAIL；还原 ⇒ PASS
//
// ⇒ 顺带定死了一个仓里一直没有日期的变更：**国债起点 09:15 → 09:30，
//
//	旧形态最后一天 2020-07-17（周五）→ 新形态第一天 2020-07-20（周一）**，T 与 TF 同日。
//
// enoughDays 判断「取到的数够不够下结论」，并在不够时给出**说得出理由**的一句话。
//
// ⛔ 原来**三条**探针写的都是这个形状（`len(all) < 2000` / `len(tdays) < 2000`）—— 一个**绝对天数**。
// 它想问的是「有没有取全」，而这两件事只在【十岁以上的品种】上等价。
// 2026-09-09 实测，三个品种被它挡在门外：
//
//	KQ.m@GFEX.si    900 天   **0 窗失败**   ⇒ 被拒
//	KQ.m@GFEX.lc    761 天   **0 窗失败**   ⇒ 被拒
//	KQ.m@SHFE.ss   1990 天   **0 窗失败**   ⇒ 被拒（差 10 天）
//
// **`0 窗失败` 就是取全了的证据** —— 这三个只是比十年年轻。
// ⇒ 「够不够」被写成了一个位置（2000），而不是性质。**本仓反复撞的那个形状。**
//
// 新判据三条：
//
//	一、取数没失败：`fails <= maxFetchFails`（今天是 2）
//	二、拿到的天数**密铺它自己的跨度**：len >= 0.85 × 跨度 × 5/7
//	    （5/7 是「一周五个交易日」的粗估；0.85 给节假日与停牌留余量）
//	三、下限 120 个交易日 —— 变更检测两边各要一个 20 日窗，41 天在理论上就够，
//	    但 41 天里一个节假日群就能把窗口打穿。**半年是给判据留的余量，不是拍的。**
//
// ⛔ **「下限」与「密铺」各答一半，缺一不可 —— 别以为密铺是加强、下限可以去掉**
// （措辞按评审方 2026-09-09 的提法，他说得比我准）：
//
//	`fails <= maxFetchFails`  只证明「**取到的**都成功」，**不证明「该取的都取了」**
//	密铺跨度      回答的正是后者：拿到的天数密不密铺它自己的跨度
//	下限 120      回答的是第三件事：**这段序列长到跑得动那个检测器吗**
//	              —— 一段密铺得很好的 30 天，密铺判据会放行，而检测器在上面没有意义
//
// ⚠️ 它**不**回答「这个品种上市那天到今天有没有取全」——
// 跨度是从**取到的**第一天算起的。真正漏掉最早那一段，这条判据看不见。
// 要答那个得有上市日期，而本仓现在没有那份数据。
// maxFetchFails 是「取数失败几窗就不报结论」的上限。
//
// ⛔ 抽成常量不是为了好看：这个数**原来写了两处**（`probeNightGap` 的早退、`enoughDays` 里），
// 而 `probeNightGap` 早退在前 ⇒ **`enoughDays` 的那一处从这条路根本走不到**。
// ⇒ 后果不是今天的错（两处值相同），是明天的：
// **有人改 `enoughDays` 的阈值，`probeNightGap` 会保持旧行为，而钉住它的测试不会响。**
// （评审方 2026-09-09 查出来的；他建议删早退，而那个删法会让「取不到」被报成「跨零点」——
// 理由写在那个早退旁边。**问题是他的，解法换了一个。**）
// ⛔ **而它是个【绝对数】，配着三条【窗口数不同】的循环 —— 这个不对称是无意的。**
// （评审方 2026-09-09 发现；参数我从源码读、逐条复算，与他一致。）
//
//	函数                back   step   窗口数   丢 2 窗缺多少数据
//	probeNightGap       3900   240    **17**   11.8%
//	probeNightHours     3900   240    **17**   11.8%
//	probeDaySegments    4000   **400**  **11**   **18.2%**
//
// ⇒ **同一个「预算」，在 `probeDaySegments` 上放行的是 18.2% 的数据缺失，在另两条上是 11.8%。**
//
//	而这一点此前没有写在任何地方。
//
// ⛔ **这是同一个教训的第二处，而第一处是本文件自己修过的：**
//
//	`enoughDays` 的「取数够不够」原来是**绝对天数**（`len(all) < 2000`）
//	⇒ 年轻品种整批被挡在门外 ⇒ 换成了**密铺自己的跨度**（相对量）
//	`maxFetchFails` 的「失败几窗就不报结论」**仍是绝对数**
//	⇒ 它的严格程度**取决于那条循环切了几窗**，而三条切得不一样
//
// ⇒ 建议（**不赶**，单独一格）：写成比例（如「失败窗 > 总窗数的 1/8」），
//
//	或至少让改 `step` 的人看见这张表 —— **他那一改，动的是失败预算。**
//
// ⛔ **而「给这张表写个守卫」这一格，是【明确不做】的 —— 理由写在这儿，免得下一个人以为漏了。**
//
//	它能抓的   「有人改了 `step` / `back` 而这张表没跟着改」
//	它的代价   又一层「给注释做对照组」，**而这条链没有自然终点**
//	更根本的   **这段注释的目的本来就是让改 `step` 的人【看见它】** ——
//	          如果他连紧挨着的表都不看，一个守卫也只会被他从红改成绿
//
//	⇒ **代价是：改 `step` 的人不会被【红】提醒，只会被【这张表】提醒。**
//	  而这一条本来就是靠人读的 —— 和 `tools/audit/high_water.txt` 里那条
//	  「合并时先分清冲突与分叉」同一族：**它的保护机制就是【你正在读它】。**
//
// ⚠️ 停在这儿是**有意的**，和 `docs_guards_test.go` 里给用例表画的那个尽头同一处理：
// **不能守（或不值得守）的部分，至少要写下它不被守，以及那样的代价是什么。**
//
// # 顺带：`probeNightGap` 那段「窗口调大更安全」的**真机制**在这儿
//
// 我原来给的机制（「调大会唤醒那两个错误」）已经撤回 —— 它和结论方向相反。
// 支撑那个结论的是**这个绝对预算**：
//
//	daysBack   窗口数   丢 2 窗缺%   比值升幅   被推过 10% 的真实比例下界
//	5400       23       8.7%        1.095×     9.1%
//	**3900**   **17**   **11.8%**   **1.133×** **8.8%**   ← 今天
//	2400       11       18.2%       1.222×     8.2%
//	1200        6       33.3%       1.500×     6.7%
//	 480        3       66.7%       3.000×     3.3%
//
// ⇒ **窗口调小 ⇒ 丢 2 窗占比变大 ⇒ 升幅变大 ⇒ 误报带变宽（更危险）；调大则相反。**
//
//	  **结论是对的，而它靠的是「预算是绝对数、窗口数会变」，不是「唤醒」。**
//
//		⚠️ **一个对的结论挂在错的理由上，下一个人会顺着理由去改错的东西。**
//		  （对照本仓那条：**弱的那句会借强的那句的信用**；这里是反过来 ——
//		  **错的机制在替对的结论说话。**）
//
// ⚠️ 射程（评审方特意分开的一格，我照收）：**上面这张误报带的表只对 `probeNightGap` 成立** ——
// 那道跨零点拦截只在它里面。对另两条循环，能套用的只有**「失败预算的严格程度不同」**那半句。
const maxFetchFails = 2

func enoughDays(days []string, fails int) (bool, string) {
	if fails > maxFetchFails {
		return false, fmt.Sprintf("取数失败 %d 窗（上限 %d）", fails, maxFetchFails)
	}
	if len(days) < 120 {
		return false, fmt.Sprintf("只有 %d 个交易日，不足 120 —— 变更检测两边各要一个 20 日窗，"+
			"太短了判据自己会抖", len(days))
	}
	first, last := days[0], days[len(days)-1]
	t0, e0 := time.Parse("2006-01-02", first)
	t1, e1 := time.Parse("2006-01-02", last)
	if e0 != nil || e1 != nil {
		return false, "日期解析失败：" + first + ".." + last
	}
	span := int(t1.Sub(t0).Hours()/24) + 1
	want := int(float64(span) * 5.0 / 7.0 * 0.85)
	if len(days) < want {
		return false, fmt.Sprintf("%s..%s 跨 %d 个自然日，按一周五天至少该有 %d 个交易日，"+
			"实到 %d —— 【中间有洞】", first, last, span, want, len(days))
	}
	return true, ""
}

// wantDayShape 是 2026-09-09 实测到的日盘形态（15m 网格）。
//
// ⛔ **`10:15` 不在里面，而它的缺席就是这条探针的全部判据** ——
// 那是 10:15–10:30 的上午休息。写死这一串的意义在于：
// **少一个标签、多一个标签，都会有人被叫回来看一眼。**
const wantDayShape = "09:00 09:15 09:30 09:45 10:00 10:30 10:45 11:00 11:15 " +
	"13:30 13:45 14:00 14:15 14:30 14:45"

func probeDaySegments(ctx context.Context, md, tok string) {
	// 三个交易所各一个主连：日盘分段是不是各所同步变，这条探针自己答不了，
	// 但**分开列**至少让「只有一家变了」显形。
	syms := []string{"KQ.m@SHFE.rb", "KQ.m@DCE.i", "KQ.m@CZCE.MA"}
	if symsFlag != "" {
		syms = strings.Split(symsFlag, ",")
	}
	// 对照组 B：同一套聚合代码指向【夜盘】。夜盘里 rb 在 2016-05-03 变过一次，
	// 那个日期是 probeNightHours 用 60m 网格独立定死的 ——
	// **拿一个答案已知的数据集去问这套代码，才知道它到底会不会响。**
	//
	// ⚠️ 夜盘跨零点，但这里比的是【集合的相等】不是大小，所以不需要 24+ 那套换算。
	inWin := func(t string) bool { return t >= "04:00" && t < "20:00" }
	if winFlag == "night" {
		inWin = func(t string) bool { return t >= "20:00" || t < "04:00" }
	}
	// ⛔ **-win night 的输出里有一个真实的假象，看见了别当成时段**：
	// 本探针按【自然日】归拢，而夜盘跨零点 ——
	// 21:00 开的那一夜，00:xx 那几根落在**第二个自然日**上。
	// ⇒ 变更点那一行的「变更后」集合里会残留 00:00–00:45，
	//   而它们来自**变更前那些夜晚的后半段**，不是变更后的时段。
	// 证据就在同一份输出里：2017 年起整年一根 00:xx 都没有。
	//
	// **日盘不跨零点，所以默认模式没有这个问题** ——
	// 这也是为什么这条限定只写在这儿，不写进 day 那一支的结论里。
	var b strings.Builder
	bad := 0
	for si, sym := range syms {
		all := map[string][]string{}
		fails := 0
		// 一窗 439 天，步长 400 留 39 天重叠 —— 重叠是为了让相邻两窗能接上，
		// 不重叠的话中间掉一天都看不出来。
		for back := 4000; back >= 0; back -= 400 {
			mm, err := minuteLabelsN(ctx, md, tok, sym, gridFlag, 10000, back)
			if err != nil {
				fails++
				continue
			}
			for d, ts := range mm {
				all[d] = append(all[d], ts...)
			}
		}
		gotDays := make([]string, 0, len(all))
		for d := range all {
			gotDays = append(gotDays, d)
		}
		sort.Strings(gotDays)
		if ok, why := enoughDays(gotDays, fails); !ok {
			fmt.Fprintf(&b, "%-14s 取数不足：%s —— 不报结论\n       ", sym, why)
			bad++
			continue
		}
		// 每个自然日 -> 日盘标签集合（排序去重）
		type ds struct {
			d, shape string
		}
		var seq []ds
		byYear := map[string]map[string]bool{}
		for d, ts := range all {
			set := map[string]bool{}
			for _, t := range ts {
				if inWin(t) {
					set[t] = true
				}
			}
			if len(set) == 0 {
				continue
			}
			ks := make([]string, 0, len(set))
			for k := range set {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			seq = append(seq, ds{d, strings.Join(ks, " ")})
		}
		sort.Slice(seq, func(a, c int) bool { return seq[a].d < seq[c].d })
		// ⛔ **窗口里一根都没有 ⇒ 这里原来直接 `seq[0]` 越界 panic**
		// （2026-09-09 实测：`-syms KQ.m@GFEX.ps -win night` ⇒
		//  `panic: index out of range [0] with length 0`）。
		//
		// 而它不是「取数失败」：`enoughDays` 已经过了，413 个交易日都在手上 ——
		// **是【窗口筛完】之后空的。** 这两件事必须分开说，否则报出来的原因是错的。
		//
		// ⇒ 一个探针**崩掉**比报错更糟：它连「我答不了」都说不出来。
		//   本仓那条「取不到和不存在长得一样」，在这儿的形状是**取到了、而窗口里没有**。
		if len(seq) == 0 {
			fmt.Fprintf(&b, "%-14s 【在所选窗口（-win %s）里一根都没有】 —— "+
				"取数是成功的（%d 个自然日），是【窗口筛完之后空的】。\n"+
				"       ⇒ 对 `-win night`：这多半意味着【该品种没有夜盘】"+
				"（GFEX 三个品种都没有）；对 `-win day`：那是异常，去看一眼。\n       ",
				sym, winFlag, len(all))
			bad++
			continue
		}
		// 20 日窗并集
		windowUnion := func(i, n int) string {
			set := map[string]bool{}
			for j := i; j > i-n && j >= 0; j-- {
				for _, t := range strings.Fields(seq[j].shape) {
					set[t] = true
				}
			}
			ks := make([]string, 0, len(set))
			for k := range set {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			return strings.Join(ks, " ")
		}
		for i := 20; i+20 < len(seq); i++ {
			by := seq[i].d[:4]
			if byYear[by] == nil {
				byYear[by] = map[string]bool{}
			}
			byYear[by][windowUnion(i, 20)] = true
		}
		var edges []string
		for i := 20; i+20 < len(seq); i++ {
			before, after := windowUnion(i, 20), windowUnion(i+20, 20)
			if before != after && len(edges) < 6 {
				// ⛔ 这里报的是**区间**，不是一个日子。
				// 判据是「前 20 日的并集 ≠ 后 20 日的并集」，
				// 而后一个窗盖的是 (i, i+20]，所以变更只能被括在这两个日子【之间】。
				// 写成「X 附近」会被下一个人读成「就是 X 那天」——
				// **本仓反复撞的那个形状：把射程写成一个位置。**
				hi := i + 20
				if hi >= len(seq) {
					hi = len(seq) - 1
				}
				// 20 日窗只把变更【括】在 (seq[i].d, seq[hi].d] 里。再往里收一次，收到【日】——
				// 和 probeNightHours 同一个做法。判据用**消失的那批标签**：
				//
				//	lost = before \ after ⇒ 最后一个还出现 lost 的日子 = 旧形态最后一天
				//	（纯新增的情况反过来用 gained，见下面那一支）
				//
				// ⚠️ 它对「少根」不免疫：旧形态里偶尔掉一根 lost 标签没关系（取的是**最后**一个），
				// 但新形态里要是冒出一根 lost 标签，lastOld 会被拖到那天去。
				// **这一条只在 (i, hi] 这 20 天里找，所以拖不远** —— 而拖了也看得见：
				// 收窄后的日期会贴在区间右端。
				inSet := func(shape string, s map[string]bool) bool {
					for _, t := range strings.Fields(shape) {
						if s[t] {
							return true
						}
					}
					return false
				}
				diff := func(x, y string) map[string]bool {
					in := map[string]bool{}
					for _, t := range strings.Fields(y) {
						in[t] = true
					}
					out := map[string]bool{}
					for _, t := range strings.Fields(x) {
						if !in[t] {
							out[t] = true
						}
					}
					return out
				}
				lost, gained := diff(before, after), diff(after, before)
				lastOld, firstNew := "", ""
				switch {
				case len(lost) > 0:
					for j := i; j <= hi; j++ {
						if inSet(seq[j].shape, lost) {
							lastOld = seq[j].d
						}
					}
					for j := i; j <= hi; j++ {
						if seq[j].d > lastOld {
							firstNew = seq[j].d
							break
						}
					}
				case len(gained) > 0:
					for j := i; j <= hi; j++ {
						if inSet(seq[j].shape, gained) {
							firstNew = seq[j].d
							break
						}
					}
					for j := i; j <= hi; j++ {
						if firstNew != "" && seq[j].d < firstNew {
							lastOld = seq[j].d
						}
					}
				}
				narrow := ""
				if lastOld != "" && firstNew != "" {
					narrow = fmt.Sprintf("【旧形态最后一天 %s → 新形态第一天 %s】；",
						lastOld, firstNew)
				}
				edges = append(edges, fmt.Sprintf("%s括在 (%s, %s] 内：[%s] → [%s]",
					narrow, seq[i].d, seq[hi].d, before, after))
				i += 20
			}
		}
		years := make([]string, 0, len(byYear))
		for y := range byYear {
			years = append(years, y)
		}
		sort.Strings(years)
		fmt.Fprintf(&b, "%-14s %d 个自然日 %s..%s", sym, len(seq), seq[0].d, seq[len(seq)-1].d)
		prev := ""
		for _, y := range years {
			ks := make([]string, 0, len(byYear[y]))
			for k := range byYear[y] {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			cur := strings.Join(ks, " ／ ")
			mark := ""
			if prev != "" && cur != prev {
				mark = " ⇐变"
			}
			fmt.Fprintf(&b, "\n                 %s %s%s", y, cur, mark)
			prev = cur
		}
		if len(edges) > 0 {
			for _, e := range edges {
				fmt.Fprintf(&b, "\n                 【变更】：%s", e)
			}
		}
		// 基线（2026-09-09 实测：三个交易所各一个主连，各 2595 个自然日
		// 2016-01-05..2026-09-08）——**十一年一个形态，一处变更都没有。**
		//
		// ⚠️ 只对【默认参数】判。-syms / -win 是探索与对照用的，那时不判 ——
		// 否则每次探索都会看见一个假红，而**假红教人忽略真红**。
		if symsFlag == "" && winFlag == "day" && gridFlag == 15 {
			for _, y := range years {
				if len(byYear[y]) != 1 {
					bad++
					fmt.Fprintf(&b, "\n                 ⚠️ %s 年出现了 %d 种日盘形态，基线是 1 种",
						y, len(byYear[y]))
					continue
				}
				for k := range byYear[y] {
					if k != wantDayShape {
						bad++
						fmt.Fprintf(&b, "\n                 ⚠️ %s 年的日盘形态与基线不符"+
							"\n                    实测 [%s]\n                    基线 [%s]",
							y, k, wantDayShape)
					}
				}
			}
			if len(edges) != 0 {
				bad++
				fmt.Fprintf(&b, "\n                 ⚠️ 检出 %d 处日盘分段变更，基线是 0 处 —— "+
					"要么交易所改了日盘，要么这条探针的取数变了，去看一眼", len(edges))
			}
		}
		if si < len(syms)-1 {
			fmt.Fprintf(&b, "\n       ")
		}
	}
	// ⛔ **没有断言在跑的那一次，不能叫 PASS**（评审方 2026-09-09 在 probeNightGap 上
	// 提的同一条，而**这里有三个开关**：`-syms` / `-win` / `-grid`，任意一个非默认，
	// 上面那段基线判定就整段跳过）。
	//
	//	判据一句话：**这一次运行有没有断言在跑？没有就不能叫 PASS。**
	//
	// ⚠️ 而 `bad > 0` 要压过 SKIP：**取数不足是真失败，换没换品种都一样。**
	// 次序因此是 **FAIL > SKIP > PASS**。
	//
	// ⛔ 记一笔：**这个缺陷正是我在 `probeNightGap` 里刚修完的那一个，
	// 而我自己的探针里原样有一份 —— 同一个文件、同一趟。**
	// 「修了看得见的那一半」在这儿的形状是：
	// **我照着别人的代码改，没有回头看我照它写出来的那份。**
	st := "PASS"
	skipped := symsFlag != "" || winFlag != "day" || gridFlag != 15
	switch {
	case bad > 0:
		st = "FAIL"
	case skipped:
		st = "SKIP"
		fmt.Fprintf(&b, "\n       ⚠️ 【换过参数（-syms / -win / -grid 之一），基线断言未运行】"+
			" —— 本行的结论只是「取到的数长这样」，【不是「与基线相符」】。"+
			"要判基线请三个开关全部用默认值。")
	}
	report("shinny-day-segments", st, b.String())
}

func main() {
	flag.StringVar(&only, "only", "", "只跑名字含该子串的探针，如 -only trading-day")
	flag.StringVar(&symsFlag, "syms", "", "临时覆盖 night-hours 的品种表，逗号分隔")
	flag.StringVar(&winFlag, "win", "day", "day-segments 量哪一段：day（默认）或 night（对照组 B）")
	flag.IntVar(&gridFlag, "grid", 15, "day-segments 用多少分钟的网格（默认 15；用 60 可复跑「为什么不是 60m」那一格）")
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
	if !skipped("shinny-night-hours") {
		probeNightHours(ctx, md, tok)
	}
	if !skipped("shinny-day-segments") {
		probeDaySegments(ctx, md, tok)
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
		fmt.Printf("含 %q 的探针与记录一致；【其余未跑，不代表通过】\n", only)
		return
	}
	fmt.Println("全部与 docs/probe.md 第六节的记录一致")
}
