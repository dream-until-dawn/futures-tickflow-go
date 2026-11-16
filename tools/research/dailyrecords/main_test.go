package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestDailyRecordExportRejectsUnreceivedAndTamperedOriginals(t *testing.T) {
	raw := []byte("郑州商品交易所期货每日行情表(2026-11-16)\n合约代码|今开盘|最高价|最低价|今收盘|今结算|昨结算|成交量(手)|持仓量|成交额(万元)\nRM701|1|2|1|2|2|1|3|4|5\n")
	sum := sha256.Sum256(raw)
	job := batch.Job{URL: "https://www.czce.com.cn/test", Format: "czce-txt", Day: 20261116}
	receipt := batch.Receipt{State: "received", BodyState: "complete", Evidence: evidence.Ref{ID: "test", URL: job.URL, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now().UTC()}, HTTP: &batch.Download{Status: 200, Complete: true}}
	rows, err := parseReceived(job, raw, receipt)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	bad := append([]byte{}, raw...)
	bad[len(bad)-2] = '6'
	if _, err = parseReceived(job, bad, receipt); err == nil {
		t.Fatal("原字节篡改未拒绝")
	}
	receipt.State = "failed"
	if _, err = parseReceived(job, raw, receipt); err == nil {
		t.Fatal("失败回执被当行情")
	}
}
