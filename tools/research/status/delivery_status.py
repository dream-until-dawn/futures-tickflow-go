"""价格包交付状态仅作附加展示；不改资料门槛、行情范围或生产准入。"""
import copy
import io
import sys


def attach(data, index, review, index_sha):
    if review['index_sha256'] != index_sha or review['consumer_status'] not in {'pending','accepted_with_limits','blocked'}:
        raise ValueError('delivery_status_identity')
    if review['consumer_status']=='accepted_with_limits' and review.get('independent_readback_verified') is not True:
        raise ValueError('delivery_status_unverified_acceptance')
    items={}
    for shard in index['shards']:
        for item in shard['products']:
            key=item['key']
            if key in items or key.startswith('CFFEX.'):
                raise ValueError('delivery_status_duplicate_or_financial')
            items[key]=dict(item,exchange=shard['exchange'],manifest_sha256=shard['manifest_sha256'],
                index_sha256=index_sha,consumer_status=review['consumer_status'],parameter_day=review.get('parameter_day'),
                delivery_registered_at=review.get('review_recorded_at_utc'))
    if len(items)!=75 or sum(p['rows'] for p in items.values())!=index['price_rows']:
        raise ValueError('delivery_status_scope_or_rows')
    if not set(items)<={p['key'] for p in data['products']}:
        raise ValueError('delivery_status_unknown_product')
    result=copy.deepcopy(data)
    result['commodity_price_delivery']=dict(review,products=75,price_rows=index['price_rows'],
        current_contracts=index['current_contracts'],shards=5)
    for product in result['products']:
        product.pop('price_delivery',None)
        if product['key'] in items:
            product['price_delivery']=items[product['key']]
    return result


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('只离线附加真实交付范围；不提高资料门槛')
