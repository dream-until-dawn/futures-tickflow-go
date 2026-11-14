package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
