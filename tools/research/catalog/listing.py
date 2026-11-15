"""复用CY/PF首次上市原件作有限目录关联；三位原码不自动变成官方完整年份。"""
import argparse
import io
import json
from pathlib import Path
import re
import sys

from build import INVENTORY, PACKS
from frozen_inputs import FrozenInputs

LISTING_MANIFEST = 'b64536dfe6698910be8e6afc077e75ffd249495554d7d0585f3f542d0c4cd9e0'


def links(inventory, cy_text, pf_text):
    cy = re.sub(r'\s+', '', cy_text)
    pf = re.sub(r'\s+', '', pf_text)
    for marker in ['2017年8月18日', '2017年8月14日', '21:00—23:30']:
        if marker not in cy:
            raise ValueError('cy_listing_marker:' + marker)
    for marker in ['2020年10月12日', '2020年9月28日', '21:00至23:00']:
        if marker not in pf:
            raise ValueError('pf_listing_marker:' + marker)
    cy_codes = re.findall(r'CY[0-9]{4}', cy)
    pf_codes = re.findall(r'PF[0-9]{3}', pf)
    if cy_codes != [f'CY180{i}' for i in range(1,9)] or pf_codes != [f'PF10{i}' for i in range(5,10)]:
        raise ValueError('first_listing_contract_set')
    rows = []
    for product, codes, day in [('CY', cy_codes, 20170818), ('PF', pf_codes, 20201012)]:
        for native in codes:
            # 完整四位通知代码可按原样对齐canonical；三位通知只给候选关系，不做最近十年推断。
            matches = [c for c in inventory['contracts'] if c['exchange']=='CZCE' and
                       (c['canonical']=='CZCE.'+native if product=='CY' else c['source_key']=='CZCE.'+native)]
            if not matches:
                raise ValueError('listing_snapshot_no_candidate')
            rows.append(dict(product=product, official_written_native=native, official_native_first_listing_day=day,
                             canonical_written_directly=(product=='CY'),
                             official_canonical_symbol='CZCE.'+native if product=='CY' else None,
                             snapshot_candidates=sorted(c['canonical'] for c in matches),
                             delivery_year_certified_from_this_notice=False,
                             last_trading_day_certified=False, annual_calendar_qualified=False,
                             financial_eligible=False))
    return rows


def review(listing_package, inventory_package):
    texts = {}
    for product in ['cy', 'pf']:
        prefix = 'czce-'+product+'-listing-pdf'
        listing_package.read(prefix+'.body')
        texts[product] = listing_package.read(prefix+'.txt').decode('utf-8')
    return dict(schema='cy-pf-first-listing-research-links-v1',
                entries=links(inventory_package.data(INVENTORY),texts['cy'],texts['pf']),
                listing_manifest_sha256=LISTING_MANIFEST,
                legacy_before_2021_entries_not_added_to_5351=True,
                new_symbols_discovered=0, new_daily_samples=0, calendar_generated=False)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--listing-package', type=Path, required=True)
    p.add_argument('--inventory-package', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args()
    original = FrozenInputs(a.listing_package, LISTING_MANIFEST)
    inventory = FrozenInputs(a.inventory_package, PACKS['shfe'][0])
    result = review(original, inventory)
    if a.out.exists() or any(a.out.resolve().is_relative_to(x.root.resolve()) for x in [original,inventory]):
        raise ValueError('listing_output_exists_or_inside_input')
    a.out.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    print('13 native listing links; no new market data, lifecycle or calendar qualification')
