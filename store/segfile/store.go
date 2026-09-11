package segfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// Store 是一个「品种 + 周期」的落盘目录：一份 `.dat` 加一份 `.meta`。
//
// 本文件承的是不变量 B1 B2 B3 C1 C2 C3a（编号见 docs/design.md §6.1 那张表）。
//
// ⚠️ 缺席的：C3b（截断要进 SyncReport）与 D2b（两种结果都进 SyncReport）——
// **通道已由丙三之一打通**（`OpenState` / `DiscardCoverage`），缺的是读它的那一头。
// **它们仍是整条缺席，不是测了一半。**
//
// ⛔ 还有三处【范围边界】，写在这儿免得被读成通用实现：
//
//	一、✅ **这一条已到期并做掉了**（2026-09-10，design.md §十八）。
//	    它原来写「周期写死成 1m，要支持多周期得让 Open 收一个周期参数」——
//	    `Open` 现在收了，落盘名是 `<PeriodDirName(p)>.dat` / `.meta`。
//	    ⇒ 本类型仍然是【一个合约的一个周期】，而现在**那个周期是它身份的一部分**：
//	    两个周期落在两个文件上 ⇒ 「混进同一个库」不是被拦住，是**不可表达**。
//	    ⚠️ 而落盘名走 `tickflow.PeriodDirName`，**不是 `Period.String()`** ——
//	    String() 的 "1M"（Monthly）与 "1m"（1 分钟）在 Windows 上是同一个文件（实测）。
//	二、没有锁。布局里那个 `.lock` 本版一个字都没碰
//	    ⇒ **两个进程同时开同一个目录，本类型不会拦。**
//	    而 B3 那个 `verified` 正是进程内状态：别的进程改了 .meta，这边的走查结论就过期了，
//	    **而它不会知道**。
//	三、Store 接口本身还没定型。design.md 列的是
//	    `Append` / `Merge` / `Iter` / `Range` / `Meta` / `AddCoverage` / `Series` / `Close`，
//	    本类型只实现了落盘格式那几条不变量要用到的部分。
//
// **这三条都不是缺陷，是没做；写下来是为了让「没做」和「做漏了」分得开。**
type Store struct {
	dir string
	dat *os.File

	// period 是【落盘名】（PeriodDirName 的输出），不是 Period 本身。
	// 存名字而不存周期，是因为本层用得到的只有名字：它要拼路径。
	// 存 Period 会让本层多背一个它不使用的类型，而**多背的那一份迟早会和真相漂开**。
	period string

	meta Meta

	// truncated / legacyMeta 是 Open 那一刻的两个读数，供 OpenState 报出去。
	//
	// ⛔ 它们【本层处置不了】：截断要进 SyncReport.TruncatedTails（C3b），
	// 而 .meta 缺 format 的处置取决于「源可不可重放」（D2a）——
	// 那两样都只有编排知道。⇒ 存下来，等编排来问。
	truncated  int64
	legacyMeta bool

	// verified 记「这一段【走查过】没有」。B3 要它：**没走查过的时候，
	// 那两个计数什么也不意味着**，不许回答「拉过，确认没有」。
	//
	// ⚠️ 它是【进程内】状态，不落盘：一份 .meta 被另一个进程改过之后，
	// 上一次走查的结论就不再成立。落盘会让它变成一个会过期的抄件。
	verified map[tickflow.Span]bool
}

// Outcome 是上游这一次响应的结果。C2 要它。
//
// ⚠️ 它不是 bool，而且**零值不合法** —— 忘了填的调用方会当场被拒，
// 而不是拿到 false 或 true 里的某一个。
// 「问了但没问成」和「问了，确认没有」在返回值上可以长得很像
// （空数组 / 错误 / 超时后的空响应），所以这件事必须由调用方**说出来**。
// ⛔ **2026-09-09 挪到根包**（`tickflow.Outcome`），这里只留别名。
// 理由不是搬家的便利：**「零值不合法」是接口契约的一部分** ——
// 留在实现包里，别的 `Store` 实现就不受它约束，而 C2 正是靠它成立。
type Outcome = tickflow.Outcome

const (
	// OutcomeComplete 上游【完整成功】。只有它允许扩 coverage。
	OutcomeComplete = tickflow.OutcomeComplete
	// OutcomeFailed 出错、超时、或响应不完整。落盘可以，扩 coverage 不行。
	OutcomeFailed = tickflow.OutcomeFailed
)

