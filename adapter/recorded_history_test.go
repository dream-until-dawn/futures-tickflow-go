package adapter_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/adapter"
	"github.com/shopspring/decimal"
)

// TestRecordedHistoryAdapterAdvance 仅读取已封存数据，不把它称为实时或撮合闭环。
func TestRecordedHistoryAdapterAdvance(t *testing.T) {
	path := os.Getenv("TICKFLOW_ADAPTER_HISTORY")
	if path == "" {
		t.Skip("TICKFLOW_ADAPTER_HISTORY 指向已核验的仓库外原始历史")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "71c8543186d9d6b2ee8fff2b9b5f4bfab5b7f7e90434ac786da88934a8cb5754" {
		t.Fatal("原始历史哈希不匹配")
	}
	var rows []struct {
		Datetime         int64
		High, Low, Close float64
	}
	// 文件是 JSONL，逐行解析；哈希已绑定完整字节，不能换成另一份同形样本。
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var row struct {
			Datetime         int64
			High, Low, Close float64
		}
		if err = json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 225 || rows[0].Close != 3096 || rows[224].Close != 3061 {
		t.Fatal("数量或独立记录的首尾 Close 不符")
	}
	d := decimal.RequireFromString
	inst, err := types.ParseSymbol("SHFE.rb2701", 20261008)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := refdata.NewBuilder(1).AddInstrument(refdata.Instrument{ID: inst, VolumeMultiple: d("10"), PriceTick: d("1"), PositionDateType: refdata.UseHistory, IsTrading: true}).
		AddCommissionRates(inst, types.Speculation, refdata.CommissionRates{OpenByVolume: d("1"), CloseByVolume: d("1"), CloseTodayByVolume: d("1")}).
		AddMarginRates(inst, types.Speculation, refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1")}).Build()
	if err != nil {
		t.Fatal(err)
	}
	var sessions []refdata.Session
	var stamps []int64
	for _, hours := range [][4]int{{9, 0, 10, 15}, {10, 30, 11, 30}, {13, 30, 15, 0}} {
		sessions = append(sessions, refdata.Session{Start: refdata.MustClockTime(hours[0], hours[1], 0), End: refdata.MustClockTime(hours[2], hours[3], 0)})
		for ts := time.Date(2026, 10, 8, hours[0], hours[1], 0, 0, tickflow.CST).UnixMilli(); ts < time.Date(2026, 10, 8, hours[2], hours[3], 0, 0, tickflow.CST).UnixMilli(); ts += 60000 {
			stamps = append(stamps, ts)
		}
	}
	cal, err := refdata.NewCalendar([]types.TradingDay{20260930, 20261008}, []refdata.SessionTable{{Exchange: types.SHFE, Product: "rb", Day: sessions}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		dir    types.Direction
		profit string
	}{{"long", types.Buy, "-350"}, {"short", types.Sell, "350"}} {
		t.Run(tc.name, func(t *testing.T) {
			choices := futsim.CTPChoices()
			choices.FeeRounding = fee.NoRounding
			s, err := futsim.New(futsim.Config{Day: 20261008, PreBalance: d("1000000"), Rules: rules, Choices: choices, Calendar: cal})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Mark(20261008, futsim.Quote{Instrument: inst, Last: d("3096"), HasLast: true, PreSettlement: d("3096"), HasPreSettlement: true}); err != nil {
				t.Fatal(err)
			}
			if err = s.ApplyTrade(20261008, match.Trade{Instrument: inst, Direction: tc.dir, Offset: types.Open, Hedge: types.Speculation, Price: d("3096"), Volume: 1}); err != nil {
				t.Fatal(err)
			}
			for i, row := range rows {
				ts := row.Datetime / 1e6
				if ts != stamps[i] {
					t.Fatalf("第%d根标签不符", i)
				}
				converted, err := adapter.Bar(tickflow.Bar{Ts: ts, TsEnd: ts + 60000, TradingDay: 20261008, High: row.High, Low: row.Low, Close: row.Close}, symbol(t, "SHFE.rb2701"))
				if err != nil {
					t.Fatal(err)
				}
				got, err := s.Advance(converted)
				if err != nil || len(got.Filled) != 0 || len(got.Unchecked) == 0 {
					t.Fatalf("第%d根：%+v / %v（涨跌停缺规则必须明示Unchecked）", i, got, err)
				}
				if i == len(rows)-1 {
					before := s.Account()
					converted.TradingDay = 20261009
					if _, err = s.Advance(converted); err == nil || !reflect.DeepEqual(before, s.Account()) {
						t.Fatal("错误交易日未拒绝或账户被改写")
					}
				}
			}
			if !s.Account().PositionProfit.Equal(d(tc.profit)) {
				t.Fatalf("最终盈亏%s，应%s", s.Account().PositionProfit, tc.profit)
			}
			t.Logf("225根封存行情转换计价通过，最终盈亏%s；无撮合，涨跌停规则未验", tc.profit)
		})
	}
}
