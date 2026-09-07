package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// dayNode 是序列节点上与「交易日边界」有关的几个字段。
type dayNode struct {
	lastID   int64
	startID  int64
	endID    int64
	newest   time.Time // 最新一根的 datetime（天勤按开盘时刻标注）
	haveSpan bool      // start/end 两个字段是否都拿到了
}

// span 返回 end − start + 1，即这个交易日【声称】有多少根 1m。
func (n dayNode) span() int64 { return n.endID - n.startID + 1 }

// dayNodes 一次连接取多个合约的序列节点字段。
//
// 与 depths 的区别：这里要的是【最新】的一段，所以不给 focus_datetime。
func dayNodes(ctx context.Context, md, tok string, syms []string) (map[string]dayNode, error) {
	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader: http.Header{
			"User-Agent":    {"tqsdk-python 3.10.2"},
			"Accept":        {"application/json"},
			"Authorization": {"Bearer " + tok},
		},
	})
	if err != nil {
		return nil, err
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)

	send := func(v any) { b, _ := json.Marshal(v); c.Write(ctx, websocket.MessageText, b) }
	const durNano = int64(60) * 1e9
	durKey := fmt.Sprintf("%d", durNano)

	for i, s := range syms {
		send(map[string]any{"aid": "set_chart", "chart_id": fmt.Sprintf("t%d", i),
			"ins_list": s, "duration": durNano, "view_width": 200})
	}
	send(map[string]any{"aid": "peek_message"})

	snap := map[string]any{}
	out := map[string]dayNode{}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && len(out) < len(syms) {
		_, msg, err := c.Read(ctx)
		if err != nil {
			break
		}
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(msg, &m) != nil || m.Aid != "rtn_data" {
			send(map[string]any{"aid": "peek_message"})
			continue
		}
		for _, d := range m.Data {
			merge(snap, d)
		}
		for i, s := range syms {
			if _, seen := out[s]; seen {
				continue
			}
			ch := obj(snap, "charts", fmt.Sprintf("t%d", i))
			if ch == nil {
				continue
			}
			if ready, _ := ch["ready"].(bool); !ready {
				continue
			}
			ser := obj(snap, "klines", s, durKey)
			if ser == nil {
				continue
			}
			last, ok := ser["last_id"].(float64)
			if !ok || last < 0 {
				continue
			}
			n := dayNode{lastID: int64(last)}
			// 「拿不到」与「值为 0」必须分开——0 是个合法的 bar id。
			st, okS := ser["trading_day_start_id"].(float64)
			en, okE := ser["trading_day_end_id"].(float64)
			n.startID, n.endID, n.haveSpan = int64(st), int64(en), okS && okE

			// 最新一根的 datetime：按 id 取，不靠 map 遍历顺序
			if r := obj(obj(ser, "data"), fmt.Sprintf("%d", int64(last))); r != nil {
				if ts, ok := r["datetime"].(float64); ok && ts != 0 {
					n.newest = time.Unix(0, int64(ts)).In(cst)
				}
			}
			out[s] = n
		}
		send(map[string]any{"aid": "peek_message"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有任何合约就绪（%d 个）", len(syms))
	}
	return out, nil
}

// 标称每交易日 1m 根数。夜盘分钟 + 日盘 225 分钟（09:00-10:15 / 10:30-11:30 / 13:30-15:00）。
//
// 与 docs/probe.md 记的 rb 345 = 225 + 120 一致。
var nominalDayMinutes = map[string]int64{
	"KQ.m@SHFE.rb": 225 + 120, // 345
	"KQ.m@SHFE.ag": 225 + 330, // 555
	"KQ.m@GFEX.si": 225 + 0,   // 225，无夜盘——本探针的对照组
}

// probeTradingDayPredicted 判定 `trading_day_end_id` 是不是【预知】的。
//
// 这是 contract.md 未验表的第一项：若它预知，
// 「last_id < trading_day_end_id ⇒ 最后一根未完结」就是比 confirm 标志更强的判据，
// 还顺带给出交易日的 bar-id 边界。
//
// # 为什么必须盘中跑
//
// 收盘后 `trading_day_end_id` 恰好等于 `last_id`，**「预知」与「就是当前最后一根」
// 在那一刻长得一模一样**。判据只在盘中成立：
//
//	trading_day_end_id > last_id  ⇒  预知
//
// # 对照组：防的不是数据变化，是【本探针自己失效】
//
// 「收盘时段跑出来的 end==last」和「盘中跑出来的 end==last」长得一样，
// 而后者才是有意义的否定结论。所以先得证明**此刻真的在盘中**：
// `SHFE.rb` 最新一根距 now 不超过 5 分钟。
//
// 但「新鲜度判据」自己可能坏（时区、时钟、服务端回放），所以要对照组。
// 两个，**前提不同、覆盖面也不同**：
//
//   - `SHFE.rb1605` 已退市，序列停在 2016，**任何时刻都必须陈旧**。
//     挡的是最危险那种坏法：判据恒返回「新鲜」。**但它只挡得住这一种**——
//     阈值从 5 分钟松到几小时，它照样是陈旧的，拦不住。
//   - `GFEX.si` **无夜盘**（v0.0 实测），夜盘时段里它必须陈旧。
//     它能挡住小时量级的松动，**但只在夜盘时段成立**——GFEX 有日盘。
//
// 于是覆盖面是**不对称**的，这一点必须说出来而不是含糊过去：
//
//	夜盘时段：两个对照组都在 → 强
//	日盘时段：只有退市那个  → 弱（小时量级的判据松动没人拦）
//
// 所以日盘时段即使得出结论，也在输出里标明「对照组较弱」。
// 本探针的正路是**夜盘跑**，那也正是这个问题所在的时段。
//
// 评审 J1：原来只有 GFEX 一个且不分时段，白天跑会走进
// 「对照组也新鲜 ⇒ 判据坏了」——**SKIP 是对的，归因是错的**，
// 而错的归因会把下一个人送去查时区和时钟，那里什么也没有。
//
// # 第二条独立证据：跨度
//
// 与「end > last」无关的另一条：`end − start + 1` 是否等于**整个交易日**的标称分钟数。
// 夜盘 21:30 时，当日才走了约 30 根，若跨度已经是 345（含明天的日盘），
// 那它只能是预知的——这条不依赖 last_id 的取值。
func probeTradingDayPredicted(ctx context.Context, md, tok string) {
	const subject = "KQ.m@SHFE.rb"
	const nightCtl = "KQ.m@GFEX.si" // 只在夜盘时段有效——GFEX【有日盘】
	const alwaysCtl = "SHFE.rb1605" // 2016 年就退市了，任何时刻都必须是陈旧的

	nodes, err := dayNodes(ctx, md, tok,
		[]string{subject, nightCtl, alwaysCtl, "KQ.m@SHFE.ag"})
	if err != nil {
		report("shinny-trading-day-predicted", "FAIL", "拉取失败: "+err.Error())
		return
	}
	sub, ok := nodes[subject]
	if !ok {
		report("shinny-trading-day-predicted", "FAIL", subject+" 未就绪")
		return
	}

	now := time.Now().In(cst)
	fresh := func(n dayNode) (bool, string) {
		if n.newest.IsZero() {
			return false, "无 datetime"
		}
		age := now.Sub(n.newest)
		// 带上年份：rb1605 打成「05-13」会被读成今年，而它是 2016 的
		return age <= 5*time.Minute, fmt.Sprintf("%s（%.1f 天前）",
			n.newest.Format("2006-01-02 15:04"), age.Hours()/24)
	}
	subFresh, subAge := fresh(sub)

	// 墙钟在不在 rb 的交易时段里，以及是不是夜盘那一段。
	//
	// 这一条**不看数据**，只看钟——所以它与「新鲜度」是两种坏法：
	// 新鲜度坏在数据侧（喂过来的 datetime 不对），这一条坏在时间侧。
	// 判据松动到几小时时，新鲜度会放行而这一条不会。
	//
	// 时段是照交易所公布的写死的，**不 import 本库**：
	// 探针要断言外部世界长什么样，用本库的日历去验本库，
	// 日历错了两边会一起错，那就不是两条路。
	inSess, inNight := rbSession(now)

	var b strings.Builder
	fmt.Fprintf(&b, "此刻 %s（%s）\n", now.Format("2006-01-02 15:04:05"),
		map[bool]string{true: "夜盘时段", false: "非夜盘时段"}[inNight])
	fmt.Fprintf(&b, "       %s  last_id=%d  最新=%s\n", subject, sub.lastID, subAge)

	// —— 对照组：证明「新鲜度判据」本身没坏 ——
	//
	// 分两个，因为它们的前提不同：
	//
	//  1. alwaysCtl 是【已退市】合约，序列停在 2016 年，**任何时刻都必须陈旧**。
	//     它挡的是最危险的那一种坏法：判据恒返回「新鲜」。
	//
	//  2. nightCtl 是 GFEX，**只在夜盘时段成立**——GFEX 有日盘
	//     （09:00-10:15 / 10:30-11:30 / 13:30-15:00，v0.0 实测）。
	//     白天它当然是新鲜的，那不是判据坏了，是**这个对照组此刻不适用**。
	//
	// 这一条是评审 J1：原来只有第 2 个，且不分时段。白天跑会走进
	// 「对照组也新鲜 ⇒ 判据坏了」——**SKIP 是对的，归因是错的**。
	// 而错的归因会把下一个人送去查时区和时钟，那里什么也没有。
	checkCtl := func(sym, why string, applicable bool) (halt bool) {
		n, ok := nodes[sym]
		if !ok {
			fmt.Fprintf(&b, "       对照 %-14s 未就绪\n", sym)
			return applicable // 适用却取不到，就不能往下判
		}
		live, age := fresh(n)
		note := "期望陈旧"
		if !applicable {
			note = "此刻【不适用】，仅记录"
		}
		fmt.Fprintf(&b, "       对照 %-14s 最新=%-22s 新鲜=%-5v %s（%s）\n",
			sym, age, live, note, why)
		return applicable && live
	}
	bad := checkCtl(alwaysCtl, "已退市，序列停在 2016", true)
	if checkCtl(nightCtl, "GFEX 无夜盘、但有日盘", inNight) {
		bad = true
	}

	// —— 先判「此刻能不能得出结论」——
	//
	// 两道，坏法不同，缺一不可：
	//   墙钟不在时段内 → 根本没开市；
	//   墙钟在时段内但数据不新鲜 → 开市了但喂不过来（或今天是节假日）。
	if !inSess {
		fmt.Fprintf(&b, "       ⇒ SKIP：墙钟 %s 不在 %s 的任何交易时段内。\n"+
			"       收盘时 end==last 对两种假设都成立，此刻【不可分辨】——"+
			"这一条不看数据只看钟，\n"+
			"       所以判据松动也绕不过去。夜盘 21:00 开。",
			now.Format("15:04"), subject)
		report("shinny-trading-day-predicted", "SKIP", b.String())
		return
	}
	if !subFresh {
		fmt.Fprintf(&b, "       ⇒ SKIP：墙钟在时段内，但 %s 的数据不新鲜——"+
			"开市了却喂不过来，或今天是节假日。\n"+
			"       不出结论。", subject)
		report("shinny-trading-day-predicted", "SKIP", b.String())
		return
	}
	if bad {
		b.WriteString("       ⇒ SKIP：有对照组在【它适用的时段里】却判成新鲜，" +
			"说明新鲜度判据坏了（时区/时钟/服务端回放），\n" +
			"       此时「在盘中」不成立，不出结论。")
		report("shinny-trading-day-predicted", "SKIP", b.String())
		return
	}

	// —— 盘中，可以判了 ——
	if !sub.haveSpan {
		b.WriteString("       ⇒ FAIL：盘中却拿不到 trading_day_start_id / end_id 字段。")
		report("shinny-trading-day-predicted", "FAIL", b.String())
		return
	}
	fmt.Fprintf(&b, "       trading_day_start_id=%d  end_id=%d  跨度=%d  已到 %d 根\n",
		sub.startID, sub.endID, sub.span(), sub.lastID-sub.startID+1)

	// 本探针是【去判定一件未验的事】，不是守一条已知基线。
	// 所以「不预知」是一个合法结论，不是 FAIL——
	// 把一个真实的否定结论标成 FAIL，等于逼着以后的人去改判据迎合期望。
	//
	// FAIL 只留给【讲不通】的状态：两条腿互相矛盾，或字段取值本身不自洽。
	// 那才说明「我对这个字段的理解是错的」，而那正是探针该报警的事。
	leg1, l1 := "不确定", ""
	switch {
	case sub.endID > sub.lastID:
		leg1 = "预知"
		l1 = fmt.Sprintf("end_id(%d) > last_id(%d)，领先 %d 根",
			sub.endID, sub.lastID, sub.endID-sub.lastID)
	case sub.endID == sub.lastID:
		leg1 = "不预知"
		l1 = "end_id == last_id，而此刻确在盘中 ⇒ 它就是「当前最后一根」"
	default:
		l1 = fmt.Sprintf("end_id(%d) < last_id(%d)——讲不通", sub.endID, sub.lastID)
	}
	fmt.Fprintf(&b, "       腿① %s：%s\n", leg1, l1)

	// 腿②：跨度是不是【整个交易日】的标称分钟数。不依赖 last_id 的取值。
	//
	// 夜盘 21:30 时当日才走了约 30 根：
	//   跨度 = 345（含明天的日盘）⇒ 预知；跨度 ≈ 已到根数 ⇒ 不预知。
	leg2 := "不确定"
	for _, s := range []string{subject, "KQ.m@SHFE.ag"} {
		n, ok := nodes[s]
		if !ok || !n.haveSpan {
			fmt.Fprintf(&b, "       腿② %s 跨度取不到\n", s)
			continue
		}
		want := nominalDayMinutes[s]
		got, arrived := n.span(), n.lastID-n.startID+1
		var verdict string
		switch {
		case got == want && arrived < want:
			verdict, leg2 = "= 标称，而实到不足 ⇒ 预知", "预知"
		case got == want && arrived == want:
			verdict = "= 标称，但实到也已满——此刻分不出"
		case got == arrived:
			verdict, leg2 = "= 实到根数 ⇒ 不预知", "不预知"
		default:
			verdict = fmt.Sprintf("既不等于标称 %d 也不等于实到 %d——讲不通", want, arrived)
		}
		fmt.Fprintf(&b, "       腿② %s 跨度=%d（实到 %d）%s\n", s, got, arrived, verdict)
	}

	st := "PASS"
	// 拿到定论后【必须】填上 baselineAnswer，见它的注释。
	if concl := verdictOf(leg1, leg2); baselineAnswer != "" && concl != "" &&
		concl != baselineAnswer {
		fmt.Fprintf(&b, "       ⇒ FAIL：基线记的是「%s」，这次测出「%s」——"+
			"上游行为变了，先判断是它变了还是记录错了。\n", baselineAnswer, concl)
		report("shinny-trading-day-predicted", "FAIL", b.String())
		return
	}
	switch {
	case sub.endID < sub.lastID:
		st = "FAIL"
		b.WriteString("       ⇒ FAIL：字段取值不自洽。\n")
	case leg1 != "不确定" && leg2 != "不确定" && leg1 != leg2:
		st = "FAIL"
		fmt.Fprintf(&b, "       ⇒ FAIL：两条腿【互相矛盾】（腿①=%s，腿②=%s）——"+
			"说明我对这个字段的理解是错的。\n", leg1, leg2)
	case leg1 == "不确定" && leg2 == "不确定":
		st = "SKIP"
		b.WriteString("       ⇒ SKIP：两条腿都判不出，不出结论。\n")
	default:
		concl := leg1
		if concl == "不确定" {
			concl = leg2
		}
		fmt.Fprintf(&b, "       ⇒ **结论：%s**\n", concl)
		if concl == "预知" {
			b.WriteString("       ⇒ 「last_id < trading_day_end_id ⇒ 未完结」成立，" +
				"且上游直接给出交易日的 bar-id 边界。\n")
		} else {
			b.WriteString("       ⇒ 判完结仍然只能靠交易日历——时间模型那条主线不变。\n")
		}
	}
	b.WriteString("       两条腿会因不同原因失败，所以是两条腿：\n" +
		"       腿①比 end 与 last，腿②只看跨度、不依赖 last_id。")
	if !inNight {
		// 覆盖面不对称，就得说出来。「得出了结论」和「结论有多硬」是两件事，
		// 把后者含糊过去，下一个人会按夜盘那一次的强度去信这一次。
		b.WriteString("\n       ⚠️ 非夜盘时段：只有【已退市】那个对照组在，" +
			"它挡得住「判据恒返回新鲜」，\n" +
			"       挡不住小时量级的阈值松动。结论强度低于夜盘那一次，" +
			"定论以夜盘的为准。")
	}
	report("shinny-trading-day-predicted", st, b.String())
}

// baselineAnswer 是【已经定论】的答案："预知" / "不预知"；空串表示尚未定论。
//
// ⚠️ **拿到定论那一刻就要把它填上**，把这条探针从「提问期」转成「守基线期」。
//
// 为什么这个转换非做不可（评审提的，我认）：
// 提问期的 `PASS` 同时承载「预知」与「不预知」两个【相反】的结论，
// 退出码根本分不开它们。于是答案一旦翻转——上游哪天改了这个字段的行为——
// 探针照样 PASS，**而这正是探针存在的理由**。
// 不转，就等于在拿到答案的同时永久丧失了发现答案翻转的能力。
//
// 同一个形状在别处也出现过：一个会红的必过项迟早被加 `|| true`；
// 一个总亮着的标志位等于没有这一位。**一个不会失败的检查不是检查。**
const baselineAnswer = ""

// verdictOf 把两条腿归并成一个结论；两腿都判不出时返回空串。
func verdictOf(leg1, leg2 string) string {
	if leg1 != "不确定" {
		return leg1
	}
	if leg2 != "不确定" {
		return leg2
	}
	return ""
}

// rbSession 报告墙钟 t 是否落在 SHFE.rb 的交易时段内，以及是不是夜盘那一段。
//
// 时段照交易所公布写死：夜盘 21:00–23:00；日盘 09:00–10:15 / 10:30–11:30 / 13:30–15:00。
// **刻意不 import 本库的日历**——探针要断言外部世界长什么样；
// 拿本库的日历去验本库，日历错了两边会一起错，那就不是两条独立的路。
//
// 它不认节假日，所以「墙钟在时段内」只是必要条件；充分性由「数据新鲜」补齐。
// 两者坏法不同：这一条坏在时间侧（时区/时钟），新鲜度坏在数据侧。
func rbSession(t time.Time) (inSession, isNight bool) {
	m := t.Hour()*60 + t.Minute()
	const (
		h9, h1015    = 9 * 60, 10*60 + 15
		h1030, h1130 = 10*60 + 30, 11*60 + 30
		h1330, h15   = 13*60 + 30, 15 * 60
		h21, h23     = 21 * 60, 23 * 60
	)
	if m >= h21 && m < h23 {
		return true, true
	}
	switch {
	case m >= h9 && m < h1015,
		m >= h1030 && m < h1130,
		m >= h1330 && m < h15:
		return true, false
	}
	return false, false
}
