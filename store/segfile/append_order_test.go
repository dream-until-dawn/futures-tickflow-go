package segfile

import (
	"errors"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 这一组守的是一个【不变量的连续性】，不是一条错误信息 ——
//
// ⛔ 由来是一次实测（2026-09-11，我造的探针，评审方提二分那条排期时我先去核前提）：
//
//	Verify 检查「记录的交易日非降序」，而 `verified[span]` 此后一直为真
//	AppendBars **一个字都不查顺序**（我读了它的全部内容）
//	⇒ 一次合法的追加就能让文件不再有序，而**直到下一次走查都没人会响**
//
// 实测那个窗口（同一份库，四步）：
//
//	① Verify 过 ⇒ verified = true，文件有序
//	② AppendBars 收下一条倒退的记录 ⇒ **没有任何拒绝**
//	③ DaysWithBars 仍给出【正确】答案 —— 线性扫描对乱序免疫
//	④ 重新走查才红：「记录的交易日不是非降序」—— 而那是**下一次 Sync** 的事
//
// 🔴 ⇒ 这个窗口**今天没有伤到任何人**，它伤的是**将来**：
// 任何「利用有序性」的读法（二分、只扫段对应的那一段）在窗口里会**静默给出错的答案**。
// 实测：同一份库上并排跑，线性答 `{0805, 0806}`（对），二分答 `{0805}`（**漏报 0806**）。
//
// ⚠️ 而那个错的**方向要说准**（我第一次说重了，改在这里）：二分只会**漏报存在**
// ⇒ 那一天被判成 `GapConfirmedEmpty` ⇒ **重复拉取（吵，不丢数据）**，
// **不是**「静默漏数据」那一类。⇒ 它不足以否掉二分，**而它是二分要先付的那笔账**。

// recordCount 直接从文件大小算记录数。
//
// ⚠️ **不走 `CountRecords`** —— 它是被测包自己的代码（里面还有残尾判断那一段），
// 拿 X 当前提去验 X。本仓在 `read_shape_bench_test.go` 里为同一件事写过同一句话：
// **独立的那一版只要一行、零逻辑、免费 —— 既然免费，就不该停在「可接受」上。**
func recordCount(t *testing.T, s *Store) int64 {
	t.Helper()
	st, err := s.dat.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return st.Size() / RecordSize
}

// guard: AppendBars 拒绝倒退的交易日 —— 把有序性从「某一刻检查过」变成「一直成立」。
// TestAppendBarsRejectsOutOfOrder 是那道拦在写入口的检查。
func TestAppendBarsRejectsOutOfOrder(t *testing.T) {
	bar := func(d tickflow.TradingDay, ts int64) tickflow.Bar {
		return tickflow.Bar{Ts: ts, TsEnd: ts + 1, TradingDay: d,
			Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	}

	t.Run("与盘上已有的记录倒退", func(t *testing.T) {
		s, _, err := Open(t.TempDir(), tickflow.Daily)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })

		if err := s.AppendBars([]tickflow.Bar{bar(20200805, 1), bar(20200807, 3)}); err != nil {
			t.Fatalf("有序的那一批该收：%v", err)
		}
		before := recordCount(t, s)
		err = s.AppendBars([]tickflow.Bar{bar(20200806, 2)})
		if !errors.Is(err, ErrOutOfOrder) {
			t.Fatalf("倒退的那一批给的是 %v，而该给 ErrOutOfOrder\n"+
				"  ⇒ 不拦的话它要到【下一次走查】才红，而那时报文只会指向文件，指不回写它的那次调用", err)
		}
		// ⛔ **拒绝必须发生在【写之前】** —— 而这一句是直接量的，不是靠走查推的。
		// 🔴 一个「先写、再报错」的实现会让上面那条断言照绿，
		// 而那条倒退的记录**已经在盘上了** ⇒ 窗口原封不动。
		if after := recordCount(t, s); after != before {
			t.Errorf("被拒之后记录数从 %d 变成了 %d —— 那一批【已经写进去了】，"+
				"这次拒绝只是一句事后的话", before, after)
		}
	})

	t.Run("这一批内部倒退", func(t *testing.T) {
		s, _, err := Open(t.TempDir(), tickflow.Daily)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })

		err = s.AppendBars([]tickflow.Bar{bar(20200807, 3), bar(20200805, 1)})
		if !errors.Is(err, ErrOutOfOrder) {
			t.Fatalf("批内倒退给的是 %v，而该给 ErrOutOfOrder", err)
		}
	})

	t.Run("同一天重复、以及空库第一批，都该收", func(t *testing.T) {
		s, _, err := Open(t.TempDir(), tickflow.Daily)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })

		// ⛔ 这一格是**误伤面**：非降序允许相等。
		// 少了它，「同一天两条」会被这道新检查拦掉 —— 而那是合法且常见的。
		if err := s.AppendBars([]tickflow.Bar{bar(20200805, 1), bar(20200805, 2)}); err != nil {
			t.Fatalf("同一天两条该收（非降序允许相等）：%v", err)
		}
		if err := s.AppendBars([]tickflow.Bar{bar(20200805, 3)}); err != nil {
			t.Fatalf("与盘上最后一条【相等】该收：%v", err)
		}
	})
}

