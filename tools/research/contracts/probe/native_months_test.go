package main

import (
	"path/filepath"
	"testing"
)

func TestCFFEXRemainingMonthsExcludeConsumedAndPartialMonth(t *testing.T) {
	file := filepath.Join("..", "..", "..", "..", "docs", "evidence", "historical-contract-directory-20261115", "cffex-remaining-months-candidate.json")
	jobs, e := remainingMonths(file)
	if e != nil || len(jobs) != 69 {
		t.Fatal(jobs, e)
	}
	if jobs[0].Month != "202102" || jobs[len(jobs)-1].Month != "202610" {
		t.Fatal("月份边界漂移", jobs)
	}
	for _, url := range []string{"http://www.cffex.com.cn/sj/historysj/202611/zip/202611.zip", "http://user@www.cffex.com.cn/sj/historysj/202101/zip/202101.zip", "http://www.cffex.com.cn/sj/historysj/202101/zip/202102.zip"} {
		if _, e = nativeMonth(url); e == nil {
			t.Fatal("越界URL未拒", url)
		}
	}
}
