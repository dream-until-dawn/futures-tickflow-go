package tickflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// 这一份的体例照 store/segfile 那两片：**每条约束都要有一个会红的反例**，
// 而且反例要核【报的是哪一条】，不是只核「它红了」。
//
// 理由是本仓撞过的那一格：一个只会报「有问题」的检查器，
// 和一个打偏了位置、恰好也报「有问题」的检查器，输出一模一样。

var cst = time.FixedZone("CST", 8*3600)

func ms(y, mo, d, h, mi int) int64 {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, cst).UnixMilli()
}

// —— Period 的密封 ——

// TestPeriodIsSealed 核对 Period 真的封住了。
//
// ⚠️ 「包外实现不了」这件事**编译期才看得见**，测试里跑不出来——
// 所以这里查的是那个封口本身：接口里必须有一个【不可导出】的方法。
// 它比不了「有人在包内加了第三种周期」，那由下面那条查。
func TestPeriodIsSealed(t *testing.T) {
	rt := reflect.TypeOf((*Period)(nil)).Elem()
	sealed := ""
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if m.PkgPath != "" { // 非空 ⇒ 不可导出
			sealed = m.Name
		}
	}
	if sealed == "" {
		t.Fatalf("Period 没有不可导出的方法 ⇒ 包外任何带 String() 的类型都能实现它，"+
			"包括 time.Duration。现有方法：%d 个", rt.NumMethod())
	}
	t.Logf("封口方法是 %s（不可导出）", sealed)
}

// periodSamples 是【每种周期一个实例】。
//
// 它是手写的，而它**不会悄悄变旧**：下面 TestPeriodImplementorsAreEnumerated
// 拿【源码里真正实现了封口方法的类型】来核这张表的键。
// 两半各补对方的盲区：
//
//	扫源码那半    抓「加了一种周期而没登记」——它枚举的是性质，不是我写下的名字
//	这张表那半    给出【实例】，才拿得到 reflect.Type 去查可比较性
//
// 上一版没有前一半：它 range 一个手写的 []Period 再和另一个手写的 []string 比，
// **两张表同一只手、同一个文件** ⇒ 加第三种时一个字都不会变。
// 而它的注释写着「加一种周期时它会红」——**那句是假的**。
// 评审方 2026-09-09 用对照组实测：包内加一个第三种 ⇒ 它照样 PASS。
// ⇒ 那是本仓记过的 `assert plain == total` 那一族：**一个不可能红的断言。**
var periodSamples = map[string]Period{
	"IntradayPeriod": MustIntraday(1),
	"CalendarPeriod": CalendarPeriod(0),
}

// periodImplementors 扫【本包的源码】，找出所有带封口方法 isPeriod 的类型。
//
// 判据按性质划：**「有 isPeriod 方法」就是「实现了 Period」**，
// 这正是密封那句话本身的定义，不是一份我另外维护的名单。
// 范围是本目录下所有 .go（含 _test.go）—— 包内任何地方加一种，它都看得见。
func periodImplementors(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读目录：%v", err)
	}
	fset := token.NewFileSet()
	var out []string
	files := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", e.Name(), err)
		}
		if f.Name.Name != "tickflow" {
			continue // 外部测试包（tickflow_test）不算本包
		}
		files++
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Name.Name != "isPeriod" {
				continue
			}
			typ := fn.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if id, ok := typ.(*ast.Ident); ok {
				out = append(out, id.Name)
			}
		}
	}
	if files == 0 {
		t.Fatal("一个本包 .go 都没解析到 —— 这条在空集上恒绿，先修判据")
	}
	sort.Strings(out)
	return out
}

