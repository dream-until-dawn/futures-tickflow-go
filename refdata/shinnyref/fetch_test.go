package shinnyref

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件**不打真上游**：那些读数在 docs/probe.md 复核 v4/v5 里，
// 而这里测的是**本包对那些读数的处置**（httptest 起一个本地服务端）。

func gz(t testing.TB, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const twoEntries = `{
  "SHFE.au2002": {"class":"FUTURE","exchange_id":"SHFE","product_id":"au",
    "delivery_year":2020,"delivery_month":2,"volume_multiple":1000,
    "price_tick":0.02,"expire_datetime":1581692400.0,
    "trading_time":{"day":[["09:00:00","10:15:00"]],"night":[["21:00:00","26:30:00"]]}},
  "KQ.m@SHFE.au": {"class":"FUTURE_CONT","exchange_id":"KQ"}
}`

// TestFetchRefusesWhenServerDoesNotGzip —— 对照组在断言里：
// 先证「服务端按 gzip 回时它收下」，再证「不按 gzip 回时它拒绝」。
//
// 🔴 而拒绝的理由是量出来的：不带压缩，同一份东西是 **351 MB** 而不是 10.6 MiB（31.6 倍）——
// 而那会被当成「网络慢」，**所以要当场拒绝，不能硬拉**。
func TestFetchRefusesWhenServerDoesNotGzip(t *testing.T) {
	// 甲｜服务端按 gzip 回 ⇒ 收下（前提自检：否则下面那个拒绝可能只是「什么都拒」）
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("请求没有显式要 gzip：%q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gz(t, twoEntries))
	}))
	defer okSrv.Close()
	body, err := Fetch(context.Background(), okSrv.Client(), okSrv.URL)
	if err != nil {
		t.Fatalf("前提不成立，本格作废：服务端按 gzip 回了而它拒绝：%v", err)
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("正常的一份却在 Close 时报错：%v", err)
	}
	if string(got) != twoEntries {
		t.Fatalf("解出来的内容不对（%d 字节）", len(got))
	}

	// 乙｜服务端不压缩 ⇒ 拒绝
	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(twoEntries)) // 没有 Content-Encoding
	}))
	defer rawSrv.Close()
	if _, err := Fetch(context.Background(), rawSrv.Client(), rawSrv.URL); !errors.Is(err, errNoGzip) {
		t.Fatalf("服务端没压缩而它没按「没按 gzip 回」拒：%v", err)
	}
}

// TestFetchRefusesPartialContent —— 206 意味着有人加了 Range，
// 而这个端点上 **Range 与 gzip 互斥**：那条路会把 351 MB 原样拉下来。
func TestFetchRefusesPartialContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("部分内容"))
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL); !errors.Is(err, errRangeAsked) {
		t.Fatalf("206 没有被按「Range 与 gzip 互斥」拒掉：%v", err)
	}
}

// TestTruncatedGzipIsCaught 是「完整性靠 gzip 自己的 CRC」那句话的检验。
//
// ⚠️ 对照组：**同一份内容不截断时必须一路干净** —— 否则「截断被抓住」可能只是「总是报错」。
func TestTruncatedGzipIsCaught(t *testing.T) {
	full := gz(t, twoEntries)
	for _, c := range []struct {
		what    string
		body    []byte
		wantErr bool
	}{
		{"完整", full, false},
		{"砍掉末尾 8 字节（CRC 与长度就在那儿）", full[:len(full)-8], true},
		{"砍掉一半", full[:len(full)/2], true},
	} {
		t.Run(c.what, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", "gzip")
				w.Write(c.body)
			}))
			defer srv.Close()
			body, err := Fetch(context.Background(), srv.Client(), srv.URL)
			if err != nil {
				if !c.wantErr {
					t.Fatalf("完整的一份在 Fetch 就失败了：%v", err)
				}
				return
			}
			_, readErr := io.ReadAll(body)
			closeErr := body.Close()
			bad := readErr != nil || closeErr != nil
			if bad != c.wantErr {
				t.Fatalf("截断=%v，而 read=%v close=%v —— "+
					"要么截断没被抓住，要么完整的那份被误伤", c.wantErr, readErr, closeErr)
			}
		})
	}
}

