package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSinaHTTPOutputStaysOutsideRepository(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executionRoot(filepath.Join(cwd, "output")); err == nil {
		t.Fatal("仓内输出竟通过")
	}
	if _, err = executionRoot(filepath.Join(t.TempDir(), "new-execution")); err != nil {
		t.Fatal(err)
	}
}

func TestSinaHTTPExclusiveMarkerNeverOverwrites(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started.json")
	if err := writeNew(marker, []byte("unknown original")); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(marker, []byte("replacement")); !os.IsExist(err) {
		t.Fatalf("独占标记未拒绝重复: %v", err)
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "unknown original" {
		t.Fatal("旧启动标记被改写", err)
	}
}

func TestSinaHTTPPreparationBindsBinaryAndOriginalReceipt(t *testing.T) {
	source := "../../../docs/evidence/sina-history-html-20261115"
	plan, _, err := makePlan(source, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := planBytes(plan)
	root := filepath.Join(t.TempDir(), "execution")
	if err = prepare(root, raw); err != nil {
		t.Fatal(err)
	}
	if err = prepare(root, raw); err == nil {
		t.Fatal("重复准备竟覆盖")
	}
	changed := plan
	changed.BinarySHA = strings.Repeat("2", 64)
	other, _ := planBytes(changed)
	called := false
	if err = begin(root, other, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("换二进制计划竟可执行")
	}
	for _, name := range []string{"history.jsonp", "receipt.json"} {
		dir := t.TempDir()
		for _, file := range []string{"history.jsonp", "receipt.json"} {
			data, err := os.ReadFile(filepath.Join(source, file))
			if err != nil {
				t.Fatal(err)
			}
			if file == name {
				data = append(data, 'x')
			}
			if err = os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err = makePlan(dir, strings.Repeat("1", 64)); err == nil {
			t.Fatal("坏出处或收据通过")
		}
	}
}

func TestSinaHTTPPersistentStartRejectsUnknownAndRepeat(t *testing.T) {
	root := filepath.Join(t.TempDir(), "execution")
	plan := []byte("frozen fixture\n")
	if err := prepare(root, plan); err != nil {
		t.Fatal(err)
	}
	calls := 0
	action := func() error {
		calls++
		if _, err := os.Stat(filepath.Join(root, "started.json")); err != nil {
			t.Fatal("启动标记未先持久化")
		}
		return nil
	}
	if err := begin(root, plan, action); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if err := begin(root, plan, action); err == nil || calls != 1 {
		t.Fatal("重复启动未拒", err, calls)
	}
	for _, name := range []string{"started.json", "archive", "result.json", "unexpected"} {
		other := filepath.Join(t.TempDir(), "execution")
		if err := prepare(other, plan); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, name), []byte("unknown partial state"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := begin(other, plan, func() error { t.Fatal("未知启动重发"); return nil }); err == nil {
			t.Fatal("未知状态通过", name)
		}
	}
}

func TestSinaHTTPFailedActionRemainsConsumed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "execution")
	plan := []byte("fixture\n")
	if err := prepare(root, plan); err != nil {
		t.Fatal(err)
	}
	calls := 0
	action := func() error { calls++; return os.ErrPermission }
	if err := begin(root, plan, action); err == nil {
		t.Fatal("失败竟报成功")
	}
	if err := begin(root, plan, action); err == nil || calls != 1 {
		t.Fatal("失败后重发", err, calls)
	}
}
