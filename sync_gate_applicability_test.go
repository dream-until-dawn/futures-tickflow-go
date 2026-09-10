package tickflow

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// 本文件是【闸门那条检查的适用性】：③「不适用」那一格 ＋ ⑧ `attempts > 0` 那半句。

// noHTTPSource 是一个**不走那个 `*http.Client`** 的源（websocket / 本地 / 缓存那一类）。
//
// ⚠️ 它照样**收下** client —— `SourceFactory` 的签名就是那样；
// 它只是不用。而那正是问题所在：**「不用」与「绕开」在计数为 0 上不可分辨。**
type noHTTPSource struct{ use ClientUse }

func (s *noHTTPSource) Bars(_ context.Context, req BarRequest) ([]Bar, error) {
	return []Bar{{Ts: 1, TsEnd: 61000, TradingDay: req.From, Close: 1.5}}, nil
}

func (s *noHTTPSource) Caps(ProductKey) Capabilities {
	return Capabilities{Periods: []Period{Daily},
		Since: map[Period]TradingDay{Daily: 20200101}, BatchDays: 1, ClientUse: s.use}
}

func syncerWith(t *testing.T, src Source) *Syncer {
	t.Helper()
	syn, err := NewSyncer(SyncerConfig{
		Calendar: week(), Store: &fakeStore{}, Pacer: pacing.NoPacing(),
		Timeout:   30 * time.Second,
		NewSource: func(c *http.Client) Source { return src },
	})
	if err != nil {
		t.Fatal(err)
	}
	return syn
}

// TestClientUseNoneIsNotAccused ③：一个不走 HTTP 的源**不许被指控**。
//
// 🔴 **上一版每一次同步都诬告它**（评审方 2026-09-09 造，我复现，读数一致）：
//
//	Bars=5 · UngatedSource 1 条 · Complete() = false —— 每一次，永远
//
// ⛔ 而它的分量**不是「误报烦人」**：
// **一个长期误报的告警，最终会关掉它自己。**
// 一份永远 `Complete()==false` 的报告，读的人三周后就不看那一格了 ——
// **于是它当初要抓的那个真假绿，又变回看不见。**
//
// ⇒ 解法照 `NightAbsentOK` 那一族：**「不适用」要能被说出来，不能靠一个 0 去猜。**
// 而说它的人是**源自己**（`Caps.ClientUse`），不是调用方 ——
// 让调用方说就又变成一句不可核的承诺，而那正是 `SourceFactory` 刚取消掉的东西。
func TestClientUseNoneIsNotAccused(t *testing.T) {
	syn := syncerWith(t, &noHTTPSource{use: ClientUseNone})
	rep, err := syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatalf("不走 HTTP 的源不该让同步失败：%v", err)
	}
	// 前提：它确实同步到了东西（否则下面两条什么也没验）。
	if rep.Bars == 0 {
		t.Fatal("一根都没同步 —— 前提没成立")
	}
	if len(rep.UngatedSource) != 0 {
		t.Errorf("一个声明了 ClientUseNone 的源被指控了 %d 次：%v\n"+
			"  ⇒ 它的闸门计数【本来就该】是 0", len(rep.UngatedSource), rep.UngatedSource)
	}
	if rep.UngatedOK {
		t.Error("UngatedOK 为真，而这个源在这一格上【不适用】——\n" +
			"  ⇒ 「不适用」与「适用且没问题」必须分得开（同 NightAbsentOK）")
	}
	if !rep.Complete() {
		t.Errorf("一次干净的同步却不 Complete：%s\n"+
			"  ⇒ 而一份永远不 Complete 的报告，读的人三周后就不看了", rep)
	}
}

// TestClientUseHTTPSourceThatSkipsHTTPIsAccused 对照：**同一个桩**，只把声明改成
// `ClientUseHTTP` ⇒ 必须被指控。
//
// ⛔ 少了这一条，上面那格也可能是「它对谁都不指控」——
// 而两条合起来才钉住那个**判别**：指控与否跟着【声明】走，不跟着实现走。
// ⚠️ 而这正是「一条一格突变」的用例版：两格之间**只差一个字段的取值**。
func TestClientUseHTTPSourceThatSkipsHTTPIsAccused(t *testing.T) {
	syn := syncerWith(t, &noHTTPSource{use: ClientUseHTTP})
	rep, err := syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Bars == 0 {
		t.Fatal("一根都没同步 —— 前提没成立")
	}
	if len(rep.UngatedSource) == 0 {
		t.Error("源声明了 ClientUseHTTP 却一次都没走那个 client，而报告一个字没说")
	}
	if !rep.UngatedOK {
		t.Error("UngatedOK 为假，而这个源声明了 ClientUseHTTP ⇒ 它【适用】")
	}
	if rep.Complete() {
		t.Error("报告说 Complete()，而这次同步既没有限流也没有超时")
	}
	// ⑨：**措辞只报读数，不报成因。**
	// 读数错是一格，成因错是两格 —— 而这条留声是会误报的那一类。
	for _, n := range rep.UngatedSource {
		for _, banned := range []string{"多半", "没有用交给它"} {
			if strings.Contains(n, banned) {
				t.Errorf("留声里含成因（%q）：%s\n"+
					"  ⇒ 一条会误报的留声，措辞里不要含成因", banned, n)
			}
		}
	}
}

