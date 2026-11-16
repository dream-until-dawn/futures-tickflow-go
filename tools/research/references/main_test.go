package main

import "testing"

func TestReferenceTransferRejectsHTMLAndBrokenXML(t *testing.T) {
	if err := validate([]byte(`<FIXML><SecDef/></FIXML>`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`<html>challenge</html>`, `<FIXML>`} {
		if err := validate([]byte(raw)); err == nil {
			t.Fatal("非参考原件通过", raw)
		}
	}
}
