package tickflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 数据总览新增的文本类型必须实际被读；只消掉“未知类”报错并不算覆盖。
func TestDashboardAssetsControlCharactersAreScanned(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"page.js", "style.css", "browser.cjs"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("safe text\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	positive := scanForControlChars(t, root, classifyByLists)
	if positive.scanned != 3 || len(positive.hits) != 0 || len(positive.unclassified) != 0 {
		t.Fatalf("新文本类型正控不成立: %+v", positive)
	}
	for _, name := range []string{"page.js", "style.css", "browser.cjs"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("changed\x08text\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	negative := scanForControlChars(t, root, classifyByLists)
	if negative.scanned != 3 || len(negative.hits) != 3 || len(negative.unclassified) != 0 {
		t.Fatalf("实际控制字符没有被抓住，或只是未知类失败: %+v", negative)
	}
	for _, hit := range negative.hits {
		if !strings.Contains(hit, "U+0008") {
			t.Fatalf("未命中目标控制字符: %s", hit)
		}
	}
}
