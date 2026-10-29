package shinnysource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// nnPlan 为真实现场与离线预检共用的执行单形状；Go独立校验Python生成的命令。
type nnPlan struct {
	Schema         int                   `json:"schema"`
	Symbol         string                `json:"symbol"`
	Days           []tickflow.TradingDay `json:"trading_days"`
	NoNightAfter   tickflow.TradingDay   `json:"no_night_after"`
	ResumeDay      tickflow.TradingDay   `json:"resume_day"`
	EveningBegin   string                `json:"evening_begin"`
	EveningEnd     string                `json:"evening_end"`
	FetchBegin     string                `json:"fetch_begin"`
	FetchEnd       string                `json:"fetch_end"`
	OfficialURL    string                `json:"official_url"`
	EvidenceFile   string                `json:"evidence_file"`
	EvidenceSHA256 string                `json:"evidence_sha256"`
	EvidenceClass  string                `json:"evidence_class"`
}

// nnWindow 的全部计数、查询与日历时刻来自执行单，历史常量仅留离线标定。
type nnWindow struct {
	eve, nightTo, fetchFrom, dayOpen, dayClose int64
}

func nnDate(day tickflow.TradingDay) (time.Time, error) {
	text := strconv.Itoa(int(day))
	if len(text) != 8 {
		return time.Time{}, fmt.Errorf("交易日须八位整数")
	}
	return time.ParseInLocation("20060102", text, tickflow.CST)
}

func nnReadPlan(path string, allowSynthetic bool) (nnPlan, nnWindow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nnPlan{}, nnWindow{}, err
	}
	return nnParsePlan(raw, path, allowSynthetic)
}

func nnParsePlan(raw []byte, path string, allowSynthetic bool) (nnPlan, nnWindow, error) {
	var p nnPlan
	var w nnWindow
	var err error
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return p, w, err
	}
	keys := []string{"schema", "symbol", "trading_days", "no_night_after", "resume_day", "evening_begin", "evening_end", "fetch_begin", "fetch_end", "official_url", "evidence_file", "evidence_sha256", "evidence_class"}
	if len(fields) != len(keys) {
		return p, w, fmt.Errorf("执行单字段数量不符")
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return p, w, fmt.Errorf("缺少字段：%s", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&p); err != nil {
		return p, w, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return p, w, fmt.Errorf("执行单有多余JSON")
	}
	if p.Schema != 1 || !regexp.MustCompile(`^SHFE\.rb[0-9]{2}(0[1-9]|1[0-2])$`).MatchString(p.Symbol) || len(p.Days) != 4 {
		return p, w, fmt.Errorf("执行单版本、合约或四个交易日无效")
	}
	for i, day := range p.Days {
		date, e := nnDate(day)
		if e != nil || date.Weekday() == time.Saturday || date.Weekday() == time.Sunday || (i > 0 && day <= p.Days[i-1]) {
			return p, w, fmt.Errorf("交易日须递增、唯一且有效，不能是周末")
		}
	}
	if p.NoNightAfter != p.Days[1] || p.ResumeDay != p.Days[2] {
		return p, w, fmt.Errorf("停夜日与恢复日须绑定日历中间两日")
	}
	evening, _ := nnDate(p.NoNightAfter)
	resume, _ := nnDate(p.ResumeDay)
	expected := []time.Time{evening.Add(20*time.Hour + 59*time.Minute), evening.Add(21*time.Hour + 10*time.Minute),
		resume.Add(15*time.Hour + 10*time.Minute), resume.Add(15*time.Hour + 15*time.Minute)}
	for i, text := range []string{p.EveningBegin, p.EveningEnd, p.FetchBegin, p.FetchEnd} {
		point, e := time.Parse(time.RFC3339, text)
		if e != nil || !strings.HasSuffix(text, "+08:00") || !point.Equal(expected[i]) {
			return p, w, fmt.Errorf("阶段窗口须绑定停夜日20:59—21:10及恢复日15:10—15:15")
		}
	}
	u, err := url.Parse(p.OfficialURL)
	if err != nil || u.Scheme != "https" || (u.Hostname() != "www.shfe.com.cn" && u.Hostname() != "www.shfe.cn") || u.User != nil || u.Port() != "" {
		return p, w, fmt.Errorf("公告必须为上期所HTTPS链接")
	}
	if p.EvidenceClass != "official" && !(allowSynthetic && p.EvidenceClass == "synthetic") {
		return p, w, fmt.Errorf("合成或未知公告不能用于实跑")
	}
	if p.EvidenceFile == "" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.EvidenceSHA256) {
		return p, w, fmt.Errorf("缺少公告文件及哈希")
	}
	evidencePath := p.EvidenceFile
	if !filepath.IsAbs(evidencePath) {
		evidencePath = filepath.Join(filepath.Dir(path), evidencePath)
	}
	evidence, err := os.ReadFile(evidencePath)
	if err != nil {
		return p, w, fmt.Errorf("读取公告证据失败")
	}
	digest := sha256.Sum256(evidence)
	if hex.EncodeToString(digest[:]) != p.EvidenceSHA256 {
		return p, w, fmt.Errorf("公告证据哈希不符")
	}
	w = nnWindow{eve: evening.Add(21 * time.Hour).UnixMilli(), nightTo: evening.Add(26*time.Hour + 30*time.Minute).UnixMilli(),
		fetchFrom: evening.Add(20 * time.Hour).UnixMilli(), dayOpen: resume.Add(9 * time.Hour).UnixMilli(), dayClose: resume.Add(15 * time.Hour).UnixMilli()}
	return p, w, nil
}

