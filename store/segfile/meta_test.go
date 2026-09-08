package segfile

import (
	"errors"
	"reflect"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// 本文件是 docs/design.md §6.1 那张不变量表的【片一】：9 条 × 2 侧 = 18 次。
//
// 命名规则（表下面那条约束③要的）：TestInvariant<编号>_Red / _Green。
//
//	_Red    喂一个【违反该不变量】的输入，判据必须响
//	_Green  喂一个【不违反该不变量】的输入，判据必须不响
//
// ⚠️ 两侧缺一不可，而且必须是【同一条判据、两个只差这一条的输入】：
// 只有 _Red 证明的是「这个测试会红」，不是「它因为那条不变量而红」。
//
// ⚠️ 本版【缺席】的（写在这儿，免得下一个人以为漏了）：
//
//	B1 B2 B3 C1 C2 C3a   要 .dat，在片二
//	C3b D2b              要 SyncReport / Source.Caps，两者都还不存在（见 pending.txt）
//	                     ⇒ 挪到「做 Source 那一版」
//	那条 go/ast 收集测试（断言编号集合恰好等于表里 17 个）在片二一并落。
//	⚠️ 这句话的有效期【到片二为止】；片二若推迟，这段要重写，不能沿用。

// —— 测试用日历。故意把周末排除在外，好让 A2 有真东西可测。——
//
//	20200731 周五 · 20200803 周一   ← 中间隔着周末，而它们是【相邻交易日】
//	20200804 周二                   ← 用它来造「中间隔着一个交易日」的反例
func testCalendar(t *testing.T) (tickflow.Calendar, tickflow.ProductKey) {
	t.Helper()
	cal, err := embedded.New([]tickflow.TradingDay{
		20200731, 20200803, 20200804, 20200805, 20200806,
	})
	if err != nil {
		t.Fatalf("造测试日历失败：%v", err)
	}
	return cal, tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "au"}
}

func span(from, to tickflow.TradingDay, bars, days int) tickflow.Span {
	return tickflow.Span{From: from, To: to, Bars: bars, Days: days}
}

// ───────────────────────── A1a：coverage 按 From 升序 ─────────────────────────

func TestInvariantA1a_Red(t *testing.T) {
	// 乱序，但【不重叠】—— 这样红了只可能是 A1a，不会是 A1b。
	bad := []tickflow.Span{span(20200803, 20200804, 2, 2), span(20200731, 20200731, 1, 1)}
	err := ValidateCoverage(bad)
	if err == nil {
		t.Fatal("A1a 没有响：coverage 乱序却通过了")
	}
	if !errors.Is(err, errUnordered) {
		t.Fatalf("A1a 响了，但响的是别的判据：%v", err)
	}
	// fixture 要坏对地方：它【不该】把 A1b 也带响。
	if errors.Is(err, errOverlap) {
		t.Fatalf("这个 fixture 同时触发了 A1b —— 那它证明不了 A1a：%v", err)
	}
}

func TestInvariantA1a_Green(t *testing.T) {
	// 与 _Red 只差【顺序】这一件事。
	good := []tickflow.Span{span(20200731, 20200731, 1, 1), span(20200803, 20200804, 2, 2)}
	if err := ValidateCoverage(good); err != nil {
		t.Fatalf("A1a 误伤：升序且不重叠的 coverage 被判不合法：%v", err)
	}
}

// ───────────────────────── A1b：coverage 各段互不重叠 ─────────────────────────

func TestInvariantA1b_Red(t *testing.T) {
	// 重叠，但【有序】—— 这样红了只可能是 A1b。
	bad := []tickflow.Span{span(20200731, 20200804, 3, 3), span(20200803, 20200806, 3, 3)}
	err := ValidateCoverage(bad)
	if err == nil {
		t.Fatal("A1b 没有响：coverage 有重叠段却通过了")
	}
	if !errors.Is(err, errOverlap) {
		t.Fatalf("A1b 响了，但响的是别的判据：%v", err)
	}
	if errors.Is(err, errUnordered) {
		t.Fatalf("这个 fixture 同时触发了 A1a —— 那它证明不了 A1b：%v", err)
	}
}

func TestInvariantA1b_Green(t *testing.T) {
	// 与 _Red 只差【第二段的起点】：挪到前一段结束之后就不重叠了。
	good := []tickflow.Span{span(20200731, 20200803, 2, 2), span(20200804, 20200806, 3, 3)}
	if err := ValidateCoverage(good); err != nil {
		t.Fatalf("A1b 误伤：紧邻但不重叠的两段被判重叠：%v", err)
	}
}

// ───────────────── A2：相邻性按【交易日】算，不按自然日 ─────────────────

