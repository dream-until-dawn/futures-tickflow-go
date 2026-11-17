package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNewOpeningReferencesRemainFiniteAndBlockedURLsStayBlocked(t *testing.T) {
	base := target{ID: "quote", URL: "https://www.9qihuo.com/qihuoshouxufeisingle?heyue=CJ", Anchors: []string{"最小开仓"}}
	if err := validate(requestPlan{Targets: []target{base}}); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{
		"https://www.9qihuo.com/qihuoshouxufeisingle?heyue=IF",
		"https://www.9qihuo.com/qihuoshouxufeisingle?heyue=CJ&retry=1",
		"https://www.9qihuo.com/qihuoshouxufeisingle?heyue=%43J",
		"http://www.9qihuo.com/qihuoshouxufeisingle?heyue=CJ",
		"https://www.9qihuo.com/qihuoshouxufeisingle?heyue=CJ#fragment",
		"https://example.org/qihuoshouxufeisingle?heyue=CJ",
	} {
		candidate := base
		candidate.URL = address
		if validate(requestPlan{Targets: []target{candidate}}) == nil {
			t.Fatalf("不应允许 %s", address)
		}
	}
	if validate(requestPlan{Targets: []target{base}, Blocked: []string{base.URL}}) == nil {
		t.Fatal("新来源也不能绕过已失败URL账本")
	}
}

func TestListingPrimaryDocumentBudgetsDoNotExpandOtherRequests(t *testing.T) {
	large := 0
	for address, limit := range listingEvidenceResources {
		if sourceByteLimit(address) != limit {
			t.Fatal("预算不匹配")
		}
		if limit > 2<<20 {
			large++
		}
		candidate := target{ID: "listing", URL: address, Anchors: []string{"source"}}
		if err := validate(requestPlan{Targets: []target{candidate}}); err != nil {
			t.Fatal(err)
		}
		if err := validate(requestPlan{Targets: []target{candidate}, Blocked: []string{address}}); err == nil {
			t.Fatal("新出版物也不得重试失败URL")
		}
	}
	if large != 2 || sourceByteLimit("https://www.shfe.com.cn/unrelated.pdf") != 2<<20 {
		t.Fatal("预算被泛化")
	}
}

func TestOfficialPlanAndFailures(t *testing.T) {
	base := target{ID: "rules", URL: "https://www.shfe.com.cn/a.html", Anchors: []string{"最小"}}
	if e := validate(requestPlan{Targets: []target{base}}); e != nil {
		t.Fatal(e)
	}
	cases := []target{base, base, base, base}
	cases[0].URL = "https://www.gfex.com.cn/"
	cases[1].URL = "http://www.shfe.com.cn/a.html"
	cases[2].ID = "../escape"
	cases[3].Anchors = nil
	for _, v := range cases {
		if validate(requestPlan{Targets: []target{v}}) == nil {
			t.Fatalf("错误放行%+v", v)
		}
	}
	if validate(requestPlan{Targets: []target{base, base}}) == nil {
		t.Fatal("重复未拒绝")
	}
	if validate(requestPlan{Targets: []target{base}, Blocked: []string{base.URL}}) == nil {
		t.Fatal("受阻未拒绝")
	}
	if content(base, []byte("最小有效正文")) != nil {
		t.Fatal("正向失败")
	}
	if content(base, []byte("无相应字段")) == nil || content(base, []byte("最小 人机识别检测")) == nil {
		t.Fatal("反例未拒绝")
	}
}

func TestPrepareHasZeroRequestsAndExistingMarkerStops(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "input.json")
	b, _ := json.Marshal(requestPlan{Targets: []target{{ID: "a", URL: "https://www.shfe.com.cn/a", Anchors: []string{"正文"}}}})
	os.WriteFile(p, b, 0600)
	ledger := filepath.Join(root, "ledger")
	if e := run(p, filepath.Join(root, "preflight.json"), ledger, false); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(ledger); !os.IsNotExist(e) {
		t.Fatal("预检不得创建启动账本")
	}
	os.Mkdir(ledger, 0700)
	os.WriteFile(filepath.Join(ledger, digest([]byte("https://www.shfe.com.cn/a"))+".started"), []byte("unknown"), 0600)
	if run(p, filepath.Join(root, "new-output"), ledger, true) == nil {
		t.Fatal("未知启动竟然重试")
	}
}
