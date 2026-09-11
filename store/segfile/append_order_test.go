package segfile

import (
	"errors"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— v0.4.1 补丁的守卫 ——
//
// ⛔ **它修的不是一个理论风险，是 v0.4.0 里一个用【最平常的动作】就能触发的数据损坏。**
// 双方各自独立复现过；在 `v0.4.0`（`f218ae6a`）那一颗上实测的读数是：
//
//	第一次 Sync              ok · `.dat` 176 字节 · Verify 绿
//	**第二次（同一条命令）**  err「coverage 有重叠段」· `.dat` **352 字节** · Verify **红**
//	第三次（照文档「走一遍就行」）        `.dat` **528 字节** · Verify 仍红
//
// 🔴 三件事同时成立：
//
//	一、`rep.Bars = 0` —— **报告说没写，而盘上写了**（`rep.Bars += len(bars)`
//	   在 `CommitSpan` 成功之后才走得到，而 `AppendBars` 在它之前）
//	二、那一段从此**走查不过**，而产品内**没有任何恢复路** ——
//	   `DiscardCoverage` 不碰 `.dat`，而全包唯一的 `Truncate(` 只截**不足一条**的残尾
//	   ⇒ 只能手工删文件
//	三、而文档那句「走一遍就行」**照做会加重**（上面那串字节数就是照它做出来的）
//
// ⇒ 所以闸门放在**写入口**是唯一对的位置：**事后没有工具。**
//
// ⚠️ 触发条件要写准：**不是「倒填」，是「取回的记录不严格晚于盘上已有的」** ——
// 而「把同一条命令再跑一遍」是其中最平常的那一种。
// 📎 对照（第一遍 `[0805..0806]`、第二遍 `[0807..0810]`，不相交且更晚）⇒ `err=nil`、Verify 绿。

// guard: AppendBars 拒绝倒退的交易日 —— 让【走查过的段】此后不再变乱。
func TestAppendBarsRejectsOutOfOrder(t *testing.T) {
	bar := func(d tickflow.TradingDay, ts int64) tickflow.Bar {
		return tickflow.Bar{Ts: ts, TsEnd: ts + 1, TradingDay: d,
			Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	}
	count := func(t *testing.T, s *Store) int64 {
		t.Helper()
		// ⚠️ 不走 `CountRecords` —— 它是被测包自己的代码（里面还有残尾判断那一段），
		// 拿 X 当前提去验 X。独立的那一版只要一行、零逻辑、免费。
		st, err := s.dat.Stat()
		if err != nil {
			t.Fatal(err)
		}
		return st.Size() / RecordSize
	}

	t.Run("与盘上已有的记录倒退", func(t *testing.T) {
		s, _, _ := newStore(t)
		if err := s.AppendBars([]tickflow.Bar{bar(20200805, 1), bar(20200807, 3)}); err != nil {
			t.Fatalf("有序的那一批该收：%v", err)
		}
		before := count(t, s)
		err := s.AppendBars([]tickflow.Bar{bar(20200806, 2)})
		if !errors.Is(err, errOutOfOrder) {
			t.Fatalf("倒退的那一批给的是 %v，而该给 errOutOfOrder", err)
		}
		// ⛔ **拒绝必须发生在【写之前】** —— 一个「先写、再报错」的实现会让上面那句照绿，
		// 而那条记录**已经在盘上了** ⇒ 窗口原封不动。
		if after := count(t, s); after != before {
			t.Errorf("被拒之后记录数从 %d 变成了 %d —— 那一批【已经写进去了】", before, after)
		}
	})

	t.Run("这一批内部倒退", func(t *testing.T) {
		s, _, _ := newStore(t)
		if err := s.AppendBars([]tickflow.Bar{bar(20200807, 3), bar(20200805, 1)}); !errors.Is(err, errOutOfOrder) {
			t.Fatalf("批内倒退给的是 %v，而该给 errOutOfOrder", err)
		}
	})

	t.Run("同一天重复、相等、空库首批，都该收", func(t *testing.T) {
		// ⛔ 这一格是**误伤面**：非降序**允许相等**。
		// 少了它，「同一天两条」会被这道新检查拦掉 —— 而那是合法且常见的
		// （实测：把判据写成严格递增 ⇒ 本包另有三条既有测试当场红）。
		s, _, _ := newStore(t)
		if err := s.AppendBars([]tickflow.Bar{bar(20200805, 1), bar(20200805, 2)}); err != nil {
			t.Fatalf("同一天两条该收：%v", err)
		}
		if err := s.AppendBars([]tickflow.Bar{bar(20200805, 3)}); err != nil {
			t.Fatalf("与盘上最后一条【相等】该收：%v", err)
		}
	})
}

// guard: 那个窗口关上了 —— 走查过之后，倒退的记录再也进不来。
//
// ⛔ 它与上面那条**不是同一件事**，差别要写清楚，否则下一个人会把它当重复删掉：
//
//	上面那条  问「倒退的那一批会不会被拒」              ← 一次调用的行为
//	这一条    问「**走查过之后，文件还能不能变成乱序**」← 一个**不变量**
//
// 🔴 而实测过的那个窗口正是从**走查之后**开始的 —— 所以这条测试的构造必须先走查。
func TestSortednessHoldsAfterVerify(t *testing.T) {
	s, cal, k := newStore(t)
	bar := func(d tickflow.TradingDay, ts int64) tickflow.Bar {
		return tickflow.Bar{Ts: ts, TsEnd: ts + 1, TradingDay: d,
			Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	}
	if err := s.AppendBars([]tickflow.Bar{bar(20200805, 1), bar(20200807, 3)}); err != nil {
		t.Fatal(err)
	}
	sp := tickflow.Span{From: 20200805, To: 20200807, Bars: 2, Days: 2}
	if err := s.CommitSpan(cal, k, sp, OutcomeComplete); err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	cov := s.Coverage()
	if len(cov) != 1 {
		t.Fatalf("期望一段 coverage，实得 %v —— 构造不成立", cov)
	}
	sp = cov[0] // ⚠️ 读回来，别复用我给出去的那个值：CommitSpan 会规整它
	if err := s.Verify(sp); err != nil {
		t.Fatalf("走查该过：%v —— 前提不成立，这一组读数作废", err)
	}

	// ⛔ 窗口的入口：走查过了，`verified` 为 true。此刻再写一条倒退的。
	if err := s.AppendBars([]tickflow.Bar{bar(20200806, 2)}); !errors.Is(err, errOutOfOrder) {
		t.Fatalf("走查之后仍然写得进倒退的记录（err=%v）——那个窗口还开着", err)
	}

	// ⛔ 承重的那一句：**重新走查必须仍然绿**。
	// 🔴 少了它，一个「把 AppendBars 改成永远返回 errOutOfOrder」的实现也能过上面那条 ——
	// 而那种实现让整个库不可写。**拒绝要拒对，不是拒得多。**
	if err := s.Verify(sp); err != nil {
		t.Fatalf("那条倒退的记录被拒之后，重新走查却红了：%v", err)
	}
}
