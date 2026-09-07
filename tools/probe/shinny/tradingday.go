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
// 而后者才是有意义的否定结论。所以先得证明**此刻真的在盘中**，两条独立的腿：
//
//  1. **新鲜度**：`SHFE.rb` 最新一根的 datetime 距now 不超过 5 分钟；
//  2. **对照组** `GFEX.si` **无夜盘**（v0.0 已实测），此刻它的最新一根**必须是陈旧的**。
//     它要是也「新鲜」，说明新鲜度判据本身坏了（时区、时钟、服务端回放……），
//     那么第 1 条对 rb 的结论一文不值。
//
// 两条腿会因为不同的原因失败，所以是两条腿。任一不成立 ⇒ SKIP，**不出结论**。
//
// # 第二条独立证据：跨度
//
// 与「end > last」无关的另一条：`end − start + 1` 是否等于**整个交易日**的标称分钟数。
// 夜盘 21:30 时，当日才走了约 30 根，若跨度已经是 345（含明天的日盘），
// 那它只能是预知的——这条不依赖 last_id 的取值。
func probeTradingDayPredicted(ctx context.Context, md, tok string) {
	const subject = "KQ.m@SHFE.rb"
	const control = "KQ.m@GFEX.si"

	nodes, err := dayNodes(ctx, md, tok, []string{subject, control, "KQ.m@SHFE.ag"})
	if err != nil {
		report("shinny-trading-day-predicted", "FAIL", "拉取失败: "+err.Error())
		return
	}
	sub, ok := nodes[subject]
	if !ok {
		report("shinny-trading-day-predicted", "FAIL", subject+" 未就绪")
		return
	}
	ctl, okCtl := nodes[control]

	now := time.Now().In(cst)
	fresh := func(n dayNode) (bool, string) {
		if n.newest.IsZero() {
			return false, "无 datetime"
		}
		age := now.Sub(n.newest)
		return age <= 5*time.Minute, fmt.Sprintf("%s（%.0f 分钟前）",
			n.newest.Format("01-02 15:04"), age.Minutes())
	}
	subFresh, subAge := fresh(sub)
	ctlFresh, ctlAge := "", ""
	ctlLive := false
	if okCtl {
		ctlLive, ctlAge = fresh(ctl)
		ctlFresh = fmt.Sprintf("对照 %s 最新=%s 新鲜=%v（期望 false——无夜盘）",
			control, ctlAge, ctlLive)
	} else {
		ctlFresh = "对照 " + control + " 未就绪"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "此刻 %s\n", now.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "       %s  last_id=%d  最新=%s\n", subject, sub.lastID, subAge)
	fmt.Fprintf(&b, "       %s\n", ctlFresh)

	// —— 先判「此刻能不能得出结论」——
	if !subFresh {
		fmt.Fprintf(&b, "       ⇒ SKIP：%s 的数据不新鲜，说明不在盘中。"+
			"收盘时 end==last 对两种假设都成立，此刻【不可分辨】。\n"+
			"       夜盘 21:00 开，21:15 之后再跑。", subject)
		report("shinny-trading-day-predicted", "SKIP", b.String())
		return
	}
	if !okCtl {
		b.WriteString("       ⇒ SKIP：对照组未就绪，无法排除「新鲜度判据本身失效」。")
		report("shinny-trading-day-predicted", "SKIP", b.String())
		return
	}
	if ctlLive {
		fmt.Fprintf(&b, "       ⇒ SKIP：对照组 %s 【无夜盘】却也判成新鲜，"+
			"说明新鲜度判据坏了（时区/时钟/服务端回放），\n"+
			"       此时 %s 的「在盘中」不成立，不出结论。", control, subject)
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
	report("shinny-trading-day-predicted", st, b.String())
}
