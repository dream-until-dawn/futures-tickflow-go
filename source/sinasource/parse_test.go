package sinasource

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 这一份的体例：**先拿真实响应对账，再拿合成用例逐条钉判据。**
//
// 只有合成用例的话，证明的是「解析器能解析我以为的那种输入」；
// 只有真实响应的话，边角情况（乱序、非数字、空数组）取不到。两边都要。

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture %s：%v", name, err)
	}
	return b
}

// —— 一、真实响应对账 ——

// fixtureRow 是 testdata/README.md 那张表里的一行。
type fixtureRow struct {
	file        string
	bars        int
	zeroSettle  int
	first, last string
	line        int
}

var tableRow = regexp.MustCompile(
	"^\\| `([^`]+\\.jsonp)` \\|[^|]*\\| *(\\d+) *\\| *(\\d+) *\\| *([\\d-]+) *\\| *([\\d-]+) *\\|")

// readFixtureTable 从 testdata/README.md 里读那张表。
//
// ⚠️ 表里的数【不在 Go 代码里再抄一份】。抄一份就有两个来源，
// 而两个来源今天一致、明天分叉 —— 本仓为这件事拆过一次文件（docs_test.go）。
func readFixtureTable(t *testing.T) []fixtureRow {
	t.Helper()
	b := read(t, "README.md")
	var out []fixtureRow
	for i, ln := range strings.Split(string(b), "\n") {
		m := tableRow.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		z, _ := strconv.Atoi(m[3])
		out = append(out, fixtureRow{m[1], n, z, m[4], m[5], i + 1})
	}
	return out
}

// TestFixturesMatchTheirTable 是 testdata/README.md 亲口承诺过的那条守卫。
//
// 它盯的是**说明文档与它描述的文件之间**的一致 ——
// 和本仓那三处过期状态句同一族：说明写下时为真，之后每一次重新取数都在削弱它，
// 而重新取数的人不会经过它。
func TestFixturesMatchTheirTable(t *testing.T) {
	rows := readFixtureTable(t)
	if len(rows) == 0 {
		t.Fatal("从 testdata/README.md 里一行表都没读到 —— 这条守卫在空集上恒绿，先修正则")
	}
	for _, r := range rows {
		t.Run(r.file, func(t *testing.T) {
			got, err := ParseDaily(read(t, r.file))
			if err != nil {
				t.Fatalf("解析真实响应失败：%v", err)
			}
			if len(got) != r.bars {
				t.Errorf("根数：文件里 %d，README.md:%d 写的是 %d", len(got), r.line, r.bars)
			}
			zero := 0
			for _, row := range got {
				if math.IsNaN(row.Settle) {
					zero++
				}
			}
			if zero != r.zeroSettle {
				t.Errorf("s 为 0 的根数：文件里 %d，README.md:%d 写的是 %d",
					zero, r.line, r.zeroSettle)
			}
			if len(got) > 0 {
				if got[0].Date != r.first {
					t.Errorf("首日：文件里 %s，README.md:%d 写的是 %s", got[0].Date, r.line, r.first)
				}
				if got[len(got)-1].Date != r.last {
					t.Errorf("末日：文件里 %s，README.md:%d 写的是 %s",
						got[len(got)-1].Date, r.line, r.last)
				}
			}
		})
	}
}

// TestSettleZeroIsTheProductNotTheParser 是那张表的**对照组**这件事本身。
//
// 「T2612 全是 0」单独证明不了「s==0 要映射成 NaN」——
// **全是 0 也可能是解析器把这个字段读坏了。**
// 两份「读得出非零」＋一份「真的全 0」，才把「字段坏了」这个解释排除掉。
func TestSettleZeroIsTheProductNotTheParser(t *testing.T) {
	nonZero := func(name string) (n, total int) {
		rows, err := ParseDaily(read(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, r := range rows {
			if !math.IsNaN(r.Settle) && r.Settle != 0 {
				n++
			}
		}
		return n, len(rows)
	}
	rb, rbAll := nonZero("daily_RB2610.jsonp")
	ta, taAll := nonZero("daily_TA2701.jsonp")
	tt, ttAll := nonZero("daily_T2612.jsonp")

	if rb != rbAll || rb == 0 {
		t.Fatalf("RB2610 应当 %d 根全都读得出非零结算价，实得 %d ⇒ **解析器坏了**，"+
			"下面那条「中金所全 0」就不能说明任何事", rbAll, rb)
	}
	if ta != taAll || ta == 0 {
		t.Fatalf("TA2701 应当 %d 根全都读得出非零结算价，实得 %d", taAll, ta)
	}
	if tt != 0 {
		t.Fatalf("T2612 应当一根非零结算价都没有（中金所系统性缺失），实得 %d/%d", tt, ttAll)
	}
	t.Logf("对照组成立：RB %d/%d 非零、TA %d/%d 非零、T %d/%d 非零",
		rb, rbAll, ta, taAll, tt, ttAll)
}

// —— 二、null 与 [] 必须分得开 ——

func TestNullIsNotEmpty(t *testing.T) {
	rows, err := ParseDaily(read(t, "daily_null.jsonp"))
	if !errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("不存在的合约应当报 ErrUnknownSymbol，得到 err=%v rows=%d", err, len(rows))
	}
	if rows != nil {
		t.Errorf("报错的同时还返回了 %d 行 —— 调用方可能只看 rows", len(rows))
	}
}

func TestEmptyArrayIsNotAnError(t *testing.T) {
	rows, err := ParseDaily([]byte("var _=([]);"))
	if err != nil {
		t.Fatalf("空数组是合法的「这段没有数据」，不该报错：%v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("空数组应当解析出 0 行，得到 %d", len(rows))
	}
}

// —— 三、合成用例：逐条钉判据 ——

const oneRow = `var _=([{"d":"2026-03-16","o":"1.5","h":"2.5","l":"0.5","c":"2.0",` +
	`"v":"10","p":"20","s":"1.75"}]);`

func TestParseDailyFieldsLandInTheRightPlace(t *testing.T) {
	rows, err := ParseDaily([]byte(oneRow))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("期望 1 行，得到 %d", len(rows))
	}
	r := rows[0]
	// 逐个字段各给一个【互不相同】的值 —— 值都一样的话，
	// 字段接错线（o 读成 h）在测试里看不出来。
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"Open", r.Open, 1.5}, {"High", r.High, 2.5}, {"Low", r.Low, 0.5},
		{"Close", r.Close, 2.0}, {"Volume", r.Volume, 10}, {"OpenInterest", r.OpenInterest, 20},
		{"Settle", r.Settle, 1.75},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v，期望 %v ⇒ 字段接错线了", c.name, c.got, c.want)
		}
	}
	if r.Date != "2026-03-16" {
		t.Errorf("Date = %q", r.Date)
	}
}

