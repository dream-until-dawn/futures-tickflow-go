"""从已有受信供应商快照流式提取显示名称，不下载、不认证官方挂牌和制度。"""
import argparse
from collections import Counter, defaultdict
import gzip
import io
import json
from pathlib import Path
import re
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/'catalog'))
from frozen_inputs import FrozenInputs
from build import PACKS, INVENTORY

GZIP_PATH = 'acquisitions/directory/symbols-response.gz'


def objects(stream):
    """逐对象解码，避免把包含全部期权的解压快照同时装进内存。"""
    decoder = json.JSONDecoder()
    buffer = ''; ended = False; total = 0

    def fill():
        nonlocal buffer, ended, total
        more = stream.read(65536)
        total += len(more)
        if total > 768<<20 or len(buffer)+len(more) > 2<<20:
            raise ValueError('name_stream_budget')
        buffer += more
        ended = not more

    def peek():
        nonlocal buffer
        while True:
            buffer = buffer.lstrip()
            if buffer or ended:
                return buffer[:1]
            fill()

    def token(expected):
        nonlocal buffer
        if peek() != expected:
            raise ValueError('name_json_token')
        buffer = buffer[1:]

    def value():
        nonlocal buffer
        peek()
        while True:
            try:
                item, end = decoder.raw_decode(buffer)
                buffer = buffer[end:]
                return item
            except json.JSONDecodeError as error:
                if ended:
                    raise ValueError('name_json_truncated') from error
                fill()

    token('{'); seen = set()
    if peek() == '}':
        token('}')
    else:
        while True:
            key = value()
            if not isinstance(key, str) or key in seen:
                raise ValueError('name_duplicate_key')
            seen.add(key); token(':'); item = value()
            if not isinstance(item, dict):
                raise ValueError('name_entry_shape')
            yield key, item
            if peek() == '}':
                token('}'); break
            token(',')
    if peek():
        raise ValueError('name_json_trailing')


def names(entries, allowed_keys=None):
    groups = defaultdict(Counter)
    for key, item in entries:
        if item.get('class') != 'FUTURE':
            continue
        # 沿用旧目录已经隔离的身份冲突，不借显示名称恢复被隔离的旧合约。
        if allowed_keys is not None and key not in allowed_keys:
            continue
        exchange, product = item.get('exchange_id'), item.get('product_id')
        name = item.get('ins_name', '')
        match = re.fullmatch(r'[A-Za-z]+\.([A-Za-z]+)([0-9]{3,4})', key)
        if not match or match[1] != product or not exchange or not isinstance(name, str):
            raise ValueError('name_future_identity')
        suffixes = [match[2]]
        if isinstance(allowed_keys, dict):
            canonical = re.search(r'([0-9]{4})$', allowed_keys[key])
            if canonical:
                suffixes.insert(0, canonical[1])
        base = name.strip()
        for suffix in suffixes:
            if base.endswith(suffix):
                base = base[:-len(suffix)].strip()
                break
        if not base or any(ord(c)<32 for c in base):
            continue
        groups[exchange+'.'+product][base] += 1
    result = {}
    for key, counts in sorted(groups.items()):
        primary = sorted(counts, key=lambda label: (-counts[label], label))[0]
        result[key] = dict(name=primary, aliases=sorted(counts), source='vendor snapshot ins_name; display only')
    return result


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--directory-package', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args(); package = FrozenInputs(a.directory_package, PACKS['shfe'][0])
    if a.out.exists() or a.out.resolve().is_relative_to(package.root.resolve()):
        raise ValueError('name_output_exists_or_inside_input')
    raw = package.read(GZIP_PATH)
    inventory = package.data(INVENTORY)
    allowed = {c['source_key']:c['canonical'] for c in inventory['contracts']}
    with gzip.GzipFile(fileobj=io.BytesIO(raw)) as z:
        result = names(objects(io.TextIOWrapper(z, encoding='utf-8')), allowed)
    output = dict(schema='frozen-product-display-names-v1', source_manifest_sha256=package.trusted,
                  source_path=GZIP_PATH, source_sha256=package.entries[GZIP_PATH]['sha256'],
                  inventory_sha256=package.entries[INVENTORY]['sha256'],
                  quarantined_identity_conflicts_retained=len(inventory['identity_conflicts']), products=result)
    a.out.write_text(json.dumps(output, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
    print('display labels',len(result),'market requests 0')
