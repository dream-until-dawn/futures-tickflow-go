package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/catalog"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func reviewExisting(root, out string, historical bool) error {
	if out == "" {
		return fmt.Errorf("离线输出须为全新文件")
	}
	raw, err := os.ReadFile(filepath.Join(root, "reference.body"))
	if err != nil {
		return err
	}
	meta, err := os.ReadFile(filepath.Join(root, "receipt.json"))
	if err != nil {
		return err
	}
	var receipt struct {
		URL, State, SHA256, Error string
		Bytes                     int
		HTTP                      batch.Download
		Finished                  time.Time `json:"finished_at_utc"`
	}
	if err = json.Unmarshal(meta, &receipt); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if receipt.SHA256 != digest || receipt.Bytes != len(raw) || !receipt.HTTP.Attempted || !receipt.HTTP.ResponseReceived || !receipt.HTTP.Complete || receipt.HTTP.Status != 200 {
		return fmt.Errorf("不是完整对应原件")
	}
	if receipt.State != "received_original_scope_unreviewed" && !(receipt.State == "failed" && receipt.Error == "不是已知FIXML参考快照") {
		return fmt.Errorf("未证为本地门禁误判，拒绝提升失败")
	}
	day := tf.TradingDay(20261116)
	if historical {
		day = 20210104
	}
	ref := evidence.Ref{ID: fmt.Sprintf("czce-reference-%d", day), URL: receipt.URL, SHA256: digest, RetrievedAt: receipt.HTTP.RequestedAt}
	entries, err := catalog.ParseCZCEReference(raw, ref, day)
	if err != nil {
		return err
	}
	var values []map[string]any
	for _, entry := range entries {
		values = append(values, map[string]any{"canonical": entry.Symbol.String(), "native": entry.Symbol.Instrument(), "exchange": entry.Symbol.Exchange, "product": entry.Symbol.Product, "observed_day": day, "listed_declared": entry.Listed.Day, "last_trading_declared": entry.LastTrading.Day})
	}
	result := map[string]any{"schema": "czce-reference-readback-v1", "observed_day": day, "body_sha256": digest, "receipt_sha256": hash(meta), "requested_at": receipt.HTTP.RequestedAt, "finished_at": receipt.Finished, "original_receipt_state": receipt.State, "entries": values, "financial_admission_added": 0, "limits": "日期绑定参考陈述；不证明完整历史/PIT或专项最小开仓约束"}
	encoded, err := json.MarshalIndent(result, "", "  ")
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

func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
