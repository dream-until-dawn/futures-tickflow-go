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

	// 这个 client 身份是从开源 tqsdk 包里读出来的，不是发给本项目的。
	// 探针里写死是为了能重跑（它本来就公开在 PyPI 上）；
	// 【库代码里必须由使用者显式提供，不设默认值】——见 contract.md 的合规风险行。
	clientID     = "shinny_tq"
	clientSecret = "REDACTED-取值见-tqsdk-包-auth.py"
)

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

func report(name, status, detail string) {
	fmt.Printf("[%s] %s\n       %s\n\n", status, name, detail)
	if status == "FAIL" {
		failed++
	}
}

// dotenv 读仓库根的 .env；找不到就回落到环境变量。
func dotenv() (string, string) {
	if u, p := os.Getenv("SHINNY_USER"), os.Getenv("SHINNY_PASS"); u != "" && p != "" {
		return u, p
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		return "", ""
	}
	f, err := os.Open(filepath.Join(root, ".env"))
	if err != nil {
		return "", ""
	}
	defer f.Close()
	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return kv["SHINNY_USER"], kv["SHINNY_PASS"]
}

func token(user, pw string) (string, []string, error) {
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
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

func main() {
	fmt.Println("天勤行情网关探针 — 基线见 docs/probe.md 第六节")
	fmt.Println()

	user, pw := dotenv()
	if user == "" || pw == "" {
		report("shinny-ws", "SKIP",
			"未提供凭证（仓库根 .env 的 SHINNY_USER / SHINNY_PASS 或同名环境变量），跳过")
		return
	}

	tok, feats, err := token(user, pw)
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

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// 关键一条：【已到期】合约还供不供分钟线。
	//
	// 主连 KQ.m@ 有 89 万根，但它未复权（见 6.5），按本库设计只作对账；
	// 自建主连的真值是合约级序列，而那需要一长串早已退市的合约。
	// 这一条不成立的话，「分钟级硬缺口已解决」就不成立——
	// 缺口只是从「新浪 1023 根」挪到了「天勤只给在市合约」。
	syms := append([]string{"KQ.m@SHFE.rb"}, baseline.expiredSyms...)
	res, err := depths(ctx, md, tok, syms)
	if err != nil {
		report("shinny-depth", "FAIL", "连接或拉取失败: "+err.Error())
		os.Exit(1)
	}

	var b strings.Builder
	bad := 0
	for _, s := range syms {
		r, ok := res[s]
		if !ok {
			fmt.Fprintf(&b, "%-16s 超时未就绪\n       ", s)
			bad++
			continue
		}
		if r.total <= 0 {
			fmt.Fprintf(&b, "%-16s 【无序列】\n       ", s)
			bad++
			continue
		}
		fmt.Fprintf(&b, "%-16s %7d 根  最早 %s\n       ", s, r.total, r.earliest)
	}
	st = "PASS"
	if bad > 0 {
		st = "FAIL"
	}
	report("shinny-depth", st, strings.TrimRight(b.String(), " \n")+
		fmt.Sprintf("\n       已到期合约 %d/%d 有 1m 历史；地板线记录为 %s",
			len(baseline.expiredSyms)-bad, len(baseline.expiredSyms), baseline.floorDay))

	if failed > 0 {
		fmt.Printf("%d 条偏离记录 → 先判断是上游变了还是记录错了，再更新 docs/probe.md\n", failed)
		os.Exit(1)
	}
	fmt.Println("全部与 docs/probe.md 第六节的记录一致")
}
