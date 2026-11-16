"""将生产者本机计划投影为artifact-ID消费视图；不冒充原计划，不改行情。"""
import io
import sys
import argparse
import copy
import json
from pathlib import Path

from derived_closure import payload, sha


def project(parent, parent_sha, values):
    """只保留包内原始输入所需语义，机器路径留在旧原计划中，不随消费视图复制。"""
    documents = []
    for document in parent['documents']:
        raw_id = 'warmup-' + document['id']
        if raw_id not in values:
            continue
        original = values[raw_id][0]
        expected = (document.get('origin') or document.get('evidence') or {}).get('sha256', document['sha256'])
        if original['role'] != 'source-original' or original['sha256'] != expected:
            raise ValueError('portable_parent_original_identity')
        receipt_id = 'receipt-' + document['id']
        receipt = values.get(receipt_id)
        if receipt and (receipt[0]['role'] != 'acquisition-receipt' or
                        document.get('receipt_sha256') != receipt[0]['sha256']):
            raise ValueError('portable_parent_receipt_identity')
        entry = dict(id=document['id'], input_format=document['format'],
                     parser_day_anchor=document['day'], accept_exchanges=document.get('accept_exchanges'),
                     original_artifact_id=raw_id, original_sha256=original['sha256'],
                     original_bytes=original['bytes'], source_url=original.get('source_url'),
                     source_snapshot_day=original.get('source_day'),
                     original_http_received_at=original.get('retrieved_at'),
                     source_version=original.get('version'),
                     parsed_input_sha256=document['sha256'], parsed_input_bytes=document.get('bytes'),
                     receipt_artifact_id=receipt_id if receipt else None,
                     receipt_sha256=receipt[0]['sha256'] if receipt else None,
                     row_lineage_artifact_id='history-row-lineage')
        if document['format'] == 'annual-decoded-v1':
            if ('annual-decoder' not in values or
                    values['annual-decoder'][0]['sha256'] != parent.get('decoder_sha256')):
                raise ValueError('portable_parent_decoder_identity')
            entry.update(full_decoded_parent_included=False, decoder_artifact_id='annual-decoder',
                         full_decoded_parent_sha256=document['sha256'])
        documents.append(entry)
    if not documents:
        raise ValueError('portable_parent_empty_projection')
    return dict(schema='portable-parent-plan-projection-v1', original_parent_plan_sha256=parent_sha,
                original_parent_plan_included=False, original_plan_schema=parent['schema'],
                upstream_revision=parent.get('upstream_revision'), decoder_sha256=parent.get('decoder_sha256'),
                selected_documents=len(documents), original_documents=len(parent['documents']),
                documents=documents, limits=[
                    '此为派生的可移植消费视图，不是原本机计划；原计划身份仅以SHA记录，旧版本保留原文件',
                    '只含当前包内原始输入对应条目，未选父计划条目不冒充随包交付',
                    'parser_day_anchor为原解析约束，不代表年度档案的真实取得或发布时间',
                    '未知原始HTTP接收时间仍为null；源版本和原件身份另存'])


def build(spec, out):
    if spec['schema'] != 'portable-parent-projection-plan-v1' or spec['network_requests'] != 0:
        raise ValueError('portable_parent_plan_scope')
    if out.exists():
        raise ValueError('portable_parent_output_exists')
    root = Path(spec['source_root'])
    manifest_raw = (root / 'manifest.json').read_bytes()
    if sha(manifest_raw) != spec['source_manifest_sha256']:
        raise ValueError('portable_parent_manifest_sha')
    original = json.loads(manifest_raw)
    values = payload(original, root)
    id = spec['parent_artifact_id']
    item, raw = values[id]
    if (item['role'] != 'review-evidence' or item['sha256'] != spec['parent_plan_sha256'] or
            json.loads(raw)['schema'] != 'all-market-existing-upstream-parse-v1'):
        raise ValueError('portable_parent_only_producer_metadata')
    # 本机计划不得属于硬项或价格源；这里只替换再生产辅助信息。
    for product in original['products']:
        for gate in product['gates'].values():
            for hard in gate['hard_items'].values():
                if id in hard.get('evidence_ids', []) or id in hard.get('coverage', {}).get('artifact_ids', []):
                    raise ValueError('portable_parent_required_by_gate')
    for artifact, body in values.values():
        if artifact['role'] == 'daily-records':
            if any(json.loads(line)['source_id'] == id for line in body.splitlines()):
                raise ValueError('portable_parent_required_by_prices')
    projection = project(json.loads(raw), item['sha256'], values)
    projection_raw = (json.dumps(projection, ensure_ascii=False, indent=2) + '\n').encode()
    new_id = 'portable-history-parent-plan'
    if new_id in values:
        raise ValueError('portable_parent_id_collision')
    kept = {k: (copy.deepcopy(a), body) for k, (a, body) in values.items() if k != id}
    new_item = dict(id=new_id, role='review-evidence', path='derivation/portable-parent-plan.json',
                    bytes=len(projection_raw), sha256=sha(projection_raw), source_url=None,
                    source_day=None, retrieved_at=None, version='派生便携视图；原父身份 ' + item['sha256'])
    kept[new_id] = (new_item, projection_raw)
    descriptor_item, descriptor_raw = kept['unbundled-derived-parents']
    descriptor = json.loads(descriptor_raw)
    for parent in descriptor['parents']:
        if parent['parent_plan_id'] != id:
            raise ValueError('portable_parent_unexpected_reference')
        parent.update(parent_plan_id=new_id, original_parent_plan_sha256=item['sha256'],
                      original_parent_plan_included=False)
    descriptor_raw = (json.dumps(descriptor, ensure_ascii=False, indent=2) + '\n').encode()
    descriptor_item.update(bytes=len(descriptor_raw), sha256=sha(descriptor_raw),
                           version='父索引引用便携派生视图；原计划SHA另列')
    kept[descriptor_item['id']] = (descriptor_item, descriptor_raw)
    manifest = copy.deepcopy(original)
    manifest['artifacts'] = [a for a, _ in kept.values()]
    manifest['limitations'].append(
        '本版本以artifact-ID便携投影替代本机父计划附件；投影不是原计划，原父SHA及旧包均保留，原始行情和价格序列不改')
    for id, (artifact, body) in values.items():
        if artifact['role'] in {'source-original', 'daily-records'} and (id not in kept or kept[id][1] != body):
            raise ValueError('portable_parent_original_or_prices_changed')
    out.mkdir(parents=True)
    for artifact, body in kept.values():
        path = (out / artifact['path']).resolve()
        if not path.is_relative_to(out.resolve()):
            raise ValueError('portable_parent_output_path')
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(body)
    manifest_raw = (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode()
    (out / 'manifest.json').write_bytes(manifest_raw)
    return dict(manifest_sha256=sha(manifest_raw), artifacts=len(kept), selected_documents=len(projection['documents']),
                original_and_price_bytes_unchanged=True, whole_bundle_queue_ready='pending_full_actual_preflight')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--spec', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(build(json.loads(args.spec.read_bytes()), args.out), ensure_ascii=False))
