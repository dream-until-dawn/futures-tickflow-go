"""由已核日表登记出现过的具体合约，不把观察首末日写成挂牌/摘牌日。"""
import datetime as dt
import io
import sys
from inventory_model import product, utc


class ObservedDirectory:
    def __init__(self, route, indexed_at, identities=None):
        self.route, self.indexed_at, self.groups = route, utc(indexed_at), {}
        self.identities=identities or {}

    def add(self, record):
        key=product(record['exchange'],record['native'])
        day=dt.datetime.strptime(str(record['day']),'%Y%m%d').date().isoformat()
        group=self.groups.setdefault(key,dict(contracts={},days=set(),shas=set(),acquired=None,receipt=None,uncertain=False))
        contract=group['contracts'].setdefault(record['native'],dict(symbol=record['exchange']+'.'+record['native'],first=day,last=day,observed_days=0))
        bindings=self.identities.get((record['exchange'],record['native']),set())
        contract['canonical']=(next(iter(bindings)) if len(bindings)==1 else None) if record['exchange']=='CZCE' and len(record['native'].lstrip('ABCDEFGHIJKLMNOPQRSTUVWXYZ'))==3 else contract['symbol']
        contract['first']=min(contract['first'],day);contract['last']=max(contract['last'],day);contract['observed_days']+=1
        group['days'].add(day)
        evidence=record.get('evidence',{});time=utc(evidence.get('retrieved_at'))
        unknown='first original HTTP UTC unresolved' in evidence.get('version','')
        group['uncertain'] |= unknown
        if time:
            group['receipt']=max(group['receipt'] or time,time)
            if not unknown:group['acquired']=max(group['acquired'] or time,time)
        if evidence.get('sha256'):group['shas'].add(evidence['sha256'])

    def finish(self):
        result=[]
        for key,g in sorted(self.groups.items()):
            contracts=sorted(g['contracts'].values(),key=lambda c:c['symbol']);rows=sum(c['observed_days'] for c in contracts)
            result.append(dict(product=key,route=self.route+'-observed-directory',source=key.split('.')[0]+' 官方日表出现合约目录',kind='directory',
                rows=rows,present_rows=rows,valid_rows=None,first=min(g['days']),last=max(g['days']),observed_days=len(g['days']),contracts=len(contracts),
                contract_observations=contracts,completeness='observed_contracts_only',expected_contract_days=None,
                last_acquired_at=g['acquired'],last_receipt_at=g['receipt'],last_indexed_at=self.indexed_at,
                original_acquisition_unknown=g['uncertain'],evidence_shas=sorted(g['shas']),
                read_level='已有日表Records逐合约重数；不是重新下载或完整挂牌清单认证',
                limitations='只列已收日表实际出现的具体合约；首末为日表条目观察，不是挂牌/摘牌日；隔离价格仍保留原符号；不认证生命周期、全集或回测准入。'))
        return result


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由只读台账入口调用，不联网')
