package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
