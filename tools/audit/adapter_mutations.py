"""在临时 Git 克隆中验证 adapter 关键断言，拒绝把编译失败当成检出。"""
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def run(cwd, *args):
    return subprocess.run(args, cwd=cwd, capture_output=True, text=True,
                          encoding="utf-8", errors="replace", timeout=120)


def main():
    status = run(ROOT, "git", "status", "--porcelain")
    if status.returncode or status.stdout:
        raise SystemExit("需要干净的已提交工作区，再在临时克隆中施加突变")
    sha = run(ROOT, "git", "rev-parse", "HEAD").stdout.strip()
    cases = [
        ("交易日偏移", "types.TradingDay(b.TradingDay)", "types.TradingDay(b.TradingDay + 1)", "TestBarTimeAndPrices"),
        ("结束时刻误取开盘", "time.UnixMilli(b.TsEnd)", "time.UnixMilli(b.Ts)", "TestBarTimeAndPrices"),
        ("合约月份偏移", "Month: month", "Month: month + 1", "TestBarSymbols"),
        ("收盘误取最高价", "decimal.NewFromFloat(b.Close)", "decimal.NewFromFloat(b.High)", "TestBarTimeAndPrices"),
        ("持仓计价误取最高价", "decimal.NewFromFloat(b.Close)", "decimal.NewFromFloat(b.High)", "TestAdvanceMarksSyntheticPositions"),
        ("价格擅自舍入", "decimal.NewFromFloat(b.Close)", "decimal.NewFromFloat(b.Close).Round(2)", "TestBarRecordedPrices"),
        ("不拒绝非有限价格", "math.IsNaN(field.value) || math.IsInf(field.value, 0)", "math.IsNaN(field.value) && math.IsInf(field.value, 0)", "TestBarRejectsNonFinite"),
        ("不拒绝非规范合约", "err != nil || canonical != sym", "err != nil && canonical != sym && false", "TestBarRejectsInvalidSymbol"),
    ]
    results = []
    with tempfile.TemporaryDirectory(prefix="tickflow-adapter-mutations-") as tmp:
        clone = Path(tmp) / "repo"
        p = run(ROOT, "git", "clone", "--quiet", "--no-hardlinks", str(ROOT), str(clone))
        if p.returncode:
            raise SystemExit(p.stderr)
        before = run(clone, "git", "status", "--porcelain").stdout
        base = run(clone / "adapter", "go", "test", "./...", "-count=1")
        if before or base.returncode:
            raise SystemExit("克隆基线无效：" + base.stdout + base.stderr)
        file = clone / "adapter" / "bar.go"
        original = file.read_bytes()
        for name, old, new, test in cases:
            source = original.decode("utf-8")
            if source.count(old) != 1:
                raise SystemExit("突变锚点不是唯一：" + name)
            try:
                file.write_text(source.replace(old, new), encoding="utf-8")
                p = run(clone / "adapter", "go", "test", "./...", "-run", "^" + test + "$", "-count=1")
                output = p.stdout + p.stderr
                killed = p.returncode == 1 and "--- FAIL: " + test in output and "[build failed]" not in output
                results.append({"mutation": name, "test": test, "rc": p.returncode,
                                "result": "KILLED" if killed else "UNRESOLVED", "output": output})
            finally:
                file.write_bytes(original)
        after = run(clone, "git", "status", "--porcelain").stdout
        restored = run(clone / "adapter", "go", "test", "./...", "-count=1")
        if after or restored.returncode:
            raise SystemExit("还原后基线失败：" + restored.stdout + restored.stderr)
    print(json.dumps({"sha": sha, "before": before, "after": after,
                      "baseline_rc": base.returncode, "restored_rc": restored.returncode,
                      "mutations": results}, ensure_ascii=True, indent=2))
    return int(any(r["result"] != "KILLED" for r in results))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit(main())
