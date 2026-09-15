package segfile

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— (y) 评审 2026-09-15 要的两格守卫 ——

// failingReaderAt 读到第 okBytes 字节之后一律报错。
type failingReaderAt struct {
	next    io.ReaderAt
	okBytes int64
}

var errInjectedRead = errors.New("测试注入：读到一半出错")

func (f failingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= f.okBytes {
		return 0, errInjectedRead
	}
	if lim := f.okBytes - off; int64(len(p)) > lim {
		n, err := f.next.ReadAt(p[:lim], off)
		if err == nil {
			err = errInjectedRead
		}
		return n, err
	}
	return f.next.ReadAt(p, off)
}

// guard: 整库扫描读到一半出错 ⇒ 不许交出部分结果（DaysWithBars 回 nil map ＋ 错误；VerifyCoverage 同；Walk 报错）。
//
// ⛔ 理由：DaysWithBars 交出半张表而 err 为 nil ⇒ PlanGaps 把后半段没读到的交易日当成「库里没有」——
// 「从缺推没有」，而且错在 I/O 层，报告里一个字不说（评审方变异 Z4：吞掉 readErr ⇒ 改造后全模块 0 红）。
// ⚠️ 注入点在 scanReaderAt（包内测试缝）；读到第 1 条记录之后出错 ⇒ 第 0 条已经交给回调过（「半张表」真的有一半）。
func TestScanReadErrorYieldsNoPartialResult(t *testing.T) {
	s, _ := twoSpanLib(t)
	cov := s.Coverage()
	if _, err := s.VerifyCoverage(); err != nil { // 置 verified，DaysWithBars 才读得了
		t.Fatalf("前提：健康库上走查跑不起来：%v", err)
	}
	// 标定：不注入时 DaysWithBars 在第一段上交出非空结果 —— 否则下面「回 nil」可能只是本来就空。
	if m, err := s.DaysWithBars(cov[0]); err != nil || len(m) == 0 {
		t.Fatalf("标定格：不注入时应交出非空结果：m=%v err=%v", m, err)
	}

	old := scanReaderAt
	scanReaderAt = func(st *Store) io.ReaderAt { return failingReaderAt{next: st.dat, okBytes: RecordSize} }
	t.Cleanup(func() { scanReaderAt = old })

	m, err := s.DaysWithBars(cov[0])
	if !errors.Is(err, errInjectedRead) || m != nil {
		t.Errorf("DaysWithBars 读到一半出错：m=%v（nil=%v）err=%v —— 应回 nil map 与带着注入错误的 err，不许交出半张表",
			m, m == nil, err)
	}
	res, err := s.VerifyCoverage()
	if !errors.Is(err, errInjectedRead) || res != nil {
		t.Errorf("VerifyCoverage 读到一半出错：res=%v err=%v —— 契约三：跑不起来回 nil map", res, err)
	}
	calls := 0
	err = s.Walk(cov[0].From, cov[0].To, func(tickflow.Bar) bool { calls++; return true })
	if !errors.Is(err, errInjectedRead) {
		t.Errorf("Walk 读到一半出错：err=%v（回调 %d 次，按约定作废）—— 应报错", err, calls)
	}
}

// guard: 参照实现不许走新读法 —— Verify(span) 与 dayHasRecords 的函数体里不许调 scanRecords，且必须逐条 ReadAt。
//
// ⛔ 理由：「参照实现故意逐条 ReadAt，等价性测试靠这个差别才不空」原来只被注释挡着 ——
// 评审方变异 Z5：dayHasRecords 改走 scanRecords ⇒ 等价性测试拿新读法比新读法，全模块 0 红。
// ⚠️ 判的是【字面调用】：绕一层（例如调一个再调 scanRecords 的辅助函数）它照不到 —— 射程写在这儿。
func TestReferenceImplementationsDoNotUseScanRecords(t *testing.T) {
	// 标定：同一个判据在两段造出来的源码上必须分得开。
	calib := `package segfile
func (s *Store) Verify(x int) error { s.scanRecords(nil); return nil }
func (s *Store) dayHasRecords(x int) (bool, error) { s.dat.ReadAt(nil, 0); return false, nil }`
	got := referenceReadShapes(t, "calib.go", calib)
	if !got["Verify"].scan || got["Verify"].readAt || got["dayHasRecords"].scan || !got["dayHasRecords"].readAt {
		t.Fatalf("标定格：判据认不出造出来的调用：%+v —— 尺子坏了，读数作废", got)
	}

	shapes := referenceReadShapes(t, "store.go", nil)
	for _, name := range []string{"Verify", "dayHasRecords"} {
		sh, ok := shapes[name]
		if !ok {
			t.Fatalf("store.go 里没找到 (*Store).%s —— 参照实现被改名或挪走了，这条守卫空转", name)
		}
		if sh.scan {
			t.Errorf("(*Store).%s 调用了 scanRecords —— 参照实现改走新读法后，等价性测试拿新读法比新读法，什么都证不了", name)
		}
		if !sh.readAt {
			t.Errorf("(*Store).%s 里没有逐条 ReadAt —— 参照实现换了读法；它必须和 scanRecords 走不同的路", name)
		}
	}
}

type readShape struct{ scan, readAt bool }

func referenceReadShapes(t *testing.T, filename string, src any) map[string]readShape {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", filename, err)
	}
	out := map[string]readShape{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil || (fd.Name.Name != "Verify" && fd.Name.Name != "dayHasRecords") {
			continue
		}
		var sh readShape
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "scanRecords":
						sh.scan = true
					case "ReadAt":
						sh.readAt = true
					}
				}
			}
			return true
		})
		out[fd.Name.Name] = sh
	}
	return out
}
