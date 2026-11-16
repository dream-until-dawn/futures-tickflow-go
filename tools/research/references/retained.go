package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/catalog"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// reviewRetained保留旧回执格式与未知业务状态；仅对完整成功传输再做离线结构复核。
func reviewRetained(root, out string) error {
	rawMeta, err := os.ReadFile(filepath.Join(root, "requests.json"))
	if err != nil {
		return err
	}
	var receipts []struct {
		ID, URL   string
		Status    int
		Bytes     int
		SHA       string    `json:"sha256"`
		State     string    `json:"transport_state"`
		Requested time.Time `json:"requested_utc"`
		Final     string    `json:"final_url"`
	}
	if err = json.Unmarshal(rawMeta, &receipts); err != nil {
		return err
	}
	pattern := regexp.MustCompile(`^https://www\.czce\.com\.cn/cn/DFSStaticFiles/Future/(\d{4})/(\d{8})/FutureDataReferenceData\.xml$`)
	safeID := regexp.MustCompile(`^[a-z0-9-]+$`)
	var output []map[string]any
	seen := map[string]bool{}
	for _, r := range receipts {
		match := pattern.FindStringSubmatch(r.URL)
		if match == nil {
			continue
		}
		if !safeID.MatchString(r.ID) || r.Status != 200 || r.State != "complete" || r.Final != r.URL || r.Requested.IsZero() {
			return fmt.Errorf("参考原件回执不完整: %s", r.ID)
		}
		if seen[r.URL] {
			return fmt.Errorf("重复参考回执")
		}
		seen[r.URL] = true
		if match[1] != match[2][:4] {
			return fmt.Errorf("参考日期不对应")
		}
		raw, err := os.ReadFile(filepath.Join(root, r.ID+".body"))
		if err != nil {
			return err
		}
		if len(raw) != r.Bytes || hash(raw) != r.SHA {
			return fmt.Errorf("参考原字节身份不符")
		}
		day, err := strconv.Atoi(match[2])
		if err != nil {
			return err
		}
		ref := evidence.Ref{ID: r.ID, URL: r.URL, SHA256: r.SHA, RetrievedAt: r.Requested}
		entries, err := catalog.ParseCZCEReference(raw, ref, tf.TradingDay(day))
		if err != nil {
			return err
		}
		values := []map[string]any{}
		for _, entry := range entries {
			values = append(values, map[string]any{"canonical": entry.Symbol.String(), "native": entry.Symbol.Instrument(), "exchange": entry.Symbol.Exchange, "product": entry.Symbol.Product, "observed_day": day, "listed_declared": entry.Listed.Day, "last_trading_declared": entry.LastTrading.Day})
		}
		output = append(output, map[string]any{"schema": "retained-reference-readback-v1", "id": r.ID, "observed_day": day, "body_sha256": r.SHA, "receipt_set_sha256": hash(rawMeta), "requested_at": r.Requested, "entries": values, "financial_admission_added": 0})
	}
	if len(output) == 0 {
		return fmt.Errorf("没有合格参考原件")
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(encoded, '\n'))
	return err
}
