package segfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 这一条守的是 `VerifyCoverage` 的【契约三条】，以及它与参照实现的【等价性】——
//
// 契约（`store.go` 的接口注释里逐字写着，这里是它的钉子）：
//
//	一、**全的**：`Coverage()` 里每一段都要有一个结论（nil ＝ 通过）
//	二、**键是 `Coverage()` 里每一段的 `Key()`**（＝ `[From, To]`，见根包的 `SpanKey`）
//	三、第二个返回值非 nil 时，第一个返回值是 **nil map**，不是空 map
//
// 🔴 契约二承重：`planGaps` 查的是同一批段。2026-09-11 的生产缺陷正是两边键不同源 ——
// 写入侧用【分块】的 span，读取侧用 `CommitSpan` 并段之后的值，**而两处代码都写着 `verified[sp]`**。
// ⚠️ 那一格差的是**区间本身**（`[d1,d1]` vs `[d1,d3]`），不是 `Bars`/`Days`
// ⇒ 2026-09-12 把键收窄成 `SpanKey` 之后，**它照旧被抓住**（下面「二之二」钉的就是这一点）。
//
// 🔴 契约三的理由是一句可测的话：**`len(m)==0` 对 nil 与空 map 是同一个读数，而 `m == nil` 分得开** ——
// 「量不了」和「量出来每段都没问题」必须分得开。
//
// —— ⚠️ 等价性那两格：`Verify(span)` 是**参照实现**，不是冗余 ——
//
// `VerifyCoverage` 是新读法（一遍扫描、按段分桶），`Verify(span)` 是旧读法（一段一遍全扫）。
// ⛔ 两边都走新代码的话，这条测试是空的 —— 它会断言「新读法等于它自己」。
// 📎 与当年 `HasBars`/`DaysWithBars` 那对同一个先例、同一个理由。
// ⚠️ 而 `Verify(span)` 留着还有第二条理由：**v0.4.1 的 tag 注解逐字要人跑它，而 tag 改不了。**

