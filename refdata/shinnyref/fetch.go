package shinnyref

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultURL 是天勤 openmd 的合约目录。免费、无鉴权。
const DefaultURL = "https://openmd.shinnytech.com/t/md/symbols/latest.json"

// ⛔ 这几个也**不导出**，而那条到期条件在**包级**（见 contract.go 的包注释）——
// 它管本包全部哨兵。⚠️ 这几个是后加的，而它们第一版**没有继承**那条到期条件
// （评审方 2026-09-10 指出）⇒ 判据的粒度是包级，它就该写在包级。
var (
	errNoGzip = errors.New("shinnyref: 服务端没有按 gzip 回——拒绝，不硬拉")
	// errNotReadToEOF 是「这份下载没被读到头」——**不是数据坏了，是【没验过】**。
	// 两者要分得开：前者可以重试，后者是调用方自己的选择。
	errNotReadToEOF = errors.New("shinnyref: 这份下载没有被读到 EOF——完整性没有被校验")
	errBadStatus    = errors.New("shinnyref: 取数返回了一个非 200 的状态")
	errRangeAsked   = errors.New("shinnyref: 这个端点上 Range 与 gzip 互斥——本函数不接受 Range")
)

// Fetch 把整份合约目录取回来，**整包、带 gzip、不用 Range**。
//
// 下面每一条都是 2026-09-10 在真上游上量出来的（读数见 docs/probe.md 复核 v4/v5）。
// ⚠️ 它们写在**这里**而不是只写在探针文档里，理由是同一天栽过的那一格：
// **文档与代码是两个载体，改一个不会带另一个** —— ㉔ 那三处代码注释就是这么漏了半天。
//
// —— 一、必须带 gzip，而这不是「优化」——
//
//	端点支持 Content-Encoding: gzip（首四字节 1f 8b 08 00 验过）
//	整包实测：wire **11,108,041 字节**（10.59 MiB）· **712 秒** · 解开后 351,330,237 字节
//	⇒ 压缩比 **31.6 倍**
//	而限速限的是【线上的字节】，与编码无关（判据：两个假说各自的不变量 ——
//	  线上速率在两种编码下相差 1.3 倍，明文速率相差 26.8 倍 ⇒ 不变的是线上字节）
//	⇒ 不带 gzip 就是同一份东西拉 351 MB
//
// ⛔ 所以服务端没按 gzip 回时**这里拒绝，而不是硬拉**：一个「慢 30 倍而看起来在工作」
// 的下载，会被当成网络慢，**而它其实是一个可以当场发现的配置问题**。
//
// —— 二、⛔ 不许用 Range，而这是【实测的互斥】不是偏好 ——
//
//	带 Range 的请求：206 · Content-Length 精确 · **没有 Content-Encoding** · 首字节不是魔数
//	⇒ **要 Range 就没有压缩，要压缩就只能整包**
//
// ⇒ 于是「断了就续传」这个直觉在这个端点上**是错的**：
//
//	下到一半断了还剩 175 MB：接着往下拿 9.9 小时 vs 带 gzip 从头重来 0.9 小时
//	⇒ **从头重来快约 11 倍** —— 而它错的原因不在带宽，
//	  **在于【续传这个动作本身会把压缩关掉】**
//
// ⚠️ 而裸续传还有第二层：这个文件**每天重建**（四次读数逐日变大），
// 一次长下载跨得过那个时刻 ⇒ Range 拼出来的是**两份快照各出一半**，
// **而拼出来的 JSON 结构完全可能解析通过** ——
// 本包那条「不做任何补全」的防线拦的是【截断】，**拦不住【拼接】**。
// ⇒ 真要续传必须带 `If-Range`（服务端认它：对 etag ⇒ 206 · 错 etag ⇒ 200 整包 · 不带 ⇒ 206）。
// **而本函数不做续传** —— 它只提供「一次拉完」，失败就整个重来。
//
// —— 三、完整性 ——
//
// gzip 流自带 CRC32 与长度，`gzip.Reader` 在读到末尾时会校验它们
// ⇒ **截断会在读的那一侧报错，而不是安静地少几条**。
// ⛔ 而本包**不做任何补全**：这条规矩的由来是本仓自己犯过的一次 ——
// 「补上闭合括号后能正常解析，只是静默少了若干合约，之后每一步都正常」。
func Fetch(ctx context.Context, client *http.Client, url string) (io.ReadCloser, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// ⚠️ 显式写 Accept-Encoding ⇒ Go 的 transport **不会**替我们透明解压
	// （只有它自己加的那次才会）⇒ 我们看得见 Content-Encoding，也就判得了服务端认没认。
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("shinnyref: 取 %s 失败: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		// ⛔ 206 单列一句：它意味着有人（调用方或代理）加了 Range，
		// 而这个端点上 Range 与 gzip 互斥 —— 那条路会把 351 MB 原样拉下来。
		if resp.StatusCode == http.StatusPartialContent {
			return nil, fmt.Errorf("%w: 收到 206", errRangeAsked)
		}
		return nil, fmt.Errorf("%w: %d", errBadStatus, resp.StatusCode)
	}
	// ⚠️ `EqualFold` 不是 `!=` —— HTTP 的 content-coding **是大小写无关的**
	// （RFC 9110 §8.4.1）⇒ 服务端回 `GZIP` 时按 `!=` 会被拒掉。
	// 那个失败方向是**大声**的（不是静默少数据），所以它不重；
	// ⛔ 而**拒得莫名其妙会让人怀疑错方向** —— 读的人会去查网络、查代理，
	// 而真正的原因是我这一行把一个合法的值判成了非法。
	if enc := resp.Header.Get("Content-Encoding"); !strings.EqualFold(enc, "gzip") {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: Content-Encoding=%q（要 gzip）——"+
			"不带压缩这一份是 351 MB 而不是 10.6 MiB，慢约 31 倍，"+
			"而那会被当成「网络慢」，所以这里当场拒绝", errNoGzip, enc)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("shinnyref: 响应说它是 gzip，而它不是: %w", err)
	}
	return &bodyReader{zr: zr, raw: resp.Body}, nil
}