func TestSettleZeroBecomesNaN(t *testing.T) {
	rows, err := ParseDaily([]byte(strings.Replace(oneRow, `"s":"1.75"`, `"s":"0.000"`, 1)))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !math.IsNaN(rows[0].Settle) {
		t.Fatalf("s=\"0.000\" 应当变成 NaN，得到 %v —— "+
			"0 是个看起来正常的价格，拿去逐日盯市会算出一整天的灾难而全程不报错", rows[0].Settle)
	}
}

func TestNonNumericIsAnErrorNotZero(t *testing.T) {
	for _, f := range []string{"o", "h", "l", "c", "v", "p", "s"} {
		t.Run(f, func(t *testing.T) {
			bad := regexp.MustCompile(`"`+f+`":"[^"]*"`).ReplaceAllString(oneRow, `"`+f+`":"N/A"`)
			if bad == oneRow {
				t.Fatalf("替换没生效 ⇒ 这个子用例什么都没测")
			}
			rows, err := ParseDaily([]byte(bad))
			if err == nil {
				t.Fatalf("字段 %s 是 \"N/A\"，应当报错而不是吞成 0；得到 %+v", f, rows)
			}
			if !strings.Contains(err.Error(), f+"=\"N/A\"") {
				t.Fatalf("红了，但报的不是字段 %s：%v", f, err)
			}
		})
	}
}

// TestParseDailyDoesNotSort 钉住那句「不排序」。
//
// 悄悄排一次会让上游乱序这个事实**消失**，而 Source 的升序约定
// 应当由组装那一层连同 tickflow.CheckBars 一起负责 —— 那样乱序会被【报出来】。
func TestParseDailyDoesNotSort(t *testing.T) {
	src := `var _=([` +
		`{"d":"2026-03-18","o":"1","h":"1","l":"1","c":"1","v":"1","p":"1","s":"1"},` +
		`{"d":"2026-03-16","o":"1","h":"1","l":"1","c":"1","v":"1","p":"1","s":"1"}]);`
	rows, err := ParseDaily([]byte(src))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rows[0].Date != "2026-03-18" || rows[1].Date != "2026-03-16" {
		t.Fatalf("顺序被改了：得到 %s, %s —— 解析层排序会把「上游乱序」这个事实抹掉",
			rows[0].Date, rows[1].Date)
	}
}

// —— 四、外壳 ——

// TestStripToleratesAChangedPrefix 钉住那个决定：**不校验前缀**。
//
// 前缀那段脚本注释是新浪塞的，校验它只会在它改的那天，
// 把一份【本可以解析】的响应判死。
func TestStripToleratesAChangedPrefix(t *testing.T) {
	if !strings.HasPrefix(string(read(t, "daily_T2612.jsonp")), jsonpPrefix) {
		t.Fatalf("真实响应已经不是这个前缀了 ⇒ jsonpPrefix 这个常量记的东西过期了：%q", jsonpPrefix)
	}
	changed := `/*<script>whatever();</script>*/` + "\n" + oneRow
	if _, err := ParseDaily([]byte(changed)); err != nil {
		t.Fatalf("换了前缀就解析不了 ⇒ 那个决定没落实：%v", err)
	}
}

func TestNotJSONPShape(t *testing.T) {
	for _, s := range []string{"", "not jsonp at all", "<html>404</html>"} {
		if _, err := ParseDaily([]byte(s)); !errors.Is(err, ErrNotJSONP) {
			t.Errorf("输入 %q 应当报 ErrNotJSONP，得到 %v", s, err)
		}
	}
}
