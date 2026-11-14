package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticQuoteFrozenBodyAndUnqualifiedScope(t *testing.T) {
	base := "../../../docs/evidence/sina-retired-page-20261114"
	b, e := os.ReadFile(filepath.Join(base, "files.json"))
	if e != nil {
		t.Fatal(e)
	}
	var files []struct {
		File   string
		Bytes  int
		SHA256 string
	}
	if e = json.Unmarshal(b, &files); e != nil {
		t.Fatal(e)
	}
	if len(files) != 5 {
		t.Fatal("静态页封存范围变化")
	}
	for _, f := range files {
		raw, e := os.ReadFile(filepath.Join(base, f.File))
		if e != nil {
			t.Fatal(e)
		}
		if len(raw) != f.Bytes || digest(raw) != f.SHA256 {
			t.Fatalf("静态页原件改变 %s", f.File)
		}
	}
	b, e = os.ReadFile(filepath.Join(base, "analysis.json"))
	if e != nil {
		t.Fatal(e)
	}
	var scope map[string]any
	if e = json.Unmarshal(b, &scope); e != nil {
		t.Fatal(e)
	}
	if scope["static_historical_roster_proved"] != false || scope["javascript_executed"] != false || scope["followup_requests"] != float64(0) || scope["new_price_codes"] != float64(0) || scope["financial_eligible"] != false {
		t.Fatal("静态页不应提升资格或扩大请求")
	}
}

func TestStaticQuotePreparePinsOneLiteralAndOriginalReceipt(t *testing.T) {
	source := "../../../docs/evidence/sina-vendor-origin-20261114"
	j, r, e := prepare(source)
	if e != nil || j.URL != quoteURL || r.SHA256 != originSHA {
		t.Fatalf("%+v %+v %v", j, r, e)
	}
	for _, name := range []string{"akshare-source.py.txt", "source-receipt.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, file := range []string{"akshare-source.py.txt", "source-receipt.json"} {
				b, e := os.ReadFile(filepath.Join(source, file))
				if e != nil {
					t.Fatal(e)
				}
				if file == name {
					b = append(b, 'x')
				}
				if e = os.WriteFile(filepath.Join(dir, file), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, _, e := prepare(dir); e == nil || !strings.Contains(e.Error(), "出处SHA") {
				t.Fatal("坏出处通过")
			}
		})
	}
}

func TestControlStaticRequiresExactPriorHTMLAndReceipt(t *testing.T) {
	source := "../../../docs/evidence/sina-retired-page-20261114"
	j, r, e := prepareControl(source)
	if e != nil || j.URL != controlURL || r.SHA256 != quoteSHA {
		t.Fatalf("%+v %v", j, e)
	}
	for _, changed := range []string{"page.jsonp", "receipt.json"} {
		dir := t.TempDir()
		for _, name := range []string{"page.jsonp", "receipt.json"} {
			b, e := os.ReadFile(filepath.Join(source, name))
			if e != nil {
				t.Fatal(e)
			}
			if name == changed {
				b = append(b, 'x')
			}
			if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
				t.Fatal(e)
			}
		}
		if _, _, e = prepareControl(dir); e == nil {
			t.Fatal("坏HTML/收据通过")
		}
	}
}

func TestModularStaticPinsApprovedPath(t *testing.T) {
	source := "../../../docs/evidence/sina-retired-page-20261114"
	job, ref, err := prepareScript(source, modularURL, "sina-modular-static")
	if err != nil || job.URL != modularURL || ref.SHA256 != quoteSHA {
		t.Fatalf("%+v %v", job, err)
	}
	for _, target := range []string{"https://example.com/other.js", modularURL + "?extra=1", "http://n.sinaimg.cn/finance/futures/moudler20200421.js"} {
		if _, _, err := prepareScript(source, target, "x"); err == nil || !strings.Contains(err.Error(), "未批准") {
			t.Fatalf("未批准路径通过: %s %v", target, err)
		}
	}
}

func TestConfigStaticPinsApprovedPath(t *testing.T) {
	job, ref, err := prepareScript("../../../docs/evidence/sina-retired-page-20261114", configURL, "sina-config-static")
	if err != nil || job.URL != configURL || ref.SHA256 != quoteSHA {
		t.Fatalf("%+v %v", job, err)
	}
	if _, _, err := prepareScript("../../../docs/evidence/sina-retired-page-20261114", configURL+"?x=1", "x"); err == nil {
		t.Fatal("未批准配置通过")
	}
}

func TestHistoryHTMLPinsFrozenConfigAndNoParameters(t *testing.T) {
	source := "../../../docs/evidence/sina-config-static-20261115"
	job, ref, err := prepareHistory(source)
	if err != nil || job.URL != historyURL || strings.ContainsAny(job.URL, "?&") || ref.SHA256 != configSHA {
		t.Fatalf("%+v %v", job, err)
	}
	for _, changed := range []string{"config.jsonp", "receipt.json"} {
		dir := t.TempDir()
		for _, name := range []string{"config.jsonp", "receipt.json"} {
			data, err := os.ReadFile(filepath.Join(source, name))
			if err != nil {
				t.Fatal(err)
			}
			if name == changed {
				data = append(data, 'x')
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := prepareHistory(dir); err == nil {
			t.Fatal("损坏配置/收据通过")
		}
	}
}

// 原件索引必须在干净克隆中也成立，避免Git换行转换被工作区正确字节掩盖。
func TestStaticScriptEvidenceFrozenBytes(t *testing.T) {
	for _, folder := range []string{"sina-control-static-20261115", "sina-modular-static-20261115", "sina-config-static-20261115"} {
		base := filepath.Join("../../../docs/evidence", folder)
		raw, err := os.ReadFile(filepath.Join(base, "files.json"))
		if err != nil {
			t.Fatal(err)
		}
		var files []struct {
			File   string
			Bytes  int
			SHA256 string
		}
		if err := json.Unmarshal(raw, &files); err != nil {
			t.Fatal(err)
		}
		if len(files) != 5 {
			t.Fatal("静态封存范围变化")
		}
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(base, file.File))
			if err != nil {
				t.Fatal(err)
			}
			if len(data) != file.Bytes || digest(data) != file.SHA256 {
				t.Fatalf("原字节改变 %s/%s", folder, file.File)
			}
		}
	}
}