// bodyReader 关的时候把两层都关掉。
//
// ⛔ **【2026-09-10 更正】这里原来写「先关 gzip 那一层 —— 它的 Close 会校验 CRC 与长度，
// 而那正是『截断会被发现』的那一步」，而那是假的。**
// 突变对照打出来的：把两行对调 ⇒ **全绿**（一条测试都没红）⇒ 那句话不承重 ⇒ 去量它：
//
//	完整            Read=nil            Close=nil
//	砍掉末尾 8 字节  **Read=unexpected EOF**  Close=nil
//	砍掉一半        Read=unexpected EOF  Close=unexpected EOF
//
// 🔴 **截断是在 `Read` 走到流末尾时被抓住的，不是在 `Close`。**
//
// ⛔ **而「顺序不承重」这句话要带地址** —— 我量的是**一个实例**，写下的却像一条通则。
// 成立的是这两条，两条都只关于 `*gzip.Reader`：
//
//	`gzip.Reader.Close` **不关底层**（标准库文档写着）⇒ 先关它不会挡住第二次 Close
//	它的 CRC 与长度校验在 **Read** 那一侧（上面那张四格表，本包实测）
//
// ⇒ **对 `gzip.Reader` 而言**，这两行的顺序不影响截断能否被发现。
// ⚠️ 换一个「Close 才校验」的内层，它立刻承重。
// 🔴 判据：**一句「不承重」是一句全称断言，而我量的是一个实例**
// ⇒ **写下「不承重」时，把【我量的是哪个实例】写在同一句里。**
// ⚠️ 而它值得留在这儿，因为它是这一族的一个新长法：
// **一句「为什么这样写」的注释，也可以是一句写下来为假的话** ——
// 而它比一个假的读数更难被发现：**没有人会去跑一条注释。**
// （抓住它的是突变：那两行对调之后一条测试都没红。）
//
// ⇒ 顺序保留（先内后外是惯例），而理由换成真的：**Read 那一侧才是校验发生的地方**。
//
// ⛔ 而「所以调用方必须把 body 读到 EOF」这句话，本来只是一条**写在注释里的约定** ——
// 🔴 **一个保证若绑在一个【事件】上，就要问「谁负责让那个事件发生」；
// 若答案是【调用方】，那它不是一道校验，是一条约定。**
// gzip 的保证绑在「流被读到 EOF」上，而让 EOF 发生的是调用方
// ⇒ 它一直是一条约定，只是没人说破。
// ⇒ 处置见下面的 `sawEOF`：**把「谁负责触发」这件事收回到本层来问一次。**
type bodyReader struct {
	zr  *gzip.Reader
	raw io.ReadCloser
	// sawEOF 记「这份流有没有被读到头」。
	//
	// ⛔ 它把「调用方必须读到 EOF」从一条**约定**变成本层**自己答得了**的一句话。
	// ⚠️ 实测（go1.26.1）：`gzip.Reader.Read` 会在**同一次调用**里返回 `(n>0, io.EOF)`
	// —— 一次大缓冲读回 n=5000 且 err=EOF ⇒ **判据只看 err，不看 n**。
	sawEOF bool
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.zr.Read(p)
	if err == io.EOF {
		b.sawEOF = true
	}
	return n, err
}

// Close 关掉两层，并且**回答一句本层才答得了的话**：这份下载被校验过没有。
//
// ⚠️ 失败方向写清楚：**提前放手的调用方（ctx 取消、回调喊停）会拿到一个 Close 错误**。
// 而那**不是误报** —— 它说的是「这份下载没有被校验过」，**而那句话是真的**；
// 中止的人自己知道为什么中止，而 `defer Close()` 不看返回值的人本来也没在读它。
//
// ⚠️ 到期条件（与本包哨兵那条同源，见 contract.go 包注释）：
// 今天包外调用方 **0** 个 ⇒ 改 Close 的返回值是免费的；
// **等有了第一个包外调用方，这就是一次破坏性变更** ⇒ 要改就趁现在。
func (b *bodyReader) Close() error {
	zerr := b.zr.Close()
	rerr := b.raw.Close()
	if zerr != nil {
		return fmt.Errorf("shinnyref: gzip 流没有完整结束（截断？）: %w", zerr)
	}
	if !b.sawEOF {
		return fmt.Errorf("%w（读到这里就放手了 ⇒ gzip 的 CRC 与长度校验【没有发生】，"+
			"这份数据是不是完整的，本层答不了）", errNotReadToEOF)
	}
	return rerr
}
