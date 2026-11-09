package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const baseJSON = `{"report_date":"20260827","ContractBaseInfo":[{"INSTRUMENTID":"rb2701","EXCHANGEID":"SHFE","COMMODITYID":"rb","TRADINGDAY":"20260827","OPENDATE":"20260116","EXPIREDATE":"20270115"}]}`

func TestSHFEBaseRetainsExpiryWithoutInventingLastTrade(t *testing.T) {
	ref := proof()
	sum := sha256.Sum256([]byte(baseJSON))
	ref.SHA256 = hex.EncodeToString(sum[:])
	entries, e := ParseSHFEBase([]byte(baseJSON), ref, 20260827)
	if e != nil || len(entries) != 1 || entries[0].Listed.Day != 20260116 || entries[0].LastTrading.Day != 0 || entries[0].ReportedExpiry.Day != 20270115 {
		t.Fatalf("%+v %v", entries, e)
	}
	for _, body := range []string{strings.Replace(baseJSON, `"TRADINGDAY":"20260827"`, `"TRADINGDAY":"20260828"`, 1), strings.Replace(baseJSON, "20260116", "20260828", 1), strings.Replace(baseJSON, `"COMMODITYID":"rb"`, `"COMMODITYID":"cu"`, 1), strings.Replace(baseJSON, "20270115", "20260230", 1)} {
		r := ref
		sum = sha256.Sum256([]byte(body))
		r.SHA256 = hex.EncodeToString(sum[:])
		if _, e = ParseSHFEBase([]byte(body), r, 20260827); e == nil {
			t.Fatal("异常基本信息通过")
		}
	}
}
