package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// probeEmbeddedTemplateMatchesMeasured 拿【实测 1m】去对 calendar/embedded 的内置时段表。
//
// # 为什么要有这条
//
// 内置表抄自天勤 openmd 目录，而那份目录被截断过，且它的 trading_time 是
// **按合约**给的——抄到老合约那一行就会把一个【过期的】时段当成当前值。
// 2026-09-07 实测抓到一个：中金所国债起点写的是 `09:15`，实际是 `09:30`，
// 而它已经随 `v0.1.0` 发出去了。
//
// **完全静默**：`embedded.DayOf` 照模板合成 Sessions，于是那一族的每一根 K 线
// 边界都错、判完结以为交易早开 15 分钟，而相位不受影响（国债无夜盘），
// 所以没有任何东西会报错。
//
// # 这条探针存在的真正理由：那张表还有五种形状没被对过
//
// 修掉国债那一行而不管其余的，就是「修了看得见的那一半」。
// 表里一共六种不同形状，这条探针每种取一个代表，全部对一遍：
//
//	商品日盘 × 夜盘 0 / 120 / 240 / 330 分   四种
//	中金所股指日盘（无夜盘）                  一种
//	中金所国债日盘（无夜盘）                  一种
//
// # 方向：本库是【被测对象】，真值来自天勤
//
// 别和「探针不该拿本库的日历验本库」搞混——那条说的是不能用本库的日历
// 去给探针自己当**对照组**（日历错了两边会一起错）。
// 这里本库是**被测的那一方**，真值来自外部，方向是对的。
func probeEmbeddedTemplateMatchesMeasured(ctx context.Context, md, tok string) {
	name := "shinny-embedded-template-matches-measured"

	// 每种形状取一个代表。**代表选错会让整族漏检**，所以注明它代表谁。
	reps := []struct {
		sym   string
		key   tickflow.ProductKey
		shape string
	}{
		{"KQ.m@SHFE.rb", pk(tickflow.SHFE, "rb"), "商品日盘 + 夜盘 120（34 个品种）"},
		{"KQ.m@SHFE.cu", pk(tickflow.SHFE, "cu"), "商品日盘 + 夜盘 240（7 个品种）"},
		{"KQ.m@SHFE.ag", pk(tickflow.SHFE, "ag"), "商品日盘 + 夜盘 330（3 个品种）"},
		{"KQ.m@GFEX.si", pk(tickflow.GFEX, "si"), "商品日盘 + 无夜盘（17 个品种）"},
		{"KQ.m@CFFEX.IF", pk(tickflow.CFFEX, "IF"), "中金所股指日盘（4 个品种）"},
		{"KQ.m@CFFEX.T", pk(tickflow.CFFEX, "T"), "中金所国债日盘（4 个品种）"},
	}

	var b strings.Builder
	bad, notSettled := 0, 0
	for _, r := range reps {
		lbls, err := minuteLabelsWide(ctx, md, tok, r.sym)
		if err != nil {
			fmt.Fprintf(&b, "  %-14s 拉取失败: %v\n", r.sym, err)
			bad++
			continue
		}
		// 把【窗口本身】打出来。这条 bug 之所以能发出一条错误的指控，
		// 就是因为「窗口覆盖到哪」是个不可见的中间量：
		// 断在交易日中间时，输出看起来只是「那天的时段短了一点」。
		// **能靠结构看见的，就别靠注意力盯住。**
		nBars, lo, hi := windowShape(lbls)
		day, segs := lastFullDaySegs(lbls)
		if day == "" {
			fmt.Fprintf(&b, "  %-14s 窗口 %d 根 %s→%s，里面没有【完整】的日盘，跳过\n",
				r.sym, nBars, lo, hi)
			continue
		}
		// —— 读第二遍，两遍必须一致 ——
		//
		// 这一格原来是空的：si/IF 那对对照组张开的是【读数】维度，
		// 而「读到的比实际少」属于【数据完整性】维度——**当时没有任何东西守它**。
		//
		// 它挡的是一整类失效，**与根因无关**：窗口截断、快照没收齐、
		// 服务端增量还在到达……只要两遍读出的不是同一个东西，就不出结论。
		// 它是在根因还没定死时加的，而**加它这个决定不依赖根因**——
		// **根因未定时，先让错误的形态变安全**；止血和定位是两个动作，
		// 前者不必等后者。这条不依赖根因定没定，所以它一直成立。
		//
		// （根因**没有完全定死**，两边都一度把强度报高了一档：
		// 已确认的是「该错误随墙钟单调消失」+「锚是 now − 10 天」；
		// 从这两条到「覆盖末端越过 14:59」那一步**没有直接证据**。
		// 中间我自我推翻过一次，理由是「间歇不是锚定错误的形状」——
		// **那句话的前提错了**，因为锚随墙钟走；但推翻一个错误的反驳，
		// **不等于证明了原结论**。
		// **「这个错误是确定性的」必须带上「相对于什么」**，
		// 而且还要说清**这条证据能推到哪一步为止**。详见 docs/probe.md 6.8。）
		//
		// 为什么非有不可：这条探针出错时**不是沉默，是发出一条指控**，
		// 而且是间歇的。间歇的错误指控比确定性的更糟——自然反应是「再跑一次」，
		// 跑一次它就绿了，于是它永远不会被修，只会被【习惯】。
		// 那就是 `|| true` 的形状，只不过不用谁去加那三个字符，它自己会变绿。
		lbls2, err2 := minuteLabelsWide(ctx, md, tok, r.sym)
		if err2 != nil {
			fmt.Fprintf(&b, "  %-14s 第二遍拉取失败，不出结论: %v\n", r.sym, err2)
			notSettled++
			continue
		}
		day2, segs2 := lastFullDaySegs(lbls2)
		if day2 != day || strings.Join(segs2, " ") != strings.Join(segs, " ") {
			fmt.Fprintf(&b, "  %-14s 两遍读出的不一样，判为【数据未收齐】，不出结论：\n"+
				"                 第一遍 %s %s\n                 第二遍 %s %s\n",
				r.sym, day, strings.Join(segs, " "), day2, strings.Join(segs2, " "))
			notSettled++
			continue
		}

		tmpl, terr := embedded.Template(r.key, dayNum(day))
		if terr != nil {
			fmt.Fprintf(&b, "  %-14s 内置表答不了: %v\n", r.sym, terr)
			bad++
			continue
		}
		want := dayShape(tmpl)
		got := strings.Join(segs, " ")
		mark := "="
		if got != want {
			mark = "≠"
			bad++
		}
		fmt.Fprintf(&b, "  %-14s %s  日盘实测 %s   [窗口 %d 根 %s→%s]\n",
			r.sym, day, got, nBars, lo, hi)
		fmt.Fprintf(&b, "  %-14s %s  日盘内置 %s   ← %s\n", "", mark, want, r.shape)

		// —— 夜盘那一半 ——
		//
		// 这一半原来【一条都没对过】：cu 那 240 分钟没有任何探针查过；
		// 而 rb/ag 的「间接覆盖」是循环的——它比的是天勤给的
		// trading_day_end_id 跨度，而那个跨度多半就是照标称模板算的
		// （这正是我们自己开的那条未验项）。**拿一个可能照标称算的数去验标称。**
		//
		// 现有的 grid-differs-by-night 也不够：它断言 CU0 的 60m 日盘标签，
		// 那只约束「夜盘分钟数 % 60 == 0」——**180 和 300 照样通过**。
		//
		// 而相位挂在夜盘上（Phase = 标称夜盘 mod 周期）——
		// **烧到我的那一半（日盘）和最承重的那一半（夜盘），不是同一半。**
		nightGot, okN := nightMinutesOf(lbls, day)
		nightWant := tmpl.NightMinutes()
		switch {
		case !okN && nightWant == 0:
			fmt.Fprintf(&b, "  %-14s =  夜盘 无，内置也是 0\n", "")
		case !okN:
			fmt.Fprintf(&b, "  %-14s ?  夜盘取不到（内置 %d 分）"+
				"——【取不到】不是【没有】，不算数\n", "", nightWant)
		case nightGot == nightWant:
			fmt.Fprintf(&b, "  %-14s =  夜盘实测 %d 分 = 内置 %d 分\n",
				"", nightGot, nightWant)
		default:
			fmt.Fprintf(&b, "  %-14s ≠  夜盘实测 %d 分 ≠ 内置 %d 分\n",
				"", nightGot, nightWant)
			bad++
		}
	}

	// 有任何一个代表没读稳，就【不宣称】全部对过——少一个代表就是少一整族。
	if bad == 0 && notSettled > 0 {
		report(name, "SKIP", fmt.Sprintf(
			"已比对的都相同，但有 %d 个代表数据未收齐，不宣称全部对过：\n", notSettled)+
			strings.TrimRight(b.String(), "\n"))
		return
	}
	if bad == 0 {
		report(name, "PASS", "六种形状各取一个代表，日盘【与夜盘】都与实测相同：\n"+
			strings.TrimRight(b.String(), "\n")+
			"\n\n       代表选错会让整族漏检，所以每行都注明它代表谁。")
		return
	}
	// ⚠️ 措辞不能预设是表错了。
	//
	// 这条探针的职责是【拿外部真值去指控内置表】——所以它出错时不是沉默，
	// 是**发出一条指控**。而它确实出过一次：窗口断在交易日中间，
	// 那一天的时段被读短一截，输出渲染成「内置时段表与实测不符」。
	// 要不是评审那边有第二个客户端去对，结论会是「快改内置表」——
	// **而那会把一张对的表改错**。
	report(name, "FAIL", "内置表与实测【不符】（"+fmt.Sprint(bad)+" 处）——先判断是哪一边：\n"+
		strings.TrimRight(b.String(), "\n")+
		"\n\n       ① 取数不全？两处看：\n"+
		"          · 每行末尾的【窗口】——断在交易日中间时，那一天会看起来短一截；\n"+
		"          · 那一天的时段是不是被读成了【分裂的两段】——时段内缺根会这样，\n"+
		"            而它不是窗口问题，容易被误读进 ②。\n"+
		"          两种都不是表错了。\n"+
		"       ② 表旧了？内置表抄自被截断的 openmd 目录，而那里的\n"+
		"          trading_time 是按合约给的；抄到老合约那一行就会把过期时段当成当前值。\n"+
		"\n       先排除 ① 再动表。")
}

