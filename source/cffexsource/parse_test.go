package cffexsource

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture %s：%v", name, err)
	}
	return b
}

// —— 一、真实响应对账：数从 testdata/README.md 读，不在 Go 里抄第二份 ——

var tableRow = regexp.MustCompile(`^\| ([^|]*?) *\| *\*{0,2}(\d+)\*{0,2} *\|`)

// readTable 从 testdata/README.md 那张表里读出「量 → 值」。
//
// ⚠️ 数不在这里再抄一遍。抄一份就有两个来源，而两个来源今天一致、明天分叉 ——
// 本仓为这件事拆过文件、也为它写过 sinasource 那条同名守卫。
func readTable(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, ln := range strings.Split(string(read(t, "README.md")), "\n") {
		m := tableRow.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		k := strings.TrimSpace(strings.ReplaceAll(m[1], "*", ""))
		k = strings.ReplaceAll(k, "`", "")
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		out[k] = n
	}
	return out
}

// TestFixturesMatchTheirTable 是 testdata/README.md 亲口承诺过的那条守卫。
func TestFixturesMatchTheirTable(t *testing.T) {
	tbl := readTable(t)
	if len(tbl) == 0 {
		t.Fatal("从 README.md 里一行表都没读到 —— 这条在空集上恒绿，先修正则")
	}
	raw := read(t, "daily_20260908.xml")

	// 直接在原始字节上数，**不经过被测的 ParseDaily** ——
	// 用被测者去核它自己的期望值，那是同义反复。
	total := strings.Count(string(raw), "<dailydata>")
	ids := regexp.MustCompile(`<instrumentid>(.*?)</instrumentid>`).FindAllStringSubmatch(string(raw), -1)
	futures, options := 0, 0
	for _, m := range ids {
		// ⚠️ **这里【不调】被测的 IsOption**，直接写判据本身。
		// 用被测者去核它自己，它一反过来断言也跟着反 —— 两边一起错，测试照绿。
		// （实测：第一版这里调了 IsOption，把它整个反过来 ⇒ **一条都不红**。）
		if strings.Contains(strings.TrimSpace(m[1]), "-") {
			options++
		} else {
			futures++
		}
	}

	for _, c := range []struct {
		key string
		got int
	}{
		{"条目总数", total},
		{"纯期货", futures},
		{"期权", options},
	} {
		want, ok := tbl[c.key]
		if !ok {
			t.Errorf("README.md 的表里没有「%s」这一行 —— 表和守卫对不上了", c.key)
			continue
		}
		if c.got != want {
			t.Errorf("%s：文件里 %d，README.md 写的是 %d", c.key, c.got, want)
		}
	}

	rows, err := ParseDaily(raw)
	if err != nil {
		t.Fatalf("解析真实响应失败：%v", err)
	}
	if len(rows) != futures {
		t.Fatalf("ParseDaily 给了 %d 行，而原始字节里期货是 %d 条", len(rows), futures)
	}
	t.Logf("总 %d / 期货 %d / 期权 %d，ParseDaily 与原始字节一致", total, futures, options)
}