func TestInvariantA2_Red(t *testing.T) {
	cal, k := testCalendar(t)
	// 20200731（周五）与 20200803（周一）之间【没有交易日】⇒ 必须合成一段。
	// 按自然日判会认为中间隔了两天 ⇒ 留下一个假洞，这正是要红的那件事。
	got, err := NormalizeCoverage(cal, k, []tickflow.Span{
		span(20200731, 20200731, 10, 1),
		span(20200803, 20200803, 20, 1),
	})
	if err != nil {
		t.Fatalf("A2：合并时报错：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("A2 没有响：周五与周一是相邻交易日，应当合成 1 段，实得 %d 段 %v"+
			"\n（按自然日判就会得到 2 段 —— 17 年下来约 900 个假洞）", len(got), got)
	}
	if got[0].From != 20200731 || got[0].To != 20200803 {
		t.Fatalf("A2：合并后的区间不对：%+v", got[0])
	}
	if got[0].Bars != 30 || got[0].Days != 2 {
		t.Fatalf("A2：合并后两个计数没有相加：%+v", got[0])
	}
}

func TestInvariantA2_Green(t *testing.T) {
	cal, k := testCalendar(t)
	// 与 _Red 只差一件事：中间【真的】隔着一个交易日（20200803）没被覆盖。
	got, err := NormalizeCoverage(cal, k, []tickflow.Span{
		span(20200731, 20200731, 10, 1),
		span(20200804, 20200804, 20, 1),
	})
	if err != nil {
		t.Fatalf("A2：报错：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("A2 误伤：中间隔着 20200803 这个交易日，不该合并，实得 %d 段 %v", len(got), got)
	}
}

// ───────────── A3：format 用 *int，「没写」与「写了 0」分得开 ─────────────

func TestInvariantA3_Red(t *testing.T) {
	// 「没写」与「写了 0」必须给出【不同】的结果。
	// 若 Format 是 int 而不是 *int，两者都会是 0 ⇒ 无从分辨，这一格就是要挡住那个。
	noField, errNo := DecodeMeta([]byte(`{"coverage":[]}`))
	zero, errZero := DecodeMeta([]byte(`{"format":0,"coverage":[]}`))

	if errNo != nil {
		t.Fatalf("A3：format 缺失不该是错误（它走 D2a）：%v", errNo)
	}
	if noField.Format != nil {
		t.Fatalf("A3 没有响：字段没写，Format 却不是 nil（=%d）", *noField.Format)
	}
	if errZero == nil {
		t.Fatalf("A3 没有响：写了 0 却被当成了合法版本，得到 %+v", zero)
	}
	// 两条路必须【可区分】——这才是这条不变量本身。
	if errors.Is(errZero, errFutureFormat) {
		t.Fatalf("A3：写了 0 被判成「超前版本」，归错类了：%v", errZero)
	}
}

func TestInvariantA3_Green(t *testing.T) {
	// 与 _Red 只差一件事：写的是本版认识的版本号。
	m, err := DecodeMeta([]byte(`{"format":1,"coverage":[]}`))
	if err != nil {
		t.Fatalf("A3 误伤：format=1 是合法的，却报错：%v", err)
	}
	if m.Format == nil || *m.Format != FormatVersion {
		t.Fatalf("A3 误伤：format=1 没被读成 %d：%+v", FormatVersion, m)
	}
}

// ───────────── D1：三种「答不了」必须互相分得开 ─────────────

func TestInvariantD1_Red(t *testing.T) {
	three := []struct {
		name string
		err  error
	}{
		{"ErrUncovered(日历)", tickflow.ErrUncovered},
		{"ErrSpanUnverified(存储)", tickflow.ErrSpanUnverified},
		{"ErrLegacyMeta(存储)", tickflow.ErrLegacyMeta},
	}
	// 任意两者【都不许】互相 Is —— 否则三种补救动作（换日历／走一遍／要人决定）会被合并。
	for i, a := range three {
		for j, b := range three {
			if i == j {
				continue
			}
			if errors.Is(a.err, b.err) {
				t.Errorf("D1 没有响：%s 与 %s 分不开 —— 而它们的补救动作完全不同",
					a.name, b.name)
			}
		}
	}
}

func TestInvariantD1_Green(t *testing.T) {
	// 同一条判据（errors.Is）在【该认出来】的时候必须认得出，包一层也认得出。
	wrapped := DecideLegacyMetaWrapped()
	if !errors.Is(wrapped, tickflow.ErrLegacyMeta) {
		t.Fatalf("D1 误伤：包了一层之后 ErrLegacyMeta 认不出来了：%v", wrapped)
	}
	if errors.Is(wrapped, tickflow.ErrSpanUnverified) {
		t.Fatalf("D1：包装把它认成了另一个哨兵：%v", wrapped)
	}
}

// DecideLegacyMetaWrapped 只为 D1_Green 造一个「被包了一层」的真实错误。
func DecideLegacyMetaWrapped() error {
	_, err := DecideLegacyMeta(false)
	return err
}

// ───────── D2a：format 缺失的判定取决于源可不可重放，不是常数 ─────────

func TestInvariantD2a_Red(t *testing.T) {
	yes, errYes := DecideLegacyMeta(true)
	no, errNo := DecideLegacyMeta(false)
	// 「不是常数」= 两种输入必须给出不同的判定。写成常数的实现会在这里红。
	if yes == no {
		t.Fatalf("D2a 没有响：可重放与不可重放给出了同一个判定（%v）"+
			"\n—— 写成常数判定时，不可重放的源会永远重试", yes)
	}
	if yes != LegacyDiscard {
		t.Fatalf("D2a：可重放应当作废重拉，实得 %v", yes)
	}
	if no != LegacyUnverified {
		t.Fatalf("D2a：不可重放应当标 unverified 并停，实得 %v", no)
	}
	if errYes != nil {
		t.Fatalf("D2a：可重放那一支不该要人介入：%v", errYes)
	}
	if !errors.Is(errNo, tickflow.ErrLegacyMeta) {
		t.Fatalf("D2a：不可重放那一支应当要一个显式决定（ErrLegacyMeta），实得 %v", errNo)
	}
}

func TestInvariantD2a_Green(t *testing.T) {
	// 与 _Red 同一条判据，喂【不违反】的那一侧：可重放的源不该被要求人工介入。
	if _, err := DecideLegacyMeta(true); err != nil {
		t.Fatalf("D2a 误伤：可重放的源被要求了一个显式决定：%v", err)
	}
}

// ───────── E1a：.meta 无法解析时报错，不拿默认值顶上 ─────────

func TestInvariantE1a_Red(t *testing.T) {
	m, err := DecodeMeta([]byte(`{"format":1,"coverage":[`)) // 截断的 JSON
	if err == nil {
		t.Fatalf("E1a 没有响：坏 JSON 被读成了 %+v —— 连「它说了什么」都不知道时，任何猜测都是编的", m)
	}
	if !errors.Is(err, errMetaUnreadable) {
		t.Fatalf("E1a 响了，但归错了类：%v", err)
	}
	if m != nil {
		t.Fatalf("E1a：报错的同时还返回了一个 Meta（%+v）—— 那正是「拿默认值顶上」", m)
	}
}

func TestInvariantE1a_Green(t *testing.T) {
	// 与 _Red 只差一件事：JSON 是完整的。
	if _, err := DecodeMeta([]byte(`{"format":1,"coverage":[]}`)); err != nil {
		t.Fatalf("E1a 误伤：合法 JSON 被判读不懂：%v", err)
	}
}

// ───────── E1b：format 大于当前已知版本时报错，不照旧版语义读 ─────────

func TestInvariantE1b_Red(t *testing.T) {
	m, err := DecodeMeta([]byte(`{"format":2,"coverage":[]}`))
	if err == nil {
		t.Fatalf("E1b 没有响：未来版本被照 v%d 读了，得到 %+v", FormatVersion, m)
	}
	if !errors.Is(err, errFutureFormat) {
		t.Fatalf("E1b 响了，但归错了类：%v", err)
	}
}

func TestInvariantE1b_Green(t *testing.T) {
	// 与 _Red 只差一个数字：等于本版认识的版本号。
	if _, err := DecodeMeta([]byte(`{"format":1,"coverage":[]}`)); err != nil {
		t.Fatalf("E1b 误伤：format 等于当前版本却报了「超前」：%v", err)
	}
}

// ───────── F1：.meta 不得含任何由交易日历派生的字段 ─────────
//
// ⚠️ 射程：这是一道【绊线】，不是语义检查。
// 它保证「往 .meta 加字段这件事会被看见」，**不保证机器能判断新字段是不是日历派生**。
// 判断仍然要人做 —— 而绊线的作用是让人【必须】做一次。

func structFields(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

func TestInvariantF1_Red(t *testing.T) {
	// 造一个「偷偷把日历答案抄进来」的结构体，同一个助手必须看得出它与声明不符。
	type metaWithCalendarField struct {
		Format       *int
		Coverage     []tickflow.Span
		IsTradingDay bool // ← 正是 F1 要挡的那类字段
	}
	want := []string{"Format", "Coverage"}
	got := structFields(metaWithCalendarField{})
	if reflect.DeepEqual(got, want) {
		t.Fatal("F1 没有响：多了一个日历派生字段，字段集却被判成一样的")
	}
	// 而且要能指出多的是哪一个 —— 否则报错的人不知道该看哪儿。
	if len(got) != len(want)+1 || got[len(got)-1] != "IsTradingDay" {
		t.Fatalf("F1：绊线响了，但没指出多出来的字段：%v", got)
	}
}

func TestInvariantF1_Green(t *testing.T) {
	// 真实结构体的字段集必须【恰好】是声明的这些。
	// 加字段 ⇒ 这里红 ⇒ 加的人必须回来确认它不是日历的答案。
	if got, want := structFields(Meta{}), []string{"Format", "Coverage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("F1：Meta 的字段集变了\n实得 %v\n声明 %v\n"+
			"⚠️ 若你在加字段：先确认它【不是】由交易日历派生的"+
			"（是不是交易日／时段模板／节假日…）——那是把日历的答案抄进存储，"+
			"抄件会过期，而过期的抄件不会报错。确认过再把它加进这张声明里。", got, want)
	}
	if got, want := structFields(tickflow.Span{}), []string{"From", "To", "Bars", "Days"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("F1：Span 的字段集变了\n实得 %v\n声明 %v\n（同上：先确认新字段不是日历派生）", got, want)
	}
}
