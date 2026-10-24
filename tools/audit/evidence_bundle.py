"""封存和迁移复核采集证据；清单证明字节一致，不提供可信签名。"""
import argparse
import contextlib
import hashlib
import io
import json
from pathlib import Path
import sys

from review_long_gap import main as review


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def inventory(root):
    """相对路径清单包含编译二进制；拒绝符号链接、目录联接和越界。"""
    files = {}
    for path in sorted(root.rglob("*")):
        if path.is_symlink() or (hasattr(path, "is_junction") and path.is_junction()) or not path.resolve().is_relative_to(root):
            raise ValueError("证据目录含链接或越界路径")
        if path.is_file() and path != root / "seal.json":
            files[path.relative_to(root).as_posix()] = dict(size=path.stat().st_size, sha256=digest(path))
    return files


def analyze(root, allow_synthetic):
    """重用独立数据复核入口，不依赖监护器的成功声明。"""
    output = io.StringIO()
    with contextlib.redirect_stdout(output):
        review([str(root)] + (["--allow-synthetic"] if allow_synthetic else []))
    return json.loads(output.getvalue())


def metadata(root, allow_synthetic):
    manifest = json.loads((root / "manifest.json").read_text(encoding="utf-8"))
    provenance = manifest.get("provenance", "real")
    if provenance not in ("real", "synthetic") or (provenance == "synthetic" and not allow_synthetic):
        raise ValueError("合成材料须显式允许，未知分类拒绝")
    name = manifest.get("binary_file")
    if not isinstance(name, str) or not name or Path(name).name != name:
        raise ValueError("缺少目录内编译产物")
    if digest(root / name) != manifest.get("binary_sha256"):
        raise ValueError("编译产物与清单不一致")
    plan = json.loads((root / "plan.json").read_text(encoding="utf-8"))
    if hashlib.sha256(json.dumps(plan, sort_keys=True).encode()).hexdigest() != manifest.get("plan_sha256"):
        raise ValueError("执行计划与采集清单不一致")
    return provenance


def seal(root, allow_synthetic=False, incomplete=False):
    root = root.resolve()
    before = inventory(root)
    status = json.loads((root / "status.json").read_text(encoding="utf-8"))
    if status.get("state") not in {"completed", "failed", "timed_out"}:
        raise ValueError("任务尚未终止，禁止封存")
    provenance = metadata(root, allow_synthetic)
    report = None
    if not incomplete:
        if status["state"] != "completed":
            raise ValueError("失败任务只能按incomplete封存")
        report = analyze(root, allow_synthetic)
    if inventory(root) != before:
        raise ValueError("封存期间证据改变")
    result = dict(schema=1, provenance=provenance, validation="incomplete" if incomplete else "passed",
                  files=before, review=report)
    with (root / "seal.json").open("x", encoding="utf-8") as output:
        json.dump(result, output, ensure_ascii=False, indent=2)
    return result


def verify(root, allow_synthetic=False):
    root = root.resolve()
    recorded = json.loads((root / "seal.json").read_text(encoding="utf-8"))
    if recorded.get("schema") != 1 or recorded.get("validation") not in {"passed", "incomplete"}:
        raise ValueError("封存清单结构无效")
    current = inventory(root)
    if not current or current != recorded.get("files"):
        raise ValueError("文件缺失、增加或字节改变")
    if metadata(root, allow_synthetic) != recorded.get("provenance"):
        raise ValueError("材料分类不一致")
    if recorded["validation"] == "passed" and analyze(root, allow_synthetic) != recorded.get("review"):
        raise ValueError("迁移后数据复核与原报告不一致")
    if inventory(root) != current:
        raise ValueError("复核期间证据改变")
    return dict(provenance=recorded["provenance"], validation=recorded["validation"], files=len(current))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["seal", "verify"])
    parser.add_argument("directory", type=Path)
    parser.add_argument("--allow-synthetic", action="store_true")
    parser.add_argument("--incomplete", action="store_true")
    args = parser.parse_args()
    if args.action == "verify" and args.incomplete:
        parser.error("incomplete只能在首次封存时明确指定")
    result = (seal(args.directory, args.allow_synthetic, args.incomplete) if args.action == "seal"
              else verify(args.directory, args.allow_synthetic))
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
