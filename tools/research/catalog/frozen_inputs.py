"""以包外可信摘要锁定旧清单；逐件读取需要的输入，不要求迁移无关的大体积原件。"""
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import shutil
import sys


def sha256(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024*1024), b''):
            h.update(block)
    return h.hexdigest()


def safe_path(root, name):
    p = PurePosixPath(name)
    if not name or '\\' in name or ':' in name or p.is_absolute() or '..' in p.parts or p.as_posix() != name:
        raise ValueError('unsafe_input_path')
    target = root / name
    if target.is_symlink() or not target.resolve().is_relative_to(root.resolve()):
        raise ValueError('unsafe_input_path')
    return target


class FrozenInputs:
    def __init__(self, root, trusted):
        self.root = Path(root)
        self.trusted = trusted
        manifest = self.root/'manifest.json'
        if sha256(manifest) != trusted:
            raise ValueError('input_manifest_sha')
        rows = json.loads(manifest.read_bytes())['files']
        self.entries = {}
        self.used = set()
        for row in rows:
            name = row['path']
            safe_path(self.root, name)
            if name in self.entries or name == 'manifest.json':
                raise ValueError('input_manifest_duplicate')
            self.entries[name] = row

    def read(self, name, *, track=True):
        if name not in self.entries:
            raise ValueError('input_not_locked:' + name)
        p = safe_path(self.root, name)
        row = self.entries[name]
        try:
            raw = p.read_bytes()
        except FileNotFoundError as error:
            raise ValueError('input_file_missing:' + name) from error
        if len(raw) != row['bytes'] or hashlib.sha256(raw).hexdigest() != row['sha256']:
            raise ValueError('input_file_sha:' + name)
        if track:
            self.used.add(name)
        return raw

    def data(self, name):
        return json.loads(self.read(name))

    def verify_all(self):
        """完整旧包检查与最小迁移输入有别；少件/多件都拒绝，不污染所用输入集合。"""
        actual = {p.relative_to(self.root).as_posix() for p in self.root.rglob('*') if p.is_file()}
        if actual != set(self.entries) | {'manifest.json'}:
            raise ValueError('input_file_set')
        for name in self.entries:
            self.read(name, track=False)
        return len(self.entries)

    def export_used(self, target):
        target.mkdir(parents=True)
        shutil.copyfile(self.root/'manifest.json', target/'manifest.json')
        for name in sorted(self.used):
            # 再读再校验，防止封存复算之间的输入变化被悄悄带走。
            raw = self.read(name)
            p = safe_path(target, name)
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(raw)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('此模块供离线目录工具导入；请运行build.py')
