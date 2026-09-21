package main

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// guard: -tail-end 是授权窗口的墙钟终点（评审方 2026-09-21 裁）—— 不给、不带偏移、不是 RFC3339、不晚于现在 ⇒ 都拒绝；
// 对照：带偏移的未来时刻 ⇒ 原样收下（不按本机时区改写）。
func TestTailDeadline(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 50, 0, 0, cst)
	for _, bad := range []string{"", "15:05", "2026-09-21 15:05:00", "2026-09-21T15:05:00", "2026-09-21T08:49:59+08:00", "2026-09-21T08:50:00+08:00"} {
		if _, err := tailDeadline(bad, now); err == nil {
			t.Errorf("-tail-end %q 应被拒绝", bad)
		}
	}
	end, err := tailDeadline("2026-09-21T15:05:00+08:00", now)
	if err != nil || end.UnixMilli() != time.Date(2026, 9, 21, 15, 5, 0, 0, cst).UnixMilli() {
		t.Errorf("对照：得 %v %v，应为 2026-09-21 15:05:00 +0800", end, err)
	}
}

// guard: openmd 取合约表到 openmdTimeout 就放弃、记「没取到」（评审方裁：20 秒；6.37 那天旧的 3 分钟两次都耗尽）——
// 本地假服务器不回话 ⇒ 在上限附近返回「没取到」，不等到服务器自己放手。
func TestOpenmdGivesUpAtTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	oldURL, oldTO := openmdURL, openmdTimeout
	openmdURL, openmdTimeout = srv.URL, 300*time.Millisecond
	defer func() { openmdURL, openmdTimeout = oldURL, oldTO }()
	t0 := time.Now()
	got := showUnderlying()
	took := time.Since(t0)
	if !strings.HasPrefix(got, "没取到") {
		t.Errorf("得 %q，应为「没取到」", got)
	}
	if took < 300*time.Millisecond || took > 3*time.Second {
		t.Errorf("用了 %v，应在上限 300ms 附近放弃", took)
	}
	if oldTO != 20*time.Second {
		t.Errorf("openmdTimeout ＝ %v，评审方裁的是 20 秒", oldTO)
	}
}

// guard: probeLiveTail 里的先后（评审方 2026-09-21 裁）—— 截止时刻由 -tail-end 定（context.WithDeadline），且在拨号之前；
// 取数前那次 openmd 在拨号【之后】、在后台 goroutine 里（不占授权窗口的开头、不挡收帧）；
// 断开之后那次 openmd 在 Flush 之后（文件先落盘）。按 go/ast 取函数体，不按行号 / 花括号切。
func TestLiveTailOrder(t *testing.T) {
	src, err := os.ReadFile("livetail.go")
	if err != nil {
		t.Fatal(err)
	}
	fs := gotoken.NewFileSet()
	f, err := parser.ParseFile(fs, "livetail.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "probeLiveTail" {
			body = string(src[fs.Position(fd.Body.Pos()).Offset:fs.Position(fd.Body.End()).Offset])
		}
	}
	if body == "" {
		t.Fatal("没找到 probeLiveTail —— 筛子坏了")
	}
	at := func(s string) int {
		i := strings.Index(body, s)
		if i < 0 {
			t.Fatalf("probeLiveTail 里没有 %q", s)
		}
		return i
	}
	deadline, dial := at("context.WithDeadline("), at("websocket.Dial(")
	firstOpenmd := at("showUnderlying()")
	goOpenmd := at("go func() { before <- showUnderlying() }()")
	flush, lastOpenmd := at("w.Flush()"), strings.LastIndex(body, "showUnderlying()")
	if !(deadline < dial) {
		t.Errorf("截止时刻（WithDeadline）应在拨号之前")
	}
	if !(dial < firstOpenmd) {
		t.Errorf("第一次 openmd 应在拨号之后")
	}
	if firstOpenmd != goOpenmd+len("go func() { before <- ") {
		t.Errorf("第一次 openmd 应就是后台 goroutine 里那一次")
	}
	if !(flush < lastOpenmd) || lastOpenmd == firstOpenmd || strings.Contains(body, "defer w.Flush()") {
		t.Errorf("断开之后那次 openmd 应在 Flush 之后（defer 的 Flush 字面在前、实际在最后，不算）")
	}
	if strings.Contains(body, "WithTimeout(") {
		t.Errorf("probeLiveTail 里不该再有按时长推终点的 WithTimeout")
	}
}
