package catalog

import "fmt"

// Merge 合并不同日期快照；生命周期矛盾、同日证据冲突均要求人工核查版本。
// 它不以最近一次快照悄悄覆盖旧证据。
func Merge(groups ...[]Entry) ([]Entry, error) {
	by := map[string]Entry{}
	for _, entries := range groups {
		if _, err := New(entries); err != nil {
			return nil, err
		}
		for _, e := range entries {
			key := e.Symbol.String()
			old, ok := by[key]
			if !ok {
				by[key] = copyEntry(e)
				continue
			}
			if (old.Listed.Day != 0 && e.Listed.Day != 0 && old.Listed.Day != e.Listed.Day) || (old.LastTrading.Day != 0 && e.LastTrading.Day != 0 && old.LastTrading.Day != e.LastTrading.Day) || (old.ExpireTs != 0 && e.ExpireTs != 0 && old.ExpireTs != e.ExpireTs) {
				return nil, fmt.Errorf("catalog: 快照生命周期冲突 %s", key)
			}
			if old.ReportedExpiry.Day != 0 && e.ReportedExpiry.Day != 0 && old.ReportedExpiry.Day != e.ReportedExpiry.Day {
				return nil, fmt.Errorf("catalog: 到期原字段冲突")
			}
			if old.ReportedExpiry.Day == 0 {
				old.ReportedExpiry = e.ReportedExpiry
			} else if e.ReportedExpiry.Day != 0 {
				old.ReportedExpiry.Evidence = append(old.ReportedExpiry.Evidence, e.ReportedExpiry.Evidence...)
			}
			if old.Listed.Day == 0 {
				old.Listed = e.Listed
			} else if e.Listed.Day != 0 {
				old.Listed.Evidence = append(old.Listed.Evidence, e.Listed.Evidence...)
			}
			if old.LastTrading.Day == 0 {
				old.LastTrading = e.LastTrading
			} else if e.LastTrading.Day != 0 {
				old.LastTrading.Evidence = append(old.LastTrading.Evidence, e.LastTrading.Evidence...)
			}
			if old.ExpireTs == 0 {
				old.ExpireTs = e.ExpireTs
			}
			if old.Candidate == nil {
				old.Candidate = e.Candidate
			}
			for _, o := range e.Observed {
				duplicate := false
				for _, p := range old.Observed {
					if p.Day == o.Day {
						if p.Evidence != o.Evidence {
							return nil, fmt.Errorf("catalog: 同日观察版本冲突")
						}
						duplicate = true
					}
				}
				if !duplicate {
					old.Observed = append(old.Observed, o)
				}
			}
			by[key] = old
		}
	}
	out := make([]Entry, 0, len(by))
	for _, e := range by {
		out = append(out, e)
	}
	c, err := New(out)
	if err != nil {
		return nil, err
	}
	return c.Entries(), nil
}
