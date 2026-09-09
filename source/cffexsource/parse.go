// Package cffexsource 从中金所官网拉日行情 XML。
//
// ⛔ **它不是「可选的对账通道」，是中金所品种结算价的【唯一可用源】。**
// 新浪对中金所八个品种（IF/IH/IC/IM/T/TF/TS/TL）系统性不给结算价 ——
// 2026-09-09 复测四个：`IF2609` 156/156、`IM2609` 156/156、
// `T2612` 122/122、`TL2612` 122/122，**`s` 全部为 0**。
// ⇒ 没有这个包，中金所品种的逐日盯市算不了（`docs/probe.md` 第一节）。
//
// ⚠️ 这一版**只做解析**：字节 → 行。组装成 `tickflow.Bar` 与 HTTP 拉取在后面几件。
// 分开的理由同前几片：**解析的不变量要先能被单独测红。**
package cffexsource

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrNoFutures 这份 XML 里一条期货都没有。
//
// ⛔ **它必须和「解析出 0 行」分开**：这份 XML 里 **96% 是期权**
// （实测 714 条里 686 条），所以「过滤完是空的」这件事**极可能是过滤写反了**，
// 而不是「那天没有期货交易」。
// ⇒ 把它当成空结果，上层会把一整天记成「拉过、确认没有」。
var ErrNoFutures = errors.New("cffexsource: 这份 XML 里一条期货都没有——" +
	"而它有 96% 是期权，所以这多半是过滤反了，不是那天没交易")

// ErrNotXML 响应不是预期的 XML 形状（多半是被 WAF 挡了，或者拿到一张错误页）。
//
// ⚠️ 单列是因为 probe.md 记着：**四家交易所官网里三家被 WAF 挡**，
// 中金所今天开着不代表明天开着 —— 而它是唯一来源。
//
// ⛔ **登记⑱：一个【休市日】也会走到这里**（那天没有这份文件、或文档为空），
// 而消息此前只写「多半被 WAF 挡了」⇒ **把读的人指向错误的方向**。
// 消息已把休市列进去；**真正的分辨要靠交易日历，那在组装层，不在这里。**
var ErrNotXML = errors.New("cffexsource: 响应不是预期的 XML——" +
	"可能是那天休市（没有这份文件或文档为空），也可能被 WAF 挡了或拿到了错误页")

// SettleRow 是一条【期货】日行情。期权已经被过滤掉。
type SettleRow struct {
	InstrumentID string // IC2609
	ProductID    string // IC
	TradingDay   string // "20260908"

	Open, High, Low, Close float64
	Volume, Turnover       float64
	OpenInterest           float64
	PreOpenInterest        float64

	// Settle / PreSettle 是这个包存在的理由。
	//
	// ⚠️ 与新浪相反：**这里它们是有的**（实测 28/28 全部非零）。
	// PreSettle 是昨结算，下游拿它推涨跌停。
	//
	// ⛔ **而 0 在这里【不可分辨】**：XML 里写 0.000 与整个字段为空白，
	// 到这里都是 0（登记⑳，见 parseNum）。**别拿 0 去推断「缺失」。**
	Settle    float64
	PreSettle float64
}

// dailyData 是 XML 里一条 <dailydata> 的原样形状。
//
// ⚠️ **全部声明成 string，不是 float64。** 空字段在这份 XML 里渲染成
// `<openprice>\n</openprice>` —— **是空白，不是缺标签**（实测：74 条期权的 openprice 长这样）。
// 声明成 float64 会让 Unmarshal 在那些条目上报错，而它们本来就该被过滤掉。
type dailyData struct {
	InstrumentID    string `xml:"instrumentid"`
	ProductID       string `xml:"productid"`
	TradingDay      string `xml:"tradingday"`
	OpenPrice       string `xml:"openprice"`
	HighestPrice    string `xml:"highestprice"`
	LowestPrice     string `xml:"lowestprice"`
	ClosePrice      string `xml:"closeprice"`
	Volume          string `xml:"volume"`
	Turnover        string `xml:"turnover"`
	OpenInterest    string `xml:"openinterest"`
	PreOpenInterest string `xml:"preopeninterest"`
	Settlement      string `xml:"settlementprice"`
	PreSettlement   string `xml:"presettlementprice"`
}

type dailyDatas struct {
	Items []dailyData `xml:"dailydata"`
}

// IsOption 报告一个合约代码是不是期权。
//
// 判据是**含不含 `-`**：`HO2609-C-2500` 是期权，`IC2609` 是期货（probe.md）。
// ⚠️ 写成独立函数而不是内联，是为了**让这条判据能被单独测红** ——
// 它一旦写反，拿到的就是 686 条期权而不是 28 条期货，
// 而两者在「解析出了 N 行」这个层面上长得一样。
func IsOption(instrumentID string) bool {
	return strings.Contains(instrumentID, "-")
}

