"""审核上市条款的业务正反例，保留错误方向，不拿SHA盖住字段错误。"""
from pathlib import Path
import unittest
from review_listing import review


class PRListingTest(unittest.TestCase):
    def inputs(self):
        base = Path(__file__).resolve().parents[3] / "docs/evidence/pr-2024-followup-20261115"
        return tuple((base / f"listing-page-{n}.txt").read_text(encoding="utf-8") for n in (49, 50))

    def test_formal_listing_does_not_certify_calendar(self):
        value = review(*self.inputs())
        self.assertEqual(value["first_announced_trading_day"], 20240830)
        self.assertEqual(value["first_contracts"], [f"PR250{n}" for n in range(3, 9)])
        self.assertFalse(value["midday_break_certified"])
        self.assertFalse(value["annual_calendar_certified"])

    def test_six_actual_business_mutations_hit_target_reason(self):
        first, second = self.inputs()
        cases = [
            (0, "8 月 30 日起施行", "8 月 31 日起施行", "effective_date"),
            (0, "8 月 30 日（星期五）起上市交易", "8 月 31 日（星期五）起上市交易", "listing_date"),
            (0, "9:00－11:30", "9:30－11:30", "day_sections"),
            (0, "8 月 30 日当晚起开展", "8 月 31 日当晚起开展", "first_night"),
            (1, "21:00－23:00", "21:00－23:30", "night_hours"),
            (1, "PR2508", "PL2508", "first_contracts"),
        ]
        for index, needle, changed, reason in cases:
            with self.subTest(reason=reason):
                pages = [first, second]
                self.assertEqual(pages[index].count(needle), 1, "反例必须实际且唯一修改")
                pages[index] = pages[index].replace(needle, changed, 1)
                with self.assertRaisesRegex(ValueError, "^"+reason+"$"):
                    review(*pages)

    def test_draft_and_page_identity_rejected(self):
        first, second = self.inputs()
        with self.assertRaisesRegex(ValueError, "draft_not_final"):
            review(first+"\n征求意见稿", second)
        with self.assertRaisesRegex(ValueError, "page_identity"):
            review(second, first)


if __name__ == "__main__":
    import io
    import sys
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
