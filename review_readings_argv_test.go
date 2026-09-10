package tickflow_test

import (
	"os/exec"
	"strings"
	"testing"
)

// guard: 跑真脚本的【行为】守卫。本仓已有的脚本类守卫都是文本断言，而文本断言
// 挡不住「把那次调用删掉」—— 这一条是那一族的第一个成员。
// TestReviewReadingsRefusesStrayFlags 打的是 `tools/audit/review_readings.py` 的**行为**，
// 不是它的文本。
//
// ⛔ 本仓已有的脚本类守卫（TestScriptsWrapBothStreams 那一族）都是**文本断言** ——
// 而文本断言挡不住「把那次调用删掉」：判据还在文件里躺着，只是没人调它。
// 🔴 **「代码里写着一道判据」和「那道判据被执行」是两回事**，
// 而这一族本仓栽过（`attempts > 0` 那半句：整条恒真，摘掉其中一项 ⇒ 0 红）。
//
// 这条守卫要钉住的那句话是：
// **任何以 `-` 开头而不是已知旗标的参数，【在任何位置】都要被拒，
// 并且报文要说「不认识的旗标」——不能让 git 的子命令去报它。**
//
// ⚠️ 它存在的直接原因：`--verify-merge -x` 曾经落到 `git rev-list` 上，
// 报出 `REFUSE: git rev-list --parents -n1 -x -> exit 129` ——
// **拒绝的方向是对的，而报文把读的人指向 `rev-list`。**
func TestReviewReadingsRefusesStrayFlags(t *testing.T) {
	py := pythonOrFail(t)
	const script = "tools/audit/review_readings.py"

	for _, c := range []struct {
		args []string
		want string // 报文里必须出现的那一段
	}{
		{[]string{"-x"}, "不认识的旗标"},
		{[]string{"--bogus"}, "不认识的旗标"},
		{[]string{"--Verify-Merge"}, "不认识的旗标"}, // 大小写不同就是不认识，没有「像」这一说
		{[]string{"-"}, "不认识的旗标"},
		{[]string{"--"}, "不认识的旗标"},
		// ⛔ 下面这两格是这条守卫真正的理由：**同一个形态在第二个参数上**。
		{[]string{"--verify-merge", "-x"}, "不认识的旗标"},
		{[]string{"--verify-merge", "--"}, "不认识的旗标"},
		// 旗标只许在第一位
		{[]string{"main", "--verify-merge"}, "只能出现在第一个参数"},
		// 多余的参数也拒 —— 静默忽略它，和静默忽略一个打错的旗标是同一件事
		{[]string{"main", "extra"}, "多了用不上的参数"},
	} {
		t.Run(strings.Join(c.args, "_"), func(t *testing.T) {
			out, code := runPy(t, py, script, c.args...)
			if code != 2 {
				t.Fatalf("exit=%d，要 2；输出：%s", code, out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("报文里没有 %q —— 它说的是：%s\n"+
					"（拒对了而说错了话，读的人会去查错的地方）", c.want, firstLine(out))
			}
		})
	}

	// ⚠️ 对照组：**合法的调用不许被这道判据拦下** ——
	// 否则上面九格可能只是在证明「它什么都拒」。
	for _, c := range [][]string{
		{"--verify-merge", "HEAD"},
		{"--verify-merge"},
		{"main"},
		{},
	} {
		t.Run("合法_"+strings.Join(c, "_"), func(t *testing.T) {
			out, _ := runPy(t, py, script, c...)
			if strings.Contains(out, "不认识的旗标") ||
				strings.Contains(out, "多了用不上的参数") {
				t.Fatalf("合法调用被参数判据拦下了：%s", firstLine(out))
			}
		})
	}
}

func pythonOrFail(t *testing.T) string {
	t.Helper()
	for _, n := range []string{"python", "python3"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	// ⛔ 这里【不 Skip】。本仓规定的自检本身就是 `python tools/audit/module_sweep.py`
	// ⇒ python 是本仓的前提，不是可选项；
	// 而**一次 Skip 在汇总里和一次 PASS 长得一样** —— 那正是本仓那条「静默绿」。
	t.Fatal("找不到 python —— 而本仓规定的自检就是 python tools/audit/module_sweep.py，" +
		"所以这不是「跳过」的理由")
	return ""
}

func runPy(t *testing.T, py, script string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(py, append([]string{script}, args...)...)
	b, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("跑不起来：%v", err)
	}
	return string(b), code
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
