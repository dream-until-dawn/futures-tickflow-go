package tickflow_test

import (
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// BarWalker 契约的各条由 segfile 的测试钉着（它是本库唯一的实现）：
//
//	前置 · 顺序        store/segfile/walk_test.go TestWalkRangeAndOrder
//	先核 · 作废        TestWalkChecksBeforeDeliveringAndErrorVoidsCallbacks
//	停                  TestWalkStopEarlyStillConcludes
//	先核（全库）        TestWalkAgreesWithVerifyCoverage
//
// 这里只做一件 segfile 包里做不到的事：**经由接口**调用 —— 证明 Feed 拿到的是同一个行为，
// 不是某个只在具体类型上才成立的东西。

// guard: 经由 tickflow.BarWalker 调 Walk，区间不在任何一段 coverage 里 ⇒ 报错，一根都不回调。
func TestBarWalkerRejectsRangeOutsideCoverage(t *testing.T) {
	store, _, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var w tickflow.BarWalker = store
	if got := len(w.Coverage()); got != 0 {
		t.Fatalf("空库 Coverage 有 %d 段，期望 0", got)
	}
	called := 0
	err = w.Walk(20260105, 20260109, func(tickflow.Bar) bool { called++; return true })
	if err == nil {
		t.Fatalf("空库上 Walk 一个区间没有报错 —— 契约「前置」要求区间整个落在某一段 coverage 里")
	}
	if called != 0 {
		t.Errorf("报错了还回调了 %d 根", called)
	}
}