// ───────── Scan ─────────

// TestScanCountsEveryClassIncludingSkipped —— 跳过的那些**必须留下数**。
//
// 🔴 承重的不是「解出了几条期货」，是**被跳过的那一类也有计数** ——
// 一个静默跳过的实现也能让前者为真，**而那时读的人分不出「上游没有」和「我漏收了」**。
func TestScanCountsEveryClassIncludingSkipped(t *testing.T) {
	var got []Contract
	res, err := Scan(strings.NewReader(twoEntries), func(c Contract) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Contracts != 1 || len(got) != 1 {
		t.Fatalf("解出 %d 条期货（回调收到 %d），要 1", res.Contracts, len(got))
	}
	if n := res.ByClass["FUTURE_CONT"]; n != 1 {
		t.Fatalf("被跳过的 FUTURE_CONT 计数是 %d，要 1 —— "+
			"跳过而不记数的话，「上游没有」与「我漏收了」就分不开了", n)
	}
	if n := res.ByClass["FUTURE"]; n != 1 {
		t.Fatalf("FUTURE 计数 %d，要 1", n)
	}
	if got[0].Symbol.String() != "SHFE.au2002" {
		t.Fatalf("解出来的是 %s", got[0].Symbol.String())
	}
}

// TestScanRefusesTruncatedDocument 是这一层最承重的一条。
//
// ⛔ 一份被截断的目录**会安安静静地结束**：`dec.More()` 在流断掉时也返回 false ——
// 于是「少了几千条」和「上游就这么多」长得一模一样。
// ⇒ 所以 Scan 把结尾那个 `}` 也读掉，**它就是「整份读完了」这件事的证据**。
func TestScanRefusesTruncatedDocument(t *testing.T) {
	// 前提自检：完整的那份是走得通的。
	if _, err := Scan(strings.NewReader(twoEntries), nil); err != nil {
		t.Fatalf("前提不成立，本格作废：完整的一份都扫不过：%v", err)
	}
	// ⛔ 切在**两条之间**（第一条完整、后面什么都没有、没有结尾的 }）——
	// 这一格是突变逼出来的：我第一版切在一条**中间**，于是 `dec.Decode` 先报错，
	// **那道「读结尾 }」的守卫根本没被走到**（把它摘掉 ⇒ 全绿）。
	// 🔴 一条测试红了，不代表它红在我以为的那一行上。
	cleanCut := twoEntries[:strings.Index(twoEntries, `"KQ.m@SHFE.au"`)-4]
	res, err := Scan(strings.NewReader(cleanCut), nil)
	if err == nil {
		t.Fatalf("截断的一份被扫过去了，还报了 %s —— "+
			"而它正是「静默少了几千条」那一格：dec.More() 在流断掉时也返回 false", res)
	}
	if res.Contracts != 1 {
		t.Fatalf("切在两条之间时，第一条应当已经解出来了，实得 %d 条", res.Contracts)
	}
	// ⚠️ 而它必须**带着已经走过的账**报错 —— 一个只说「坏了」的错误，
	// 读的人不知道是第 3 条坏了还是第 20 万条坏了。
	if !strings.Contains(err.Error(), "1") {
		t.Fatalf("报错里没有「已走过多少条」：%v", err)
	}
}

// TestScanStopsOnCallbackError —— 回调说停就停，而账要带回去。
func TestScanStopsOnCallbackError(t *testing.T) {
	sentinel := fmt.Errorf("调用方喊停")
	res, err := Scan(strings.NewReader(twoEntries), func(Contract) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("回调的错误没有原样传出来：%v", err)
	}
	if res.ByClass["FUTURE"] != 1 {
		t.Fatalf("中止时那本账丢了：%v", res.ByClass)
	}
}

// TestFetchThenScanCatchesTruncation 打的是**两层合起来**那条路，
// 而它存在的理由是一个接缝：
//
// ⛔ `Scan` 读到那个结尾的 `}` 就返回了 —— **它从不把流读到 EOF**，
// 于是 `gzip.Reader` 那次「读到末尾时校验 CRC 与长度」**在这条路上根本不跑**。
// ⇒ 也就是说：合起来用的时候，**接住截断的是 JSON 那道守卫，不是 gzip 的 CRC**。
//
// 🔴 而这一条是先量后写的：单看 fetch_test 全绿、单看 scan_test 全绿，
// **两层各自的测试都不覆盖「谁在合起来时接住它」**。
func TestFetchThenScanCatchesTruncation(t *testing.T) {
	full := gz(t, twoEntries)
	for _, c := range []struct {
		what    string
		body    []byte
		wantErr bool
	}{
		{"完整", full, false},
		{"gzip 被截断", full[:len(full)-8], true},
	} {
		t.Run(c.what, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", "gzip")
				w.Write(c.body)
			}))
			defer srv.Close()
			body, err := Fetch(context.Background(), srv.Client(), srv.URL)
			if err != nil {
				t.Fatalf("Fetch 就失败了：%v", err)
			}
			defer body.Close()
			res, err := Scan(body, nil)
			if (err != nil) != c.wantErr {
				t.Fatalf("截断=%v 而 Scan 给的是 err=%v（%s）—— "+
					"要么截断被扫过去了，要么完整的那份被误伤", c.wantErr, err, res)
			}
		})
	}
}