// TestPeriodImplementorsAreEnumerated 盯住「周期在类型层面是穷举的」这句话。
//
// 加一种周期时它会红 —— **而这一次是真的会**：左边那份是从源码扫出来的。
// 红了之后要做的不是把名字补进表，是先确认这几处都想过新的那一种：
// Capabilities.Since 的键、各源的 Caps、以及聚合口径。
func TestPeriodImplementorsAreEnumerated(t *testing.T) {
	found := periodImplementors(t)
	if len(found) == 0 {
		t.Fatal("源码里一个 isPeriod 方法都没扫到 —— 封口没了，或者判据打偏了")
	}
	registered := make([]string, 0, len(periodSamples))
	for k := range periodSamples {
		registered = append(registered, k)
	}
	sort.Strings(registered)

	if !reflect.DeepEqual(found, registered) {
		t.Fatalf("Period 的实现者与 periodSamples 对不上。\n"+
			"  源码里扫到：%v\n  periodSamples 登记：%v\n"+
			"  ⇒ 新增一种周期时，先确认 Capabilities.Since 的键、各源的 Caps、"+
			"以及聚合口径都想过它，再补这张表。", found, registered)
	}
	t.Logf("源码里扫到 %d 种实现者：%v", len(found), found)
}

// TestPeriodImplementorsAreComparable 查一条**原本没写下来**的不变量。
//
// ⛔ `Period` 要求实现者【可比较】：`Capabilities.Since` 拿它当 map 键，
// `Supports` 用 `==`。而不可比较的实现者**编译期一声不响**，
// 到运行期才 `panic: hash of unhashable type`。
//
// ⚠️ **密封挡不住它** —— 密封挡的是包外，而不可比较的类型可以从包【内】加进来。
// 今天没炸，是因为恰好只有 `struct{min int}` 与 `int` 两种，两种都可比较：
// **又是一个碰巧成立的性质替一句声明背书。**
// （评审方 2026-09-09 实测：一个带 []string 字段的实现者放进 Since ⇒ 当场 panic。）
func TestPeriodImplementorsAreComparable(t *testing.T) {
	if len(periodSamples) == 0 {
		t.Fatal("periodSamples 是空的 —— 这条在空集上恒绿")
	}
	for name, p := range periodSamples {
		rt := reflect.TypeOf(p)
		if !rt.Comparable() {
			t.Errorf("%s 不可比较 ⇒ 放进 Capabilities.Since 会在运行期 "+
				"panic: hash of unhashable type，而编译期一声不响", name)
			continue
		}
		// 不只查类型，连 map 键这条真实用法一起走一遍 ——
		// Comparable() 是【类型】的性质，而 panic 发生在【使用】那一刻。
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s 当 map 键时 panic：%v", name, r)
				}
			}()
			m := map[Period]time.Duration{}
			m[p] = time.Hour
			_ = m[p]
		}()
	}
}

// —— BarRequest.Validate ——

func goodReq() BarRequest {
	return BarRequest{
		Symbol: Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2701},
		Period: MustIntraday(1),
		From:   20260901,
		To:     20260904,
	}
}

func TestBarRequestValidate(t *testing.T) {
	if err := goodReq().Validate(); err != nil {
		t.Fatalf("干净的请求应当通过，却报：%v\n"+
			"  ⇒ 基线不绿的话，下面每一条反例都不能说明任何事", err)
	}

	cases := []struct {
		name string
		mut  func(*BarRequest)
		want string // 失败信息里必须出现的字样 —— 核【报的是哪一条】
	}{
		{"交易所空", func(r *BarRequest) { r.Symbol.Exchange = "" }, "Symbol 不完整"},
		{"品种空", func(r *BarRequest) { r.Symbol.Product = "" }, "Symbol 不完整"},
		{"周期 nil", func(r *BarRequest) { r.Period = nil }, "Period 是 nil"},
		{"From 非法", func(r *BarRequest) { r.From = 0 }, "From=0"},
		{"To 非法", func(r *BarRequest) { r.To = 99999999 }, "To=99999999"},
		{"起晚于止", func(r *BarRequest) { r.From, r.To = r.To, r.From }, "晚于"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := goodReq()
			c.mut(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("这一处改动应当被拦下，却通过了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("红了，但报的不是要它报的那一条。\n  期望含：%s\n  实际：%v", c.want, err)
			}
		})
	}
}

// —— Capabilities.Validate ——

