"""在可丢弃克隆中检查长缺口分析尺子的判别力，不修改原工作区。"""
import io
import json
from pathlib import Path
import sys
import tempfile

from adapter_mutations import ROOT, run


def main():
    status = run(ROOT, "git", "status", "--porcelain")
    if status.returncode or status.stdout:
        raise SystemExit("需要干净的已提交工作区")
    sha = run(ROOT, "git", "rev-parse", "HEAD").stdout.strip()
    results = []
    with tempfile.TemporaryDirectory(prefix="tickflow-gap-mutations-") as tmp:
        clone = Path(tmp) / "repo"
        p = run(ROOT, "git", "clone", "--quiet", "--no-hardlinks", str(ROOT), str(clone))
        if p.returncode:
            raise SystemExit(p.stderr)
        before = run(clone, "git", "status", "--porcelain").stdout
        args = ("go", "test", "./source/shinnysource", "-run", "^TestGap", "-count=1")
        base = run(clone, *args)
        if before or base.returncode:
            raise SystemExit("基线失败：" + base.stdout + base.stderr)
        file = clone / "source/shinnysource/long_gap_real_test.go"
        original = file.read_bytes()
        for name, old, new in [
            ("忽略参照缺根", "r.r3Compared != want", "false"),
            ("忽略交出价格错误", "len(r.r4Diff) != 0", "false"),
            ("忽略交易日错误", "o.Day != int(p.day)", "false"),
        ]:
            source = original.decode("utf-8")
            if source.count(old) != 1:
                raise SystemExit("突变锚点不唯一：" + name)
            try:
                file.write_bytes(source.replace(old, new).encode("utf-8"))
                p = run(clone, "go", "test", "./source/shinnysource", "-run", "^TestGapCompareCalibration$", "-count=1")
                output = p.stdout + p.stderr
                killed = p.returncode == 1 and "--- FAIL: TestGapCompareCalibration" in output and "[build failed]" not in output
                results.append({"mutation": name, "result": "KILLED" if killed else "UNRESOLVED", "output": output})
            finally:
                file.write_bytes(original)
        after = run(clone, "git", "status", "--porcelain").stdout
        restored = run(clone, *args)
        if after or restored.returncode:
            raise SystemExit("还原失败：" + restored.stdout + restored.stderr)
    print(json.dumps({"sha": sha, "before": before, "after": after, "baseline_rc": base.returncode,
                      "restored_rc": restored.returncode, "mutations": results}, ensure_ascii=True, indent=2))
    return int(any(r["result"] != "KILLED" for r in results))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit(main())
