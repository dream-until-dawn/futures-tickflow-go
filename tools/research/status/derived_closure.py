"""裁去不被行情用途消费的全年中间镜像，保留原件、序列及可重做链路。"""
import io
import sys
import argparse
import copy
import hashlib
import json
from pathlib import Path


JSON_LIMIT = 32 * 1024 * 1024
TOTAL_LIMIT = 512 * 1024 * 1024


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def payload(manifest, root):
    """先核原包全体成员身份；裁剪选择不能掩盖原包已经损坏的文件。"""
    result = {}
    for item in manifest['artifacts']:
        path = (root / item['path']).resolve()
        if not path.is_relative_to(root.resolve()) or item['id'] in result:
            raise ValueError('closure_artifact_identity')
        raw = path.read_bytes()
        if len(raw) != item['bytes'] or sha(raw) != item['sha256']:
            raise ValueError('closure_source_sha:' + item['id'])
        result[item['id']] = (copy.deepcopy(item), raw)
    return result


def transform(manifest, values, excluded, original_bindings):
    """只有未被硬项或行情引用的派生产物可以离包，原件和价格序列一字不改。"""
    required = set()
    for product in manifest['products']:
        for gate in product['gates'].values():
            for item in gate['hard_items'].values():
                required.update(item.get('evidence_ids', []))
                required.update(item.get('coverage', {}).get('artifact_ids', []))
    for item, raw in values.values():
        if item['role'] == 'daily-records':
            required.add(item['id'])
            for line in raw.splitlines():
                required.add(json.loads(line)['source_id'])
    if not required <= set(values):
        raise ValueError('closure_missing_required_reference')
    if required & set(excluded):
        raise ValueError('closure_required_reference_drop')
    parents = []
    for id in excluded:
        if id not in values or values[id][0]['role'] != 'derived-source':
            raise ValueError('closure_only_unused_derived_sources')
        raw_id = original_bindings[id]
        if raw_id not in values or values[raw_id][0]['role'] != 'source-original':
            raise ValueError('closure_missing_original')
        item, _ = values[id]
        parents.append(dict(unbundled_id=id, bytes=item['bytes'], sha256=item['sha256'],
                            original_source_id=raw_id, decoder_id='annual-decoder',
                            parent_plan_id='history-parent-plan'))
    kept = {id: (copy.deepcopy(item), raw) for id, (item, raw) in values.items() if id not in excluded}
    for parent in parents:
        if any(id not in kept for id in (parent['decoder_id'], parent['parent_plan_id'])):
            raise ValueError('closure_missing_regeneration_chain')
    # decoded_source_id原本可空；按完整原件的member/row仍可定位。父镜像SHA另列，避免悬空引用。
    if 'history-row-lineage' in kept:
        item, raw = kept['history-row-lineage']
        rows = []
        for line in raw.splitlines():
            row = json.loads(line)
            if row.get('decoded_source_id') in excluded:
                row['decoded_source_id'] = None
            rows.append(row)
        raw = ''.join(json.dumps(r, ensure_ascii=False, separators=(',', ':')) + '\n' for r in rows).encode()
        item.update(bytes=len(raw), sha256=sha(raw))
        kept[item['id']] = (item, raw)
    descriptor = dict(schema='unbundled-derived-parent-index-v1', parents=parents,
                      limits=['未随包复制的仅是未被用途消费的全年中间镜像，原始归档与全部规范化价格序列仍在',
                              '可按所附原件、解码器和父计划重做中间产物并比对父SHA；不冒充已交完整父镜像'])
    raw = (json.dumps(descriptor, ensure_ascii=False, indent=2) + '\n').encode()
    item = dict(id='unbundled-derived-parents', role='review-evidence', path='derivation/unbundled-parents.json',
                bytes=len(raw), sha256=sha(raw), source_url=None, source_day=None,
                retrieved_at=None, version='derived-reference-closure-v1')
    if item['id'] in kept:
        raise ValueError('closure_descriptor_collision')
    kept[item['id']] = (item, raw)
    for item, raw in kept.values():
        if Path(item['path']).suffix.lower() in {'.json', '.jsonl'} and len(raw) > JSON_LIMIT:
            raise ValueError('closure_json_freeze_budget:' + item['id'])
    if sum(len(raw) for _, raw in kept.values()) > TOTAL_LIMIT:
        raise ValueError('closure_total_freeze_budget')
    output = copy.deepcopy(manifest)
    output['artifacts'] = [item for item, _ in kept.values()]
    output['limitations'] = [
        '本包交付指定目标日的信号用途及真实结算预热历史；未交长期OHLC和资金约束，回测pending'
        if text.startswith('本包只支持指定日期和所列合约信号，回测未通过') else text
        for text in output.get('limitations', [])]
    output.setdefault('limitations', []).append(
        '本版本只裁去未被价格用途引用的全年解码镜像；完整原件、解码器、父SHA和逐行member/row保留；现有32MiB JSON冻结预算不变')
    return output, kept


def build(spec, out):
    """用精确父SHA选择闭包；保留主接口及原数据，输出只写新目录。"""
    if spec['schema'] != 'derived-reference-closure-plan-v1' or spec['network_requests'] != 0:
        raise ValueError('closure_plan_scope')
    if out.exists():
        raise ValueError('closure_output_exists')
    root = Path(spec['source_root'])
    raw = (root / 'manifest.json').read_bytes()
    if sha(raw) != spec['source_manifest_sha256']:
        raise ValueError('closure_manifest_sha')
    original = json.loads(raw)
    values = payload(original, root)
    for expected in spec['unbundle_derived']:
        item = values[expected['id']][0]
        if any(item[k] != expected[k] for k in ('sha256', 'bytes')):
            raise ValueError('closure_parent_identity')
    excluded = {r['id'] for r in spec['unbundle_derived']}
    manifest, kept = transform(original, values, excluded, spec['original_bindings'])
    if (manifest['products'] != original['products'] or manifest['policy'] != original['policy'] or
            manifest['current_contracts'] != original['current_contracts']):
        raise ValueError('closure_gate_or_policy_mutation')
    for id, (item, body) in values.items():
        if item['role'] in {'source-original', 'daily-records'} and (id not in kept or kept[id][1] != body):
            raise ValueError('closure_original_or_prices_changed')
    out.mkdir(parents=True)
    for item, body in kept.values():
        path = (out / item['path']).resolve()
        if not path.is_relative_to(out.resolve()):
            raise ValueError('closure_output_path')
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(body)
    raw = (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode()
    (out / 'manifest.json').write_bytes(raw)
    return dict(manifest_sha256=sha(raw), artifacts=len(kept), json_limit_unchanged=JSON_LIMIT,
                total_bytes=sum(len(body) for _, body in kept.values()),
                original_and_price_bytes_unchanged=True, whole_bundle_queue_ready='pending_actual_consumer')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--spec', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(build(json.loads(args.spec.read_bytes()), args.out), ensure_ascii=False))