func TestCapabilitiesValidate(t *testing.T) {
	m1, d1 := MustIntraday(1), CalendarPeriod(1)
	good := Capabilities{
		Periods:   []Period{m1, d1},
		MaxBars:   1023,
		BatchDays: BatchDaysUnbounded,
		ClientUse: ClientUseHTTP,
		Since:     map[Period]TradingDay{m1: 20200506, d1: 20090327},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("干净的能力声明应当通过，却报：%v", err)
	}
	if !good.Supports(m1) || !good.Supports(d1) {
		t.Fatalf("Supports 认不出自己 Periods 里的周期")
	}
	if good.Supports(MustIntraday(5)) {
		t.Fatalf("Supports 认出了一个不在 Periods 里的周期 ⇒ 它恒真，等于没有")
	}

	t.Run("Periods 里有而 Since 里没有", func(t *testing.T) {
		bad := good
		bad.Since = map[Period]TradingDay{m1: 20200506} // 少了 d1
		err := bad.Validate()
		if err == nil {
			t.Fatal("少一条起点应当被拦下——读出来会是零值，而「忘了填」必须查得出来")
		}
		if !strings.Contains(err.Error(), "不在 Since 里") {
			t.Fatalf("红了，但报的不是那一条：%v", err)
		}
	})

	// 这一条在旧形状（Depth map[Period]time.Duration）下【根本写不出来】：
	// 任何 Duration 都是「合法」的，包括 0。换成 TradingDay 之后它才有话可说。
	t.Run("Since 里的值不是合法交易日", func(t *testing.T) {
		bad := good
		bad.Since = map[Period]TradingDay{m1: 20200506, d1: 0}
		err := bad.Validate()
		if err == nil {
			t.Fatal("零值起点应当被拦下 —— 这正是换成 TradingDay 买到的那一格")
		}
		if !strings.Contains(err.Error(), "不是一个合法交易日") {
			t.Fatalf("红了，但报的不是那一条：%v", err)
		}
	})

	t.Run("MaxBars 负数", func(t *testing.T) {
		bad := good
		bad.MaxBars = -1
		// ⛔ BatchDays 的零值必须被拒 —— 它说不清是「想好了」还是「忘了」（登记⑫ 同族）。
		//
		// ⚠️ 而这一格是【实现时撞出来的】：加上这个字段之后，
		// **两个真实源的 Caps 当场变红**（cffexsource / sinasource 各一条）——
		// 那正好证明 Validate() 真的作用在它们身上，而不只是作用在这个 fixture 上。
		for _, c := range []struct {
			name string
			v    int
		}{{"零值", 0}, {"负数但不是那个具名常量", -2}} {
			bad := good
			bad.BatchDays = c.v
			if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "BatchDays") {
				t.Errorf("BatchDays=%s（%d）应当被拒，实得 %v", c.name, c.v, err)
			}
		}
		// —— ClientUse 同族：零值不合法，而两个方向的后果【相反】 ——
		//
		//	想好了不走 HTTP 却被当成走 ⇒ **每一次同步都被诬告**（长期误报会关掉它自己）
		//	忘了填却被当成不走        ⇒ 「源绕开了闸门」这件事**永远沉默**
		//
		// ⇒ 两个方向都坏 ⇒ 不能有默认值。
		for _, c := range []struct {
			name string
			v    ClientUse
		}{{"零值", 0}, {"越出已知集合", ClientUse(7)}, {"负数", ClientUse(-1)}} {
			bad := good
			bad.ClientUse = c.v
			if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "ClientUse") {
				t.Errorf("ClientUse=%s（%d）应当被拒，实得 %v", c.name, int(c.v), err)
			}
		}
		// 对照：两种合法形态都要通过 —— 否则上面那条可能是「ClientUse 恒被拒」。
		for _, v := range []ClientUse{ClientUseHTTP, ClientUseNone} {
			ok := good
			ok.ClientUse = v
			if err := ok.Validate(); err != nil {
				t.Errorf("ClientUse=%v 是合法形态，却被拒：%v", v, err)
			}
		}

		// 对照：两种合法形态都要通过 —— 否则上面那条可能是「BatchDays 恒被拒」。
		for _, v := range []int{1, 5, BatchDaysUnbounded} {
			okc := good
			okc.BatchDays = v
			if err := okc.Validate(); err != nil {
				t.Errorf("BatchDays=%d 是合法形态，却被拒：%v", v, err)
			}
		}

		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "MaxBars=-1") {
			t.Fatalf("期望报 MaxBars 负数，得到：%v", err)
		}
	})
}

