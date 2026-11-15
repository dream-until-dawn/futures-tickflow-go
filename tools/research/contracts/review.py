"""目录与既有官方年度解码原件离线交叉核对，不认证官方挂牌全集。"""
import argparse
import hashlib
import io
import json
import sys
from collections import Counter
from pathlib import Path


ARCHIVES = {2024: '68505aefe460ffa3167bd200477a5ebb05a8aaac5e477bb013e97ffbcf7bc99e',
            2025: 'fe0122e1ff0e072fc200ce924c488407a0305d3e5790692a19efc999709d4902'}


def sha(path):
    digest = hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda: f.read(1024*1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def summarize(inventory, official):
    contracts = inventory['contracts']
    keys = {c['canonical'] for c in contracts}
    if len(keys) != len(contracts):
        raise ValueError('duplicate_canonical')
    eligible = [c for c in contracts if float(c['expiry_seconds']) >= 1609430400]
    observed = {r['exchange'] + '.' + r['native'] for dataset in official for r in dataset['records']}
    return dict(schema='historical-directory-review-v1', total_entries=sum(inventory['by_class'].values()),
                by_class=inventory['by_class'], futures_by_market=inventory['by_market'],
                six_market_futures=len(contracts), expiry_not_before_2021=len(eligible),
                selected_by_market=dict(sorted(Counter(c['exchange'] for c in eligible).items())),
                selected_by_delivery_year=dict(sorted(Counter(str(c['delivery_year']) for c in eligible).items())),
                official_observed_contracts=len(observed), official_present_in_snapshot=len(observed & keys),
                official_absent_from_snapshot=sorted(observed - keys),
                product_identity_conflicts=inventory.get('product_identity_conflicts', []),
                evidence_scope='official existing decoded rows cross-check, not new full annual re-decoding',
                listing_dates_available=False, point_in_time_catalog=False,
                complete_historical_listing_certified=False, financial_admission_expanded=False)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--directory', type=Path, required=True)
    p.add_argument('--inventory-root', type=Path, required=True)
    p.add_argument('--official-root', type=Path, action='append', required=True)
    p.add_argument('--out', type=Path, required=True)
    args = p.parse_args()
    if any(args.out.resolve().is_relative_to(root.resolve()) for root in [args.directory, args.inventory_root]):
        raise ValueError('output_inside_source_tree')
    receipt = json.loads((args.directory/'receipt.json').read_bytes())
    derivation = json.loads((args.inventory_root/'scan-review.json').read_bytes())
    if receipt['status'] != 200 or not receipt.get('body_complete') or sha(args.directory/'symbols-response.gz') != receipt['sha256'] or derivation['state'] != 'derived-complete' or not derivation['gzip_and_json_complete'] or derivation['input_sha256'] != receipt['sha256'] or derivation['inventory_sha256'] != sha(args.inventory_root/'inventory.json'):
        raise ValueError('incomplete_or_changed_directory')
    inventory = json.loads((args.inventory_root/'inventory.json').read_bytes())
    official, inputs = [], []
    decoder = Path(__file__).resolve().parents[2]/'archive/decode_annual.py'
    for root in args.official_root:
        manifest = json.loads((root/'manifest.json').read_bytes())
        expected = {r['path']: r for r in manifest['files']}
        for source in sorted(root.glob('*-decoded.json')):
            actual_sha = sha(source)
            if source.name not in expected or expected[source.name]['sha256'] != actual_sha or expected[source.name]['bytes'] != source.stat().st_size:
                raise ValueError('official_decoded_source_sha')
            decoded = json.loads(source.read_bytes())
            year = decoded['year']
            archive = root/f'shfe-annual-{year}.body'
            if decoded['original_sha256'] != ARCHIVES[year] or sha(archive) != ARCHIVES[year] or decoded['decoder_sha256'] != sha(decoder):
                raise ValueError('official_original_or_decoder_sha')
            official.append(decoded)
            inputs.append(dict(file=str(source), sha256=actual_sha, rows=len(decoded['records']),
                               manifest_sha256=sha(root/'manifest.json'), original_sha256=ARCHIVES[year]))
    if not official:
        raise ValueError('no_official_control')
    result = summarize(inventory, official)
    result.update(directory_sha256=receipt['sha256'], inventory_sha256=sha(args.inventory_root/'inventory.json'),
                  original_scan_state=receipt['state'], original_scan_error=receipt.get('error'),
                  official_inputs=inputs, market_requests=0)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