// guard: VerifyCoverage 的契约三条（全的 · 键同源 · 跑不起来时回 nil map）。
func TestVerifyCoverageContract(t *testing.T) {
	t.Run("一 全的_每一段都有结论", func(t *testing.T) {
		s, _ := twoSpanLib(t)
		cov := s.Coverage()
		// ⛔ 前提自检：真的有两段 —— 一段的库上「全的」这条几乎不可能红。
		if len(cov) != 2 {
			t.Fatalf("前提没成立：期望 2 段，实得 %v ⇒ 读数作废", cov)
		}
		res, err := s.VerifyCoverage()
		if err != nil {
			t.Fatalf("健康库上它跑不起来：%v", err)
		}
		if len(res) != len(cov) {
			t.Fatalf("回了 %d 个结论，而 coverage 有 %d 段 —— 契约一要求【全的】。\n"+
				"  ⇒ 漏掉一段时上层会把它报成「本次没走查过」，而那句话会被读成「不表示这一段有问题」。",
				len(res), len(cov))
		}
		for _, sp := range cov {
			// —— 契约二：键必须是 Coverage() 里那一段的 Key() ——
			verr, ok := res[sp.Key()]
			if !ok {
				t.Fatalf("Coverage() 里的 %v 在结论里查不到 —— 键不同源了。\n"+
					"  ⇒ planGaps 查的正是 Coverage() 这一批段的 Key()。", sp)
			}
			if verr != nil {
				t.Errorf("健康库上 %v 报了：%v", sp, verr)
			}
		}
	})

	// —— ✅ 这两格是 **丙（2026-09-12）翻面之后的样子** ——
	//
	// 上一版这里钉的是「键是**整个结构体**」：一个 `Bars` 不同的自拼 Span **查不到**。
	// 🔴 而那条约定**没有任何人同意过** —— 是 Go 的 `==` 替我们答的，而它答错过两次（(o) 与 (j)）。
	// ⇒ 丙 把身份收窄成 `[From, To]`（`SpanKey`），于是这一格翻成两格：
	//
	//	二之一  `Bars` 不同而区间相同 ⇒ **查得到**（那两个计数是**内容**，不是身份）
	//	二之二  区间不同             ⇒ **查不到**（(o) 那个缺陷靠的正是这一条，它没被削弱）

	t.Run("二之一 Bars 不同而区间相同_查得到", func(t *testing.T) {
		s, _ := twoSpanLib(t)
		res, err := s.VerifyCoverage()
		if err != nil {
			t.Fatalf("跑不起来：%v", err)
		}
		real0 := s.Coverage()[0]
		fake := tickflow.Span{From: real0.From, To: real0.To, Bars: real0.Bars + 97, Days: real0.Days}
		if _, ok := res[fake.Key()]; !ok {
			t.Errorf("区间相同而 Bars 不同的 Span，它的 Key() 查不到 ⇒ 身份又不是 [From,To] 了。\n"+
				"  真值：%v  自拼：%v", real0, fake)
		}
	})

	t.Run("二之二 区间不同_查不到", func(t *testing.T) {
		// ⛔ 这一格钉的是 (o) 那个生产缺陷靠的那一条：
		// 分块的 [d1,d1] 与并段后的 [d1,d3] **区间就不同** ⇒ 键仍然不相等 ⇒ 缺陷仍被抓住。
		// 🔴 少了它，一个「所有 Span 都映到同一个键」的实现也能让上面那格绿。
		s, _ := twoSpanLib(t)
		res, err := s.VerifyCoverage()
		if err != nil {
			t.Fatalf("跑不起来：%v", err)
		}
		real0 := s.Coverage()[0]
		chunk := tickflow.Span{From: real0.From, To: real0.From, Bars: 1, Days: 1}
		// ⛔ 前提自检：造出来的真的是【区间不同】的那一种。
		if chunk.To == real0.To {
			t.Fatalf("前提没成立：%v 与 %v 区间相同 ⇒ 读数作废", chunk, real0)
		}
		if _, ok := res[chunk.Key()]; ok {
			t.Errorf("一个区间不同的 span 竟然查得到 ⇒ 键塌了。\n"+
				"  ⇒ (o) 那个缺陷（写入侧用分块的 span、读取侧用并段后的值）正是靠这一条被抓住的。\n"+
				"  登记的：%v  分块的：%v", real0, chunk)
		}
	})

	t.Run("三 跑不起来时回 nil map_不是空 map", func(t *testing.T) {
		s, _ := twoSpanLib(t)
		// 把底下那个文件关掉 ⇒ Stat 失败 ⇒ 这一遍跑不起来。
		if err := s.Close(); err != nil {
			t.Fatalf("关库失败：%v", err)
		}
		res, err := s.VerifyCoverage()
		// ⛔ 前提自检：真的跑不起来了 —— 否则下面两句在量一个正常的库。
		if err == nil {
			t.Fatalf("前提没成立：关掉之后它仍然跑得起来 ⇒ 读数作废（回了 %d 个结论）", len(res))
		}
		if res != nil {
			t.Errorf("跑不起来时回的不是 nil map，而是 %#v（len=%d）。\n"+
				"  🔴 len(m)==0 对 nil 与空 map 是同一个读数，而 m == nil 分得开 ——\n"+
				"     一个只看 map 不看 error 的调用方，会把它读成「每一段都没问题」。",
				res, len(res))
		}
	})
}

