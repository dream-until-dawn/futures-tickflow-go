package catalog

import (
	"strings"
	"testing"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/shinnyref"
)

func proof() evidence.Ref {
	return evidence.Ref{ID: "synthetic-roster", URL: "https://example.org/roster", SHA256: strings.Repeat("1", 64), RetrievedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
}
func sym(s string) tf.Symbol {
	v, e := tf.ParseSymbol(s)
	if e != nil {
		panic(e)
	}
	return v
}
func TestCatalogAmbiguityUnknownAndSnapshot(t *testing.T) {
	r := proof()
	s := sym("CZCE.RM2701")
	candidates, e := Candidates([]shinnyref.Contract{{Symbol: s, ExpireTs: 1800000000000}}, r)
	if e != nil {
		t.Fatal(e)
	}
	c, e := New(candidates)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Resolve("CZCE", "RM701", 20261103); e == nil {
		t.Fatal("候选快照竟证明历史日期")
	}
	entries := []Entry{{Symbol: s, Observed: []Observation{{Day: 20261103, Evidence: r}}}}
	c, e = New(entries)
	if e != nil {
		t.Fatal(e)
	}
	if got, e := c.Resolve("CZCE", "RM701", 20261103); e != nil || got != s {
		t.Fatalf("%v %v", got, e)
	}
	if _, e = c.Resolve("CZCE", "RM701", 20261104); e == nil {
		t.Fatal("从观察日外推")
	}
	entries = append(entries, Entry{Symbol: sym("CZCE.RM1701"), Observed: []Observation{{Day: 20261103, Evidence: r}}})
	c, e = New(entries)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Resolve("CZCE", "RM701", 20261103); e == nil {
		t.Fatal("歧义未拒绝")
	}
}
func TestCatalogIndependentRosterAndLifecycle(t *testing.T) {
	a, b := sym("SHFE.rb2701"), sym("SHFE.rb2705")
	want := Expected{Day: 20261103, Symbols: []tf.Symbol{a, b}, Evidence: []evidence.Ref{proof()}}
	if e := CheckSet(want, []tf.Symbol{b, a}); e != nil {
		t.Fatal(e)
	}
	for _, actual := range [][]tf.Symbol{{a}, {a, b, sym("SHFE.rb2801")}, {a, a}} {
		if e := CheckSet(want, actual); e == nil {
			t.Fatal("删除/未来/重复合约通过")
		}
	}
	good := Entry{Symbol: a, Listed: DateFact{Day: 20261102, Evidence: []evidence.Ref{proof()}}, LastTrading: DateFact{Day: 20270115, Evidence: []evidence.Ref{proof()}}}
	c, e := New([]Entry{good})
	if e != nil {
		t.Fatal(e)
	}
	got, _ := c.Get(a)
	got.Listed.Evidence[0].ID = "changed"
	if v, _ := c.Get(a); v.Listed.Evidence[0].ID == "changed" {
		t.Fatal("副本泄漏")
	}
	good.Observed = []Observation{{Day: 20261101, Evidence: proof()}}
	if _, e = New([]Entry{good}); e == nil {
		t.Fatal("上市前观察通过")
	}
	good.Observed = nil
	good.Listed.Evidence = nil
	if _, e = New([]Entry{good}); e == nil {
		t.Fatal("日期无证据通过")
	}
}

func TestCatalogHistoryQueryKeepsUnknownSeparate(t *testing.T) {
	r := proof()
	candidate := r
	c, e := New([]Entry{{Symbol: sym("CZCE.RM2701"), Listed: DateFact{Day: 20260116, Evidence: []evidence.Ref{r}}, LastTrading: DateFact{Day: 20270115, Evidence: []evidence.Ref{r}}}, {Symbol: sym("CZCE.RM2801"), Listed: DateFact{Day: 20270116, Evidence: []evidence.Ref{r}}, LastTrading: DateFact{Day: 20280115, Evidence: []evidence.Ref{r}}}, {Symbol: sym("CZCE.RM2705"), Observed: []Observation{{Day: 20261103, Evidence: r}}}, {Symbol: sym("CZCE.RM2901"), Candidate: &candidate}})
	if e != nil {
		t.Fatal(e)
	}
	report, e := c.QueryHistory(Query{Exchange: "CZCE", From: 20261102, To: 20261104})
	if e != nil || len(report.LifecycleOverlap) != 1 || len(report.Observed) != 1 || len(report.Unresolved) != 1 || report.CompleteUniverseCertified {
		t.Fatalf("%+v %v", report, e)
	}
}
