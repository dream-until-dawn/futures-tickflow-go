"""真实交付展示不能污染门槛或把未验包标为验收。"""
import copy
import io
import sys
import unittest

from delivery_status import attach


class DeliveryStatusTest(unittest.TestCase):
    def fixture(self):
        products=[dict(key='DCE.p'+str(n),user_data_gates=dict(backtest='pending')) for n in range(75)]
        items=[dict(key=p['key'],rows=1,current_contracts=1) for p in products]
        index=dict(shards=[dict(exchange='DCE',manifest_sha256='a'*64,products=items)],price_rows=75,current_contracts=75)
        review=dict(index_sha256='b'*64,consumer_status='pending')
        return dict(products=products+[dict(key='CFFEX.IF',price_delivery='phantom')]),index,review

    def test_projection_preserves_gates_and_removes_outside_claims(self):
        data,index,review=self.fixture();before=copy.deepcopy(data)
        result=attach(data,index,review,'b'*64)
        self.assertEqual(data,before)
        self.assertEqual(result['products'][0]['user_data_gates'],data['products'][0]['user_data_gates'])
        self.assertEqual(result['products'][0]['price_delivery']['consumer_status'],'pending')
        self.assertNotIn('price_delivery',result['products'][-1])

    def test_unverified_acceptance_tampered_index_and_duplicate_product_are_rejected(self):
        data,index,review=self.fixture()
        with self.assertRaisesRegex(ValueError,'identity'):attach(data,index,review,'c'*64)
        with self.assertRaisesRegex(ValueError,'unverified_acceptance'):
            attach(data,index,dict(review,consumer_status='accepted_with_limits'),'b'*64)
        altered=copy.deepcopy(index);altered['shards'][0]['products'].append(altered['shards'][0]['products'][0])
        with self.assertRaisesRegex(ValueError,'duplicate_or_financial'):attach(data,altered,review,'b'*64)
        altered=copy.deepcopy(index);altered['price_rows']=74
        with self.assertRaisesRegex(ValueError,'scope_or_rows'):attach(data,altered,review,'b'*64)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
