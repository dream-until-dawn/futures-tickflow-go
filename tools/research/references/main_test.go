package main

import "testing"

func TestReferenceTransferRejectsHTMLAndBrokenXML(t *testing.T) {
	// 与既有catalog真实格式一致：显式年月和到期，不使用猜测的FIXML空壳。
	good := []byte(`<Contracts><Contract><CtrCd>RM701</CtrCd><PrdCd>RM</PrdCd><PrdTp>期货</PrdTp><ExchCd>XZCE</ExchCd><YrCd>2027</YrCd><MnthCd>01</MnthCd><ExpiryTime>2027-01-15 15:00 北京时间</ExpiryTime><FrstTrdDt>2026-01-16</FrstTrdDt><LstTrdDt>2027-01-15</LstTrdDt></Contract></Contracts>`)
	if err := validate(good); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`<html>challenge</html>`, `<Contracts>`, `<FIXML><SecDef/></FIXML>`, `<Contracts/>`} {
		if err := validate([]byte(raw)); err == nil {
			t.Fatal("非参考原件通过", raw)
		}
	}
}