// guard: 新读法 VerifyCoverage 与参照实现 Verify(span) 对每一段给出一致结论。
func TestVerifyCoverageMatchesVerifyPerSpan(t *testing.T) {
	cases := []struct {
		name   string
		damage func(t *testing.T, dir string)
		want   error // nil ＝ 该全过
	}{
		{"健康库", nil, nil},
		{"零值记录", func(t *testing.T, dir string) {
			appendRaw(t, dir, tickflow.Bar{Ts: 1, TsEnd: 2, TradingDay: 0,
				Open: 1, High: 1, Low: 1, Close: 1, Volume: 1})
		}, errZeroTradingDay},
		{"倒序记录", func(t *testing.T, dir string) {
			appendRaw(t, dir, tickflow.Bar{Ts: 99, TsEnd: 100, TradingDay: tradingDays[0],
				Open: 1, High: 1, Low: 1, Close: 1, Volume: 1})
		}, errRecordDisorder},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, dir := twoSpanLib(t)
			if c.damage != nil {
				if err := s.Close(); err != nil {
					t.Fatalf("关库失败：%v", err)
				}
				c.damage(t, dir)
				var truncated int64
				var err error
				s, truncated, err = Open(dir, tickflow.Daily)
				if err != nil {
					t.Fatalf("重开失败：%v", err)
				}
				// ⛔ 前提自检：那条坏记录留住了（OpenDat 会砍半截记录）。
				if truncated != 0 {
					t.Fatalf("前提没成立：重开砍掉了 %d 字节 ⇒ 坏记录没留住，读数作废", truncated)
				}
				t.Cleanup(func() { s.Close() })
			}
			cov := s.Coverage()
			if len(cov) != 2 {
				t.Fatalf("前提没成立：期望 2 段，实得 %v ⇒ 读数作废", cov)
			}

			res, err := s.VerifyCoverage()
			if err != nil {
				t.Fatalf("新读法跑不起来：%v", err)
			}
			for _, sp := range cov {
				got := res[sp.Key()]
				ref := s.Verify(sp) // ← 参照实现，一段一遍全扫
				switch {
				case c.want == nil:
					if got != nil || ref != nil {
						t.Errorf("%v 健康库上却报了：新=%v 旧=%v", sp, got, ref)
					}
				default:
					// ⛔ 判据不是「两边都非 nil」，是**同一个哨兵** ——
					// 两个不同的错误也能让「都非 nil」成立，而那时两个读法已经漂开了。
					if !errors.Is(got, c.want) {
						t.Errorf("%v 新读法报的不是 %v：%v", sp, c.want, got)
					}
					if !errors.Is(ref, c.want) {
						t.Errorf("%v 参照实现报的不是 %v：%v", sp, c.want, ref)
					}
				}
			}
		})
	}
}

// twoSpanLib 造一个两段（中间隔一个有交易日的空档）的健康库，回 (库, 目录)。
func twoSpanLib(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, _, err := Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	t.Cleanup(func() { s.Close() })
	cal := testCal(t)
	commit := func(from, to tickflow.TradingDay) {
		t.Helper()
		if err := s.AppendBars([]tickflow.Bar{bar(from, int64(from)), bar(to, int64(to))}); err != nil {
			t.Fatalf("落盘失败：%v", err)
		}
		if err := s.CommitSpan(cal, eqKey,
			tickflow.Span{From: from, To: to, Bars: 2, Days: 2}, tickflow.OutcomeComplete); err != nil {
			t.Fatalf("登记失败：%v", err)
		}
	}
	commit(tradingDays[0], tradingDays[1])
	commit(tradingDays[3], tradingDays[4])
	return s, dir
}

// appendRaw 绕过写入口，直接往 .dat 末尾塞一条记录。
//
// ⚠️ 写入口（`AppendBars`）拒收零值与倒序，所以只能绕过它 ——
// **而那正是走查存在的理由**：写入口守得住未来，守不住已经在盘上的东西。
func appendRaw(t *testing.T, dir string, b tickflow.Bar) {
	t.Helper()
	var dat string
	if werr := filepath.Walk(dir, func(p string, fi os.FileInfo, e error) error {
		if e == nil && !fi.IsDir() && strings.HasSuffix(p, ".dat") {
			dat = p
		}
		return e
	}); werr != nil {
		t.Fatalf("走一遍库目录失败：%v", werr)
	}
	if dat == "" {
		t.Fatal("没找到 .dat —— 构造作废")
	}
	rec := EncodeBar(b)
	f, err := os.OpenFile(dat, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 .dat 失败：%v", err)
	}
	if _, err := f.Write(rec[:]); err != nil {
		f.Close()
		t.Fatalf("写失败：%v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关 .dat 失败：%v", err)
	}
}
