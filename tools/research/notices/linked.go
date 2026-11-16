package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// linkedPlan 只取目录已经实际列出的一个新原件，不改变原六项的启动记录。
func linkedPlan(p plan, original, remaining string) (plan, error) {
	body, err := os.ReadFile(filepath.Join(original, "01.body"))
	if err != nil {
		return plan{}, err
	}
	raw, err := os.ReadFile(filepath.Join(remaining, "results.json"))
	if err != nil {
		return plan{}, err
	}
	var prior []observation
	if err = json.Unmarshal(raw, &prior); err != nil {
		return plan{}, err
	}
	for i, row := range prior {
		part, err := os.ReadFile(filepath.Join(remaining, fmt.Sprintf("%02d.body", i+3)))
		if err != nil || digest(part) != row.SHA {
			return plan{}, fmt.Errorf("同主机原件字节未核")
		}
	}
	return selectLinked(p, body, raw, prior, "7fbbd10ec4ad0b48abd4788f51b1b40e2160772c11ef9caab14eed42e37960f9")
}

func selectLinked(p plan, index, raw []byte, prior []observation, expected string) (plan, error) {
	if digest(index) != expected || !bytes.Contains(index, []byte("20261116_834086.html")) {
		return plan{}, fmt.Errorf("新链接没有已核原目录依据")
	}
	if len(prior) != 4 {
		return plan{}, fmt.Errorf("同主机剩余批次未完成")
	}
	for i, row := range prior {
		if row.URL != targets[i+2] || row.State != "received_original_scope_unreviewed" || !row.HTTP.Attempted || !row.HTTP.Complete || row.HTTP.Status != 200 || row.HTTP.Failure != "" {
			return plan{}, fmt.Errorf("同主机仍有真实失败，禁止新请求")
		}
	}
	p.URLs, p.StartIndex, p.PriorSHA = targets[6:], 6, digest(append(append([]byte{}, index...), raw...))
	return p, nil
}