// ErrOutOfOrder 这一批的交易日与盘上已有的记录合不成非降序。
//
// ⛔ **它是本包里【唯一】导出的错误哨兵，理由写清楚，别让它读起来像随手导出的。**
//
// 本包别的哨兵都只回答「这个文件怎么了」——那是**本包内部**的事，
// 而调用方对它们唯一能做的事就是报出去。
// 🔴 而这一条不同：它回答的是「**你刚才那次调用做错了什么**」，
// 且它有一个**调用方够得着的处置**（换一个不倒退的区间、或先重建）。
// ⇒ 一个调用方要分辨得出它，就得 `errors.Is` 得到它。
//
// ⚠️ 而它导出的**代价**一并写下：从今天起它是契约的一部分，改文案可以，
// **换值会打断下游的 `errors.Is`** —— 而那是一次静默的打断。
var ErrOutOfOrder = errors.New("segfile: 交易日倒退了——这一批与盘上已有的记录合不成非降序")

var (
	errNotDurable     = errors.New("segfile: coverage 想扩到 .dat 还没有的数据上")
	errUnknownSibling = errors.New("segfile: 这个目录里有本版读不懂的旧库——不在它旁边新建")
	errIncomplete     = errors.New("segfile: 上游没有完整成功，不许扩 coverage")
	errBarsMismatch   = errors.New("segfile: bars 与走查数出来的对不上")
	errDaysMismatch   = errors.New("segfile: days 与走查数出来的对不上")
	errRecordOutside  = errors.New("segfile: 记录的交易日落在本段之外")
	errRecordDisorder = errors.New("segfile: 记录的交易日不是非降序")
	errZeroTradingDay = errors.New("segfile: 记录的 TradingDay 是零值")
)

// ⛔ **编译期断言：本类型必须满足根包的 `Store` 接口。**
//
// 这不是一条测试，是**编译期** —— 接口与实现哪天对不上，`go build` 当场不过。
// 而那个接口正是**按本类型实到的方法反推**出来的（design.md 七之零冲突三）：
// 这一行的作用是**把那次「反推」钉住**，免得两边各自漂。
//
// ⚠️ 它同时是那句「Open 不进接口」的对照物：`Open` 是包级函数，
// **本断言不要求它** —— 若哪天有人把构造塞进接口，这一行会当场红。
var _ tickflow.Store = (*Store)(nil)

