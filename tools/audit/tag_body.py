#!/usr/bin/env python3
"""把 `docs/release/<tag>.md` 里【要进 tag 注解】的那一段裁出来。

## 为什么有这支脚本

tag 注解是本仓这套门禁里**唯一一件既不可改、又不在任何树里**的产物 ——
比树 / 筛子 / 突变 / 自检 / porcelain 全都够不着它。
（实测过一次它的代价：一版处置表有两格是反的，**已经准备刻进版本了**，
 是靠人读出来的，不是靠任何闸门。）

⇒ 处置是把注解落成仓里一个文件，走常规评审。
⚠️ **而那样只解了一半**：从「评审放行的那个文件」到「真正打进 tag 的那串字节」，
中间还有一步 —— 若那一步靠人手复制，就等于把刚关上的洞又开了一半。
⇒ 这支脚本就是那一步，而它**只做裁剪，不做别的**：

    python tools/audit/tag_body.py docs/release/v0.4.1.md > body.txt
    git tag -a v0.4.1 -F body.txt <SHA>

## 判据

文件开头那个 HTML 注释块（`<!-- ... -->`）是**写给仓里读者的**，不进 tag；
它之后的全部内容就是注解正文。

⛔ **而「靠一个 `-->` 切」是一个会漂的判据**，所以它有一条守卫：
`docs_guards_test.go` 的 `TestReleaseNotesTagBodyIsExtractable` 断言
每个 `docs/release/*.md` 都恰好有一个这样的头、且裁出来的正文非空。
⇒ 有人哪天把那个头删了、或加了**第二个「`-->` 紧跟空行」**，**当场红**，
  而不是等到打 tag 那一刻。
⚠️ 射程：判据数的是那个**组合**，不是单个 `-->` ⇒ 一个孤零零的 `-->` 不会被拦，
  而它会原样进注解（实测）。写下来，别让这句话看起来比判据宽。
"""
import io
import sys

# ⚠️ 本仓规矩（`TestScriptsWrapBothStreams` 当场抓住了我）：脚本里有中文就要包**两个流**。
# 🔴 **stderr 也要包**，理由是本仓记过的那条：断言消息与 `SystemExit` 走的是 stderr，
# 而**一条读不懂的失败信息，和没有失败信息差不多**。
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

HEAD_OPEN = "<!--"
HEAD_CLOSE = "-->\n\n"


def tagBody(text):
    """裁出注解正文。头不合规就抛 —— **失败方向是「不出正文」，不是「出一份可疑的」**。"""
    if not text.startswith(HEAD_OPEN):
        raise ValueError("文件开头不是 <!-- 注释块 —— 裁法没有依据，拒绝出正文")
    if text.count(HEAD_CLOSE) != 1:
        raise ValueError("`-->` ＋ 空行 出现 %d 次（要恰好 1 次）—— "
                         "裁法会切错地方，拒绝出正文" % text.count(HEAD_CLOSE))
    body = text[text.index(HEAD_CLOSE) + len(HEAD_CLOSE):]
    if not body.strip():
        raise ValueError("裁出来的正文是空的 —— 拒绝出一份空注解")
    return body


def main():
    if len(sys.argv) != 2:
        print("用法：python tools/audit/tag_body.py docs/release/<tag>.md", file=sys.stderr)
        raise SystemExit(2)
    text = io.open(sys.argv[1], encoding="utf-8").read()
    sys.stdout.buffer.write(tagBody(text).encode("utf-8"))


if __name__ == "__main__":
    main()
