package tickflow_test

// (v) 端到端：真 segfile ＋ Syncer。开库时「盘上完整记录 n < coverage 声称的 Σbars」⇒ Sync 在规划之前中止并出声。
//
// 由来（评审方登记 (v)，2026-09-13）：v0.5.0 勘误四丙格 —— 只删 .dat 之后每条 Sync 都 err=nil、一条不重拉，
// 而 segfile.Open 与 OpenState 什么都不说（改前读数：OpenState={TruncatedTail:0 LegacyMeta:false}，与好库同形）。
//
// 每个中止格断言五件事：errors.Is(err, ErrMissingRecords) · rep.MissingRecords 恰好一条且报文指向「一起删、从早到晚」·
// Complete()=false · 没有向源要过数据 · 盘上 .dat 与 .meta 一个字节都没动。
// 不触发的格断言：err 不是 ErrMissingRecords、rep.MissingRecords 为空（其余保持今天的行为，不在这里断言）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// mrSource 四天都给；数被要过几次数据。声明 ClientUseNone：它不经那个 http.Client，
// 声明 HTTP 会多出一条「闸门没被用到」把 Complete() 染成 false（与本题无关）。
type mrSource struct{ calls *int }

func (s mrSource) Caps(tickflow.ProductKey) tickflow.Capabilities {
	return tickflow.Capabilities{
		Periods:   []tickflow.Period{tickflow.Daily},
		Since:     map[tickflow.Period]tickflow.TradingDay{tickflow.Daily: 20000101},
		MaxBars:   1000,
		BatchDays: 30,
		ClientUse: tickflow.ClientUseNone,
	}
}

func (s mrSource) Bars(_ context.Context, req tickflow.BarRequest) ([]tickflow.Bar, error) {
	*s.calls++
	var out []tickflow.Bar
	for d := req.From; d <= req.To; d++ {
		switch d {
		case 20200805, 20200806, 20200807, 20200810, 20200811, 20200812:
			ts := dayStartMs(d)
			out = append(out, tickflow.Bar{Ts: ts, TsEnd: ts + 86400000 - 1, TradingDay: d,
				Open: 1, High: 1, Low: 1, Close: 1, Volume: 1})
		}
	}
	return out, nil
}

type mrLib struct {
	dir   string
	st    *segfile.Store
	syn   *tickflow.Syncer
	calls int
}

func mrOpen(t *testing.T, dir string) *mrLib {
	t.Helper()
	cal, err := embedded.New([]tickflow.TradingDay{20200805, 20200806, 20200807, 20200810, 20200811, 20200812})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("开库：%v", err)
	}
	l := &mrLib{dir: dir, st: st}
	l.syn, err = tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar:  cal,
		Store:     st,
		NewSource: func(*http.Client) tickflow.Source { return mrSource{calls: &l.calls} },
		Pacer:     pacing.NoPacing(),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("造 Syncer：%v", err)
	}
	return l
}

func (l *mrLib) close(t *testing.T) {
	t.Helper()
	if err := l.st.Close(); err != nil {
		t.Fatalf("关库：%v", err)
	}
}

func (l *mrLib) sync(from, to tickflow.TradingDay) (tickflow.SyncReport, error) {
	req := tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily, From: from, To: to,
	}
	return l.syn.Sync(context.Background(), req, dayStartMs(20200901))
}

// mrGoodLib 造一个好库 [0805..0810]（4 条），关掉，返回目录。前提自检：确实是 4 条、Complete。
func mrGoodLib(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	l := mrOpen(t, dir)
	rep, err := l.sync(20200805, 20200810)
	if err != nil || rep.Bars != 4 || !rep.Complete() {
		t.Fatalf("打底失败（构造不成立）：err=%v Bars=%d Complete=%v Incidents=%v", err, rep.Bars, rep.Complete(), rep.Incidents())
	}
	l.close(t)
	return dir
}

func mrFile(dir, ext string) string { return filepath.Join(dir, "1d"+ext) }

func mrRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b
}

// mrMustAbort 是中止格的五件事。
func mrMustAbort(t *testing.T, dir string, from, to tickflow.TradingDay, wantMissing int64) {
	t.Helper()
	datBefore, metaBefore := mrRead(t, mrFile(dir, ".dat")), mrRead(t, mrFile(dir, ".meta"))
	l := mrOpen(t, dir)
	defer l.close(t)
	if got := l.st.OpenState().MissingRecords; got != wantMissing {
		t.Errorf("OpenState().MissingRecords = %d，期望 %d", got, wantMissing)
	}
	rep, err := l.sync(from, to)
	t.Logf("err=%v · Halt=%v · Incidents=%q · 源被要过 %d 次", err, rep.Halt, rep.Incidents(), l.calls)
	if !errors.Is(err, tickflow.ErrMissingRecords) {
		t.Errorf("Sync 的 err 不是 ErrMissingRecords：%v —— 缺记录的库没有被拦下，同步会把缺的天当成拉过而跳过", err)
	}
	if len(rep.MissingRecords) != 1 {
		t.Errorf("rep.MissingRecords 有 %d 条，期望恰好 1 条：%q", len(rep.MissingRecords), rep.MissingRecords)
	} else {
		for _, want := range []string{"一起删", "从早到晚", "别再重跑"} {
			if !strings.Contains(rep.MissingRecords[0], want) {
				t.Errorf("出声的报文里没有「%s」—— 它要把人送到那条出路：%q", want, rep.MissingRecords[0])
			}
		}
	}
	if rep.Complete() {
		t.Error("Complete() 为真 —— 中止了而报告说完整")
	}
	if l.calls != 0 {
		t.Errorf("中止之前向源要了 %d 次数据 —— 检查不在规划之前", l.calls)
	}
	if !bytes.Equal(mrRead(t, mrFile(dir, ".dat")), datBefore) || !bytes.Equal(mrRead(t, mrFile(dir, ".meta")), metaBefore) {
		t.Error("中止格动了盘上的文件（.dat 或 .meta 的字节变了）—— 中止必须在任何写之前")
	}
}

func mrMustNotTrigger(t *testing.T, dir string, from, to tickflow.TradingDay) {
	t.Helper()
	l := mrOpen(t, dir)
	defer l.close(t)
	if got := l.st.OpenState().MissingRecords; got != 0 {
		t.Errorf("OpenState().MissingRecords = %d，期望 0", got)
	}
	rep, err := l.sync(from, to)
	t.Logf("err=%v · Halt=%v · Incidents=%q", err, rep.Halt, rep.Incidents())
	if errors.Is(err, tickflow.ErrMissingRecords) || len(rep.MissingRecords) != 0 {
		t.Errorf("不该触发却触发了：err=%v · MissingRecords=%q", err, rep.MissingRecords)
	}
}