// Open 打开一个落盘目录里【某一个周期】的库。返回被截掉的残尾字节数（C3a，见 OpenDat）。
//
// ⚠️ 周期是【身份】不是选项：它决定开哪两个文件。落盘名来自 `tickflow.PeriodDirName`，
// **不是 `Period.String()`** —— 后者把 Monthly 叫成 "1M"，而那与 1 分钟的 "1m"
// 在 Windows 上是同一个文件（实测两组，见 design.md §十七）。
//
// ⛔ 这里有一个【顺序】要紧的地方：`readMeta` 与 `refuseUnknownSiblings` 都在
// `OpenDat` **之前**。原因是 `OpenDat` 带 `O_CREATE` —— 它一跑就把 `.dat` 建出来了。
// 拦截放在它后面的话，**拦是拦住了，而文件已经落地**（本仓在 AppendBars/CommitSpan
// 那一格记过同一个形状）。
func Open(dir string, p tickflow.Period) (s *Store, truncated int64, err error) {
	name, err := tickflow.PeriodDirName(p)
	if err != nil {
		return nil, 0, fmt.Errorf("segfile: 开 %s 失败: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, err
	}
	b, rerr := os.ReadFile(filepath.Join(dir, name+".meta"))
	// ⛔ 要【新建】一个库之前，先看看这个目录里有没有本版读不懂的旧库。见 §十八 三。
	if os.IsNotExist(rerr) {
		if serr := refuseUnknownSiblings(dir, name); serr != nil {
			return nil, 0, serr
		}
	}
	f, truncated, err := OpenDat(filepath.Join(dir, name+".dat"))
	if err != nil {
		return nil, 0, err
	}
	st := &Store{dir: dir, dat: f, period: name, truncated: truncated, verified: map[tickflow.Span]bool{}}
	err = rerr
	switch {
	case err == nil:
		m, derr := DecodeMeta(b)
		if derr != nil {
			f.Close()
			return nil, 0, derr
		}
		// ⛔ 读进来的 coverage 也要过 A1a/A1b —— 不变量在【两个入口】都要设卡。
		//
		// 此前它们只在 CommitSpan（写）那一侧执行，于是一份手改的、
		// 或别的版本/工具写的 .meta【进得来】：实测一份乱序 + 重叠 + 端点非交易日的
		// coverage 被 Open 悄悄收下。
		// ⇒ 按本节第一原则（读不懂就报错，不猜）：结构不合法就不收。
		//
		// ⚠️ 而 A1c（端点必须是交易日）**在这里查不了**：它要日历，而 Open 没有。
		// 这是一个【被声明的边界】，不是遗漏：A1c 仍然只在 CommitSpan 那一侧执行。
		// ⇒ 一份端点非交易日的 .meta 仍然进得来，直到有人拿着日历去动它。
		// 要在这里也堵上，得让 Open 收一个 Calendar —— 那是接口形状的变更，
		// 已提给评审方定，本版不擅自改。
		if verr := ValidateCoverage(m.Coverage); verr != nil {
			f.Close()
			return nil, 0, fmt.Errorf("segfile: %s 里的 coverage 结构不合法: %w", dir, verr)
		}
		st.meta = *m
		// A3：format 缺失时 Format 为 nil，与「写了 0」分得开。
		// ⇒ 这一格就是 D2a 的入口条件；处置不在这一层。
		st.legacyMeta = m.Format == nil
	case os.IsNotExist(err):
		v := FormatVersion
		st.meta = Meta{Format: &v}
		// ⛔ 【没有 .meta】不是【旧 .meta】—— 前者是一个新库，
		// 后者是一份 v0.3 之前写的、语义未知的 coverage。
		// 合成一格会让每一个新目录都去走 D2a，而那条路要人做决定。
	default:
		f.Close()
		return nil, 0, err
	}
	return st, truncated, nil
}

// Close 关掉底下的文件。
func (s *Store) Close() error { return s.dat.Close() }

// Coverage 返回当前的 coverage（拷贝）。
func (s *Store) Coverage() []tickflow.Span {
	return append([]tickflow.Span(nil), s.meta.Coverage...)
}

// AppendBars 只把这一批根写进 `.dat` 并落到盘上，**不碰 coverage**。
//
// ⚠️ 这是 C1 的一半：**coverage 只能在数据落盘之后扩大。**
// 顺序反了，崩溃就会留下「声称拉过而其实没有」——正是最危险的那个方向
// （静默漏数据且不会自愈）。反过来（先写数据后扩 coverage）崩溃留下的是
// 「拉过却没记」⇒ 重拉一遍，**吵而不丢**。
//
// ⇒ 所以本方法与 CommitSpan 是**两个**方法：让「先写后记」成为唯一写得出来的顺序。
func (s *Store) AppendBars(bars []tickflow.Bar) error {
	if len(bars) == 0 {
		return nil
	}
	// ⛔ 零值 TradingDay 在这里拦掉，而理由不是「防御性编程」。
	//
	// 评审方 2026-09-09 指出的那条因果，是这一格存在的全部原因：
	//
	//	他要来的 Verify 三分支修法，把 default 支的文案从含糊改成
	//	「谁的段都不属于 ⇒ **这才是真的损坏**」
	//	而零值 TradingDay **正好落进这一支**
	//	⇒ 修之后那条错误信息更自信了，**而对这一支它说错了**：
	//	  文件没坏，是**源没填字段**。
	//
	// ⇒ **一条被改得更自信的错误信息，在它说错的那一支上，比含糊时更贵。**
	//
	// 拦在写入口而不是走查，是因为这两者答的不是同一个问题：
	//
	//	走查时报   「这个文件里有一条谁的段都不属于的记录」—— 指向文件
	//	写入时报   「你给我的第 i 条没填 TradingDay」      —— 指向调用方，也就是真因
	//
	// 无误伤面：交易日零值从来不是合法值（同 `Outcome` 零值不合法、`format` 用 `*int`
	// 那一族）——**没有任何合法调用会传零值。**
	for i, b := range bars {
		if b.TradingDay == 0 {
			return fmt.Errorf("%w: 第 %d 条（Ts=%d）—— "+
				"多半是 Source 没填这个字段；它落盘之后会在走查时被报成【文件损坏】，"+
				"而那条信息指不到真因", errZeroTradingDay, i, b.Ts)
		}
	}
	// ⛔ **顺序也拦在写入口**，理由与上面那一格【同一条】，而距离更远。
	//
	// 它关掉的是一个实测出来的窗口（2026-09-11，探针量的，不是推的）：
	//
	//	Verify 过 ⇒ verified[span] = true，此刻文件有序
	//	一次**合法的** AppendBars 写进一条倒退的记录 ⇒ **一个字都没人查**
	//	⇒ 此后 verified 仍为 true 而文件已乱序，直到**下一次**走查才红
	//
	// ⚠️ 而这个窗口**今天没有伤到任何人**：现行读法是线性扫描，**对乱序免疫**
	// （同一份库上实测：乱序之后 `DaysWithBars` 给的答案仍然是对的）。
	// 🔴 **它伤的是将来** —— 任何「利用有序性」的读法（二分、只扫段对应的那一段）
	// 在这个窗口里会**静默给出错的答案**（实测：二分把一天漏报成缺席）。
	// ⚠️ 而那个错的**方向要说准**：二分只会**漏报存在** ⇒ 那一天被判成
	// `GapConfirmedEmpty` ⇒ **重复拉取（吵，不丢数据）**，**不是**「静默漏数据」那一类。
	// ⇒ 所以这一格的价值不是修一个今天的 bug，是**把一个不变量从「某一刻检查过」
	// 变成「一直成立」** —— 而那是那类读法能不能被考虑的前提。
	//
	// ⚠️ **它禁掉了什么，写清楚**：**按追加做的倒填**（先写晚的、再补早的）。
	// 而它**没有拿走任何今天可用的能力** —— 那样的文件**本来就过不了 `Verify`**
	// （实测：倒填之后重新走查当场红）。⇒ 这一格搬的是**报错的位置**，不是规矩本身。
	if last, ok, err := s.lastTradingDay(); err != nil {
		return err
	} else if ok && bars[0].TradingDay < last {
		return fmt.Errorf("%w: 这一批第 0 条是 %s，而盘上最后一条是 %s——"+
			"这样的文件过不了走查，而走查要到【下一次同步】才跑，"+
			"那时报文只会说「文件里第 i 条比上一条早」，指不回写它的这次调用",
			ErrOutOfOrder, bars[0].TradingDay, last)
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].TradingDay < bars[i-1].TradingDay {
			return fmt.Errorf("%w: 这一批内部第 %d 条是 %s，而第 %d 条是 %s",
				ErrOutOfOrder, i, bars[i].TradingDay, i-1, bars[i-1].TradingDay)
		}
	}

	buf := make([]byte, 0, len(bars)*RecordSize)
	for _, b := range bars {
		r := EncodeBar(b)
		buf = append(buf, r[:]...)
	}
	if _, err := s.dat.Seek(0, os.SEEK_END); err != nil {
		return err
	}
	if _, err := s.dat.Write(buf); err != nil {
		return err
	}
	// ⚠️ 必须 Sync：C1 说的是「数据【落盘】之后」，不是「写进页缓存之后」。
	return s.dat.Sync()
}

