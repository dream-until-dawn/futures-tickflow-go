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
	// errStreamBroken 是「这份流坏了」——**和上面那条是相反的两件事**。
	//
	// ⛔ 它是被逼出来的：原来这两种情况**共用上面那一个哨兵**，
	// 于是一份 CRC 坏掉的下载会被报成「你没读完、校验没发生」——**两句都假**。
	// ⇒ 分开的判据不是措辞，是**处置不同**：
	//
	//	errNotReadToEOF ⇒ 调用方自己的选择（中止），而流是好的 ⇒ **不必重取**
	//	errStreamBroken ⇒ 这份数据是坏的                     ⇒ **该重取**
	//
	// ⚠️ **【2026-09-10 第二次更正】它的第一版句子写着「读到底了，而校验失败了」——
	// 而那半句「读到底了」是假的**：`砍掉一半 · 只读 4 字节就放手` 这一格也归它
	// （流是断的，而调用方并没有读到底）。
	// 🔴 同一格教训的第三次：**留声措辞不含成因** —— 这次是**新哨兵继承了旧哨兵的毛病**。
	// ⇒ 现在它只承诺「校验没通过 ⇒ 该重取」，**不承诺调用方读了多少**。
	errStreamBroken = errors.New("shinnyref: 这份下载的完整性校验没有通过——该重取")
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
	//
	// ⛔ 而「不看 n」这半句**由一条测试钉住**：`TestCloseAcceptsFullyReadEmptyPayload`。
	// 它的输入是**明文 0 字节**的合法 gzip 流 —— 那一次 EOF 上**没有数据**
	// ⇒ 把这个条件收窄成「这次读到了数据」的改动，会在那一条上红。
	//
	// 🔴 而这条测试是被两次错误逼出来的，两次形状相同：
	// 我量了 28 组（明文 31/350/1000/4096 × 缓冲 6 档 ＋ 逐字节喂），
	// 每一组末尾都是 `(n>0, EOF)` ⇒ 我写下「两个判据**等价**」，评审方还据此写成「不可判别」。
	// **两句都是假的** —— 缺的那一格在**小**的那一头（明文 0），而我们两个**只往大的方向铺档位**。
	// ⇒ **铺档位要两个方向都铺，而「更大」是默认想到的那个方向。**
	sawEOF bool
	// readErr 记「读的过程中出过一个【不是 EOF】的错误」。
	//
	// ⛔ 没有它的话，`Close` 把两件完全不同的事说成同一句话（评审方 2026-09-10 打出来）：
	//
	//	调用方提前放手        ⇒ 「你没读完」为真，「校验没发生」也为真
	//	**流本身坏了**（CRC 错 / ISIZE 错 / 尾巴被截）
	//	                     ⇒ 调用方**读到底了**，**校验发生了并且失败了**
	//	                        ⇒ 那句「你没读完、校验没发生」**两句都假**
	//
	// 实测（本机 go1.26.1，明文 30 字节，三种坏法并排）：
	//
	//	完整          最后一次 Read=(30, EOF)                  · zr.Close()=nil
	//	CRC 翻一位     最后一次 Read=(30, gzip: invalid checksum) · zr.Close()=**nil**
	//	ISIZE 翻一位   最后一次 Read=(30, gzip: invalid checksum) · zr.Close()=**nil**
	//	砍掉末尾 8B    最后一次 Read=(30, unexpected EOF)         · zr.Close()=**nil**
	//
	// 🔴 三种坏法**都不走 `zr.Close()`**（它一律返回 nil）⇒ 只看 `zr.Close()` 接不住它们。
	//
	// ⚠️ 而反过来也不成立 —— `zr.Close()` **会单独说话**，我造出了那一格：
	//
	//	砍掉一半 · **只读 4 字节就放手** ⇒ readErr=**nil** · zr.Close()=**unexpected EOF**
	//	（成因：flate 已经预读到断点并存下错误，而调用方只取走了 4 字节解压输出）
	//
	// ⇒ **两个来源各自都出现过，谁也不蕴含谁** ⇒ `Close` 里把它们**合成一格**，
	// 哪一侧先看见都算 `errStreamBroken`。（评审方判「zerr 那支从不单独说话」，
	// 而这一格是它的反例 —— 所以修法是**合并**，不是删除。）
	readErr error
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.zr.Read(p)
	switch {
	case err == io.EOF:
		b.sawEOF = true
	case err != nil:
		// ⚠️ 只记**第一个**。
		//
		// ⛔ 而「记第一个」与「记最后一个」在**今天的内层上是等价的** ——
		// 这不是我猜的，是量的（突变「改成记最后一个」⇒ **0 红**，于是我去量了它）：
		//
		//	CRC 翻一位  ⇒ 第 1..5 次 Read 都是 `gzip: invalid checksum`
		//	砍掉末尾 8B ⇒ 第 1..5 次 Read 都是 `unexpected EOF`
		//	⇒ **`gzip.Reader` 的错误是【黏】的：后续每一次都原样回同一个。**
		//
		// ⇒ 所以那个突变活着是**正确的**，不是一个缺口 —— 单列在这儿，
		// 免得下一个人看见「0 红」以为这里少一条测试。
		// ⚠️ 而仍然选「第一个」的理由是**射程**：这条等价性是 `gzip.Reader` 的性质，
		// 不是本层的；**换一个不黏的内层，第一个才是有信息的那个。**
		if b.readErr == nil {
			b.readErr = err
		}
	}
	return n, err
}