// guard: 那个窗口关上了 —— 走查过之后，倒退的记录再也进不来。
// TestSortednessHoldsAfterVerify 钉的是【连续性】本身，不是那条错误信息。
//
// ⛔ 它与上面那条**不是同一件事**，差别要写清楚，否则下一个人会把它当重复删掉：
//
//	上面那条  问「倒退的那一批会不会被拒」            ← 一次调用的行为
//	这一条    问「**走查过之后，文件还能不能变成乱序**」← 一个**不变量**
//
// ⇒ 后者才是二分那类读法要的东西；而前者是它的实现手段。
// 🔴 而实测过的那个窗口正是从**走查之后**开始的 —— 所以这条测试的构造必须先走查。
func TestSortednessHoldsAfterVerify(t *testing.T) {
	cal := testCal(t)
	s, _, err := Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bar := func(d tickflow.TradingDay, ts int64) tickflow.Bar {
		return tickflow.Bar{Ts: ts, TsEnd: ts + 1, TradingDay: d,
			Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	}
	if err := s.AppendBars([]tickflow.Bar{bar(20200805, 1), bar(20200807, 3)}); err != nil {
		t.Fatal(err)
	}
	sp := tickflow.Span{From: 20200805, To: 20200807, Bars: 2, Days: 2}
	if err := s.CommitSpan(cal, eqKey, sp, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	cov := s.Coverage()
	if len(cov) != 1 {
		t.Fatalf("期望一段，实得 %v —— 构造不成立", cov)
	}
	sp = cov[0]
	if err := s.Verify(sp); err != nil {
		t.Fatalf("走查该过：%v —— 前提不成立，这一组读数作废", err)
	}

	// ⛔ 窗口的入口：走查过了，`verified` 为 true。此刻再写一条倒退的。
	if err := s.AppendBars([]tickflow.Bar{bar(20200806, 2)}); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("走查之后仍然写得进倒退的记录（err=%v）——那个窗口还开着。\n"+
			"  ⇒ 它今天不伤人（线性扫描对乱序免疫），而任何利用有序性的读法\n"+
			"     在这个窗口里会【静默】给出错的答案。", err)
	}

	// ⛔ 承重的那一句：**重新走查必须仍然绿**。
	// 🔴 少了它，一个「把 AppendBars 改成永远返回 ErrOutOfOrder」的实现也能过上面那条 ——
	// 而那种实现让整个库不可写。**拒绝要拒对，不是拒得多。**
	if err := s.Verify(sp); err != nil {
		t.Fatalf("那条倒退的记录被拒之后，重新走查却红了：%v\n"+
			"  ⇒ 说明有别的东西被写进去了，或者那次拒绝是在【写完之后】发生的", err)
	}
}