// CommitSpan 扩 coverage。
//
//	C1  先核对数据【真的在盘上】：.dat 的完整记录数必须够得上这一段声称的 bars
//	C2  只有 OutcomeComplete 才允许扩；其余一律拒绝
//	A1a/A1b/A1c/A2  经 NormalizeCoverage 走一遍（升序、不重叠、端点是交易日、按交易日合并）
func (s *Store) CommitSpan(cal tickflow.Calendar, k tickflow.ProductKey,
	span tickflow.Span, out Outcome) error {
	if out != OutcomeComplete {
		return fmt.Errorf("%w: outcome=%d"+
			"——「问了但没问成」和「问了，确认没有」在返回值上长得很像，"+
			"所以这件事必须由调用方说出来", errIncomplete, int(out))
	}
	st, err := s.dat.Stat()
	if err != nil {
		return err
	}
	have := CountRecords(st.Size())
	want := int64(span.Bars)
	for _, sp := range s.meta.Coverage {
		want += int64(sp.Bars)
	}
	if have < want {
		return fmt.Errorf("%w: .dat 里有 %d 条完整记录，而 coverage 加上这一段要 %d 条"+
			"——先落盘，再扩 coverage", errNotDurable, have, want)
	}
	next := append(append([]tickflow.Span(nil), s.meta.Coverage...), span)
	merged, err := NormalizeCoverage(cal, k, next)
	if err != nil {
		return err
	}
	s.meta.Coverage = merged
	return s.writeMeta()
}

