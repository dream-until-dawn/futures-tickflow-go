package segfile

import (
	"os"
	"path/filepath"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件测【丙三之一】加进接口的那两样：`OpenState` 与 `DiscardCoverage`。
//
// ⛔ 它们不是便利方法，是 C3b / D2b 的【输入通道】：
// 那两条要求编排为「打开时发现的事」留声，而编排够不到 `Open`
// （构造不在接口里）。⇒ 让 Store 自己报，而不是让调用方转告 ——
// **转告是一句承诺，而承诺不可核。**

// ───────── C3b 的上半：残尾字节数要到得了编排 ─────────

// TestOpenStateCarriesTruncatedTail 断言的是【同一个数】，不是「有一个正数」。
//
// ⚠️ 写成 `> 0` 的话，一个返回常量 1 的实现照样绿 ——
// 而这一条要证的是「`Open` 看见的那个数，编排问得到」。
func TestOpenStateCarriesTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	// 两条完整记录 + 30 字节半截。
	body := make([]byte, 0, RecordSize*2+30)
	for _, b := range []tickflow.Bar{bar(20200731, 1), bar(20200803, 2)} {
		r := EncodeBar(b)
		body = append(body, r[:]...)
	}
	body = append(body, make([]byte, 30)...)
	if err := os.WriteFile(filepath.Join(dir, "1m.dat"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	s, truncated, err := Open(dir, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer s.Close()
	if truncated != 30 {
		t.Fatalf("前提没成立：Open 报残尾 %d 字节，这条用例是按 30 写的", truncated)
	}
	if got := s.OpenState().TruncatedTail; got != truncated {
		t.Errorf("OpenState().TruncatedTail = %d，而 Open 报的是 %d\n"+
			"  ⇒ 这个数到不了编排，C3b 就只能靠调用方转告", got, truncated)
	}
}

// TestOpenStateCleanDirHasNoTail 对照：干净目录必须是 0。
//
// ⛔ 少了这一条，上面那条也可能是「它把 Open 的返回值抄了过来，
// 而那个返回值本身恒为 30」。**两条合起来才钉住「它跟着实际的残尾走」。**
func TestOpenStateCleanDirHasNoTail(t *testing.T) {
	s, _, err := Open(t.TempDir(), tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.OpenState().TruncatedTail; got != 0 {
		t.Errorf("空目录的 TruncatedTail = %d，期望 0", got)
	}
}

// ───────── D2b 的上半：「旧 .meta」与「新目录」必须分得开 ─────────

// TestOpenStateDistinguishesLegacyFromFresh 三格一起测，因为**只有一格没有意义**。
//
// ⛔ **「没有 .meta」不是「旧 .meta」**：
//
//	没有 .meta   一个【新库】—— 什么都还没拉过，没有语义未知的 coverage
//	旧 .meta     一份 v0.3 之前写的 coverage —— 语义未知，要一个决定（D2a）
//
// 合成一格的后果是**每一个新目录都会去走 D2a**，而那条路要人做决定 ——
// 一份对每个新库都要求人工介入的实现，会被人加个跳过。
func TestOpenStateDistinguishesLegacyFromFresh(t *testing.T) {
	// 一份【本版】的 .meta：EncodeMeta 一律写 format。
	current, err := EncodeMeta(Meta{})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		meta []byte // nil = 不写 .meta
		want bool
	}{
		{"新目录（没有 .meta）", nil, false},
		{"本版 .meta（有 format）", current, false},
		{"旧 .meta（没有 format）", []byte(`{"coverage":[]}`), true},
		// ⚠️ 这一格与上一格只差一个字段，而它证的是「判据是 format 的【缺席】，
		// 不是『coverage 是不是空的』」—— 两者在上一格里同时成立，分不开。
		{"旧 .meta 且 coverage 非空", []byte(
			`{"coverage":[{"from":20200803,"to":20200803,"bars":1,"days":1}]}`), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.meta != nil {
				if err := os.WriteFile(filepath.Join(dir, "1m.meta"), c.meta, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s, _, err := Open(dir, tickflow.MustIntraday(1))
			if err != nil {
				t.Fatalf("Open 失败：%v", err)
			}
			defer s.Close()
			if got := s.OpenState().LegacyMeta; got != c.want {
				t.Errorf("LegacyMeta = %v，期望 %v", got, c.want)
			}
		})
	}
}

// ───────── LegacyDiscard 那一支要的那个【动作】 ─────────

// TestDiscardCoverageClearsPersistsAndForgetsVerified 三件事一起断言，而它们各自会单独坏。
//
// ⛔ 只断言「Coverage() 空了」是不够的：
//
//	不落盘      ⇒ 下次打开它又回来了 ⇒ 「已作废」是一句只在本进程里为真的话
//	不清 verified ⇒ 留下一批指向已不存在的 Span 的走查记录
//
// ⚠️ 第二条今天**查不出问题**（键是 Span 值，对不上就当没走查过 ⇒ 落在安全那侧）——
// **而那是巧合，不是设计**：键的形状一变，那批陈旧记录就会开始回答问题。
// ⇒ 所以它现在就要被钉住，而不是等它变成一个 bug。
func TestDiscardCoverageClearsPersistsAndForgetsVerified(t *testing.T) {
	dir := t.TempDir()
	cal, k := testCalendar(t)
	s, _, err := Open(dir, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}

	span := tickflow.Span{From: 20200803, To: 20200803, Bars: 1, Days: 1}
	if err := s.AppendBars([]tickflow.Bar{bar(20200803, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, k, span, tickflow.OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(span); err != nil {
		t.Fatalf("走查失败：%v", err)
	}
	// 前提：三样东西现在都【在】。前提不成立的话，下面三条什么也没验。
	if len(s.Coverage()) != 1 {
		t.Fatalf("前提没成立：coverage 应当有 1 段，实得 %d", len(s.Coverage()))
	}
	if len(s.verified) != 1 {
		t.Fatalf("前提没成立：verified 应当有 1 条，实得 %d", len(s.verified))
	}

	if err := s.DiscardCoverage(); err != nil {
		t.Fatalf("DiscardCoverage 失败：%v", err)
	}
	if n := len(s.Coverage()); n != 0 {
		t.Errorf("作废之后 coverage 还有 %d 段", n)
	}
	if n := len(s.verified); n != 0 {
		t.Errorf("作废之后 verified 还有 %d 条 ——\n"+
			"  ⇒ 它们指向已经不存在的 Span；今天不出事只是因为键对不上", n)
	}
	s.Close()

	// 落盘了吗 —— 换一个进程视角（重新打开）再问一次。
	s2, _, err := Open(dir, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	defer s2.Close()
	if n := len(s2.Coverage()); n != 0 {
		t.Errorf("重新打开之后 coverage 又有 %d 段 ——\n"+
			"  ⇒ 「已作废」只在本进程里为真，而 D2a 要的是一个落了盘的决定", n)
	}
}

// TestDiscardCoverageKeepsBars 作废的是【声称拉过】，不是数据。
//
// ⚠️ 写下来是因为「作废」这个词会被读成「删掉」——
// 而删掉是不可逆的，coverage 作废不是。
func TestDiscardCoverageKeepsBars(t *testing.T) {
	dir := t.TempDir()
	cal, k := testCalendar(t)
	s, _, err := Open(dir, tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AppendBars([]tickflow.Bar{bar(20200803, 1)}); err != nil {
		t.Fatal(err)
	}
	span := tickflow.Span{From: 20200803, To: 20200803, Bars: 1, Days: 1}
	if err := s.CommitSpan(cal, k, span, tickflow.OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "1m.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != RecordSize {
		t.Fatalf("前提没成立：.dat 应当有一条记录（%d 字节），实得 %d", RecordSize, before.Size())
	}
	if err := s.DiscardCoverage(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(dir, "1m.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Errorf(".dat 从 %d 字节变成 %d —— DiscardCoverage 动了数据，"+
			"而它只该动「声称拉过」这件事", before.Size(), after.Size())
	}
}
