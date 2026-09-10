package shinnyref

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ScanResult 是一次整包扫描的账。
//
// ⛔ **`ByClass` 存在的理由不是好奇** —— 上游全量里有 **8 个 class**，
// 而本包只要 `FUTURE` 那一种。若把其余的**静默跳过**，
// 「跳过了多少条」这个数就没了，而**读的人分不出「上游没有」和「我漏收了」**。
//
// ⇒ 判据用的是本包给 NightState 那条同一个：**可逆性**。
// 报出来 ⇒ 调用方可以选择不看（信息还在）；不报 ⇒ 它拿不回来（不可逆）。
type ScanResult struct {
	// Contracts 是成功解出来的期货合约数。
	Contracts int
	// ByClass 是**每一个 class 见到了多少条**，包括被跳过的那些。
	//
	// 2026-09-10 的全量读数（可以拿来对账）：
	//
	//	FUTURE_OPTION 199023 · FUTURE_COMBINE 22483 · OPTION 10242 · **FUTURE 7671**
	//	· FUTURE_INDEX 88 · FUTURE_CONT 88 · SPOT 21 · INDEX 4      合计 239620
	ByClass map[string]int
}

// String 让它能直接贴进一份报告里。
func (r ScanResult) String() string {
	keys := make([]string, 0, len(r.ByClass))
	for k := range r.ByClass {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return r.ByClass[keys[i]] > r.ByClass[keys[j]] })
	var b strings.Builder
	fmt.Fprintf(&b, "解出合约 %d 条；各 class：", r.Contracts)
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "%s %d", k, r.ByClass[k])
	}
	return b.String()
}

// Scan 流式扫一份完整的合约目录，对每一条**期货合约**调用 fn。
//
// ⛔ **流式**，而这是量出来的必须：整包明文 **351,330,237 字节**、**239,620 条**记录。
// 一次全读进来再解，是把三百多 MB 装进内存**再**建二十多万个对象。
// ⚠️ 而本仓在 `store/segfile` 那组基准上量过同族的一次：
// 「一次全给」相对流式**内存约 945 倍，而时间还多约 24%** ——
// **它不是拿内存换时间，它两头都输**（那组数的射程是单趟顺序读，与这里一致）。
//
// ⇒ 用 `json.Decoder` 逐条走：顶层是一个大对象，每个值单独 `Decode` 一次。
//
// ⛔ **不做任何补全**：中途解不出来就带着已经走过的账**报错返回**，
// 不跳过、不猜。本仓自己犯过那一次的原话是：
// 「补上闭合括号后能正常解析，只是静默少了若干合约，之后每一步都正常」。
//
// ⚠️ 而「非 FUTURE 跳过」与「解不出来报错」是**两件事**：
// 前者是**上游本来就有别的东西**（8 个 class），后者是**这一条坏了**。
// ⇒ 前者记进 `ByClass`（可逆），后者中止（因为它答不了「坏了多少」）。
func Scan(r io.Reader, fn func(Contract) error) (ScanResult, error) {
	res := ScanResult{ByClass: map[string]int{}}
	dec := json.NewDecoder(r)

	tok, err := dec.Token()
	if err != nil {
		return res, fmt.Errorf("shinnyref: 读不到开头: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return res, fmt.Errorf("shinnyref: 顶层不是一个对象，开头是 %v", tok)
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return res, fmt.Errorf("shinnyref: 读键失败（已走过 %d 条）: %w", total(res), err)
		}
		key, _ := keyTok.(string)

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return res, fmt.Errorf("shinnyref: %q 那一条读不出来（已走过 %d 条）: %w",
				key, total(res), err)
		}
		var head struct {
			Class string `json:"class"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return res, fmt.Errorf("shinnyref: %q 那一条没有可读的 class: %w", key, err)
		}
		res.ByClass[head.Class]++
		if head.Class != ClassFuture {
			continue
		}
		c, err := DecodeContract(raw)
		if err != nil {
			return res, fmt.Errorf("shinnyref: %q: %w", key, err)
		}
		res.Contracts++
		if fn != nil {
			if err := fn(c); err != nil {
				return res, err
			}
		}
	}

	// ⛔ 把结尾那个 `}` 也读掉 —— **它就是「整份读完了」这件事的证据**。
	// 不读它的话，一份被截断的文件会在这里安安静静地结束
	// （`dec.More()` 在流断掉时也返回 false）。
	if _, err := dec.Token(); err != nil {
		return res, fmt.Errorf("shinnyref: 没读到结尾的 }（截断？已走过 %d 条）: %w",
			total(res), err)
	}

	// ⛔ 再往下读一次，要求它给 **io.EOF**。这一步有两个作用，
	// 而**第二个是它真正的理由**：
	//
	// 一、`}` 之后不该再有东西（多出来的内容 ⇒ 这不是我以为的那份文档）。
	//
	// 二、🔴 **它把底层的流推到 EOF，而那是 gzip 校验 CRC 与长度的唯一时机。**
	//    这一格是量出来的：把整份 gzip **砍掉末尾 8 字节**（正好是 CRC32＋长度那个尾），
	//    明文**一个字节都不少** ⇒ JSON 完整 ⇒ 上面那道守卫**满意地放行** ⇒
	//    `Scan` 返回 `err == nil`，**而这一份下载是坏的**。
	//    成因不在 gzip 那一层 —— 它的校验从来没被触发：
	//    **`Scan` 读到自己要的东西就停了，而校验发生在「读到头」那一刻。**
	//
	// ⇒ 教训写成一句可搬走的：**一个「读到末尾才校验」的层，
	// 被一个「读到我要的就停」的层包住时，那道校验等于不存在** ——
	// 而它不报错、不留痕，两层各自的测试还都是绿的
	// （本包就是这样：fetch_test 全绿、scan_test 全绿，
	//  是**合起来那条路**的测试把它打出来的）。
	if _, err := dec.Token(); err != io.EOF {
		return res, fmt.Errorf("shinnyref: 结尾的 } 之后这份流没有干净地结束"+
			"（截断？后面还有东西？已走过 %d 条）: %v", total(res), err)
	}
	return res, nil
}

func total(r ScanResult) int {
	n := 0
	for _, v := range r.ByClass {
		n += v
	}
	return n
}