// writeMeta 把 `.meta` 写出去 —— **先写临时文件，再 rename 覆盖**。
//
// ⚠️ 为什么不直接 WriteFile（实测，2026-09-09）：
// 直接写时，崩在写一半会留下**半份坏 JSON** ⇒ 下一次 Open 走 E1a（报错不猜）
// ⇒ **整个 store 打不开**。
//
// 而 §6.1 自己写明了安全的失败方向：
// 「先写数据后扩 coverage，崩溃留下的是【拉过却没记】⇒ 重拉一遍，**吵而不丢**」。
// ⇒ rename 之后，崩在写一半留下的是**上一份好的 .meta**：
// coverage 落后于数据，正是那个吵而不丢的方向。**直接写会把它变成硬失败。**
//
// ⛔ 而这一条【没有测试】，说清楚：
// 「rename 是原子的」是文件系统的性质，**单元测试里没法真的崩在中间**。
// 能测的只有「写完之后没有留下临时文件、目标仍然可解」——那两条测的不是原子性。
// ⇒ 它靠的是一个**结构性论证**，不是一次观测。写在这儿，免得下一个人以为它被守着。
func (s *Store) writeMeta() error {
	b, err := EncodeMeta(s.meta)
	if err != nil {
		return err
	}
	final := filepath.Join(s.dir, s.period+".meta")
	tmp := final + ".tmp"
	// ⛔ 必须 Sync 之后再 Rename：改名只保证【名字】换了，不保证【内容】已经落盘。
	//
	// 上一版是 os.WriteFile(tmp) → os.Rename，中间没有 Sync ——
	// **而同一个文件里 AppendBars 早就立了这条规矩**（「C1 说的是数据落盘之后，
	// 不是写进页缓存之后」）。⇒ 同一条规矩，给 .dat 立了，没给 .meta 用。
	//
	// ⚠️ 射程说清楚（评审方 2026-09-09 的两句限定，我照收）：
	// 「崩溃后会不会真的留下【已改名而内容为空】的文件」是**文件系统相关**的，
	// 这里不替文件系统下判断；论据是**内部一致性** —— 本仓已为 .dat 立过这条，理由一字不改地适用。
	// 而缺 Sync **不破 C1 的方向**（.dat 已 Sync、coverage 未 Sync ⇒ 丢的是 coverage ⇒ 吵而不丢），
	// 它破的是【这一格自己宣称的目的】。
	//
	// ⛔ 而**目录项的 fsync 没有做**：那是更严的一档，Windows 上拿不到。
	// 写成声明的边界，不假装做了。
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp) // 别把半成品留在目录里冒充别的东西
		return err
	}
	return nil
}

// Verify 走查一段：**逐条读出来数**，再和 `.meta` 里那两个被写下去的计数比。
//
// ⛔ 判据写死了（B2）：**和一次真正的枚举比，不和一个算出来的期望比。**
// 「文件长度 ÷ 记录长」抓得住少了/多了/残尾，**抓不住「记录都在而读不到」**——
// 而后者恰恰是这一节要防的那一类：**读不到的东西和不存在的东西长得一样。**
//
// B1：**两个计数都要比**。只比 bars 不够——记录一条没少而某天的起点找不到了时，
// bars 仍然相符，于是那一天读成「拉过，确认没有」。days 对不上就报错
// ⇒ **缺席重新只意味着损坏。**
func (s *Store) Verify(span tickflow.Span) error {
	st, err := s.dat.Stat()
	if err != nil {
		return err
	}
	n := CountRecords(st.Size())
	buf := make([]byte, RecordSize)
	bars := 0
	days := 0
	var prev, prevInSpan tickflow.TradingDay
	for i := int64(0); i < n; i++ {
		if _, err := s.dat.ReadAt(buf, i*RecordSize); err != nil {
			return fmt.Errorf("segfile: 读第 %d 条记录失败: %w", i, err)
		}
		b, err := DecodeBar(buf)
		if err != nil {
			return err
		}
		// ⛔ SYN-10：**零值 TradingDay 要指得到【真因】，而它必须判在最前面。**
		//
		// 一条零值记录会被下游【三个不同的分支】各自接住，而三句话都指向文件：
		//
		//	顺序判      「交易日不是非降序」   ⇒ 像是写入顺序乱了
		//	default     「不落在任何一段里」   ⇒ 像是 coverage 与数据对不上
		//	（两者都会让人去查磁盘、比长度、怀疑截断）
		//
		// **而真因是【源没填这个字段】，处置在另一头。**
		// ⇒ 所以它判在最前：**一个更准的诊断，必须排在所有更泛的诊断之前** ——
		// 排在后面的话它永远不会被走到（实测：它先撞上顺序判）。
		//
		// ⚠️ SRC-7 落地之后（AppendBars 那一侧已拒零值），这种记录**只可能**来自
		// 本版写入口之外 —— 旧版写的、别的写者写的、手工造的。
		// **而那正是走查存在的理由**：写入口守得住未来，守不住已经在盘上的东西。
		if b.TradingDay == 0 {
			return fmt.Errorf("%w: 第 %d 条记录（Ts=%d）的 TradingDay 是零值——"+
				"它来自本版写入口之外（旧版/别的写者/手工造的）；"+
				"这不是文件损坏，别去查长度和截断",
				errZeroTradingDay, i, b.Ts)
		}

		// 顺序是全库的性质，不是某一段的：先在这一层查。
		if b.TradingDay < prev {
			return fmt.Errorf("%w: 第 %d 条是 %s，而上一条是 %s",
				errRecordDisorder, i, b.TradingDay, prev)
		}
		prev = b.TradingDay

		switch {
		case b.TradingDay >= span.From && b.TradingDay <= span.To:
			// 本段的：数。
			if b.TradingDay != prevInSpan {
				days++
				prevInSpan = b.TradingDay
			}
			bars++
		case s.dayInAnySpan(b.TradingDay):
			// ⛔ 别的段的：**跳过，不是错**。
			//
			// 上一版这里是「不在本段 ⇒ errRecordOutside」，于是**任何多段库都通不过走查**
			// —— 而多段（= 有缺口的序列）正是同步中的常态。
			// 后果比「用不了」重：它把一个【健康的库】报成【损坏】，
			// 而 B1/B2 整套的目的正是「缺席重新只意味着损坏」——那一版把它反了过来：
			// **损坏重新可以只意味着分了两段。**
			//
			// 评审方从 28e37cc 报到 08d0d33，**六个尖端**；而我四次撤回重送都没带上它，
			// 因为每次只按自己新发现的问题改、没复述他的结论。⇒ 那条流程规矩的代价是四轮。
		default:
			// 谁的段都不属于 ⇒ 这才是真的损坏。
			return fmt.Errorf("%w: 第 %d 条记录是 %s，而它不落在任何一段 coverage 里",
				errRecordOutside, i, b.TradingDay)
		}
	}
	if bars != span.Bars {
		return fmt.Errorf("%w: 走查数出 %d 条，而 .meta 记的是 %d 条", errBarsMismatch, bars, span.Bars)
	}
	if days != span.Days {
		return fmt.Errorf("%w: 走查数出 %d 个交易日，而 .meta 记的是 %d 个"+
			"——只比 bars 会让「某天的起点丢了」读成「那天确认没有」",
			errDaysMismatch, days, span.Days)
	}
	s.verified[span] = true
	return nil
}

