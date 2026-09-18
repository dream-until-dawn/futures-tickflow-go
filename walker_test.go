package tickflow_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// BarWalker 契约（walker.go 注释）各条的测试，全部**经由接口**调用 —— 证明 Feed 拿到的是同一个行为，
// 不是某个只在具体类型上才成立的东西。segfile 包里另有同名语义的测试（它是本库唯一的实现）：
//
//	前置 · 顺序        store/segfile/walk_test.go TestWalkRangeAndOrder
//	先核 · 作废        TestWalkChecksBeforeDeliveringAndErrorVoidsCallbacks
//	停                  TestWalkStopEarlyStillConcludes
//	先核（全库）        TestWalkAgreesWithVerifyCoverage
//
// ⚠️ 契约里「代价」「并发」两条是给实现的**许可**（可以没有 seek 索引、可以不并发安全），
// 不是实现必须有的行为 ⇒ 没有能让它们变红的输入，这里不配测试 —— 写明，免得读者以为有人守着。

var walkerDays = []tickflow.TradingDay{20200805, 20200806, 20200807, 20200812, 20200813, 20200814}

var walkerKey = tickflow.ProductKey{Exchange: "SHFE", Product: "rb"}

func walkerBar(d tickflow.TradingDay) tickflow.Bar {
	return tickflow.Bar{Ts: int64(d), TsEnd: int64(d) + 60000, TradingDay: d, Close: 1.5}
}

// walkerLib 造一个两段的日线库：[0805, 0806] 与 [0812, 0813]，每段两根。
func walkerLib(t *testing.T) (*segfile.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, _, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	cal, err := embedded.New(walkerDays)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range [][2]tickflow.TradingDay{{walkerDays[0], walkerDays[1]}, {walkerDays[3], walkerDays[4]}} {
		if err := s.AppendBars([]tickflow.Bar{walkerBar(sp[0]), walkerBar(sp[1])}); err != nil {
			t.Fatal(err)
		}
		if err := s.CommitSpan(cal, walkerKey, tickflow.Span{From: sp[0], To: sp[1], Bars: 2, Days: 2}, tickflow.OutcomeComplete); err != nil {
			t.Fatal(err)
		}
	}
	return s, dir
}

// guard: 前置 —— 经由接口调 Walk，区间不在任何一段 coverage 里（空库，以及跨过两段之间的空档）⇒ 报错，一根都不回调。
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
	if !errors.Is(err, tickflow.ErrWalkOutsideCoverage) {
		t.Fatalf("空库上 Walk 一个区间：err=%v，应 errors.Is ErrWalkOutsideCoverage —— 契约「前置」", err)
	}
	if called != 0 {
		t.Errorf("报错了还回调了 %d 根", called)
	}

	lib, _ := walkerLib(t)
	defer lib.Close()
	w = lib
	called = 0
	if err := w.Walk(walkerDays[1], walkerDays[3], func(tickflow.Bar) bool { called++; return true }); !errors.Is(err, tickflow.ErrWalkOutsideCoverage) {
		t.Errorf("跨过两段之间空档的区间：err=%v，应 errors.Is ErrWalkOutsideCoverage —— 空档是「没拉过」", err)
	}
	if called != 0 {
		t.Errorf("跨空档报错了还回调了 %d 根", called)
	}
	// 对照：同一个库上，整个落在一段里的区间不报错、回调 2 根（否则上面的「报错」可能只是库坏了）
	called = 0
	if err := w.Walk(walkerDays[0], walkerDays[1], func(tickflow.Bar) bool { called++; return true }); err != nil || called != 2 {
		t.Fatalf("对照失败：段内区间 (err=%v, 回调 %d)，期望 (nil, 2)", err, called)
	}
}

// guard: 先核（全库）—— 坏记录在【另一段】里，而 Walk 的区间整个落在好的那一段里 ⇒ 仍然报错（契约：任何一处坏，对任何区间都可以报错）。
func TestBarWalkerChecksWholeLibraryBeforeDelivering(t *testing.T) {
	lib, dir := walkerLib(t)
	lib.Close()
	// 第二段的最后一条（索引 3）改成交易日倒退回第一段 ⇒ 顺序坏；它不在 [0805, 0806] 里
	var dat string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".dat") {
			dat = p
		}
		return nil
	})
	if dat == "" {
		t.Fatal("没找到 .dat —— 构造作废")
	}
	f, err := os.OpenFile(dat, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	rec := segfile.EncodeBar(walkerBar(walkerDays[0]))
	if _, err := f.WriteAt(rec[:], 3*segfile.RecordSize); err != nil {
		t.Fatal(err)
	}
	f.Close()

	store, truncated, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if truncated != 0 {
		t.Fatalf("前提没成立：重开截掉了 %d 字节 ⇒ 坏记录没留住", truncated)
	}
	var w tickflow.BarWalker = store
	err = w.Walk(walkerDays[0], walkerDays[1], func(tickflow.Bar) bool { return true })
	if err == nil {
		t.Fatalf("另一段里有坏记录，而 Walk 好的那一段没有报错 —— 契约「先核」：任何一处坏，对任何区间都可以报错")
	}
	// 两种状态不许共用一个判断：坏了 ≠ 没拉过
	if errors.Is(err, tickflow.ErrWalkOutsideCoverage) {
		t.Errorf("数据坏了的错误 Is 了 ErrWalkOutsideCoverage：%v —— 消费方会把「坏了」当成「没拉过」去重拉", err)
	}
}

// guard: 停 —— fn 第一根就返回 false ⇒ 只停回调，结论照给（健康库 ⇒ nil）。
func TestBarWalkerStopEarlyStillConcludes(t *testing.T) {
	lib, _ := walkerLib(t)
	defer lib.Close()
	var w tickflow.BarWalker = lib
	called := 0
	err := w.Walk(walkerDays[0], walkerDays[1], func(tickflow.Bar) bool { called++; return false })
	if err != nil || called != 1 {
		t.Errorf("fn 第一根返回 false：(err=%v, 回调 %d)，期望 (nil, 1) —— 停只停回调，结论照给", err, called)
	}
}
