package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const referenceXML = `<Contracts><Contract><CtrCd>RM701</CtrCd><PrdCd>RM</PrdCd><PrdTp>期货</PrdTp><ExchCd>XZCE</ExchCd><YrCd>2027</YrCd><MnthCd>01</MnthCd><ExpiryTime>2027-01-15 15:00 北京时间</ExpiryTime><FrstTrdDt>2026-01-16</FrstTrdDt><LstTrdDt>2027-01-15</LstTrdDt></Contract></Contracts>`

func TestCZCEExplicitYearAndLifecycle(t *testing.T) {
	ref := proof()
	ref.URL = "https://example.org/20261103/reference.xml"
	hash := sha256.Sum256([]byte(referenceXML))
	ref.SHA256 = hex.EncodeToString(hash[:])
	entries, e := ParseCZCEReference([]byte(referenceXML), ref, 20261103)
	if e != nil || entries[0].Symbol != sym("CZCE.RM2701") || entries[0].Listed.Day != 20260116 || entries[0].LastTrading.Day != 20270115 {
		t.Fatalf("%+v %v", entries, e)
	}
	for _, raw := range []string{strings.Replace(referenceXML, "2027</YrCd>", "2017</YrCd>", 1), strings.Replace(referenceXML, "<YrCd>2027</YrCd>", "", 1), strings.Replace(referenceXML, "<YrCd>2027</YrCd>", "<YrCd>2027</YrCd><YrCd>2017</YrCd>", 1), strings.Replace(referenceXML, "2026-01-16", "2026-11-04", 1), "<!DOCTYPE Contracts>" + referenceXML} {
		r := ref
		hash = sha256.Sum256([]byte(raw))
		r.SHA256 = hex.EncodeToString(hash[:])
		if _, e = ParseCZCEReference([]byte(raw), r, 20261103); e == nil {
			t.Fatal("年月/生命周期/重复/声明反例通过")
		}
	}
	if _, e = ParseCZCEReference([]byte(referenceXML), ref, 20261104); e == nil {
		t.Fatal("历史日期错绑通过")
	}
	if _, e = Merge(entries, entries); e != nil {
		t.Fatal(e)
	}
	other := copyEntry(entries[0])
	other.Listed.Day = 20260115
	if _, e = Merge(entries, []Entry{other}); e == nil {
		t.Fatal("不同快照生命周期冲突被覆盖")
	}
}
