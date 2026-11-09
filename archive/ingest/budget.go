package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

// 恢复时扣除旧尝试及已读取正文；未知网络执行不能假定零消耗。
func priorUsage(out string, jobs []batch.Job) (requests int, bodyBytes int64, pending bool, unknown bool, err error) {
	for _, j := range jobs {
		if j.Local != "" || j.Blocked != "" {
			continue
		}
		path := filepath.Join(out, "originals", j.ID+".json")
		raw, e := os.ReadFile(path)
		if os.IsNotExist(e) {
			pending = true
			continue
		}
		if e != nil {
			return 0, 0, false, false, e
		}
		if len(raw) > 1<<20 {
			return 0, 0, false, false, fmt.Errorf("ingest: 收据超预算")
		}
		var r batch.Receipt
		if e = json.Unmarshal(raw, &r); e != nil {
			return 0, 0, false, false, e
		}
		if r.State == "in_progress" {
			unknown = true
		}
		if r.HTTP != nil && r.HTTP.Attempted {
			requests++
			if r.Bytes < 0 {
				return 0, 0, false, false, fmt.Errorf("ingest: 负正文计数")
			}
			bodyBytes += r.Bytes
		}
		if r.HTTP != nil && r.HTTP.ResponseReceived && !r.HTTP.Attempted {
			return 0, 0, false, false, fmt.Errorf("ingest: 响应身份与尝试计数冲突")
		}
	}
	return
}