// TestSettlementIsActuallyThere 是这个包存在的理由，拿真实数据钉住。
//
// 新浪对中金所八个品种**全都不给**结算价（0/122、0/156），
// 而这里 **28/28 全有** —— 两边合起来才是「分流」这个结论，
// **只有一边的话，「全都有」也可能是解析器把这个字段读坏了。**
func TestSettlementIsActuallyThere(t *testing.T) {
	rows, err := ParseDaily(read(t, "daily_20260908.xml"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(rows) == 0 {
		t.Fatal("一行都没有 —— 下面的计数恒真")
	}
	tbl := readTable(t)
	nz, pnz := 0, 0
	for _, r := range rows {
		if r.Settle != 0 {
			nz++
		}
		if r.PreSettle != 0 {
			pnz++
		}
	}
	if want := tbl["期货 settlementprice 非零"]; nz != want {
		t.Errorf("结算价非零 %d 条，README.md 写的是 %d", nz, want)
	}
	if want := tbl["期货 presettlementprice 非零"]; pnz != want {
		t.Errorf("昨结算非零 %d 条，README.md 写的是 %d", pnz, want)
	}
	if nz != len(rows) {
		t.Errorf("%d/%d 条有结算价 —— 中金所这边应当是全有", nz, len(rows))
	}
}

// TestEightProductsNotSix 钉住那处刚改正的文档过期。
//
// probe.md / contract.md 此前把中金所品种写成**六个**（漏了 IM 与 TL），
// 而 `calendar/embedded` 里一直是八个。**清单写死了当时看到的样子。**
func TestEightProductsNotSix(t *testing.T) {
	rows, err := ParseDaily(read(t, "daily_20260908.xml"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ProductID] = true
	}
	for _, p := range []string{"IF", "IH", "IC", "IM", "T", "TF", "TS", "TL"} {
		if !seen[p] {
			t.Errorf("真实数据里没有品种 %s —— 八品种这个结论要重新量", p)
		}
	}
	if len(seen) < 8 {
		t.Errorf("只看到 %d 个品种：%v", len(seen), seen)
	}
}

// —— 二、过滤那一条：它写反了，结果长得一样 ——

func TestIsOption(t *testing.T) {
	for _, c := range []struct {
		id   string
		want bool
	}{
		{"IC2609", false}, {"IF2609", false}, {"T2612", false}, {"TL2612", false},
		{"HO2609-C-2500", true}, {"IO2609-P-4000", true},
	} {
		if got := IsOption(c.id); got != c.want {
			t.Errorf("IsOption(%q) = %v，期望 %v", c.id, got, c.want)
		}
	}
}

// TestFilterDirectionMatters 是「过滤写反」那一格的对照组。
//
// ⛔ 写反的后果不是报错，是**拿到 686 条期权而不是 28 条期货** ——
// 而「解析出了 N 行」这个层面上两者长得一样。
// ⇒ 所以要断言的不是「行数 > 0」，是**每一行都不是期权**。
func TestFilterDirectionMatters(t *testing.T) {
	rows, err := ParseDaily(read(t, "daily_20260908.xml"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, r := range rows {
		// 同上：判据在这里写死，**不借道 IsOption**。
		if strings.Contains(r.InstrumentID, "-") {
			t.Fatalf("%s 是期权，却出现在结果里 —— 过滤方向反了", r.InstrumentID)
		}
	}
	// 而反过来也要有样本：原始文件里必须真的有期权，否则这条断言恒真。
	if !strings.Contains(string(read(t, "daily_20260908.xml")), "-C-") {
		t.Fatal("fixture 里一条期权都没有 —— 上面那条断言恒真，等于没测")
	}
}

// —— 三、三条不静默 ——

func TestEmptyResultIsAnErrorNotSilence(t *testing.T) {
	// 一份只有期权的 XML：过滤完是空的，而那**多半意味着过滤反了**，不是「那天没交易」。
	onlyOptions := `<?xml version="1.0" encoding="UTF-8"?><dailydatas>` +
		`<dailydata><instrumentid>HO2609-C-2500</instrumentid><tradingday>20260908</tradingday>` +
		`<closeprice>404</closeprice><settlementprice>398.4</settlementprice></dailydata>` +
		`</dailydatas>`
	rows, err := ParseDaily([]byte(onlyOptions))
	if !errors.Is(err, ErrNoFutures) {
		t.Fatalf("过滤完为空应当报 ErrNoFutures，得到 err=%v rows=%d", err, len(rows))
	}
	if rows != nil {
		t.Errorf("报错的同时还给了 %d 行", len(rows))
	}
}

func TestNotXMLIsAnError(t *testing.T) {
	for _, s := range []string{"", "<html>403 Forbidden</html>", "not xml at all"} {
		if _, err := ParseDaily([]byte(s)); !errors.Is(err, ErrNotXML) {
			t.Errorf("输入 %q 应当报 ErrNotXML，得到 %v", s, err)
		}
	}
}

// TestBlankFieldsBecomeZeroNotError 钉住那个实测出来的形态。
//
// 空字段在这份 XML 里是 `<openprice>\n</openprice>` —— **空白，不是缺标签**。
// 实测：74 条期权的 openprice 长这样。声明成 float64 会让整份 XML 解析失败。
func TestBlankFieldsBecomeZeroNotError(t *testing.T) {
	x := "<?xml version=\"1.0\"?><dailydatas><dailydata>" +
		"<instrumentid>IC2609</instrumentid><tradingday>20260908</tradingday>" +
		"<openprice>\n</openprice><closeprice>7743.4</closeprice>" +
		"<settlementprice>7736</settlementprice></dailydata></dailydatas>"
	rows, err := ParseDaily([]byte(x))
	if err != nil {
		t.Fatalf("空白字段不该让整份 XML 判死：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("期望 1 行，得到 %d", len(rows))
	}
	if rows[0].Open != 0 {
		t.Errorf("空白 openprice 应当读成 0，得到 %v", rows[0].Open)
	}
	if rows[0].Close != 7743.4 || rows[0].Settle != 7736 {
		t.Errorf("同一条里非空字段被带坏了：close=%v settle=%v", rows[0].Close, rows[0].Settle)
	}
}

func TestNonNumericIsAnError(t *testing.T) {
	x := "<?xml version=\"1.0\"?><dailydatas><dailydata>" +
		"<instrumentid>IC2609</instrumentid><tradingday>20260908</tradingday>" +
		"<settlementprice>N/A</settlementprice></dailydata></dailydatas>"
	_, err := ParseDaily([]byte(x))
	if err == nil {
		t.Fatal("非数字应当报错而不是吞成 0 —— 0 在结算价这一格是缺失的伪装")
	}
	if !strings.Contains(err.Error(), "settlementprice") {
		t.Fatalf("红了，但报的不是那个字段：%v", err)
	}
}

// TestZeroSettleIsNotMappedHere 钉住一条【明确不做】。
//
// bar.go 里 `s==0 ⇒ NaN` 那条映射**不在本包做**：本包只报告 XML 里写的是什么，
// 把「0 该不该当成缺失」的处置留给知道语境的组装层。
// ⇒ 有人把那个映射搬进来时，这条会红，并逼他先想清楚该在哪一层做。
//
// ⚠️ **射程：它分辨不了「写了 0」与「整个字段空白」** —— 把下面那个 `0` 换成空白，
// 它照样绿（登记⑳）。**它守的是「本包不做映射」，不是「本包分得清那两者」。**
// 后一件由 TestNoBlankSettlementInFutures 的到期条件盯着。
func TestZeroSettleIsNotMappedHere(t *testing.T) {
	x := "<?xml version=\"1.0\"?><dailydatas><dailydata>" +
		"<instrumentid>IC2609</instrumentid><tradingday>20260908</tradingday>" +
		"<settlementprice>0</settlementprice></dailydata></dailydatas>"
	rows, err := ParseDaily([]byte(x))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rows[0].Settle != 0 {
		t.Fatalf("本包不做 0→NaN 的映射（那是组装层的事），得到 %v", rows[0].Settle)
	}
}

// TestNoBlankSettlementInFutures 是登记⑳ 的【到期条件】，不是一条普通断言。
//
// ⛔ 本包把「字段空白」和「写了 0」抹成同一个 0（parseNum 那段有对照组）。
// 今天这件事没有后果，理由是**真实数据里期货结算价一个空白都没有** ——
// 而那是一条【读数】，不是一条保证。
//
// ⇒ 所以这条测试盯的是那个读数 —— **而它订阅的是【这份 fixture】，不是世界**：
//
//	响的时刻    **下次有人刷新 fixture、而新数据里有空白结算价**
//	不响的情况  **没人刷新 ⇒ 世界变了它也不响**
//
// ⇒ **不是「以后记得看」，是「出现那一天会有东西响」。**
//
// ⚠️ 它在【原始字节】上判空白，**不经过 ParseDaily** —— 经过它就什么都看不见了，
// 那正是这条缺陷本身。
func TestNoBlankSettlementInFutures(t *testing.T) {
	raw := string(read(t, "daily_20260908.xml"))
	item := regexp.MustCompile(`(?s)<dailydata>(.*?)</dailydata>`)
	field := func(b, k string) string {
		m := regexp.MustCompile(`(?s)<` + k + `>(.*?)</` + k + `>`).FindStringSubmatch(b)
		if m == nil {
			return ""
		}
		return strings.TrimSpace(m[1])
	}
	futures, blank := 0, 0
	for _, m := range item.FindAllStringSubmatch(raw, -1) {
		id := field(m[1], "instrumentid")
		if id == "" || strings.Contains(id, "-") { // 期权：判据写死，不借道 IsOption
			continue
		}
		futures++
		if field(m[1], "settlementprice") == "" || field(m[1], "presettlementprice") == "" {
			blank++
			t.Errorf("%s 的结算价或昨结算是【空白】—— 登记⑳ 的到期条件到了："+
				"本包会把它读成 0，而 0 与「真的是 0」不可分辨。"+
				"⇒ 现在要决定：*float64，还是并列一个「哪些字段是空白」的集合。", id)
		}
	}
	if futures == 0 {
		t.Fatal("一条期货都没扫到 —— 这条在空集上恒绿，先修判据")
	}
	t.Logf("期货 %d 条，结算价空白 %d 条（到期条件未触发）", futures, blank)
}

// TestMissingTradingDayIsAnErrorInParse 补的是**真实调用路径上**那一道闸。
//
// ⛔ 上一轮我给 AssembleDay 的空串检查补了测试，而评审方实测指出：
//
//	assemble.go 的那道闸  有测试，**而它 0 个非测试调用方**
//	parse.go 的这道闸     **没有测试**，而它是真实管线上唯一挡着的那一道
//	（对照组：把它整段拆掉 ⇒ 全包仍然全绿）
//
// ⇒ 判据：**补测试之前先问「真实调用路径上，第一道挡住它的闸是哪一道」** ——
// 补在那一道上，不是补在【讨论发生】的那一道上。
//
// ⚠️ 而更难看的是它的沉默：**测试的位置本身就是一句关于「哪里危险」的断言**，
// A 有测试而 B 没有，会让下一个人以为这条路已经守住了。
// **显式的断言会被审、被反驳；沉默的断言只会被继承。**
func TestMissingTradingDayIsAnErrorInParse(t *testing.T) {
	mk := func(td string) []byte {
		return []byte(`<?xml version="1.0"?><dailydatas><dailydata>` +
			`<instrumentid>IC2609</instrumentid>` + td +
			`<closeprice>1</closeprice><settlementprice>1</settlementprice>` +
			`</dailydata></dailydatas>`)
	}
	// 三种形态，实测它们同归一路（TrimSpace 之后都是空）
	for _, c := range []struct{ name, td string }{
		{"空元素", "<tradingday></tradingday>"},
		{"缺标签", ""},
		{"纯空白", "<tradingday>   </tradingday>"},
	} {
		rows, err := ParseDaily(mk(c.td))
		if err == nil {
			t.Errorf("%s：少了交易日却通过了，拿到 %d 行——"+
				"这一格是真实管线上唯一挡着的那道闸", c.name, len(rows))
			continue
		}
		if !strings.Contains(err.Error(), "tradingday") {
			t.Errorf("%s：红了，但报的不是那一条：%v", c.name, err)
		}
	}
	// 对照：正常一行必须过，否则上面三条在「什么都不通过」上恒真
	rows, err := ParseDaily(mk("<tradingday>20260908</tradingday>"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("正常一行应当通过：rows=%d err=%v", len(rows), err)
	}
}