// TestScanRefusesTrailingGarbage —— 结尾的 `}` 之后还有东西 ⇒ 这不是我以为的那份文档。
//
// ⚠️ 它和上面那条**测的是同一行守卫的两个方向**：
// 一边是「流没结束」（gzip 尾巴掉了），一边是「结束了还有话」。
func TestScanRefusesTrailingGarbage(t *testing.T) {
	if _, err := Scan(strings.NewReader(twoEntries+"\n{}"), nil); err == nil {
		t.Fatal("结尾的 } 之后还有一个对象，而它被扫过去了")
	}
	// 对照组：结尾后的**空白**必须仍然算干净（否则这道守卫会误伤真实文件）。
	if _, err := Scan(strings.NewReader(twoEntries+"\n\n  \n"), nil); err != nil {
		t.Fatalf("结尾后只有空白却被拒了：%v —— 这道守卫会误伤真文件", err)
	}
}

// TestCloseReportsUnreadDownload —— **本层自己回答「这份下载被校验过没有」**。
//
// ⛔ 它存在的理由：gzip 的完整性保证绑在「流被读到 EOF」这个**事件**上，
// 而让那个事件发生的是**调用方** ⇒ 在有这道断言之前，它只是一条**约定**。
//
// 🔴 而这一条是补出来的：改法是评审方给的，他**手测了两端却没有留下测试** ——
// 而本仓那条是「永远绿的测试等于没有测试」，它的姊妹是
// **「手测过而没有测试」等于下一次改动时没有测试**。
func TestCloseReportsUnreadDownload(t *testing.T) {
	newBody := func(t *testing.T) io.ReadCloser {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(gz(t, twoEntries))
		}))
		t.Cleanup(srv.Close)
		body, err := Fetch(context.Background(), srv.Client(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	// 甲｜对照组：读到头再关 ⇒ **不许报错**（否则这道断言就是在误伤正常路径）
	body := newBody(t)
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("完整的一份读不完：%v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("读到头了却报错 —— 这道断言在误伤正常路径：%v", err)
	}

	// 乙｜读几个字节就放手 ⇒ 必须报出来
	body = newBody(t)
	buf := make([]byte, 4)
	if _, err := body.Read(buf); err != nil {
		t.Fatalf("读前 4 字节就失败了：%v", err)
	}
	err := body.Close()
	if !errors.Is(err, errNotReadToEOF) {
		t.Fatalf("提前放手而 Close 没说话：%v —— "+
			"那时「这份数据完不完整」这句话没有人回答得了", err)
	}
}
