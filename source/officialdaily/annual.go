package officialdaily

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

// 年包表格由tools/archive/decode_annual.py解码。Go不启动Python子进程；
// 消费者先冻结解码产物，再同时提供原始ZIP和产物，各自核验SHA。
// 这证明来源绑定；解码正确性还须独立复核。XLS数值为二进制单元格，
// 产物十进制是解码表示，不声称恢复不存在的报价文本。
func parseAnnualDecoded(d Document) ([]Record, error) {
	if err := d.Origin.Verify(d.Original); err != nil {
		return nil, err
	}
	z, err := zip.NewReader(bytes.NewReader(d.Original), int64(len(d.Original)))
	if err != nil {
		return nil, err
	}
	if len(z.File) > 4096 {
		return nil, fmt.Errorf("officialdaily: ZIP成员超预算")
	}
	var expanded uint64
	seen := map[string]bool{}
	for _, f := range z.File {
		if seen[f.Name] || strings.Contains(strings.ReplaceAll(f.Name, "\\", "/"), "../") || strings.HasPrefix(f.Name, "/") {
			return nil, fmt.Errorf("officialdaily: ZIP成员重复/路径异常")
		}
		seen[f.Name] = true
		if f.UncompressedSize64 > 1<<30-expanded {
			return nil, fmt.Errorf("officialdaily: ZIP展开超预算")
		}
		expanded += f.UncompressedSize64
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		_, e = io.Copy(io.Discard, r)
		closeErr := r.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	var envelope struct {
		Schema      string   `json:"schema"`
		Year        int      `json:"year"`
		OriginalSHA string   `json:"original_sha256"`
		DecoderSHA  string   `json:"decoder_sha256"`
		Records     []Record `json:"records"`
	}
	if err := json.Unmarshal(d.Raw, &envelope); err != nil {
		return nil, err
	}
	y, _, _ := d.Day.Split()
	if envelope.Schema != "official-annual-decoded-v1" || envelope.Year != y || envelope.OriginalSHA != d.Origin.SHA256 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(envelope.DecoderSHA) || d.Evidence.Version != envelope.DecoderSHA {
		return nil, fmt.Errorf("officialdaily: 年包解码身份/年份错配")
	}
	for i := range envelope.Records {
		r := &envelope.Records[i]
		if _, err := verified.Date(r.Day); err != nil {
			return nil, err
		}
		year, _, _ := r.Day.Split()
		if year != y {
			return nil, fmt.Errorf("officialdaily: 年包日期越界")
		}
		if strings.Contains(r.Member, "..") || r.Member == "" || r.Row <= 0 {
			return nil, fmt.Errorf("officialdaily: 缺少原件成员/行号")
		}
		symbol, err := tf.ParseSymbol(r.Exchange + "." + r.Native)
		if err != nil || (symbol.Exchange != tf.SHFE && symbol.Exchange != tf.INE) || ine[symbol.Product] != (symbol.Exchange == tf.INE) {
			return nil, fmt.Errorf("officialdaily: 年包合约市场不符")
		}
		for _, key := range []string{"open", "high", "low", "close", "settle", "pre_settle", "volume", "open_interest", "turnover"} {
			if _, ok := r.Fields[key]; !ok {
				return nil, fmt.Errorf("officialdaily: 年包缺必要字段")
			}
		}
		// 成交额单位未独立核实，不能靠字段名Amount作元单位换算。
		if r.TurnoverUnit != "unknown" {
			return nil, fmt.Errorf("officialdaily: 年包成交额单位须独立核实")
		}
		r.Evidence = d.Origin
		artifact := d.Evidence
		r.Artifact = &artifact
	}
	return finish(envelope.Records)
}
