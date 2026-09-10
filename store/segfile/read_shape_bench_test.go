package segfile

import (
	"os"
	"path/filepath"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 这一组基准是 v0.4 定下的那次【读法探针】（docs/design.md §十六 那段：
// 「探针要量的是两种读法的代价；读数写进设计，**而它不进接口**」）。
//
// ⚠️ 所以两种形状都在**本文件里本地实现**，一个方法都没有加到 `Store` 上 ——
// 那是已定的：`Store` 的读口子在 v0.5 才落地，而它的形状要等**真正的消费者**
// （`calendar/derived` 与 `Feed`）出现之后再选。**这里只交代价，不交形状。**
//
// ⛔ 它跑起来会写一个约 78 MB 的 `.dat`（890,000 × 88 字节）。
// 写在 `b.TempDir()` 里，跑完自动删。**这一句是评审方要求写下的**：
// 本仓刚记过一条 ——「证明『能不能』的那个动作，本身可能带着『不该做』的副作用」。
//
// ⚠️ 数据是**合成的**，理由与 calendar/embedded 那组基准一样：
// 读一条定长记录的代价与它装的是什么**无关**（88 字节，偏移固定）。
// 而它有一条边界：**这里量不到磁盘之外的东西** —— 页缓存是热的、没有并发写者、
// 没有网络。⇒ 这些数是【下界】，真实场景只会更慢。
//
// 跑法（一次 op 就读 78 MB，所以固定 1 次）：
//
//	go test ./store/segfile/ -run XXX -bench ReadShape -benchmem -benchtime=1x
//
// —— 读数 ——
//
// ⛔ **一组读数没有基准就会被下一个人当成【当前值】读**（本仓索引那一段记过同一条）。
// 所以基准写死在这儿：
//
//	被测的那份代码   commit **8eeefad**（本文件落地那一颗）
//	                此后对本文件的改动只在**计时区间之外**（注释与 buildDat 的前提自检）
//	机器            AMD Ryzen 7 5700X 8-Core（16 线程）· Windows
//	Go              go1.26.1 windows/amd64
//	命令            go test ./store/segfile/ -run XXX -bench ReadShape -benchmem -benchtime=1x -count=4
//	日期            2026-09-10
//
//	                       ns/op（四轮）              B/op        allocs/op
//	一次全给 ReadShapeAll   49.6 / 51.6 / 44.7 / 47.2   85,5xx,xxx     8–19
//	流式     ReadShapeWalk  38.3 / 38.8 / 37.5 / 38.3       90,5xx      4–7
//	提前喊停 …WalkFirstDay   4.4 /  6.4 /  5.1 /  5.3       90,4xx      4–5
//
// 🔴 **决定（流式赢）单靠【内存】那一项就已经定了**：约 **945 倍**（81.6 MiB vs 88 KiB）。
// ⚠️ 而这一项**不是计时**，是 Go 数出来的字节数 ⇒ **它本身就是精确值**，
// 加轮次或做统计检验对它没有意义。
//
// ⛔ **而时间那一行【不参与结论】**（评审方 2026-09-10 判，我收）：
//
//	读数      All 44.7–51.6 ms  vs  Walk 37.5–38.8 ms ⇒ 约慢 24%
//	性质      **方向观察**：四轮，两组区间不重叠；**未做任何统计检验，也没换机器**
//	用法      **不参与这一节的结论** —— 结论由上面那个精确的内存项单独定
//	成因      未验，只写指向：全给那条要把 89 万个 `Bar` 逐个 append 进切片
//
// 🔴 按本仓那条：**不承重的数，问题不是准不准，是会不会被当成规格。**
// ⇒ 处置不是加轮次，是**在它旁边写清楚它不承重**。
//
// ⛔ **而这些数的射程要一起读**：
//
//	量到的    单趟顺序读、页缓存是热的、没有并发写者、没有网络、消费者什么也不做
//	量不到    随机访问 · 多趟重读 · 冷启动 · 真实消费者的计算
//	⇒ 这些是【下界】；而**若消费者要多趟重读，「一次全给」可以摊薄** ——
//	  那正是「选形状要等真正的消费者出现」的原因，不是这组数能替它答的。
//
// ✅ 而有一格是这组数**答得了**的：**提前喊停只有流式做得到**，
// 而它把「只要每天第一根」这类需求从 38 ms 降到 5 ms（约 9 倍）。
// ⇒ 设计里那句「derived 可能只需要每天最早/最晚那一根」在这里有了价钱。

const benchBars = 890_000 // 天勤实测：十年 1m 约 89 万根

// buildDat 造一份 benchBars 根的 .dat，返回路径。
// 它【不】造 .meta —— 本组量的是读根，不是读 coverage。
func buildDat(tb testing.TB) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "1m.dat")
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	buf := make([]byte, 0, RecordSize*1024)
	for i := 0; i < benchBars; i++ {
		// 只求条数够、交易日升序，不求它是真的交易日（同 calendar/embedded 那组）。
		r := EncodeBar(tickflow.Bar{
			Ts:         int64(i) * 60000,
			TsEnd:      int64(i)*60000 + 60000,
			TradingDay: tickflow.TradingDay(20200101 + int32(i/240)%10000),
			Close:      1.5,
		})
		buf = append(buf, r[:]...)
		if len(buf) >= RecordSize*1024 {
			if _, err := f.Write(buf); err != nil {
				tb.Fatal(err)
			}
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		if _, err := f.Write(buf); err != nil {
			tb.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		tb.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		tb.Fatal(err)
	}
	// ⚠️ 这一行【不走 CountRecords】，而这不是不信任它：
	// CountRecords 里有残尾判断那段逻辑，而它是**被测包自己的**代码 ——
	// 拿 X 当前提去验 X，本仓记过。
	// 🔴 而真正的理由是评审方那句：**独立的那一版只要一行、零逻辑、免费** ——
	// **既然免费，就不该停在「可接受」上。**
	if want := int64(benchBars) * RecordSize; st.Size() != want {
		tb.Fatalf("造出来的 .dat 大小不对：%d 字节，要 %d", st.Size(), want)
	}
	// 📎 而 readAllLocal 里那次 CountRecords【该留】：那不是自检，
	// 那是**被测的实现本身**（一次全给要拿它预分配切片）。
	return path
}

// readAllLocal 是候选【一】：一次全给。
func readAllLocal(path string) ([]tickflow.Bar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	n := CountRecords(st.Size())
	out := make([]tickflow.Bar, 0, n)
	buf := make([]byte, RecordSize*1024)
	for {
		got, rerr := f.Read(buf)
		if got > 0 {
			for off := 0; off+RecordSize <= got; off += RecordSize {
				b, derr := DecodeBar(buf[off : off+RecordSize])
				if derr != nil {
					return nil, derr
				}
				out = append(out, b)
			}
		}
		if rerr != nil {
			break
		}
	}
	return out, nil
}

// walkLocal 是候选【二】：流式，一根一根交出去，可中途喊停。
func walkLocal(path string, fn func(tickflow.Bar) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, RecordSize*1024)
	for {
		got, rerr := f.Read(buf)
		for off := 0; off+RecordSize <= got; off += RecordSize {
			b, derr := DecodeBar(buf[off : off+RecordSize])
			if derr != nil {
				return derr
			}
			if !fn(b) {
				return nil
			}
		}
		if rerr != nil {
			return nil
		}
	}
}

