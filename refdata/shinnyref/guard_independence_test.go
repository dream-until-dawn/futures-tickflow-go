package shinnyref

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

// 本文件回答一个**在评审信里被提出、并被读数解掉**的问题，
// ⛔ 而它落在**代码里**而不是只留在信里，理由是本仓那条：
// **文档与代码是两个载体，改一个不会带另一个** —— 一封信不会在下次改动时红。
//
// 问题（我提的）：`Scan` 那道「`}` 之后必须是 io.EOF」的守卫，
// 与 `bodyReader.sawEOF` 那道，**是不是一道守卫加一个巧合**？
// 也就是说：`Scan` 反正会把流推到头，那 `sawEOF` 是不是永远为真、因而是冗余的？
//
// 🔴 **两条读数说：不是。而它们回答的是两个【不同的命题】** ——
//
//	Scan 那道说：**我拿到的这个 reader 干净地结束了**
//	sawEOF 那道说：**gzip 那一层走到了它自己的末尾**
//
// 它们在当前接线下同时成立，**而下面第一条测试给出一个把它们分开的输入**。

// eofWatch 记「底下那个 reader 有没有给过 io.EOF」。
type eofWatch struct {
	r      io.Reader
	sawEOF bool
	reads  int
}

func (w *eofWatch) Read(b []byte) (int, error) {
	n, err := w.r.Read(b)
	w.reads++
	if err == io.EOF {
		w.sawEOF = true
	}
	return n, err
}

// TestScanGuardDoesNotImplyStreamWasRead —— **分离输入**，打的是**真实接线**。
//
// 构造：`bodyReader`（生产用的那个）包住一份 gzip，明文 = 完整目录 ＋ `}` 之后 4096 个空格；
// 而只让 `Scan` 看见前 len(目录) 个字节（`io.LimitReader`）。
//
// ⇒ `Scan` 拿到一个**干净的 EOF**，它的守卫**满意**（err == nil）；
// **而 gzip 一个字节的尾巴都没读到** ⇒ CRC 与长度从未被校验
// ⇒ **`Close` 必须把这件事说出来。**
//
// ⛔ 第一版我把这条写成「断言 Scan 返回 nil 而底下没到 EOF」就完了 ——
// 🔴 **那是一条永远绿的测试**：它断言的是一个关于 `io.Reader` 的必然事实
// （隔着 `LimitReader`，`Scan` 本来就不可能够到底层的 EOF），
// **没有任何现实的改动能让它红**。
// ⇒ 改成打生产对象：**摘掉 `sawEOF` ⇒ 这一条红。**
//
// ⚠️ 而 `LimitReader` 不是牵强的构造：**任何在上层做了长度截断的东西
// （代理、缓存、一个「只读前 N 字节」的调用方）都会造出同一个形状。**
func TestScanGuardDoesNotImplyStreamWasRead(t *testing.T) {
	doc := twoEntries
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(doc + strings.Repeat(" ", 4096))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := io.NopCloser(bytes.NewReader(buf.Bytes()))
	zr, err := gzip.NewReader(raw)
	if err != nil {
		t.Fatal(err)
	}
	body := &bodyReader{zr: zr, raw: raw} // 与 Fetch 交出来的是同一个东西

	res, err := Scan(io.LimitReader(body, int64(len(doc))), nil)
	if err != nil {
		t.Fatalf("这个构造要的是「Scan 认为它干净地结束了」，实得 %v", err)
	}
	if res.Contracts != 1 {
		t.Fatalf("解出 %d 条，要 1", res.Contracts)
	}
	if body.sawEOF {
		t.Fatalf("前提不成立：这个构造要的是「gzip 没被读到头」")
	}
	// ⇒ 两个命题在这里分开了：Scan 说「干净」，而这份数据没有被校验过。
	// 🔴 而**只有 Close 答得了后一句**。
	if cerr := body.Close(); !errors.Is(cerr, errNotReadToEOF) {
		t.Fatalf("Scan 说干净、流没被校验过，而 Close 什么都没说：%v —— "+
			"那时「这份数据完不完整」这句话没有人回答得了", cerr)
	}
}