// TestNoAttemptsMeansNoAccusation ⑧：**一次数据都没要过时，不许指控。**
//
// 🔴 这一格的来历值得写下来，它是**两个人各测一半拼成的**：
//
//	评审方的 G1  只摘掉判据里 `attempts > 0` 那半句 ⇒ **全仓 0 红**
//	             ⇒ 那半句当时【没有任何断言守着】
//	我的 PROBE2  请求区间取周六周日 ⇒ attempts == 0 ⇒ 我以为那条路径可达
//
// ⛔ **而我第一版照 PROBE2 写的这条测试是【空转的】** —— 自己的合取项突变抓到的：
// 删掉 `attempts > 0` 之后它**仍然绿**。
// 去读代码才明白：请求区间没有交易日时 `Sync` 在 `len(days) == 0` 那一行就
// **早退了，根本走不到闸门那一格**。
//
// > ⇒ **「构造出 attempts==0」与「构造出一个【走到那一行】的 attempts==0」是两回事。**
// > 我量到了前者，就以为拿到了后者 —— 而这正是四因表第二格（输入走不到那一行）。
//
// ⇒ 换成 **ctx 先取消**：有交易日、有 chunk、进了循环，
// 第一格 `ctx.Err()` 就返回 ⇒ `attempts == 0`，**而闸门那一格照样被走到**
// （实测：calls=0 · hits=0 · UngatedOK=true）。
// ⚠️ 而哪一半是谁量的，写在这儿：**两个人各测一半拼成的结论，要说清分工** ——
// 他量出「那半句没有断言」，我量出「那条路径可达」，**而可达那一半我第一次量错了。**
func TestNoAttemptsMeansNoAccusation(t *testing.T) {
	h := newHarness(t, pacing.NoPacing(), 1, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rep, err := h.syn.Sync(ctx, req(0), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实得 %v", err)
	}
	// —— 前提三条，缺一条这条测试就什么也不证明 ——
	if h.src.calls != 0 {
		t.Fatalf("前提没成立：源被调了 %d 次，而这一格要的是 0 次", h.src.calls)
	}
	// ⛔ 而**这一条是上一版缺的那个前提**：闸门那一格必须【被走到】。
	// UngatedOK 被赋过值 ⇒ 说明代码走过了那一段；早退的话它是零值 false。
	if !rep.UngatedOK {
		t.Fatalf("前提没成立：UngatedOK 是 false ⇒ 闸门那一格【没有被走到】，" +
			"这条测试是空转的（上一版正是这样：请求区间无交易日 ⇒ 在 len(days)==0 就早退了）")
	}
	if len(rep.UngatedSource) != 0 {
		t.Errorf("一次数据都没要过，却被指控了 %d 次：%v\n"+
			"  ⇒ 计数为 0 在这里是【因为压根没问】，不是因为绕开了闸门",
			len(rep.UngatedSource), rep.UngatedSource)
	}
}

// TestSyncValidatesCapsBeforeRelyingOnThem `Caps` 要在被依赖之前自证。
//
// 🔴 **这一格是量出来的**：`Capabilities.Validate()` 当时**生产侧零调用点**
// （2026-09-09 实测，只有两个源各自的测试在调它）。
// ⇒ 于是 `sync.go` 里那句「`BatchDays` 的零值在 `Capabilities.Validate()` 那一侧
// 已经被拒」**在生产侧是假的** —— 一个第三方 `Source` 带着零值会一路畅通。
//
// ⇒ 判据（那条的第三次）：**为一句「从 Y 那里保证」辩护之前，先 grep【Y 有没有被调用】。**
// 一个零调用点的检查器，它挡住的东西全在别人的想象里。
func TestSyncValidatesCapsBeforeRelyingOnThem(t *testing.T) {
	for _, c := range []struct {
		name string
		src  Source
		want string
	}{
		{"ClientUse 零值", &noHTTPSource{}, "ClientUse"},
		{"BatchDays 零值", &badCapsSource{}, "BatchDays"},
	} {
		t.Run(c.name, func(t *testing.T) {
			syn := syncerWith(t, c.src)
			rep, err := syn.Sync(context.Background(), req(0), 0)
			if err == nil {
				t.Fatal("Caps 立不住，Sync 却照跑")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("红了，但报的不是那一条（期望含 %q）：%v", c.want, err)
			}
			if rep.Complete() {
				t.Error("Caps 立不住的那份报告说 Complete()")
			}
		})
	}
}

// badCapsSource 的 BatchDays 是零值 —— 它到不了循环，因为 Caps 先被拒。
type badCapsSource struct{}

func (s *badCapsSource) Bars(context.Context, BarRequest) ([]Bar, error) { return nil, nil }
func (s *badCapsSource) Caps(ProductKey) Capabilities {
	return Capabilities{Periods: []Period{Daily},
		Since: map[Period]TradingDay{Daily: 20200101}, ClientUse: ClientUseHTTP}
}
