package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSessionEndReadout(t *testing.T) {
	closeMs := time.Date(2026, 10, 23, 23, 0, 0, 0, cst).UnixMilli()
	build := func(late bool) []tailFrame {
		fr := synthTail(closeMs-5*60000, 5, map[int64]bool{4: late}, map[int64]bool{4: true})
		return append(fr, tailFrame{RecvMs: closeMs + 10000, Raw: json.RawMessage(`{"aid":"rtn_data","data":[]}`)})
	}
	r, e := inspectSessionEnd(build(false), tailSym, closeMs)
	if e != nil || r.last != closeMs-1000 || r.repeats != 1 || r.preceding != 4 {
		t.Fatalf("基线 %+v / %v", r, e)
	}
	late := build(true)
	late[len(late)-2].RecvMs = closeMs + 5000
	r, e = inspectSessionEnd(late, tailSym, closeMs)
	if e != nil || r.last-closeMs != 5000 {
		t.Fatalf("超过四秒的修改未计入：%+v / %v", r, e)
	}
	for _, name := range []string{"missing-last", "first-after-close", "reversed", "no-close-coverage", "malformed", "historical-only"} {
		t.Run(name, func(t *testing.T) {
			fr := build(false)
			switch name {
			case "missing-last":
				fr = append(fr[:8], fr[len(fr)-1])
			case "first-after-close":
				for i := range fr {
					if fr[i].RecvMs >= closeMs-60000 && fr[i].RecvMs < closeMs {
						fr[i].RecvMs += 60000
					}
				}
				fr = fr[:len(fr)-1]
			case "reversed":
				fr[1], fr[2] = fr[2], fr[1]
			case "no-close-coverage":
				fr = fr[:len(fr)-1]
			case "malformed":
				fr[0].Raw = json.RawMessage(`{`)
			case "historical-only":
				for i := range fr {
					fr[i].RecvMs = closeMs - 500
				}
				fr = append(fr, tailFrame{RecvMs: closeMs + 10000, Raw: json.RawMessage(`{"aid":"rtn_data","data":[]}`)})
			}
			if _, e := inspectSessionEnd(fr, tailSym, closeMs); e == nil {
				t.Fatal("无效样本未拒绝")
			}
		})
	}
}