// TestScanNormallyReachesEOF —— **另一半**：在没有人截断的正常接线上，
// `Scan` 走完之后底下的流**确实**被推到了头（所以 `sawEOF` 那道不会误伤正常路径）。
//
// ⚠️ 扫五个尺寸，而不是一个：**「它在小输入上成立」和「它成立」是两句话** ——
// `json.Decoder` 有自己的缓冲，缓冲够大时一次 Read 就吞完了，那样什么都没证明。
// 明文从 40 B 到 1.6 MB，Read 次数实测 2 → 61（本机 go1.26.1），**每一格都真的多读了几次**。
//
// ⛔ **而这一条是【表征测试】，不是守卫，射程要说清楚**：
// 我拿突变问过它 —— **把 `Scan` 那道 EOF 守卫摘掉，这一条仍然绿**
// ⇒ 🔴 **把流推到头的是 `dec.More()` 自己，不是那道守卫。**
// ⇒ 所以它护不住**那道 EOF 守卫**。
//
// ⛔ **【更正】此处原写「所以它护不住本仓的任何一行代码」，而那是假的**
// （评审方 2026-09-10 造 M3 打出来，我复现）：**它护得住「`Scan` 读完整份文档」。**
//
//	突变：让主循环在读完第一条之后就 `return`（只在后面还有东西时触发，
//	      好让「单条」那一格照常走完 —— 否则会先撞前提自检那一行）
//	⇒ 本条红：`明文 1576 B：Scan 走完了而底下的流没被推到头（Read 1 次）`
//
// 🔴 而那句话为假的分量不在措辞：**它是一张【删掉这条测试】的许可证** ——
// 下一个做清理的人读到「它护不住任何一行代码」，就会把它数出覆盖率再删掉，
// **而删掉它会丢一道真守卫。**
// ⚠️ 更难看的是：**那句话的反例就写在它自己的下一行**
// （「失败方向是……或『`Scan` 从中间提前返回了』」——**那就是本仓的一行代码**）
// ⇒ 判据：**写下一句「它护不住 X」时，把紧接着列出的失败方向逐条对回去** ——
// 一个全称否定和一张例子清单并排放着时，**清单里常常就躺着反例**。
//
// ⇒ 所以它的射程精确地是这样：
//
//	护不住：`Scan` 那道「`}` 之后必须是 io.EOF」的守卫（摘掉它 ⇒ 本条仍绿）
//	护得住：**`Scan` 会把整份文档读完**（主循环提前返回 ⇒ 本条红）
//	还护着：一个关于依赖的假设 —— 「`json.Decoder` 会把 reader 读到 EOF」
//	        （Go 改了它的行为 ⇒ 本条红，而那时 `sawEOF` 会开始在正常路径上误报）
func TestScanNormallyReachesEOF(t *testing.T) {
	for _, pad := range []int{0, 1500, 15000, 155000, 1590000} {
		entries := []string{`"SHFE.au2002": {"class":"FUTURE_CONT"}`}
		if pad > 0 {
			entries = append(entries,
				`"KQ.pad": {"class":"PAD","note":"`+strings.Repeat("x", pad)+`"}`)
		}
		doc := "{" + strings.Join(entries, ",") + "}"
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write([]byte(doc)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		w := &eofWatch{r: zr}
		if _, err := Scan(w, nil); err != nil {
			t.Fatalf("明文 %d B：Scan 失败 %v", len(doc), err)
		}
		if !w.sawEOF {
			t.Fatalf("明文 %d B：Scan 走完了而底下的流没被推到头（Read %d 次）",
				len(doc), w.reads)
		}
		// 前提自检：至少读了两次，否则「一次吞完」这一格什么也没证明。
		if w.reads < 2 {
			t.Fatalf("明文 %d B 只读了 %d 次 —— 这一格是空的", len(doc), w.reads)
		}
	}
}
