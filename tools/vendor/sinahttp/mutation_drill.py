"""只用Go源码overlay验证四个目标守卫；不请求市场或执行HTTP工具。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path, help="必须全新证据目录")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    root = Path(__file__).resolve().parents[3]
    sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    cases = [
        ("source-pin", "archive/batch/sina_static_http.go", "g.Source.SHA256 != sinaStaticSourceSHA || ", "", "./archive/batch", "TestSinaStaticHTTPPinsOriginalSourceHashEvenWhenRehashed", "重算SHA竟绕过冻结出处"),
        ("plaintext-class", "archive/batch/native_http.go", "if d.sinaStaticHTTP && metadata.DataClass != SinaStaticHTTPVersion {", "if false {", "./archive/batch", "TestSinaStaticHTTPReceiptCannotLoseVendorClass", "供应商身份洗白未拒"),
        ("exclusive-marker", "tools/vendor/sinahttp/main.go", "os.O_CREATE|os.O_EXCL|os.O_WRONLY", "os.O_CREATE|os.O_WRONLY", "./tools/vendor/sinahttp", "TestSinaHTTPExclusiveMarkerNeverOverwrites", "独占标记未拒绝重复"),
        ("unknown-start", "tools/vendor/sinahttp/main.go", 'if entry.Name() != "plan.json" || !entry.Type().IsRegular() {', 'if entry.Name() == "" && !entry.Type().IsRegular() {', "./tools/vendor/sinahttp", "TestSinaHTTPPersistentStartRejectsUnknownAndRepeat", "未知启动重发"),
    ]
    results = []
    for name, file, needle, replacement, package, target, reason in cases:
        source = root / file
        text = source.read_text(encoding="utf-8")
        if text.count(needle) != 1:
            raise ValueError(f"目标源码不唯一: {name}")
        command = ["go", "test", package, "-run", "^"+target+"$", "-count=1"]
        baseline = subprocess.run(command, cwd=root, capture_output=True, encoding="utf-8", timeout=90)
        positive = baseline.stdout + baseline.stderr
        if baseline.returncode != 0:
            raise ValueError(f"正控未过: {name}: {positive}")
        with tempfile.TemporaryDirectory(prefix="tickflow-http-overlay-") as tmp:
            mutant = Path(tmp) / "mutant.go"
            mutant.write_text(text.replace(needle, replacement), encoding="utf-8", newline="\n")
            overlay = Path(tmp) / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(source): str(mutant)}}), encoding="utf-8")
            mutated = subprocess.run(["go", "test", "-overlay", str(overlay), package, "-run", "^"+target+"$", "-count=1"], cwd=root, capture_output=True, encoding="utf-8", timeout=90)
        negative = mutated.stdout + mutated.stderr
        if mutated.returncode == 0 or reason not in negative or "[build failed]" in negative:
            raise ValueError(f"未准确命中目标反控: {name}: {negative}")
        for state, body in (("baseline", positive), ("mutated", negative)):
            (args.output / f"{name}-{state}.log.txt").write_text(body, encoding="utf-8", newline="\n")
        results.append(dict(case=name, target_test=target, source=file, source_sha256=hashlib.sha256(source.read_bytes()).hexdigest(), baseline_returncode=baseline.returncode, mutated_returncode=mutated.returncode, target_reason=reason))
        print(f"{name} 正控绿，目标守卫破坏精准报红")
    (args.output / "mutations.json").write_text(json.dumps(dict(candidate=sha, cases=results, market_requests=0), ensure_ascii=False, indent=2)+"\n", encoding="utf-8", newline="\n")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
