"""仅审核已核PDF两页的上市公告事实；不推导三节日盘或全年日历。"""
import re


def normalize_page(text, printed_page):
    lines = text.strip().splitlines()
    if not lines or lines[0].strip() != str(printed_page):
        raise ValueError("page_identity")
    return re.sub(r"\s+", "", "".join(lines[1:])).replace("－", "-").replace("—", "-")


def review(page49, page50):
    text = normalize_page(page49, 48) + normalize_page(page50, 49)
    if "征求意见" in text:
        raise ValueError("draft_not_final")
    release_title = "关于发布瓶片期货合约及业务细则的公告"
    listing_title = "关于挂牌瓶片期货有关事项的公告"
    if release_title not in text or listing_title not in text:
        raise ValueError("product_identity")
    before, listing = text.split(listing_title, 1)
    release = before.split(release_title, 1)[1]
    listing = listing.split("关于征集瓶片期货指定交割仓", 1)[0]
    checks = {
        "release_identity": (release, "〔2024〕105号"),
        "effective_date": (release, "自2024年8月30日起施行"),
        "listing_identity": (listing, "〔2024〕106号"),
        "listing_date": (listing, "自2024年8月30日（星期五）起上市交易"),
        "day_sections": (listing, "交易时间为9:00-11:30和13:30-15:00"),
        "first_night": (listing, "8月30日当晚起开展夜盘交易"),
        "night_hours": (listing, "夜盘交易时间为21:00-23:00"),
        "release_publication": (release, "2024年8月16日"),
        "listing_publication": (listing, "2024年8月16日"),
    }
    for name, (body, expected) in checks.items():
        if expected not in body:
            raise ValueError(name)
    contracts = re.findall(r"PR\d{4}", listing)
    if contracts != [f"PR250{month}" for month in range(3, 9)]:
        raise ValueError("first_contracts")
    return dict(product="CZCE.PR", source_pdf_pages=[49, 50], printed_pages=[48, 49],
                notice_date=20240816, first_availability_time_proved=False,
                rules_announced_effective_date=20240830,
                first_announced_trading_day=20240830, first_night_wall_date=20240830,
                announced_day_sections=["09:00-11:30", "13:30-15:00"],
                announced_night="21:00-23:00", first_contracts=contracts,
                midday_break_certified=False, annual_calendar_certified=False,
                full_lifecycle_certified=False, financial_eligible=False,
                scope="announced listing facts only; no inferred yearly hours, holidays or raw-code decade mapping")


if __name__ == "__main__":
    import argparse
    import io
    import json
    from pathlib import Path
    import sys
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("page49", type=Path)
    parser.add_argument("page50", type=Path)
    args = parser.parse_args()
    print(json.dumps(review(args.page49.read_text(encoding="utf-8"), args.page50.read_text(encoding="utf-8")), ensure_ascii=False, indent=2))
