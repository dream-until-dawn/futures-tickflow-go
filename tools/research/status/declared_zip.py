"""只封装manifest声明的文件；工作目录中的缓存、临时文件和目录条目不属于交付。"""
import io
import sys
import hashlib
import json
from pathlib import Path
import zipfile


def declared(manifest):
    names=['manifest.json']+[r['file'] for r in manifest['files']]
    if len(set(n.casefold() for n in names))!=len(names):
        raise ValueError('declared_zip_duplicate_name')
    if any(not n or n in {'.','..'} or any(c in n for c in '/\\:') for n in names):
        raise ValueError('declared_zip_unsafe_name')
    return names


def verify(archive):
    with zipfile.ZipFile(archive) as z:
        manifest=json.loads(z.read('manifest.json'))
        names=declared(manifest)
        if len(z.infolist())!=len(names) or set(z.namelist())!=set(names) or any(i.is_dir() for i in z.infolist()):
            raise ValueError('declared_zip_member_set')
        for row in manifest['files']:
            raw=z.read(row['file'])
            if len(raw)!=row['bytes'] or hashlib.sha256(raw).hexdigest()!=row['sha256']:
                raise ValueError('declared_zip_member_identity:'+row['file'])
    return manifest


def build(root,archive):
    root=Path(root);raw=(root/'manifest.json').read_bytes();manifest=json.loads(raw)
    names=declared(manifest);payload={'manifest.json':raw}
    for row in manifest['files']:
        path=(root/row['file']).resolve()
        if not path.is_relative_to(root.resolve()):raise ValueError('declared_zip_source_path')
        value=path.read_bytes()
        if len(value)!=row['bytes'] or hashlib.sha256(value).hexdigest()!=row['sha256']:
            raise ValueError('declared_zip_source_identity:'+row['file'])
        payload[row['file']]=value
    # 字节已核且保存在本次内存中，不再遍历目录，也不隐式写入任何空目录。
    with zipfile.ZipFile(archive,'x',compression=zipfile.ZIP_DEFLATED) as z:
        for name in names:z.writestr(name,payload[name])
    verify(archive)
    return hashlib.sha256(Path(archive).read_bytes()).hexdigest()


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由交付构建调用；必须全新ZIP，不能覆写旧包')