// dayInAnySpan 报告这一天在不在【任何】一段 coverage 里。
//
// Verify 用它把「别的段的记录」与「谁的段都不属于的记录」分开 ——
// 前者跳过，后者才是损坏。
func (s *Store) dayInAnySpan(day tickflow.TradingDay) bool {
	for _, sp := range s.meta.Coverage {
		if day >= sp.From && day <= sp.To {
			return true
		}
	}
	return false
}

// HasBars 回答「这一天有没有根」。
//
// ⛔ B3：**没走查过的时候，那两个计数什么也不意味着。**
// 那时不许回答「拉过，确认没有」——只能答「答不了」（ErrSpanUnverified）。
// 走查发生在给出「确认没有」这个答案【之前】，不是发现异常之后。
//
// 这是本节第一原则的又一次应用：**缺少验证不能悄悄变成一个肯定的答案。**
// 「没验过」和「验过了，是空的」如果都回答「确认没有」，
// 那么加那两个计数**只是把原来那个洞挪了个位置**。
func (s *Store) HasBars(day tickflow.TradingDay) (bool, error) {
	for _, sp := range s.meta.Coverage {
		if day < sp.From || day > sp.To {
			continue
		}
		if !s.verified[sp] {
			return false, fmt.Errorf("%w: %s 落在 [%s, %s] 里，而这一段还没走查过",
				tickflow.ErrSpanUnverified, day, sp.From, sp.To)
		}
		// ⛔ 这里【不能】用 sp.Bars > 0。
		//
		// 那两个计数说的是**这一段整体**对不对，§6.1 明写了它们的边界：
		// **说不出是哪一天丢了**。拿 sp.Bars 去答「这一天有没有」，
		// 等于把一个关于整段的事实当成了一个关于某一天的答案——
		// 一段跨三天、只有第一天有根的 coverage，会对另外两天都答「有」。
		//
		// ⇒ 走一遍，找这一天的记录。**而「找不到」之所以能读成「确认没有」，
		// 靠的是上面那一步：这一段【走查过】。** 没走查过时同样的缺席只意味着损坏。
		return s.dayHasRecords(day)
	}
	return false, nil // 不在任何 coverage 里 ⇒ 没拉过，这不是「确认没有」
}