// guard: 开库时盘上记录少于 coverage 声称的条数 ⇒ Sync 在规划之前中止、出声、指向「一起删、从早到晚」、不碰盘；多于或相等不触发。
func TestMissingRecordsAbortsBeforePlanning(t *testing.T) {
	t.Run("S1_好库不触发", func(t *testing.T) {
		mrMustNotTrigger(t, mrGoodLib(t), 20200805, 20200810)
	})
	t.Run("S2_只删dat", func(t *testing.T) {
		dir := mrGoodLib(t)
		if err := os.Remove(mrFile(dir, ".dat")); err != nil {
			t.Fatal(err)
		}
		mrMustAbort(t, dir, 20200805, 20200810, 4)
	})
	t.Run("S2b_只删dat而请求在coverage之外", func(t *testing.T) {
		// 中止对这个周期的所有请求都生效，包括 coverage 之外的新日子（写在 disposeOpenState 那段注释里）
		dir := mrGoodLib(t)
		if err := os.Remove(mrFile(dir, ".dat")); err != nil {
			t.Fatal(err)
		}
		mrMustAbort(t, dir, 20200811, 20200812, 4)
	})
	t.Run("S3_dat截成两条整", func(t *testing.T) {
		dir := mrGoodLib(t)
		if err := os.Truncate(mrFile(dir, ".dat"), 2*segfile.RecordSize); err != nil {
			t.Fatal(err)
		}
		mrMustAbort(t, dir, 20200805, 20200810, 2)
	})
	t.Run("S4_dat截出半截", func(t *testing.T) {
		dir := mrGoodLib(t)
		if err := os.Truncate(mrFile(dir, ".dat"), 2*segfile.RecordSize+24); err != nil {
			t.Fatal(err)
		}
		// ⚠️ 开库会先截掉那 24 字节残尾（C3a）⇒ 盘上字节在 Open 那一刻就变了 ⇒ 「不碰盘」要从截完之后算。
		st, tr, err := segfile.Open(dir, tickflow.Daily)
		if err != nil || tr != 24 {
			t.Fatalf("截残尾前提不成立：truncated=%d err=%v", tr, err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		mrMustAbort(t, dir, 20200805, 20200810, 2)
	})
	t.Run("S5_孤儿记录不触发", func(t *testing.T) {
		dir := mrGoodLib(t)
		st, _, err := segfile.Open(dir, tickflow.Daily)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		bars, _ := mrSource{calls: &calls}.Bars(context.Background(), tickflow.BarRequest{From: 20200811, To: 20200812})
		if err := st.AppendBars(bars); err != nil { // 追加而不 CommitSpan：崩在两次写之间
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		mrMustNotTrigger(t, dir, 20200805, 20200810)
	})
	t.Run("S6_只删meta不触发", func(t *testing.T) {
		dir := mrGoodLib(t)
		if err := os.Remove(mrFile(dir, ".meta")); err != nil {
			t.Fatal(err)
		}
		mrMustNotTrigger(t, dir, 20200811, 20200812)
	})
	t.Run("S7_旧格式meta加缺记录_检查在LegacyMeta之前", func(t *testing.T) {
		// 造「旧 .meta（没有 format）＋ 缺记录」：若检查排在 LegacyMeta 之后，这个源重放得了（Since 2000）
		// ⇒ LegacyDiscard 会先作废 coverage 并写 .meta ⇒ 「不碰盘」那一件红。
		dir := mrGoodLib(t)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(mrRead(t, mrFile(dir, ".meta")), &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["format"]; !ok {
			t.Fatalf("好库的 .meta 里没有 format 字段 —— 造不出「旧格式」，构造不成立：%v", m)
		}
		delete(m, "format")
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mrFile(dir, ".meta"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(mrFile(dir, ".dat"), 2*segfile.RecordSize); err != nil {
			t.Fatal(err)
		}
		// 前提自检：开库确实认成旧格式
		st, _, err := segfile.Open(dir, tickflow.Daily)
		if err != nil {
			t.Fatal(err)
		}
		legacy := st.OpenState().LegacyMeta
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if !legacy {
			t.Fatal("去掉 format 之后开库没有认成旧格式 —— 这一格验不了「在 LegacyMeta 之前」")
		}
		mrMustAbort(t, dir, 20200805, 20200810, 2)
	})
	t.Run("S8_出路走得通_两份一起删从早到晚", func(t *testing.T) {
		dir := mrGoodLib(t)
		if err := os.Remove(mrFile(dir, ".dat")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(mrFile(dir, ".meta")); err != nil {
			t.Fatal(err)
		}
		l := mrOpen(t, dir)
		defer l.close(t)
		for _, r := range [][2]tickflow.TradingDay{{20200805, 20200806}, {20200807, 20200810}} {
			rep, err := l.sync(r[0], r[1])
			if err != nil || !rep.Complete() || rep.Bars != 2 {
				t.Errorf("出路 [%s..%s]：err=%v Complete=%v Bars=%d Incidents=%q", r[0], r[1], err, rep.Complete(), rep.Bars, rep.Incidents())
			}
		}
	})
}