// Close 关掉两层，并且**回答一句本层才答得了的话**：这份下载被校验过没有。
//
// ⚠️ 失败方向写清楚：**提前放手的调用方（ctx 取消、回调喊停）会拿到一个 Close 错误**。
// 而那**不是误报** —— 它说的是「这份下载没有被校验过」，**而那句话是真的**；
// 中止的人自己知道为什么中止，而 `defer Close()` 不看返回值的人本来也没在读它。
//
// ⛔ **而 raw.Close() 的错误此前在出错的两支里被整个丢掉**（评审方 2026-09-10 报，建议格）：
// 突变「把最后那行 return rerr 改成 return nil（并 _ = rerr 保住构建）」⇒ **0 红**
// —— 它不是「一个被守着的行为被丢了」，是**一个从没被断言过的返回值**。
// ⇒ 现在用 errors.Join 把它并上去：两个哨兵的 errors.Is 都照旧成立，而 rerr 不再静默消失。
// ⚠️ 而它可测，因为 raw 是一个 io.ReadCloser —— 测试里给一个 Close 会报错的就行。
//
// ⚠️ 到期条件（与本包哨兵那条同源，见 contract.go 包注释）：
// **在出现第一个包外调用方之前**，改 Close 的返回值是免费的；有了就是破坏性变更。
// ⇒ 当前读数**不写在这里** —— 它由 `TestRefdataExpiryConditionNotYetDue` 当场求，
// 而**到期那天它自己红**。（一个带时刻的读数只会旧；这一行只写命题与指针。）
//
// ⛔ **【2026-09-10 更正】此处原来只有一个失败出口，而它把两件事说成同一句话。**
// 详见 `readErr` 上面那段：**流坏掉时调用方是读到底了的，校验也发生了** ——
// 而原来那句留声说「你没读完、校验没发生」，**两句都假**。
// ⇒ 本仓那条（我自己写的）在这里被打了一次：
// **会误报的留声，措辞里不要含成因 —— 读数错一格，成因错两格。**
// 这里读数（Close 该报错）是对的，而成因整整错了两格。
func (b *bodyReader) Close() error {
	zerr := b.zr.Close()
	rerr := b.raw.Close()

	// ⛔ **两个来源合成一格**：`Read` 那一侧看见的错误，和 `zr.Close()` 那一侧看见的错误，
	// **说的是同一件事（这份流坏了）**，而它们各自单独出现过。
	//
	// 【2026-09-10 必改】此前它们是两支，而**第一支 `%w` 的是 `zerr` 不是哨兵**
	// ⇒ 最严重的那种坏（流中段就断、且调用方读到底）报出来的错
	// **既不是 errStreamBroken 也不是 errNotReadToEOF** ——
	// 一个照着这套分类写的调用方**不会重取**。（评审方 2026-09-10 判必改，我复现。）
	//
	// ⚠️ 而修法**不能是「摘掉 zerr 那一支」** —— 他判「它从不单独说话」，
	// 而我造出了反例（七格里那一格）：
	//
	//	砍掉一半 · **只读 4 字节就放手** ⇒ readErr=**nil** · zr.Close()=**unexpected EOF**
	//	（成因：flate 已经预读到断点并存下错误，而调用方只取走了 4 字节解压输出）
	//
	// ⇒ 摘掉它，这一格会被报成「你放手了」，**而这份流其实是断的** ⇒ 信息反而丢了。
	// ⇒ 所以是**合并**，不是删除；哪一侧先看见都算数。
	//
	// ⛔ **【2026-09-10 第二条必改】而「合并」把另一维压掉了：【谁造成的】。**
	// 评审方铺的第三个维度是这个，实测（分片慢速回，读 4096 字节后调用方自己 cancel）：
	//
	//	Read ⇒ **context canceled** ⇒ 落进 errStreamBroken ⇒ 报「该重取」
	//	而它是**调用方自己的选择**，按本包的处置表该是「不必重取」
	//
	// 🔴 而最难看的一格：**它与本函数自己上方那段注释直接矛盾** ——
	// 那段写着「提前放手的调用方（**ctx 取消**、回调喊停）会拿到一个 Close 错误，
	// 而它说的是『这份下载没有被校验过』」。**同一个函数里代码与注释各说各的，而注释那版是对的。**
	// ⇒ 留声含成因的**第四次**：「这份流坏了」假、「不是调用方少读了」也假。
	//
	// ⚠️ 射程先划清（评审方划的，我照收并复量）：
	// **`readErr` 的取值不封闭**（它是 transport / OS / ctx 交回来的任何错误）
	// ⇒ **只能兜底，不能枚举** ⇒ 默认仍落 errStreamBroken（保守：宁可多说一次「该重取」）。
	// 而 **ctx 那两个是封闭且判得了的** ⇒ 单独摘出去。
	// ⚠️ 实测：`context.Canceled` **两个来源都会出现**（`Read` 与 `zr.Close()` 各量到一次）
	// ⇒ 判据要看合并之后的那一个，不能只看 readErr。
	e := b.readErr
	if e == nil {
		e = zerr
	}
	// ⚠️ `rerr` 不再被丢掉 —— 见 `Close` 说明末尾那一段。
	join := func(err error) error {
		if rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	}
	switch {
	case errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded):
		// 调用方自己放手 ⇒ 与「读到一半 return」同一侧：**流没坏，只是没验过**。
		return join(fmt.Errorf("%w：%v（调用方自己中止的 ⇒【不必】重取；"+
			"而这份数据有没有问题，本层答不了）", errNotReadToEOF, e))
	case e != nil:
		return join(fmt.Errorf("%w：%v（【该重取】—— 这份流坏了，不是调用方少读了）",
			errStreamBroken, e))
	case !b.sawEOF:
		return join(fmt.Errorf("%w（读到这里就放手了 ⇒ gzip 的 CRC 与长度校验【没有发生】，"+
			"这份数据是不是完整的，本层答不了）", errNotReadToEOF))
	}
	return rerr
}