// DaysWithBars 一次回答【一整段】里哪些交易日有根。
//
// ⛔ 它换掉的是 `HasBars` 在生产路径上的位置，而**换的是形状不是常数**：
//
//	旧  每天一次 HasBars ⇒ dayHasRecords 线性扫 .dat  ⇒ O(天数 × 根数)
//	新  一整段一次       ⇒ 只扫一遍                    ⇒ O(根数)
//
// 代价与它在哪一族输入上**更慢**，逐条写在 `docs/design.md` 的
// 「问五｜新读法在哪些输入上比旧读法【慢】」里 —— 这里不复述那些数，
// 只留一句判据：**新读法的代价是【恒定】的 ≈N 次读，旧读法可低到 1 次。**
//
// ⛔ **三值语义必须原样保住**（B3）：这一段没走查过时返回 `ErrSpanUnverified`，
// **不返回一个空 map**。🔴 空 map 与「这一段每天都没有根」在调用方那儿长得一模一样 ——
// 而那正是「没验过悄悄变成一个肯定的答案」那个洞。
//
// ⚠️ 射程：只回答 `[span.From, span.To]` 之内的交易日；
// 落在段外的记录一概不进结果（它们属于别的段，由那一段自己的调用回答）。
func (s *Store) DaysWithBars(span tickflow.Span) (map[tickflow.TradingDay]bool, error) {
	if !s.verified[span] {
		return nil, fmt.Errorf("%w: [%s, %s] 这一段还没走查过",
			tickflow.ErrSpanUnverified, span.From, span.To)
	}
	st, err := s.dat.Stat()
	if err != nil {
		return nil, err
	}
	n := CountRecords(st.Size())
	buf := make([]byte, RecordSize)
	out := make(map[tickflow.TradingDay]bool)
	for i := int64(0); i < n; i++ {
		if _, err := s.dat.ReadAt(buf, i*RecordSize); err != nil {
			return nil, fmt.Errorf("segfile: 读第 %d 条记录失败: %w", i, err)
		}
		b, err := DecodeBar(buf)
		if err != nil {
			return nil, err
		}
		if b.TradingDay >= span.From && b.TradingDay <= span.To {
			out[b.TradingDay] = true
		}
	}
	return out, nil
}

// lastTradingDay 读盘上**最后一条**记录的交易日。ok 为 false 表示文件是空的。
//
// ⚠️ 它读盘而不缓存一个字段，理由是本仓那条：**多背的那一份迟早会和真相漂开**
// （`Store` 只存 `period` 名字而不存 `Period` 那一格写的是同一句话）。
// 代价可核：**一次 `ReadAt`**，而它旁边就是一次 `Write` ＋ 一次 `Sync`。
func (s *Store) lastTradingDay() (tickflow.TradingDay, bool, error) {
	st, err := s.dat.Stat()
	if err != nil {
		return 0, false, err
	}
	n := CountRecords(st.Size())
	if n == 0 {
		return 0, false, nil
	}
	buf := make([]byte, RecordSize)
	if _, err := s.dat.ReadAt(buf, (n-1)*RecordSize); err != nil {
		return 0, false, err
	}
	b, err := DecodeBar(buf)
	if err != nil {
		return 0, false, err
	}
	return b.TradingDay, true, nil
}

// dayHasRecords 走一遍 `.dat`，看这一天有没有记录。
//
// ⚠️ 它是 B2 那条判据的同一条：**和一次真正的枚举比，不和一个算出来的期望比。**
func (s *Store) dayHasRecords(day tickflow.TradingDay) (bool, error) {
	st, err := s.dat.Stat()
	if err != nil {
		return false, err
	}
	n := CountRecords(st.Size())
	buf := make([]byte, RecordSize)
	for i := int64(0); i < n; i++ {
		if _, err := s.dat.ReadAt(buf, i*RecordSize); err != nil {
			return false, fmt.Errorf("segfile: 读第 %d 条记录失败: %w", i, err)
		}
		b, err := DecodeBar(buf)
		if err != nil {
			return false, err
		}
		if b.TradingDay == day {
			return true, nil
		}
	}
	return false, nil
}

// OpenState 报打开这一份库时发现的两件事（C3b / D2b 的输入）。
//
// ⚠️ 这两格都是 `Open` 那一刻的读数，**此后不再变** ——
// 它们记的是「打开时的世界」，不是「现在的世界」。
// 写下来是因为下一个人会想在这儿加第三格，而那一格未必也有这个性质。
func (s *Store) OpenState() tickflow.OpenState {
	return tickflow.OpenState{
		TruncatedTail: s.truncated,
		LegacyMeta:    s.legacyMeta,
	}
}