// —— CheckBars：六条约定里机械查得动的那五条 ——

func goodBars() []Bar {
	return []Bar{
		{Ts: ms(2026, 9, 1, 9, 0), TsEnd: ms(2026, 9, 1, 9, 1), TradingDay: 20260901},
		{Ts: ms(2026, 9, 2, 9, 0), TsEnd: ms(2026, 9, 2, 9, 1), TradingDay: 20260902},
	}
}

var checkNow = ms(2026, 9, 5, 0, 0)

func TestCheckBars(t *testing.T) {
	// 基线：干净的一组必须过。基线不绿 ⇒ 下面每一条都不说明任何事。
	if err := CheckBars(goodReq(), goodBars(), checkNow); err != nil {
		t.Fatalf("干净的一组应当通过，却报：%v", err)
	}
	// 空切片合法：停牌、上市前都会缺，那不是错误。
	if err := CheckBars(goodReq(), nil, checkNow); err != nil {
		t.Fatalf("空切片应当合法（停牌/上市前），却报：%v", err)
	}

	cases := []struct {
		name string
		mut  func(*BarRequest, []Bar) []Bar
		want string
	}{
		{"逆序", func(_ *BarRequest, b []Bar) []Bar {
			b[0], b[1] = b[1], b[0]
			return b
		}, "没有按升序"},

		{"重复 Ts", func(_ *BarRequest, b []Bar) []Bar {
			b[1].Ts = b[0].Ts
			return b
		}, "与上一根重复"},

		{"Ts 零值", func(_ *BarRequest, b []Bar) []Bar {
			b[0].Ts = 0
			return b
		}, "非正"},

		{"TsEnd 没填", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TsEnd = 0
			return b
		}, "不大于 Ts"},

		{"TradingDay 没填", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TradingDay = 0
			return b
		}, "TradingDay=0 不合法"},

		{"落在区间之外", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TradingDay = 20260910
			return b
		}, "之外"},

		{"未完结的那一根", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TsEnd = checkNow + 1
			return b
		}, "未完结的绝不进入"},

		{"请求本身就不合法", func(r *BarRequest, b []Bar) []Bar {
			r.From, r.To = r.To, r.From
			return b
		}, "请求本身不合法"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, b := goodReq(), goodBars()
			b = c.mut(&r, b)
			err := CheckBars(r, b, checkNow)
			if err == nil {
				t.Fatalf("这一处改动应当被拦下，却通过了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("红了，但报的不是要它报的那一条。\n  期望含：%s\n  实际：%v", c.want, err)
			}
		})
	}
}

// TestCheckBarsReportsEveryViolation 盯住「一次报全部」这句话。
//
// 只报第一条的话，写实现的人要跑六遍才能修完六处——
// 而跑第二遍的前提是他知道还有第二处，**那正是这句承诺要替他知道的事**。
func TestCheckBarsReportsEveryViolation(t *testing.T) {
	b := goodBars()
	b[0].TsEnd = 0      // 一处
	b[1].TradingDay = 0 // 两处
	b[1].TsEnd = 0      // 三处（同一根的另一条）
	err := CheckBars(goodReq(), b, checkNow)
	if err == nil {
		t.Fatal("三处违反，却全绿")
	}
	msg := err.Error()
	for _, want := range []string{"第 0 根", "第 1 根", "TradingDay=0 不合法"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报告里少了「%s」——它只报了一部分：\n%v", want, err)
		}
	}
	if n := strings.Count(msg, "\n") + 1; n < 3 {
		t.Errorf("三处违反只报出 %d 行 ⇒ 它不是「一次报全部」：\n%v", n, err)
	}
}
