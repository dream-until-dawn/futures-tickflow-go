"""核验五年官方原表出现的具体合约，保留观察日期，不声称挂牌生命周期。"""
import argparse
import io
import json
import sys
from collections import Counter
from pathlib import Path

from review import sha

EXPECTED = {
    2021: 'e9962695770b84da1001a108fd42b2422ce5be2b9c954fcd7c40e879c32eba48',
    2022: '38be8b5ff728a6aa34aa1e25a961857cac319a194db8f9c161d3e1af10b5399c',
    2023: '605e119b8f6e7cd73b02fa18aa75ce1b388d5628aa024d7f5b7961d028a9ec2c',
    2024: '68505aefe460ffa3167bd200477a5ebb05a8aaac5e477bb013e97ffbcf7bc99e',
    2025: 'fe0122e1ff0e072fc200ce924c488407a0305d3e5790692a19efc999709d4902',
}


def accumulate(contracts, dataset):
    seen, year = set(), dataset['year']
    by_exchange = Counter()
    for row in dataset['records']:
        key, day = row['exchange']+'.'+row['native'], row['day']
        if day//10000 != year or (key, day) in seen:
            raise ValueError('official_year_or_duplicate')
        seen.add((key, day))
        by_exchange[row['exchange']] += 1
        item = contracts.setdefault(key, dict(symbol=key, first_observed_day=day, last_observed_day=day,
                                               days=[], years=[], lifecycle_certified=False))
        item['first_observed_day'] = min(item['first_observed_day'], day)
        item['last_observed_day'] = max(item['last_observed_day'], day)
        item['days'].append(day)
        if year not in item['years']:
            item['years'].append(year)
    return dict(year=year, rows=len(seen), contracts=len({k for k, d in seen}), by_exchange=dict(by_exchange),
                first_day=min(d for k, d in seen), last_day=max(d for k, d in seen))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--decoded-root', required=True, type=Path)
    p.add_argument('--original-root', required=True, type=Path)
    p.add_argument('--inventory-root', required=True, type=Path)
    p.add_argument('--out-root', required=True, type=Path)
    args = p.parse_args()
    args.out_root.mkdir()  # 既有目录拒绝，原始证据不改写。
    source = args.inventory_root/'inventory.json'
    derivation = json.loads((args.inventory_root/'scan-review.json').read_bytes())
    if sha(source) != derivation['inventory_sha256'] or derivation['state'] != 'derived-complete':
        raise ValueError('inventory_source_sha')
    inventory = json.loads(source.read_bytes())
    keys = {c['canonical'] for c in inventory['contracts']}
    decoder = Path(__file__).resolve().parents[2]/'archive/decode_annual.py'
    contracts, controls = {}, []
    for year, expected in EXPECTED.items():
        original = args.original_root/f'shfe-annual-{year}.body'
        receipt_file = args.original_root/f'shfe-annual-{year}.json'
        receipt = json.loads(receipt_file.read_bytes())
        decoded = args.decoded_root/f'{year}.json'
        dataset = json.loads(decoded.read_bytes())
        if sha(original) != expected or receipt['evidence']['sha256'] != expected or receipt['state'] != 'received' or receipt['body_state'] != 'complete' or receipt['bytes'] != original.stat().st_size or dataset['original_sha256'] != expected or dataset['decoder_sha256'] != sha(decoder) or dataset['product_filter'] != '':
            raise ValueError('official_input_chain')
        result = accumulate(contracts, dataset)
        result.update(original_sha256=expected, original_bytes=original.stat().st_size,
                      original_receipt_sha256=sha(receipt_file), original_retrieved_at=receipt['evidence']['retrieved_at'],
                      original_url=receipt['evidence']['url'], decoded_sha256=sha(decoded), decoded_file=str(decoded),
                      decoder_sha256=sha(decoder), excluded_options=dataset['excluded_options'], excluded_tas=dataset['excluded_tas'])
        controls.append(result)
    for item in contracts.values():
        item['days'].sort()
    catalog = args.out_root/'official-observed-contracts.json'
    catalog.write_text(json.dumps(dict(schema='official-observed-inventory-v1', contracts=list(sorted(contracts.values(),key=lambda c:c['symbol'])),
                                       full_listing_certified=False), ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
    absent = sorted(set(contracts)-keys)
    summary = dict(schema='official-five-year-directory-review-v1', years=controls,
                   official_contracts=len(contracts), official_present_in_snapshot=len(set(contracts)&keys),
                   official_absent_from_snapshot=absent, observed_contracts_sha256=sha(catalog),
                   inventory_sha256=sha(source), total_contract_days=sum(c['rows'] for c in controls),
                   by_market_contracts=dict(Counter(k.split('.')[0] for k in contracts)),
                   official_listing_or_delisting_certified=False, complete_six_market_history_certified=False,
                   market_requests=0, original_retrieval_time_preserved=True)
    (args.out_root/'review.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2)+'\n', encoding='utf-8', newline='\n')
    print(json.dumps({k:summary[k] for k in ['official_contracts','official_present_in_snapshot','official_absent_from_snapshot','total_contract_days','by_market_contracts']},ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