// DiscardCoverage 把 coverage 整个作废，全区间按「没拉过」（LegacyDiscard 那一支）。
//
// ⛔ **`verified` 也必须一起清掉，而这一格差点被漏掉。**
// coverage 清空而 `verified` 留着，会留下一批「指向已经不存在的 Span」的走查记录；
// 它们今天查不出来（键是 Span 值，对不上就当没走查过 ⇒ 落在安全的那一侧），
// **而那是巧合，不是设计** —— 键的形状一变，那批陈旧记录就会开始回答问题。
//
// ⚠️ 它**不落盘 `.dat`**：作废的是「声称拉过」这件事，不是数据。
// 旧记录留在 `.dat` 里，重拉时按 AppendBars 追加 ——
// 这与 D2a 那句「重拉能把语义未知的旧记录换成语义已知的新记录」是一致的：
// 换的是【语义的来源】（coverage 由新版写入），不是把字节删掉。
func (s *Store) DiscardCoverage() error {
	s.meta.Coverage = nil
	s.verified = map[tickflow.Span]bool{}
	return s.writeMeta()
}

// refuseUnknownSiblings 是迁移那一格（design.md §十八 三）。
//
// 触发条件很窄：我们正要在 dir 里【新建】一个库（`<self>.meta` 不存在）。
//
// ⚠️ 「只在新建那一刻扫一次」读起来像个偷懒，所以把射程写下来（评审方 2026-09-10 判它是
// 正确的射程而不是缺口，并要求这三行落进注释）：
//
//	本函数防的是一个【只在新建那一刻存在】的危险：在旧数据旁边悄悄建一个空库
//	而「目录里已有合法库时再开另一个周期」不会让旧数据消失 ⇒ 那一刻没有这个危险
//	而真去开那个旧周期时，readMeta 读到 format=1 ⇒ DecodeMeta 当场拒
//	⇒ **旧数据两条路都护着**：新建这一侧靠本函数，直接开这一侧靠 format 判定
//
// 而若同目录里还有一份【本版读不懂的】`.meta`，那说明这里躺着一份**周期未知**的旧数据 ——
// 本版之前 `Open` 无条件开 `1m.dat`，而当时两个源的 `Periods` 都只有 `Daily`
// ⇒ **那些库其实是「日线数据躺在一个叫 1m.dat 的文件里」。**
//
// 🔴 悄悄在它旁边新建一个空库，就是一次**静默的数据消失** ——
// 旧数据还在盘上，而库当它不存在。所以这里拒绝，并把碰到的文件名报出来。
//
// ⚠️ 「读不懂」包含两格，它们是**两件事**：
//
//	DecodeMeta 报错     ⇒ format 不在本版已知集合内（E1b）
//	format 字段缺失     ⇒ A3 那一格；它是 v0.3 之前写的，语义未知
//
// ⛔ 而本函数**不做迁移**：它答不了「那份数据到底是哪个周期」——
// 那要人去看。自作主张改名，正是本仓在 refdata 那一节记过的「修补」。
func refuseUnknownSiblings(dir, self string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var bad []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".meta") || n == self+".meta" {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, n))
		if rerr != nil {
			return rerr
		}
		m, derr := DecodeMeta(b)
		switch {
		case derr != nil:
			bad = append(bad, fmt.Sprintf("%s（%v）", n, derr))
		case m.Format == nil:
			bad = append(bad, fmt.Sprintf("%s（没有 format 字段——v0.3 之前写的，语义未知）", n))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s 里有 %s；"+
		"本版把周期写进文件名，而那些文件是【周期写死成 1m】的那一版留下的，"+
		"它们装的是哪个周期没有任何东西记着。"+
		"⇒ 在旁边新建一个空库会让那份数据静默消失，所以这里拒绝。"+
		"处置要人做，三步，缺一步都走不完："+
		"一 确认那份数据到底是哪个周期——本版答不了这个问题，只有你知道当时同步的是什么。⚠️ 猜错了会把两个周期拼进同一个库，而【那正是这里拒绝的理由】——下面两步只保证这个过程走得完，保证不了第一步猜得对；"+
		"二 把那两个文件改成该周期的落盘名（名字见 tickflow.PeriodDirName，例如日线是 1d.dat / 1d.meta）；"+
		"三 把那份 .meta 里的 format 从 1 改成 2——.meta 的字段一个都没变，升版本只是【周期已经进了文件名】这件事的信号，所以直接改那个数字是安全的",
		errUnknownSibling, dir, strings.Join(bad, "；"))
}
