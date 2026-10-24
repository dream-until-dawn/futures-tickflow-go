"""只读审计本地历史分支，输出处置清单，不删除、重置或合并。"""
import argparse
import io
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, timeout=30).decode("utf-8", "replace").strip()


def report():
    main = git("rev-parse", "main")
    current = git("branch", "--show-current")
    branches = git("for-each-ref", "--format=%(refname:short)", "refs/heads").splitlines()
    merged = set(git("branch", "--merged", "main", "--format=%(refname:short)").splitlines())
    pending = [name for name in branches if name not in merged and name != current]
    lines = ["# 历史分支只读审计", "", "审计基线main：" + main + "。当前工作分支：" + current + "。",
             "", f"本地共{len(branches)}条分支，{len(merged)}条tip已被main包含；下表仅列未包含的历史分支，排除当前工作分支。",
             "已包含的分支可列为归档候选，但本次不删除。未包含不等于应合并：重排前备份可能含主线已有的等价补丁。",
             "", "| 分支 | tip | 独有提交数 | 等价补丁数 | 新补丁数 | 建议 |", "|---|---|---|---|---|---|"]
    for name in pending:
        tip = git("rev-parse", name)
        commits = git("rev-list", "--count", "main.." + name)
        cherry = git("cherry", "main", name).splitlines()
        equivalent = sum(line.startswith("-") for line in cherry)
        novel = sum(line.startswith("+") for line in cherry)
        advice = "保留重排前备份，不直接合并" if name.startswith("backup/") else "保留；逐提交复核后再决定归档或迁移"
        safe_name = name.replace("|", "\\|")
        lines.append(f"| {safe_name} | {tip[:7]} | {commits} | {equivalent} | {novel} | {advice} |")
    lines += ["", "提交独有数基于祖先关系；补丁等价基于git cherry，不证明运行行为或最终文件一致。",
              "本次没有删除分支、创建tag或合并任何历史未包含分支。远端分支不因这份本地审计而自动视为相同。", ""]
    return "\n".join(lines)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    content = report()
    if args.output:
        args.output.write_text(content, encoding="utf-8", newline="\n")
    else:
        print(content)
