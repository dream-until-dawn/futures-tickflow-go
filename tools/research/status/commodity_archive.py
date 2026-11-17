"""总索引和五个闭合子包的封存：只收声明成员，迁移后逐件重读。"""
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import sys
import zipfile


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def safe(name):
    path=PurePosixPath(name)
    if path.is_absolute() or str(path)!=name or any(p in {'.','..'} for p in path.parts) or '\\' in name or ':' in name:
        raise ValueError('commodity_archive_path')
    return name


def members(read):
    index_raw=read('index.json');index=json.loads(index_raw);result={'index.json':index_raw}
    if index['schema']!='commodity-price-delivery-index-v1' or index['products']!=75 or len(index['shards'])!=5:
        raise ValueError('commodity_archive_scope')
    products=[]
    for shard in index['shards']:
        name=safe(shard['manifest']);raw=read(name)
        if len(raw)!=shard['manifest_bytes'] or digest(raw)!=shard['manifest_sha256']:
            raise ValueError('commodity_archive_manifest')
        result[name]=raw;manifest=json.loads(raw);parent=str(PurePosixPath(name).parent)
        products += [p['key'] for p in manifest['products']]
        for item in manifest['artifacts']:
            name=safe(parent+'/'+safe(item['path']))
            if name in result:
                raise ValueError('commodity_archive_duplicate')
            raw=read(name)
            if len(raw)!=item['bytes'] or digest(raw)!=item['sha256']:
                raise ValueError('commodity_archive_member:'+name)
            result[name]=raw
    if len(products)!=75 or len(set(products))!=75 or any(k.startswith('CFFEX.') for k in products):
        raise ValueError('commodity_archive_product_scope')
    return index,result


def verify_root(root):
    root=Path(root)
    def read(name):
        path=root/safe(name)
        if not path.resolve().is_relative_to(root.resolve()) or path.is_symlink():
            raise ValueError('commodity_archive_link')
        return path.read_bytes()
    index,payloads=members(read)
    actual={p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file()}
    if actual!=set(payloads):
        raise ValueError('commodity_archive_member_set')
    return index,payloads


def build(root,target):
    index,payloads=verify_root(root)
    with zipfile.ZipFile(target,'x',compression=zipfile.ZIP_DEFLATED) as archive:
        for name,raw in payloads.items():archive.writestr(name,raw)
    verify_zip(target)
    return dict(index_sha256=digest(payloads['index.json']),zip_sha256=digest(Path(target).read_bytes()),
                members=len(payloads),zip_bytes=Path(target).stat().st_size,price_rows=index['price_rows'])


def verify_zip(path):
    with zipfile.ZipFile(path) as archive:
        if any(i.is_dir() for i in archive.infolist()) or len(set(archive.namelist()))!=len(archive.infolist()):
            raise ValueError('commodity_archive_extra_or_duplicate')
        index,payloads=members(archive.read)
        if set(archive.namelist())!=set(payloads):
            raise ValueError('commodity_archive_member_set')
        return index


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由交付流程调用；目标ZIP须全新，旧包不覆写')
