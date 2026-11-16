package main

import (
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"testing"
)

func TestNoticeNewLinkedURLRequiresOriginalAndHealthyHost(t *testing.T) {
	index := []byte(`<html><a href="./202611/t20261116_834086.html">监管公告</a></html>`)
	prior := make([]observation, 4)
	for i := range prior {
		prior[i] = observation{URL: targets[i+2], State: "received_original_scope_unreviewed", HTTP: batch.Download{Attempted: true, Complete: true, Status: 200}}
	}
	p, _ := makePlan([]byte("binary"), []byte(`{"urls":[]}`))
	next, err := selectLinked(p, index, []byte("receipts"), prior, digest(index))
	if err != nil || len(next.URLs) != 1 || next.URLs[0] != targets[6] || next.StartIndex != 6 {
		t.Fatal(err)
	}
	prior[0].HTTP.Status = 412
	if _, err = selectLinked(p, index, nil, prior, digest(index)); err == nil {
		t.Fatal("真实失败后同主机仍发新请求")
	}
	prior[0].HTTP.Status = 200
	if _, err = selectLinked(p, index, nil, prior, "wrong"); err == nil {
		t.Fatal("原目录SHA漂移未拒绝")
	}
	if _, err = selectLinked(p, []byte("no link"), nil, prior, digest([]byte("no link"))); err == nil {
		t.Fatal("盲猜新URL被放行")
	}
}
