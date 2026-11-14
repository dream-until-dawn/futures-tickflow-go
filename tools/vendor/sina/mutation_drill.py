"""用Go overlay破坏目标守卫；不改工作树、不联网，要求测试因目标断言而红。"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
CASES = [
    ("unknown-field", "source/sinavendor/parse.go", "!allowed[key]", "(!allowed[key] && key == \"\")", "./source/sinavendor", "TestSinaVendorFrozenFourAndStrictWrapper"),
    ("missing-day", "source/sinavendor/source.go", "if !ok {", "if !ok && d.Num == 0 {", "./source/sinavendor", "TestSinaVendorMissingWholeRequestAndQuality"),
    ("host-stop", "archive/batch/sina_vendor.go", "d.sina.stopped[u.Host] ||", '(d.sina.stopped[u.Host] && u.Host == "never.invalid") ||', "./archive/batch", "TestSinaVendorNetworkBudgetAndStopHost"),
]
NULL_CASES = [
    ("null-http200", "tools/vendor/sina/null_archive.go", "d.Status == 200", "d.Status > 0", "./tools/vendor/sina", "TestToolExactNullGuardRejectsEveryOtherForm"),
    ("null-exact-body", "tools/vendor/sina/null_archive.go", "!bytes.Equal(b, []byte(`var _V21052021_4_12=(null);`))", "(len(b) == 0 && !bytes.Equal(b, []byte(`var _V21052021_4_12=(null);`)))", "./tools/vendor/sina", "TestToolExactNullGuardRejectsEveryOtherForm"),
    ("null-old-request", "tools/vendor/sina/null_profile.go", "if !p.Allowed[j.URL] {", "if false && !p.Allowed[j.URL] {", "./tools/vendor/sina", "TestToolNullPlanRejectsOldRequestBeforeFetch"),
]


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("output", type=Path)
    p.add_argument("--null-tool", action="store_true", help="只核工具层精确null候选，旧三项默认不变")
    args = p.parse_args()
    args.output.mkdir(exist_ok=False)
    env = {**os.environ, "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off"}
    results = []
    for name, source, old, new, package, test in (NULL_CASES if args.null_tool else CASES):
        path = ROOT / source
        original = path.read_bytes()
        text = original.decode("utf-8")
        if text.count(old) != 1:
            raise ValueError("目标源码不唯一: " + name)
        common = ["go", "test", package, "-run", "^" + test + "$", "-count=1"]
        baseline = subprocess.run(common, cwd=ROOT, env=env, capture_output=True, timeout=90)
        (args.output / (name + "-baseline.log")).write_bytes(baseline.stdout + baseline.stderr)
        if baseline.returncode:
            raise ValueError("正向控制失败: " + name)
        mutant = args.output / (name + ".go")
        mutant.write_text(text.replace(old, new), encoding="utf-8", newline="\n")
        overlay = args.output / (name + "-overlay.json")
        overlay.write_text(json.dumps({"Replace": {str(path): str(mutant.resolve())}}), encoding="utf-8", newline="\n")
        run = subprocess.run(common[:2] + ["-overlay", str(overlay.resolve())] + common[2:], cwd=ROOT, env=env, capture_output=True, timeout=90)
        output = (run.stdout + run.stderr).decode("utf-8", "replace")
        (args.output / (name + "-mutated.log")).write_text(output, encoding="utf-8", newline="\n")
        if run.returncode != 1 or "--- FAIL: " + test not in output or "[build failed]" in output:
            raise ValueError("未由目标测试杀死: " + name)
        if path.read_bytes() != original:
            raise ValueError("工作树被改写")
        results.append(dict(case=name, source=source, source_sha256=hashlib.sha256(original).hexdigest(), mutant_sha256=hashlib.sha256(mutant.read_bytes()).hexdigest(), positive_control=0, mutant_returncode=run.returncode, target_test=test, unchanged_worktree=True))
    with (args.output / "results.json").open("x", encoding="utf-8", newline="\n") as f:
        json.dump(results, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print(f"{len(results)}项正向控制和目标源码破坏均通过，工作树未改")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
