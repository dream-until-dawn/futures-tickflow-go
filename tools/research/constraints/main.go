// Command constraints 单次保存有据官方网页；收到正文不等于制度审核通过。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

type target struct {
	ID      string   `json:"id"`
	URL     string   `json:"url"`
	Anchors []string `json:"anchors"`
}
type requestPlan struct {
	Targets []target `json:"targets"`
	Blocked []string `json:"blocked"`
}

// brokerMarginURL 是独立发布者的当日公司保证金表；不冒充交易所原始数据。
const brokerMarginURL = "https://www.minfutures.com/main/a/20261116/86319.shtml"

func newOpeningReference(address string) bool {
	u, err := url.Parse(address)
	if err == nil && u.Host == "www.9qihuo.com" && u.Path == "/qihuoshouxufeisingle" {
		code := strings.TrimPrefix(u.RawQuery, "heyue=")
		codes := " ad ag al ao au br bu cu fu hc ni op pb rb ru sn sp ss wr zn bc ec lu nr sc AP CF CJ CY FG JR LR MA OI PF PK PL PM PR PX RI RM RS SA SF SH SM SO SR TA UR WH ZC a b bb bz c cs eb eg fb i j jd jm l lg lh m p pg pp rr v y lc pd ps pt si "
		return u.RawQuery == "heyue="+code && code != "" && strings.Contains(codes, " "+code+" ")
	}
	// 新一轮逐品种发现的具名发布者资源；不扩成任意域名或旧失败入口的替代重试。
	return address == "https://www.founderfu.com/fzzqqh_2019/service_varieties.html" ||
		address == "https://www.ciccwmf.cn/gsgg/86056.jhtml" ||
		address == "https://www.guosenqh.com.cn/article/125210/index.html" ||
		address == "https://www.9qihuo.com/qihuoshouxufeisingle?heyue=CJ"
}

func validate(p requestPlan) error {
	if len(p.Targets) == 0 || len(p.Targets) > 40 {
		return fmt.Errorf("请求数不在1至40范围")
	}
	seen := map[string]bool{}
	blocked := map[string]bool{}
	for _, u := range p.Blocked {
		blocked[u] = true
	}
	for _, t := range p.Targets {
		u, e := url.Parse(t.URL)
		if e != nil || u.Scheme != "https" || u.User != nil || (u.RawQuery != "" && !newOpeningReference(t.URL)) || u.Fragment != "" || (u.Host != "www.shfe.com.cn" && u.Host != "www.czce.com.cn" && t.URL != brokerMarginURL && !newOpeningReference(t.URL)) {
			return fmt.Errorf("非允许官方HTTPS资源")
		}
		if t.ID == "" || filepath.Base(t.ID) != t.ID || strings.ContainsAny(t.ID, "./\\:") || len(t.Anchors) == 0 {
			return fmt.Errorf("原件身份或正文锚点无效")
		}
		if seen[t.URL] || seen[t.ID] || blocked[t.URL] {
			return fmt.Errorf("重复或受阻资源")
		}
		seen[t.URL] = true
		seen[t.ID] = true
	}
	return nil
}

func content(t target, body []byte) error {
	for _, a := range t.Anchors {
		if a == "" || !bytes.Contains(body, []byte(a)) {
			return fmt.Errorf("未取得指定正文锚点")
		}
	}
	for _, a := range []string{"向右滑动填充拼图", "人机识别检测", "<title>WEB 应用防火墙"} {
		if bytes.Contains(body, []byte(a)) {
			return fmt.Errorf("挑战不是正文")
		}
	}
	return nil
}

func write(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func run(planFile, root, ledger string, launch bool) error {
	raw, e := os.ReadFile(planFile)
	if e != nil {
		return e
	}
	var p requestPlan
	if e = json.Unmarshal(raw, &p); e != nil {
		return e
	}
	if e = validate(p); e != nil {
		return e
	}
	if !launch {
		return write(root, p)
	} // 默认只保存预检清单，不创建请求标记。
	if e = os.Mkdir(root, 0700); e != nil {
		return e
	}
	if e = write(filepath.Join(root, "plan.json"), p); e != nil {
		return e
	}
	stopped := map[string]bool{}
	failed := false
	for _, t := range p.Targets {
		u, _ := url.Parse(t.URL)
		if stopped[u.Host] {
			continue
		}
		// 标记名仅由URL决定；换输出目录或修改ID不能重试同一资源。
		if e = os.MkdirAll(ledger, 0700); e != nil {
			return e
		}
		f, err := os.OpenFile(filepath.Join(ledger, digest([]byte(t.URL))+".started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("未知或既有请求禁止重试: %w", err)
		}
		_, e = f.WriteString(root + "\n" + t.URL)
		f.Close()
		if e != nil {
			return e
		}
		time.Sleep(2 * time.Second)
		dl, err := batch.NewDownloader(&http.Client{}, batch.Limits{Requests: 1, Bytes: 2 << 20, Interval: time.Second, Timeout: 15 * time.Second})
		if err != nil {
			return err
		}
		result, fetchErr := dl.FetchDetailed(context.Background(), t.URL)
		if fetchErr == nil {
			fetchErr = content(t, result.Body)
		}
		state, errorText := "received_scope_unreviewed", ""
		if fetchErr != nil {
			state, errorText = "failed", fetchErr.Error()
			stopped[u.Host] = true
			failed = true
		}
		if e = os.WriteFile(filepath.Join(root, t.ID+".body"), result.Body, 0600); e != nil {
			return e
		}
		receipt := map[string]any{"id": t.ID, "url": t.URL, "state": state, "error": errorText, "http": result, "sha256": digest(result.Body), "bytes": len(result.Body), "finished_at": time.Now().UTC(), "market_rules_qualified": false}
		if e = write(filepath.Join(root, t.ID+".receipt.json"), receipt); e != nil {
			return e
		}
		fmt.Println(t.ID, state, len(result.Body))
	}
	if failed {
		return fmt.Errorf("原件读取失败；已保留原始回执，禁止重试失败资源")
	}
	return nil
}

func main() {
	providerPrepare := flag.Bool("prepare-provider", false, "仅生成供应商直接开仓字段刷新执行单，零请求")
	providerApproved := flag.Bool("provider-approved", false, "仅获得本轮明确例外批准后执行一次刷新")
	p := flag.String("plan", "", "已冻结URL和正文锚点")
	r := flag.String("out", "", "预检文件或全新采集目录")
	l := flag.String("ledger", "C:/Temp/tickflow-constraints-20261117-once", "本轮跨目录单次账本")
	launch := flag.Bool("launch", false, "执行单次读取")
	flag.Parse()
	if *providerPrepare || *providerApproved {
		if *providerPrepare && *providerApproved {
			fmt.Fprintln(os.Stderr, "预检与执行互斥")
			os.Exit(1)
		}
		var err error
		if *providerPrepare {
			err = write(*r, providerPlan())
		} else {
			if !providerWindow(time.Now()) {
				fmt.Fprintln(os.Stderr, "已过本次指定日盘窗口，禁止补启")
				os.Exit(1)
			}
			err = fetchProvider(*r, "C:/Temp/tickflow-explicit-minimum-refresh-20261117.once", providerClient(), providerURL)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if e := run(*p, *r, *l, *launch); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
