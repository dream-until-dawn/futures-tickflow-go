// Command dailyrecords 只读既有归档原件，导出Records供资料台账统计；不创建Source/Feed。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/ingest"
	"github.com/dream-until-dawn/futures-tickflow-go/source/officialdaily"
)

func parseReceived(job batch.Job, raw []byte, receipt batch.Receipt) ([]officialdaily.Record, error) {
	if receipt.State != "received" || receipt.BodyState != "complete" || receipt.HTTP == nil || receipt.HTTP.Status != 200 || !receipt.HTTP.Complete || receipt.Evidence.URL != job.URL {
		return nil, fmt.Errorf("不是完整对应原件")
	}
	return officialdaily.Parse(officialdaily.Document{Format: job.Format, Day: tf.TradingDay(job.Day), Evidence: receipt.Evidence, Raw: raw})
}

func run(planPath, archive, out string, prepare bool) error {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	plan, err := ingest.Parse(raw)
	if err != nil {
		return err
	}
	if prepare {
		fmt.Printf("%d项日表归档清单校验通过；请求0\n", len(plan.Jobs))
		return nil
	}
	var all []map[string]any
	for _, job := range plan.Jobs {
		body, err := os.ReadFile(filepath.Join(archive, "originals", job.ID+".body"))
		if err != nil {
			return err
		}
		meta, err := os.ReadFile(filepath.Join(archive, "originals", job.ID+".json"))
		if err != nil {
			return err
		}
		var receipt batch.Receipt
		if err = json.Unmarshal(meta, &receipt); err != nil {
			return err
		}
		rows, err := parseReceived(job, body, receipt)
		if err != nil {
			return err
		}
		for _, row := range rows {
			all = append(all, map[string]any{"document_id": job.ID, "record": row})
		}
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	for _, row := range all {
		if err = encoder.Encode(row); err != nil {
			f.Close()
			return err
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	encoded, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(encoded)
	fmt.Printf("%d合约日；records SHA256 %s；仅原字段，不认证全历史资格\n", len(all), hex.EncodeToString(sum[:]))
	return nil
}

func main() {
	plan := flag.String("plan", "", "冻结归档清单")
	archive := flag.String("archive", "", "已有归档目录")
	out := flag.String("out", "", "全新仓外JSONL")
	prepare := flag.Bool("prepare", false, "仅离线校清单，零请求")
	flag.Parse()
	if err := run(*plan, *archive, *out, *prepare); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
