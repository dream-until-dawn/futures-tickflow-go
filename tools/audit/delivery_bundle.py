"""校验阶段交接包的精确字节与范围；manifest须由包外可信渠道给定SHA。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys


def digest(path):
    """流式计算文件SHA，不把摘要当作来源真实性认证。"""
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def unique_object(pairs):
    """拒绝重复JSON键，避免检查器和消费者选择不同身份。"""
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("重复JSON键")
        result[key] = value
    return result


def inventory(root):
    """拒绝链接、越界和过大包；包括多出的文件，不静默忽略。"""
    files, total = {}, 0
    for path in sorted(root.rglob("*")):
        if path.is_symlink() or path.is_junction() or not path.resolve().is_relative_to(root):
            raise ValueError("交接包含链接或越界路径")
        if path.is_file() and path != root / "manifest.json":
            size = path.stat().st_size
            total += size
            if len(files) >= 10000 or size > 512 * 1024**2 or total > 1024**3:
                raise ValueError("交接包超过文件/字节上限")
            files[path.relative_to(root).as_posix()] = dict(bytes=size, sha256=digest(path))
    return files


def verify(root, expected_sha):
    """只能验证本阶段包，不将缺现场/缺原件的资料提升成完整发布。"""
    root = Path(root)
    if root.is_symlink() or root.is_junction():
        raise ValueError("交接包根目录是链接")
    root = root.resolve(strict=True)
    manifest = root / "manifest.json"
    if manifest.is_symlink():
        raise ValueError("manifest是链接")
    if manifest.stat().st_size > 16 * 1024**2:
        raise ValueError("manifest过大")
    if not re.fullmatch(r"[0-9a-f]{64}", expected_sha) or digest(manifest) != expected_sha:
        raise ValueError("包外manifest SHA不符")
    declared = json.loads(manifest.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    if not isinstance(declared, dict) or declared.get("schema") != "staged-delivery-files-v1" or not isinstance(declared.get("files"), dict):
        raise ValueError("未知交接清单schema")
    actual = inventory(root)
    if set(declared.get("files", {})) != set(actual):
        raise ValueError("文件集合不符")
    for name, item in actual.items():
        if declared["files"][name] != item:
            raise ValueError("文件字节/SHA不符：" + name)
    if (root / "scope.json").stat().st_size > 1024**2:
        raise ValueError("范围说明过大")
    scope = json.loads((root / "scope.json").read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    if not isinstance(scope, dict) or scope.get("schema") != "staged-delivery-scope-v1" or not isinstance(scope.get("source_sha"), str) or not re.fullmatch(r"[0-9a-f]{40}", scope["source_sha"]):
        raise ValueError("固定源码身份或范围schema无效")
    for key in ("full_release", "all_market_originals_included", "financial_admission_extended", "live_l9_verified", "tag_created"):
        if scope.get(key) is not False:
            raise ValueError("阶段范围不能提升资格：" + key)
    return dict(source_sha=scope["source_sha"], files=len(actual), bytes=sum(x["bytes"] for x in actual.values()), full_release=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("--manifest-sha", required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(verify(args.root, args.manifest_sha), ensure_ascii=False))
    except (ValueError, OSError, TypeError, KeyError) as error:
        print("FAIL " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit(main())
