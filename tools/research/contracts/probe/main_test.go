package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestOfficialHTTPSProbeCRCAndMissingTarget(t *testing.T) {
	for _, name := range []string{"20210104_1.csv", "other.csv", "../20210104_1.csv"} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		f, e := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte("header\n1,2\n"))
		z.Close()
		raw := b.Bytes()
		sum := sha256.Sum256(raw)
		ref := evidence.Ref{ID: "fixture", URL: jobs()[1].URL, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Unix(1, 0)}
		err := validate(jobs()[1], raw, ref)
		if (err == nil) != (name == "20210104_1.csv") {
			t.Fatal(name, err)
		}
		if name == "20210104_1.csv" {
			bad := append([]byte(nil), raw...)
			position := bytes.Index(bad, []byte("header\n1,2\n"))
			if position < 0 {
				t.Fatal("store正控数据定位失败")
			}
			bad[position] ^= 1
			sum = sha256.Sum256(bad)
			ref.SHA256 = hex.EncodeToString(sum[:])
			badErr := validate(jobs()[1], bad, ref)
			if badErr == nil || !strings.Contains(badErr.Error(), "CRC") {
				t.Fatal("损坏ZIP不能视作完整")
			}
		}
	}
}
