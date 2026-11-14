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
