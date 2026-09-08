package segfile

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// RecordSize 是一条记录的字节数。布局见 docs/design.md §三「记录布局」：
//
//	ts(8) tsEnd(8) tradingDay(4) flags(4) | open high low close(32) | vol(8) turnover(8) oi(8) settle(8)
//
// 小端，**无文件头**，`offset = i * RecordSize`。
//
// ⚠️ 它现在只活在【本版编译进来的这个常量】里，`.meta` 里没有它。
// 后果写在这儿，因为它是 C3a 的地基：**记录长一旦变了，旧 .dat 会被新代码
// 按新长度整除、算出一个「没有残尾」的假结论**——而那是个静默的错误答案。
// ⇒ 「要不要把 recordSize 存进 .meta 并与本常量比对」已提给评审方定（见 docs/contract.md §5
// 那处 magic / version / recordSize 的冲突）。**在它定下来之前，这里是一个已知的单点。**
const RecordSize = 88

// errRaggedTail 是 C3a：`.dat` 的长度不是记录长的整数倍 ⇒ 尾部有半截记录。
//
// 不导出的理由同 meta.go 那一组：`.dat` 层的失败分类还没进 design.md。
var errRaggedTail = errors.New("segfile: .dat 尾部有半截记录")

// EncodeBar 把一根 K 线写成 RecordSize 字节。
func EncodeBar(b tickflow.Bar) [RecordSize]byte {
	var r [RecordSize]byte
	le := binary.LittleEndian
	le.PutUint64(r[0:], uint64(b.Ts))
	le.PutUint64(r[8:], uint64(b.TsEnd))
	le.PutUint32(r[16:], uint32(b.TradingDay))
	le.PutUint32(r[20:], uint32(b.Flags))
	for i, v := range [...]float64{b.Open, b.High, b.Low, b.Close,
		b.Volume, b.Turnover, b.OpenInterest, b.Settle} {
		le.PutUint64(r[24+i*8:], math.Float64bits(v))
	}
	return r
}

// DecodeBar 是 EncodeBar 的逆。
//
// ⚠️ NaN 必须原样回来：`Settle` 在分钟线上是 NaN，而**把 NaN 读成 0 会算出
// 一整天的灾难性盈亏而全程不报错**（bar.go 里那条）。`math.Float64bits` 往返保住它。
func DecodeBar(r []byte) (tickflow.Bar, error) {
	if len(r) < RecordSize {
		return tickflow.Bar{}, fmt.Errorf("segfile: 记录只有 %d 字节，不足 %d", len(r), RecordSize)
	}
	le := binary.LittleEndian
	var b tickflow.Bar
	b.Ts = int64(le.Uint64(r[0:]))
	b.TsEnd = int64(le.Uint64(r[8:]))
	b.TradingDay = tickflow.TradingDay(int32(le.Uint32(r[16:])))
	b.Flags = tickflow.BarFlags(le.Uint32(r[20:]))
	f := [8]float64{}
	for i := range f {
		f[i] = math.Float64frombits(le.Uint64(r[24+i*8:]))
	}
	b.Open, b.High, b.Low, b.Close = f[0], f[1], f[2], f[3]
	b.Volume, b.Turnover, b.OpenInterest, b.Settle = f[4], f[5], f[6], f[7]
	return b, nil
}

// TailCheck 是 C3a 的判据：文件长度不是记录长的整数倍 ⇒ 有残尾。
//
// 返回 (完整记录数, 残尾字节数)。残尾字节数 > 0 就是有残尾。
func TailCheck(size int64) (records int64, ragged int64) {
	return size / RecordSize, size % RecordSize
}

// OpenDat 打开 `.dat`，**并在打开时截掉未完成的尾部记录**（C3a）。
//
// ⚠️ 为什么必须在【打开】时截而不是写之前：
// 前两条写入顺序约束（C1/C2）管的是**两个文件之间**的顺序，
// 它们【不管 `.dat` 自身写到一半】——崩在追加中途，半截记录留在文件尾，
// coverage 没扩（那一格顺序守住了），**但下一次追加会落在那半截【后面】**。
//
// ⚠️ 截断必须留声：本函数把截掉多少字节返回出去。
// **自愈动作必须留声，否则它和「本来就没事」长得一样。**
// ⇒ 而「留声要进 `SyncReport.TruncatedTails`」是 C3b，`SyncReport` 还不存在
// （见 tools/doccheck/pending.txt）⇒ **C3b 不在本版**，本函数只负责把它交出去。
func OpenDat(path string) (f *os.File, truncated int64, err error) {
	f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	n, ragged := TailCheck(st.Size())
	if ragged == 0 {
		return f, 0, nil
	}
	if err := f.Truncate(n * RecordSize); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("%w: 截断失败: %v", errRaggedTail, err)
	}
	return f, ragged, nil
}

// CountRecords 数 `.dat` 里有多少条**完整**记录。
//
// ⛔ 它【不是】B2 要的那个数。B2 的判据写死了：**和一次真正的枚举比，
// 不和一个算出来的期望比** —— 而本函数正是「文件长度 ÷ 记录长」那一类。
//
// 留着它是因为 C3a 要用（判残尾），**而它不许被拿去当 `bars`**：
// 评审方给过失败场景 —— 区间 [A,B] 有 N 条记录，某天的起点因映射损坏而找不到了，
// 记录一条没少 ⇒ 长度除法仍然等于 N ⇒ 那天被判成「拉过，确认没有」。
// **「长度 ÷ 记录长」抓得住少了/多了/残尾，抓不住「记录都在而读不到」。**
func CountRecords(size int64) int64 {
	n, _ := TailCheck(size)
	return n
}