func BenchmarkReadShapeAll(b *testing.B) {
	path := buildDat(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bars, err := readAllLocal(path)
		if err != nil {
			b.Fatal(err)
		}
		if len(bars) != benchBars {
			b.Fatalf("读回 %d 根，要 %d", len(bars), benchBars)
		}
	}
}

func BenchmarkReadShapeWalk(b *testing.B) {
	path := buildDat(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := 0
		if err := walkLocal(path, func(tickflow.Bar) bool { n++; return true }); err != nil {
			b.Fatal(err)
		}
		if n != benchBars {
			b.Fatalf("走过 %d 根，要 %d", n, benchBars)
		}
	}
}

// BenchmarkReadShapeWalkFirstDay 是【提前喊停】那一格 —— 它只有流式做得到。
// 设计里那句「derived 可能只需要每天最早/最晚那一根」正是这一种。
func BenchmarkReadShapeWalkFirstDay(b *testing.B) {
	path := buildDat(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := 0
		var first tickflow.TradingDay
		err := walkLocal(path, func(x tickflow.Bar) bool {
			n++
			if first == 0 {
				first = x.TradingDay
			}
			return x.TradingDay == first // 第一天走完就停
		})
		if err != nil {
			b.Fatal(err)
		}
		if n == 0 || n >= benchBars {
			b.Fatalf("提前喊停没生效：走了 %d 根", n)
		}
	}
}