func TestNoNightRunnerHasNoHistoricalWindowConstants(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "live_nonight_real_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"nnPrepare": false, "nnReport": false, "TestNoNightRealEvening": false, "TestNoNightRealFetch": false}
	legacy := map[string]bool{"nnDays": true, "nnEve": true, "nnNightTo": true, "nnFetchFrom": true, "nnDayOpen": true, "nnDayClose": true}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, ok := wanted[function.Name.Name]; !ok {
			continue
		}
		wanted[function.Name.Name] = true
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && legacy[id.Name] {
				t.Errorf("现场函数%s仍引用历史常量%s", function.Name.Name, id.Name)
			}
			if value, ok := node.(*ast.BasicLit); ok && (strings.Contains(value.Value, "20260930") || strings.Contains(value.Value, "20261008")) {
				t.Errorf("现场函数%s仍含固定日期", function.Name.Name)
			}
			return true
		})
	}
	for name, seen := range wanted {
		if !seen {
			t.Errorf("未审计到现场函数%s", name)
		}
	}
}

func TestNoNightParameterizedPlan(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "..", "tools", "probe", "testdata", "nonight-synthetic", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, w, err := nnReadPlan(fixture, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.eve != nnEve || w.dayOpen != nnDayOpen {
		t.Fatal("历史执行单未还原原标定")
	}
	if _, _, err := nnReadPlan(fixture, false); err == nil {
		t.Fatal("合成公告进入了实跑")
	}
	p.EvidenceFile = filepath.Join(filepath.Dir(fixture), "evidence.txt")
	for _, kind := range []string{"valid", "future_synthetic", "unknown", "duplicate", "invalid_date", "weekend", "binding", "window", "zone", "symbol", "hash", "url", "class"} {
		t.Run(kind, func(t *testing.T) {
			candidate := p
			candidate.Days = append([]tickflow.TradingDay(nil), p.Days...)
			switch kind {
			case "future_synthetic":
				candidate.Days = []tickflow.TradingDay{20261228, 20261229, 20261230, 20261231}
				candidate.NoNightAfter, candidate.ResumeDay = 20261229, 20261230
				candidate.EveningBegin, candidate.EveningEnd = "2026-12-29T20:59:00+08:00", "2026-12-29T21:10:00+08:00"
				candidate.FetchBegin, candidate.FetchEnd = "2026-12-30T15:10:00+08:00", "2026-12-30T15:15:00+08:00"
			case "duplicate":
				candidate.Days[0] = candidate.Days[1]
			case "invalid_date":
				candidate.Days[0] = 20260230
			case "weekend":
				candidate.Days[0] = 20260927
			case "binding":
				candidate.ResumeDay = 20261009
			case "window":
				candidate.FetchBegin = "2026-10-08T09:05:00+08:00"
			case "zone":
				candidate.EveningBegin = "2026-09-30T12:59:00Z"
			case "symbol":
				candidate.Symbol = "SHFE.rb2713"
			case "hash":
				candidate.EvidenceSHA256 = strings.Repeat("0", 64)
			case "url":
				candidate.OfficialURL = "https://www.shfe.cn.evil.example/"
			case "class":
				candidate.EvidenceClass = "unknown"
			}
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unknown" {
				raw = append(raw[:len(raw)-1], []byte(`,"extra":1}`)...)
			}
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			got, window, err := nnReadPlan(path, true)
			if kind != "valid" && kind != "future_synthetic" {
				if err == nil {
					t.Fatal("无效计划被接受")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cal, err := embedded.New(got.Days, embedded.NoNightAfter(got.NoNightAfter))
			if err != nil {
				t.Fatal(err)
			}
			symbol, err := tickflow.ParseSymbol(got.Symbol)
			if err != nil {
				t.Fatal(err)
			}
			day, err := cal.DayOf(symbol.ProductKey(), got.ResumeDay)
			if err != nil {
				t.Fatal(err)
			}
			if day.Sessions[0].Start != window.dayOpen {
				t.Fatal("动态恢复日未与注入日历同步")
			}
			if kind == "future_synthetic" && window.eve == nnEve {
				t.Fatal("仍沿用9/30常量")
			}
		})
	}
}