func pk(exch, prod string) tickflow.ProductKey {
	return tickflow.ProductKey{Exchange: exch, Product: prod}
}

// dayShape 把模板的日盘段渲染成 "09:00-10:15 10:30-11:30 13:30-15:00"。
func dayShape(t tickflow.SessionTemplate) string {
	out := make([]string, 0, len(t.Day))
	for _, s := range t.Day {
		out = append(out, fmt.Sprintf("%s-%s", hhmmOf(s.Start), hhmmOf(s.End)))
	}
	return strings.Join(out, " ")
}

// hhmmOf 把「当日 00:00 起的毫秒偏移」渲染成 HH:MM。
func hhmmOf(off int64) string {
	m := off / 60000
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// lastFullDaySegs 从「自然日 → 时刻标签」里挑最后一个【完整的】日盘自然日，
// 把连续分钟聚成时段，返回 "09:00-10:15 10:30-11:30 13:30-15:00" 这样的分段。
//
// 「完整」判据：既有 09:xx 之后的根，也有 14:xx 之后的根——
// 只看「有没有根」会把今天这种刚开盘的日子也算进来，
// 那时段会被截成一小截，然后与内置表比出一个假的不符。
func lastFullDaySegs(m map[string][]string) (string, []string) {
	days := make([]string, 0, len(m))
	for d := range m {
		days = append(days, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for _, d := range days {
		var day []string
		for _, l := range m[d] {
			if l >= "09:00" && l < "16:00" { // 只看日盘，夜盘另说
				day = append(day, l)
			}
		}
		if len(day) == 0 {
			continue
		}
		sort.Strings(day)

		// 「完整」的判据是**精确的**，不是余量。
		//
		// 前两版都栽在余量上：
		//   第一版 `末根 >= 14:00`   → 只剩 14:59 一根的截断日也算完整
		//   第二版 `末根 >= 14:50`   → 缺末尾 1–10 分钟的截断日仍算完整（实测缺到 14:56）
		// **我第二次只是把余量收紧了，没有把判据换成精确的**——
		// 于是同一个 bug 换了个幅度又回来了。
		//
		// 精确的判据：**窗口里必须还有【更晚的自然日】**。
		// 那证明窗口的末端已经越过了这一天，所以这一天不可能是被窗口切断的。
		// 它不看根数、不看时刻、**也不看内置表**（拿模板去判「测全了没有」是循环的）。
		//
		// 前端同理由 day[0] 守着：窗口起点落在日中时，首根会偏晚。
		if day[0] > "09:31" || !hasLaterDate(days, d) {
			continue
		}
		// 连续分钟聚成段。天勤按【开盘时刻】标注，所以段末要 +1 分钟才是收盘。
		var segs []string
		start := day[0]
		for i := 1; i < len(day); i++ {
			if mins(day[i]) != mins(day[i-1])+1 {
				segs = append(segs, start+"-"+plus1(day[i-1]))
				start = day[i]
			}
		}
		segs = append(segs, start+"-"+plus1(day[len(day)-1]))
		return d, segs
	}
	return "", nil
}

func mins(hhmm string) int {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return h*60 + m
}

func plus1(hhmm string) string {
	t := mins(hhmm) + 1
	return fmt.Sprintf("%02d:%02d", t/60, t%60)
}

// dayNum 把 "2026-09-04" 转成 TradingDay。
//
// 用的是**自然日**当交易日编号——对日盘段来说两者相同（日盘不跨日），
// 而本条只比日盘，所以够用。夜盘长度由别的探针管。
func dayNum(date string) tickflow.TradingDay {
	t, err := time.ParseInLocation("2006-01-02", date, cst)
	if err != nil {
		return 0
	}
	y, mo, d := t.Date()
	return tickflow.TradingDay(y*10000 + int(mo)*100 + d)
}

// minuteLabelsWide 与 minuteLabels 同源，但窗口大得多。
//
// 共用的那个 view_width=300：对 rb 恰好够一个完整日盘，对 ag（每日 555 根）
// 就不够了——于是「窗口里没有完整的日盘」，而那看起来像是数据的问题，
// 其实是取数窗口的问题。**取不到和不存在长得一样**，所以这里单开一个。
func minuteLabelsWide(ctx context.Context, md, tok, sym string) (map[string][]string, error) {
	// daysBack=0 ⇒ 锚在末端：取最新的 2000 根。
	// 锚在起点时窗口末端会断在某个交易日中间，而那正是这条探针最会误判的地方。
	return minuteLabelsN(ctx, md, tok, sym, 1, 2000, 0)
}

// nightMinutesOf 从 1m 标签反推「属于交易日 D 的那段夜盘」有多少分钟。
//
// 夜盘挂在【前一个交易日】的自然日上：交易日 2026-09-07 的夜盘在 09-04（周五）
// 21:00 起，跨午夜的部分落在 09-05（周六）的日期上。所以要找 D 之前
// 最近一个有 ≥21:00 根的自然日 P，再把 P 的晚间与 P+1 的凌晨加起来。
//
// 返回 (分钟数, 找到没有)。找不到就说找不到——**别把「取不到」当成 0**，
// 那正好是「停夜盘」的读数，两者混起来会让一次取数失败伪装成一条结论。
func nightMinutesOf(m map[string][]string, d string) (int, bool) {
	dt, err := time.ParseInLocation("2006-01-02", d, cst)
	if err != nil {
		return 0, false
	}
	for back := 1; back <= 4; back++ {
		p := dt.AddDate(0, 0, -back).Format("2006-01-02")
		var evening int
		for _, l := range m[p] {
			if l >= "21:00" {
				evening++
			}
		}
		if evening == 0 {
			continue
		}
		next := dt.AddDate(0, 0, -back+1).Format("2006-01-02")
		var early int
		for _, l := range m[next] {
			if l < "04:00" {
				early++
			}
		}
		return evening + early, true
	}
	return 0, false
}

// hasLaterDate 报告 days 里有没有严格晚于 d 的自然日。
//
// 这是「这一天没被窗口切断」的**精确**证据：窗口的末端既然已经落到更晚的日子上，
// 就不可能同时切在这一天中间。
//
// 为什么不用「根数够不够 / 末根够不够晚」——那些都是【余量】判据，
// 而余量判据的失效方式是**渐进的**：窗口边界每天挪一点，
// 总有一天它落进余量里，而那天探针会发出一条【指控内置表】的错误结论。
// 这条探针的唯一职责就是拿外部真值去指控内置表，
// **它出错时不是沉默，是发出一条指控**——所以它的前置判据不能有余量。
func hasLaterDate(days []string, d string) bool {
	for _, x := range days {
		if x > d {
			return true
		}
	}
	return false
}

// windowShape 返回这次取数的窗口形状：总根数、最早、最晚。
//
// 存在的理由只有一个：**让「窗口覆盖到哪」变成可见的**。
//
// 这条探针出过一个真错：窗口锚在起点、末端断在某个交易日中间，
// 于是那一天的时段被读短了一截，而输出把它渲染成
// 「内置时段表与实测不符」——**一条没有依据的指控**。
// 当时我写注释时是小心的（甚至专门算过「对 rb 恰好够」），
// 但「此刻窗口覆盖到哪」这个量不在输出里，所以它变化时没有人会知道。
//
// ⇒ **凡是要求「小心」的地方，先问那里是不是缺了一个可见的中间量。**
func windowShape(m map[string][]string) (n int, lo, hi string) {
	for d, ls := range m {
		n += len(ls)
		if lo == "" || d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	return n, lo, hi
}
