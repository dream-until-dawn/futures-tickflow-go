"""成功封存也必须在截止前；原件身份或时钟不对应即拒绝。"""
import copy
import io
import json
import sys
import unittest
from review_consumer_originals import rb_packet,digest


class ConsumerTest(unittest.TestCase):
    def test_identity_deadline_and_day_positive_negative(self):
        packet=dict(Day=20261116,NextDay=20261117,CreatedAt='2026-11-16T10:00:00Z',CloseAt='2026-11-16T15:00:00+08:00',NextOpenAt='2026-11-16T21:00:00+08:00',Forecast=dict(ActualContract='',NextContract='SHFE.rb2701'))
        raw=json.dumps(packet).encode();receipt=dict(PacketSHA=digest(raw),Status='sealed',PersistedAt='2026-11-16T10:00:01Z')
        self.assertEqual(rb_packet(raw,receipt,digest(raw))['market_day'],20261116)
        with self.assertRaisesRegex(ValueError,'consumer_packet_identity'):rb_packet(raw+b' ',receipt,digest(raw))
        late=copy.deepcopy(receipt);late['PersistedAt']='2026-11-16T13:00:00Z'
        with self.assertRaisesRegex(ValueError,'consumer_after_deadline'):rb_packet(raw,late,digest(raw))
        packet['NextDay']=20261116;raw=json.dumps(packet).encode();receipt['PacketSHA']=digest(raw)
        with self.assertRaisesRegex(ValueError,'consumer_trading_days'):rb_packet(raw,receipt,digest(raw))


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
