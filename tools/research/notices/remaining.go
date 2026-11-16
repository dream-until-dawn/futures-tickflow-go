package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// remainingPlan 只接受这次已取得的两份原字节；原回执/启动标记均不改写。
func remainingPlan(p plan, root string) (plan, error) {
	raw, err := os.ReadFile(filepath.Join(root, "results.json"))
	if err != nil {
		return plan{}, err
	}
	var prior []observation
	if err = json.Unmarshal(raw, &prior); err != nil {
		return plan{}, err
	}
	bodies := make([][]byte, 2)
	for i := range bodies {
		bodies[i], err = os.ReadFile(filepath.Join(root, fmt.Sprintf("%02d.body", i+1)))
		if err != nil {
			return plan{}, err
		}
	}
	expected := []string{"7fbbd10ec4ad0b48abd4788f51b1b40e2160772c11ef9caab14eed42e37960f9", "37bf7491df08305c47ddd89789394ec0c539296a343cf18f23d27434383058f9"}
	return selectRemaining(p, raw, prior, bodies, expected)
}

func selectRemaining(p plan, raw []byte, prior []observation, bodies [][]byte, expected []string) (plan, error) {
	if len(prior) != 6 || len(bodies) != 2 || len(expected) != 2 {
		return plan{}, fmt.Errorf("旧批次集合不符")
	}
	for i, r := range prior {
		if r.URL != targets[i] {
			return plan{}, fmt.Errorf("旧URL集合漂移")
		}
		if i >= 2 {
			if r.HTTP.Attempted || r.HTTP.ResponseReceived || r.State != "blocked_after_host_failure" {
				return plan{}, fmt.Errorf("后四项并非从未请求")
			}
			continue
		}
		if !r.HTTP.Attempted || !r.HTTP.Complete || r.HTTP.Status != 200 || r.HTTP.Failure != "" || r.HTTP.FinalURL != targets[i] || r.SHA != expected[i] || digest(bodies[i]) != expected[i] || !contentOK(i, bodies[i]) {
			return plan{}, fmt.Errorf("前两项不是已核本地文字误判，禁止续取")
		}
	}
	if prior[0].State != "received_original_scope_unreviewed" || prior[1].State != "failed" || prior[1].Error != "expected business text absent; not a verified document" {
		return plan{}, fmt.Errorf("不属于这次已核本地误判")
	}
	p.URLs, p.StartIndex, p.PriorSHA = targets[2:], 2, digest(raw)
	return p, nil
}
