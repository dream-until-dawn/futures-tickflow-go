package proof

import (
	"errors"
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

// 拼接只合并同一品种的连续Records；不能把各年Series直接作为重复ProductKey提交。
func TestINEJoinedCalendarBoundaries(t *testing.T) {
	keys := []tf.ProductKey{{Exchange: tf.INE, Product: "lu"}, {Exchange: tf.INE, Product: "ec"}}
	grouped := map[tf.ProductKey][]verified.Record{}
	var direct []verified.Series
	for _, file := range []string{"ine-lu-2020-listed.json", "ine-lu-2021.json", "ine-lu-2022.json", "ine-lu-2023.json", "listed-2023.json", "ine-2024.json", "ine-2025.json", "ine-2026-to-1104.json"} {
		v := ineHistoricalFixture(t, file)
		for _, s := range v.Series {
			if s.Product == keys[0] || s.Product == keys[1] {
				direct = append(direct, s)
				grouped[s.Product] = append(grouped[s.Product], s.Records...)
			}
		}
	}
	joined := []verified.Series{{Product: keys[0], Records: grouped[keys[0]]}, {Product: keys[1], Records: grouped[keys[1]]}}
	cal, err := verified.New(joined)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key   tf.ProductKey
		from  tf.TradingDay
		count int
	}{{keys[0], 20200622, 1544}, {keys[1], 20230818, 776}} {
		from, to, ok := cal.Covers(tc.key)
		if !ok || from != tc.from || to != 20261104 {
			t.Fatalf("scope %s %s %s", tc.key, from, to)
		}
		n := 0
		if err := cal.Walk(tc.key, from, to, func(tf.Day) bool { n++; return true }); err != nil || n != tc.count {
			t.Fatalf("walk %s %d %v", tc.key, n, err)
		}
		if _, err := cal.DayOf(tc.key, ymd(natural(tc.from).AddDate(0, 0, -1))); !errors.Is(err, tf.ErrUncovered) {
			t.Fatalf("before listing %v", err)
		}
		if _, err := cal.DayOf(tc.key, 20261105); !errors.Is(err, tf.ErrUncovered) {
			t.Fatalf("future %v", err)
		}
	}
	day, err := cal.DayAt(keys[0], instant(20250103, 1260))
	if err != nil || day.Num != 20250106 {
		t.Fatalf("cross weekend %s %v", day.Num, err)
	}
	if _, err := cal.DayAt(keys[0], instant(20231229, 1260)); !errors.Is(err, tf.ErrClosed) {
		t.Fatalf("known year-end no-night %v", err)
	}
	if _, err := cal.DayAt(keys[0], instant(20261104, 1260)); !errors.Is(err, tf.ErrUncovered) {
		t.Fatalf("unknown next trading day %v", err)
	}
	if _, err := verified.New(direct); err == nil || !strings.Contains(err.Error(), "重复品种") {
		t.Fatalf("direct append should reject duplicates: %v", err)
	}
	// 即便删的是已核休市自然日，也不能把资料孔洞悄悄当休市。
	rr := append([]verified.Record(nil), grouped[keys[0]]...)
	for i, r := range rr {
		if r.Day == 20221231 {
			rr = append(rr[:i], rr[i+1:]...)
			break
		}
	}
	if _, err := verified.New([]verified.Series{{Product: keys[0], Records: rr}}); err == nil || !strings.Contains(err.Error(), "缺日/乱序") {
		t.Fatalf("missing closed day should reject: %v", err)
	}
}

// 复核其余四个已核品种的消费者拼接，上市首日与跨午夜不能被年界截断。
func TestSHFEJoinedCalendarBoundaries(t *testing.T) {
	grouped := map[tf.ProductKey][]verified.Record{}
	for _, file := range []string{"listed-2023.json", "shfe-2024.json", "shfe-2025.json", "shfe-2026-to-1104.json"} {
		v := ineHistoricalFixture(t, file)
		for _, s := range v.Series {
			if s.Product.Exchange == tf.SHFE {
				grouped[s.Product] = append(grouped[s.Product], s.Records...)
			}
		}
	}
	cases := []struct {
		product string
		from    tf.TradingDay
		count   int
	}{{"ao", 20230619, 818}, {"br", 20230728, 791}, {"ad", 20250610, 342}, {"op", 20250910, 276}}
	var joined []verified.Series
	for _, tc := range cases {
		key := tf.ProductKey{Exchange: tf.SHFE, Product: tc.product}
		joined = append(joined, verified.Series{Product: key, Records: grouped[key]})
	}
	cal, err := verified.New(joined)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		key := tf.ProductKey{Exchange: tf.SHFE, Product: tc.product}
		from, to, ok := cal.Covers(key)
		if !ok || from != tc.from || to != 20261104 {
			t.Fatalf("scope %s %s %s", key, from, to)
		}
		n := 0
		if err := cal.Walk(key, from, to, func(tf.Day) bool { n++; return true }); err != nil || n != tc.count {
			t.Fatalf("walk %s %d %v", key, n, err)
		}
		if _, err := cal.DayOf(key, ymd(natural(from).AddDate(0, 0, -1))); !errors.Is(err, tf.ErrUncovered) {
			t.Fatalf("before listing %s %v", key, err)
		}
		if _, err := cal.DayOf(key, 20261105); !errors.Is(err, tf.ErrUncovered) {
			t.Fatalf("future %s %v", key, err)
		}
	}
	for _, product := range []string{"ao", "ad"} {
		key := tf.ProductKey{Exchange: tf.SHFE, Product: product}
		d, err := cal.DayAt(key, instant(20260110, 30))
		if err != nil || d.Num != 20260112 {
			t.Fatalf("Saturday overnight %s %s %v", key, d.Num, err)
		}
	}
	for _, product := range []string{"br", "op"} {
		key := tf.ProductKey{Exchange: tf.SHFE, Product: product}
		if _, err := cal.DayAt(key, instant(20260110, 30)); !errors.Is(err, tf.ErrClosed) {
			t.Fatalf("23h night must be closed %s %v", key, err)
		}
	}
}
