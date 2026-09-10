package shinnyref

import (
	"errors"
	"fmt"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件的用例**全部取自 2026-09-10 在全量（7671 条 FUTURE）上量到的真实形状**，
// 而不是我想出来的边界。读数见 docs/design.md 那一节。

// ───────── 时段端点：它是偏移，不是时刻 ─────────

// TestParseOffsetAcceptsWhatTimeParseRejects 是 `ParseOffset` 存在的**全部理由**。
//
// ⚠️ 对照组在断言里：每一格都先证 `time.Parse` 对它怎么反应，
// **否则「我们自己写了个解析器」读起来像是没事找事。**
func TestParseOffsetAcceptsWhatTimeParseRejects(t *testing.T) {
	for _, c := range []struct {
		in         string
		want       time.Duration
		stdRejects bool // time.Parse 拒不拒它
	}{
		{"09:00:00", 9 * time.Hour, false},
		{"23:30:00", 23*time.Hour + 30*time.Minute, false},
		{"25:00:00", 25 * time.Hour, true},                 // 全量 1013 条：夜盘到次日 01:00
		{"26:30:00", 26*time.Hour + 30*time.Minute, true},  // 全量 374 条：到次日 02:30
		{"22:30:00", 22*time.Hour + 30*time.Minute, false}, // 全量仅 1 条的孤例，而它是真的
	} {
		t.Run(c.in, func(t *testing.T) {
			// 前提：time.Parse 的反应确实是本格假设的那一种。
			_, stdErr := time.Parse("15:04:05", c.in)
			if got := stdErr != nil; got != c.stdRejects {
				t.Fatalf("前提不成立，本格作废：time.Parse 拒它=%v，而本格是按 %v 写的（err=%v）",
					got, c.stdRejects, stdErr)
			}
			got, err := ParseOffset(c.in)
			if err != nil {
				t.Fatalf("ParseOffset(%q) 报错：%v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("ParseOffset(%q) = %v，要 %v", c.in, got, c.want)
			}
		})
	}
}

// TestParseOffsetStillRejectsGarbage —— 不设小时上界，不等于什么都收。
func TestParseOffsetStillRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"", "9:00:00", "09:00", "09:60:00", "09:00:60", "aa:00:00", "-1:00:00", "09:00:00:00",
	} {
		if got, err := ParseOffset(in); err == nil {
			t.Errorf("ParseOffset(%q) 收下了，得到 %v", in, got)
		}
	}
}

// ───────── night 的三态 ─────────

func entryJSON(tt string) []byte {
	return []byte(fmt.Sprintf(`{
      "class":"FUTURE","exchange_id":"SHFE","product_id":"au",
      "delivery_year":2020,"delivery_month":2,
      "volume_multiple":1000,"price_tick":0.02,"expire_datetime":1581692400.0,
      "trading_time":%s}`, tt))
}

const dayOnly = `[["09:00:00","10:15:00"],["10:30:00","11:30:00"],["13:30:00","15:00:00"]]`

// TestNightThreeStatesStayApart 是「三态不许合并」那条规矩的机械形态。
//
// 🔴 承重的不是「三种都解得出来」，是**它们两两不相等** ——
// 一个把 unset 与 empty 挤成同一个读数的实现，也能让「三种都解得出来」为真。
func TestNightThreeStatesStayApart(t *testing.T) {
	cases := map[string]struct {
		tt    string
		want  NightState
		nRang int
	}{
		"没有 night 键": {`{"day":` + dayOnly + `}`, NightUnset, 0},
		"night 是空数组": {`{"day":` + dayOnly + `,"night":[]}`, NightEmpty, 0},
		"night 有时段":  {`{"day":` + dayOnly + `,"night":[["21:00:00","26:30:00"]]}`, NightRanges, 1},
	}
	seen := map[NightState]string{}
	for name, c := range cases {
		got, err := DecodeContract(entryJSON(c.tt))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Sessions.Night.State != c.want {
			t.Fatalf("%s: State=%v，要 %v", name, got.Sessions.Night.State, c.want)
		}
		if n := len(got.Sessions.Night.Ranges); n != c.nRang {
			t.Fatalf("%s: Ranges 有 %d 段，要 %d", name, n, c.nRang)
		}
		if prev, dup := seen[c.want]; dup {
			t.Fatalf("两种输入解成了同一个状态：%q 与 %q 都是 %v —— 【三态塌了】",
				prev, name, c.want)
		}
		seen[c.want] = name
	}
	if len(seen) != 3 {
		t.Fatalf("只解出 %d 种状态，要 3 种 —— 三态塌了", len(seen))
	}
	// ⚠️ 前提自检：零值不是任何一种（一个没被解出来的 Night 不该长得像 unset）。
	var zero Night
	if zero.State == NightUnset || zero.State == NightEmpty || zero.State == NightRanges {
		t.Fatal("零值 Night 落在了三态之一上 —— 那样「没解出来」与「上游没写」分不开")
	}
}

// ───────── 缺字段：报错，不补 ─────────

