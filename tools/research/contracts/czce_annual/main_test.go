package main

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestAnnualCandidateRejectsWrongMemberAndHTML(t *testing.T) {
	pack := func(name string) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte("fixture")); e != nil {
			t.Fatal(e)
		}
		if e = z.Close(); e != nil {
			t.Fatal(e)
		}
		return b.Bytes()
	}
	if e := validate(pack("datahistory2021.txt")); e != nil {
		t.Fatal(e)
	}
	for _, b := range [][]byte{pack("datahistory2020.txt"), []byte("<html>challenge</html>"), pack("../datahistory2021.txt")} {
		if validate(b) == nil {
			t.Fatal("不符合目标结构仍通过")
		}
	}
}
