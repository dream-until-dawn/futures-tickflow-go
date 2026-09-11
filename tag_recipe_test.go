package tickflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— 这一条守的是【写下来的那条打 tag 命令，照做会得到我们说的结果】——
//
// ⛔ 由来（2026-09-11 实测，那条验证链第一次真用就撞上）：
// `git tag -a -F <file>` 默认走 `--cleanup=whitespace`，**它会把连续空行折叠掉**。
// 于是照仓里写的两步做，打出来的注解与 `tools/audit/tag_body.py` 裁出的正文**不等**
// （当时差一个字节，而两端 sha256 不同）。
//
// 🔴 本仓那条：**凡是要保住确切字节的那一步，别让它经过一层会翻译的管道**
// —— 而 `git tag -F` 自己就是那层管道。
// ⇒ 这支脚本保住了字节，**而下一步会把它们改掉**：两步里只要有一步会翻译，整条链就断，
//    **而断了的样子是「两条命令都成功了」**。
//
// ⚠️ 为什么做成守卫而不是「记住」：这条食谱现在有【两份副本】
// （`docs/release/v0.4.1.md` 与 `tools/audit/tag_body.py` 的 docstring），
// 而本仓记过的那条 —— **副本会和正本漂开**。再加一份副本时，这里当场红。

// guard: 仓里每一条写出来的 `git tag -a … -F` 食谱都必须带 --cleanup=verbatim。
func TestTagRecipesPinVerbatimCleanup(t *testing.T) {
	const want = "--cleanup=verbatim"

	// ⚠️ 射程，写清楚，别让这句话看起来比判据宽：
	// 判据只看【缩进的命令行】——「去掉行首空白之后以 `git tag -a` 开头」。
	// ⇒ 夹在散文里、用反引号括起来的那种（`docs/release/v0.4.1.md` 里
	//   逐字引用【旧的错误版本】的那一句）**不在射程内，这是故意的**：
	//   那是一份历史记录，改了它就把历史改成假的。
	// ⇒ 代价：有人若把食谱写成一句话而不是一行命令，这条守卫照不到。
	isRecipe := func(line string) bool {
		return strings.HasPrefix(strings.TrimLeft(line, " \t"), "git tag -a")
	}

	var checked, recipes int
	err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".md", ".py", ".go", ".txt", ".sh", ".yml", ".yaml":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		checked++
		for i, line := range strings.Split(string(b), "\n") {
			if !isRecipe(line) {
				continue
			}
			recipes++
			if !strings.Contains(line, want) {
				t.Errorf("%s:%d 这条食谱少了 %s：\n    %s\n"+
					"  ⇒ `git tag -F` 默认折叠连续空行 ⇒ 打进 tag 的字节\n"+
					"     与 tools/audit/tag_body.py 裁出来的【不是同一串】，\n"+
					"     而两条命令都会报成功。", p, i+1, want, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走查仓库时出错：%v", err)
	}

	// ⛔ 前提自检：真的扫到了文件，也真的扫到了食谱。
	// 🔴 一条食谱都没有时，上面的循环一次都不跑，**而这条测试照绿** —— 空转与「全都合规」同形。
	if checked == 0 {
		t.Fatal("一个文件都没扫到 —— 这条守卫是空转的，读数作废")
	}
	if recipes == 0 {
		t.Fatalf("扫了 %d 个文件，一条 `git tag -a` 食谱都没找到 —— "+
			"要么食谱被删了、要么判据漂了；两种都得有人看一眼，不能算绿", checked)
	}
	t.Logf("扫了 %d 个文件，%d 条食谱都带着 %s", checked, recipes, want)
}