func TestMissingFieldIsRefusedNotFilled(t *testing.T) {
	full := `{"class":"FUTURE","exchange_id":"SHFE","product_id":"au",
	  "delivery_year":2020,"delivery_month":2,"volume_multiple":1000,
	  "price_tick":0.02,"expire_datetime":1581692400.0,
	  "trading_time":{"day":` + dayOnly + `}}`
	// 前提：整份是解得出来的 —— 否则下面每一格都会因为别的原因红。
	if _, err := DecodeContract([]byte(full)); err != nil {
		t.Fatalf("前提不成立，本格作废：完整的一条都解不出来：%v", err)
	}
	for _, field := range []string{
		"exchange_id", "product_id", "delivery_year", "delivery_month",
		"volume_multiple", "price_tick", "expire_datetime", "trading_time",
	} {
		t.Run(field, func(t *testing.T) {
			// 把那个键改名 ⇒ 等价于「上游没给它」。
			broken := replaceKey(full, field)
			if broken == full {
				t.Fatalf("前提不成立：%q 在样本里没找到，本格没测到东西", field)
			}
			_, err := DecodeContract([]byte(broken))
			if err == nil {
				t.Fatalf("缺 %s 而它收下了 —— 那正是「自作聪明的修补」那一格", field)
			}
			if !errors.Is(err, errMissing) && field != "trading_time" {
				t.Fatalf("缺 %s 报的不是「缺字段」：%v", field, err)
			}
		})
	}
}

func replaceKey(s, key string) string {
	old := `"` + key + `"`
	nw := `"zz_` + key + `"`
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + nw + s[i+len(old):]
		}
	}
	return s
}

// ───────── 合约身份：从交割年月拼，不从三位代码猜 ─────────

// TestSymbolComesFromDeliveryNotFromCode 钉住那条会咬人的歧义。
func TestSymbolComesFromDeliveryNotFromCode(t *testing.T) {
	// 郑商所线格式是三位（ZC002）—— 而三位跨十年会撞。
	// 前提：本仓那个「按线格式解」的入口确实需要一个 asOf 才展得开。
	if _, err := tickflow.ParseNative("CZCE", "ZC002", 0); err == nil {
		t.Fatal("前提不成立，本格作废：三位年月不给 asOf 也解得开 ⇒ 那这条规矩不必存在")
	}
	raw := []byte(`{"class":"FUTURE","exchange_id":"CZCE","product_id":"ZC",
	  "delivery_year":2020,"delivery_month":2,"volume_multiple":100,
	  "price_tick":0.2,"expire_datetime":1581692400.0,
	  "trading_time":{"day":` + dayOnly + `}}`)
	got, err := DecodeContract(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := "CZCE.ZC2002"; got.Symbol.String() != want {
		t.Fatalf("Symbol = %q，要 %q —— 四位年月是本仓给 refdata 定的键，"+
			"三位会跨十年撞（见 ProductKey 上的注释）", got.Symbol.String(), want)
	}
}

// ───────── price_tick：两种 JSON 数字都要收得下 ─────────

func TestPriceTickTakesBothJSONNumberShapes(t *testing.T) {
	// 全量实测：int 5989 条 / float 1682 条。两种都是上游真的会给的。
	for _, c := range []struct {
		raw  string
		want float64
	}{
		{"1", 1},       // 螺纹 tick=1
		{"0.02", 0.02}, // 沪金
		{"50", 50},     // 碳酸锂
		{"0.005", 0.005},
	} {
		body := `{"class":"FUTURE","exchange_id":"SHFE","product_id":"rb",
		  "delivery_year":2020,"delivery_month":2,"volume_multiple":10,
		  "price_tick":` + c.raw + `,"expire_datetime":1581692400.0,
		  "trading_time":{"day":` + dayOnly + `}}`
		got, err := DecodeContract([]byte(body))
		if err != nil {
			t.Fatalf("price_tick=%s: %v", c.raw, err)
		}
		if got.PriceTick != c.want {
			t.Fatalf("price_tick=%s 解成 %v，要 %v", c.raw, got.PriceTick, c.want)
		}
	}
}

// ───────── 非 FUTURE：拒绝，而不是静默跳过 ─────────

func TestNonFutureIsRefusedLoudly(t *testing.T) {
	// 上游全量里有 8 个 class；本包只收一种。
	for _, cls := range []string{"FUTURE_OPTION", "FUTURE_CONT", "FUTURE_INDEX", "SPOT", "INDEX", ""} {
		body := `{"class":"` + cls + `","exchange_id":"SHFE","product_id":"au",
		  "delivery_year":2020,"delivery_month":2,"volume_multiple":1000,
		  "price_tick":0.02,"expire_datetime":1581692400.0,
		  "trading_time":{"day":` + dayOnly + `}}`
		if _, err := DecodeContract([]byte(body)); !errors.Is(err, errNotFuture) {
			t.Errorf("class=%q 没被按「不是期货」拒掉：%v", cls, err)
		}
	}
}

// ───────── 到期时刻：单位是毫秒，与 Bar.Ts 一致 ─────────

func TestExpireIsMillisecondsLikeBarTs(t *testing.T) {
	got, err := DecodeContract(entryJSON(`{"day":` + dayOnly + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(1581692400000); got.ExpireTs != want {
		t.Fatalf("ExpireTs = %d，要 %d（上游给的是 unix 秒，本仓的时刻单位是毫秒）",
			got.ExpireTs, want)
	}
}
