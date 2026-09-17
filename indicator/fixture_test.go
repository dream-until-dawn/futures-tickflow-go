package indicator

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件读 testdata/ 里的新浪原始响应（README 记着它们从哪来、为什么在这里）。
//
// ⚠️ 不走 source/sinasource：那个包的解析要注入日历（交易日与时段），而指标只要价格序列。
// 这里只解出 d / o / h / l / c / v / p / s 八个字段；结算价 0 照本库约定映射成 NaN，
// 成交额新浪不给 ⇒ NaN（Bar.Turnover 的注释）。

type fixtureRow struct {
	D, O, H, L, C, V, P, S string
}

func loadSinaDaily(t *testing.T, name string) []tickflow.Bar {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(b, []byte("var _=("))
	j := bytes.LastIndex(b, []byte(");"))
	if i < 0 || j <= i {
		t.Fatalf("%s 不是预期的 JSONP", name)
	}
	var rows []struct {
		D string `json:"d"`
		O string `json:"o"`
		H string `json:"h"`
		L string `json:"l"`
		C string `json:"c"`
		V string `json:"v"`
		P string `json:"p"`
		S string `json:"s"`
	}
	if err := json.Unmarshal(b[i+len("var _=("):j], &rows); err != nil {
		t.Fatalf("%s：%v", name, err)
	}
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("%s：%q 不是数：%v", name, s, err)
		}
		return v
	}
	out := make([]tickflow.Bar, len(rows))
	for k, r := range rows {
		d, err := strconv.Atoi(strings.ReplaceAll(r.D, "-", ""))
		if err != nil {
			t.Fatalf("%s：日期 %q：%v", name, r.D, err)
		}
		settle := num(r.S)
		if settle == 0 {
			settle = math.NaN()
		}
		out[k] = tickflow.Bar{
			TradingDay: tickflow.TradingDay(d),
			Open:       num(r.O), High: num(r.H), Low: num(r.L), Close: num(r.C),
			Volume: num(r.V), OpenInterest: num(r.P),
			Turnover: math.NaN(), Settle: settle,
		}
		if k > 0 && out[k].TradingDay <= out[k-1].TradingDay {
			t.Fatalf("%s：第 %d 根的日期 %s 不在前一根 %s 之后", name, k, out[k].TradingDay, out[k-1].TradingDay)
		}
	}
	return out
}

// TestIndicatorFixturesUntouched 钉住 testdata/README.md 那张表：根数、首末日、md5。
//
// 这几份是「真实响应」才有意义；有人换了文件或改了字节（哪怕只是格式化），
// 依赖它们的读数（收敛点、golden）就不再是 6.33 那次量的东西了。
func TestIndicatorFixturesUntouched(t *testing.T) {
	for _, c := range []struct {
		file        string
		md5         string
		n           int
		first, last tickflow.TradingDay
	}{
		{"daily_RB0.jsonp", "f96645228821067dad9084173631eb21", 4246, 20090327, 20260917},
		{"daily_RB2501.jsonp", "d45d3421e89523f2b7e11e569d85b132", 242, 20240116, 20250115},
		{"daily_RB2412.jsonp", "9b143df38daeebf3808030d8c6f6d686", 238, 20231218, 20241213},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		sum := md5.Sum(raw)
		if got := hex.EncodeToString(sum[:]); got != c.md5 {
			t.Errorf("%s 的 md5 是 %s，README 记的是 %s —— 文件被换过或改过字节", c.file, got, c.md5)
		}
		bars := loadSinaDaily(t, c.file)
		if len(bars) != c.n || bars[0].TradingDay != c.first || bars[len(bars)-1].TradingDay != c.last {
			t.Errorf("%s：%d 根 %s…%s，README 记的是 %d 根 %s…%s",
				c.file, len(bars), bars[0].TradingDay, bars[len(bars)-1].TradingDay, c.n, c.first, c.last)
		}
	}
}