// ParseDaily 把一份日行情 XML 解析成【期货】行，期权已过滤。
//
// 它做三件事，每件对应一个实测出来的粗糙面：
//
//	一、期权与期货混在一份里     ⇒ 按 `-` 过滤（714 条里只有 28 条期货）
//	二、空字段是【空白】不是缺标签 ⇒ 一律按字符串读进来再转
//	三、过滤完为空 ⇒ **报错，不当成「那天没有」**（96% 是期权，空多半意味着过滤反了）
//
// ⚠️ 它**不**做的：不判完结、不排序、不换算交易日。
// 那些要日历，属于组装那一层。**写下来是为了让「没做」和「做漏了」分得开。**
func ParseDaily(b []byte) ([]SettleRow, error) {
	var doc dailyDatas
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%w：%v（前 80 字节：%s）", ErrNotXML, err, head(b, 80))
	}
	if len(doc.Items) == 0 {
		return nil, fmt.Errorf("%w：一条 <dailydata> 都没有（前 80 字节：%s）",
			ErrNotXML, head(b, 80))
	}

	out := make([]SettleRow, 0, 32)
	for i, it := range doc.Items {
		id := strings.TrimSpace(it.InstrumentID)
		if id == "" {
			return nil, fmt.Errorf("cffexsource: 第 %d 条没有 instrumentid", i)
		}
		if IsOption(id) {
			continue
		}
		var row SettleRow
		row.InstrumentID = id
		row.ProductID = strings.TrimSpace(it.ProductID)
		row.TradingDay = strings.TrimSpace(it.TradingDay)
		if row.TradingDay == "" {
			return nil, fmt.Errorf("cffexsource: %s 没有 tradingday", id)
		}

		var errs []error
		num := func(name, s string, dst *float64) {
			v, err := parseNum(s)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s 的 %s=%q：%w", id, name, s, err))
				return
			}
			*dst = v
		}
		num("openprice", it.OpenPrice, &row.Open)
		num("highestprice", it.HighestPrice, &row.High)
		num("lowestprice", it.LowestPrice, &row.Low)
		num("closeprice", it.ClosePrice, &row.Close)
		num("volume", it.Volume, &row.Volume)
		num("turnover", it.Turnover, &row.Turnover)
		num("openinterest", it.OpenInterest, &row.OpenInterest)
		num("preopeninterest", it.PreOpenInterest, &row.PreOpenInterest)
		num("settlementprice", it.Settlement, &row.Settle)
		num("presettlementprice", it.PreSettlement, &row.PreSettle)
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		out = append(out, row)
	}

	if len(out) == 0 {
		return nil, ErrNoFutures
	}
	return out, nil
}

// parseNum 把一个可能是空白的字段转成数。
//
// ⚠️ **空白 → 0，而非报错**：这份 XML 用空白表示「这一格没有值」
// （未成交的合约没有开高低）。而**报错会把整份 XML 判死**。
//
// ⛔⛔ **而这一步把「没写」和「写了 0」抹成了同一个值 —— 这是一个已知缺陷，登记⑳。**
//
//	<settlementprice></settlementprice>       ⇒ Settle = 0
//	<settlementprice>0.000</settlementprice>  ⇒ Settle = 0
//	⇒ 实测：两行 **SettleRow 逐字段完全相同**
//
// ⚠️ 本注释此前写着「把『0 是不是缺失』的判断留给知道语境的那一层」——
// **那句是假的，已撤回**：分辨它所需的信息**在这里就被销毁了**，上层无从判起。
// （评审方 2026-09-09 指出并给了对照组；我复现一致。）
//
//	它和本仓已经处理对的三件同族：`null` ≠ `[]`／`ErrCalendarGap` ≠ 没数据／
//	主连 ≠ 不存在 —— **这一次被合并掉的是「没写」与「写了 0」。**
//
// ⇒ **今天不修，而它有一个会自己响的到期条件**：
// 实测期货 28/28 结算价非空 ⇒ 分辨它的收益暂时为零。
// TestNoBlankSettlementInFutures 盯着这一条 —— **而它订阅的是【仓库里那份 fixture】，不是世界。**
//
//	它响的时刻    **下次有人刷新 fixture、而新数据里有空白结算价**
//	它不响的情况  **没人刷新 ⇒ 真实世界变了，它也不响**
//
// ⚠️ 这不是缺陷：注释永远不响，它至少在刷新那一刻响。
// ⛔ **而「有触发器」和「触发器会在正确的时刻响」是两句话** ——
// 我此前写成「哪天真实数据里出现空白，它就红」，**那是把后者说成了前者**
// （评审方 2026-09-09 收窄的）。
//
// ⇒ 真正订阅【世界】的是**探针**（默认跳过、靠环境变量开的那一类），不是单元测试。
// **分类不是命名：它决定了谁盯世界。**
// ⇒ **不是「以后记得看」，是「出现那一天会有东西响」。**
func parseNum(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

func head(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
