package shinnysource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// readGapRecords 必须读至 EOF，不能以 Decoder.More 代替 JSONL 的结束检查。
// More 用于数组/对象，流末尾多出的闭括号不能被当作正常文件结束。
func readGapRecords(reader io.Reader) ([]l8Rec, error) {
	dec := json.NewDecoder(reader)
	var records []l8Rec
	for {
		var r l8Rec
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) {
			return records, nil
		}
		if err != nil {
			return nil, err
		}
		raw := bytes.TrimSpace(r.Raw)
		if r.Conn <= 0 || r.Recv <= 0 || len(raw) == 0 || raw[0] != '{' {
			return nil, fmt.Errorf("原始帧元数据或载荷无效")
		}
		records = append(records, r)
	}
}

func TestGapRecordStreamIntegrity(t *testing.T) {
	valid := `{"conn":1,"recv_ms":1,"raw":{"aid":"rtn_data","data":[]}}`
	r, err := readGapRecords(strings.NewReader(valid + "\n" + valid + "\n"))
	if err != nil || len(r) != 2 {
		t.Fatalf("正常两行 %d / %v", len(r), err)
	}
	for _, bad := range []string{valid + `]`, valid + `}`, valid + `{`, `null`, `{"conn":1,"recv_ms":1,"raw":null}`} {
		if _, err := readGapRecords(strings.NewReader(bad)); err == nil {
			t.Fatalf("损坏流未拒绝：%s", bad)
		}
	}
}
