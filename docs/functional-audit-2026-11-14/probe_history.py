"""Small serial public-data probes; no credentials, no production writes.

Use --group ths or --group eastmoney. This records observed coverage, not a
vendor retention guarantee. Stop a group after two transport/business failures.
"""

from __future__ import annotations

import argparse
from collections import Counter
from datetime import datetime
import hashlib
import json
from pathlib import Path
import time
from zoneinfo import ZoneInfo

import requests

ZONE = ZoneInfo("Asia/Shanghai")
ROOT = Path(__file__).resolve().parent


def ths_cases():
    endpoint = "https://quota-h.10jqka.com.cn/fuyao/common_hq_aggr/quote/v1/single_kline"
    for target in ("2026-11-13", "2026-10-12", "2026-08-14", "2025-11-14"):
        end = int(datetime.fromisoformat(target + "T15:00:00").replace(tzinfo=ZONE).timestamp() * 1000)
        body = {
            "code_list": [{"codes": ["000001"], "market": "33"}],
            "trade_class": "intraday", "time_period": "min_5", "trade_date": -1,
            "begin_time": -400, "end_time": end, "adjust_type": "actual", "gpid": 1,
            "data_fields": ["1", "7", "8", "9", "11", "13", "19"],
        }
        yield "ths_min5_" + target, target, endpoint, body


def eastmoney_cases():
    yield "em_industry_catalog", None, "https://push2.eastmoney.com/api/qt/clist/get", {
        "pn": 1, "pz": 100, "po": 1, "np": 1, "fltt": 2, "invt": 2,
        "fid": "f12", "fields": "f12,f14", "fs": "m:90+t:2+f:!50",
    }
    yield "em_stock_min5", "2026-11-13", "https://push2his.eastmoney.com/api/qt/stock/kline/get", {
        "secid": "0.000001", "klt": 5, "fqt": 0, "beg": "20200101", "end": "20261113", "lmt": 10000,
        "fields1": "f1,f2,f3,f4,f5,f6", "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61",
    }


def ths_extended_cases():
    endpoint = "https://quota-h.10jqka.com.cn/fuyao/common_hq_aggr/quote/v1/single_kline"
    cases = [
        ("ths_min5_2400", "000001", "33", "2026-11-13", "min_5", -2400, "actual"),
        ("ths_min5_2020", "000001", "33", "2020-11-13", "min_5", -100, "actual"),
        ("ths_min5_sh_2025", "600000", "17", "2025-11-14", "min_5", -100, "actual"),
        ("ths_day_2025_actual", "000001", "33", "2025-03-24", "day", -10, "actual"),
        ("ths_day_2025_forward", "000001", "33", "2025-03-24", "day", -10, "forward"),
        ("ths_day_long", "000001", "33", "2026-11-13", "day", -2400, "actual"),
    ]
    for name, code, market, target, period, count, adjust in cases:
        end = int(datetime.fromisoformat(target + "T15:00:00").replace(tzinfo=ZONE).timestamp() * 1000)
        body = {
            "code_list": [{"codes": [code], "market": market}],
            "trade_class": "intraday", "time_period": period, "trade_date": -1,
            "begin_time": count, "end_time": end, "adjust_type": adjust, "gpid": 1,
            "data_fields": ["1", "7", "8", "9", "11", "13", "19"],
        }
        yield name, target, endpoint, body


def eastmoney_direct_cases():
    yield "em_direct_industry_catalog", None, "https://push2.eastmoney.com/api/qt/clist/get", {
        "pn": 1, "pz": 100, "po": 1, "np": 1, "fltt": 2, "invt": 2,
        "fid": "f12", "fields": "f12,f14", "fs": "m:90+t:2+f:!50",
    }
    yield "em_direct_day_raw", "2025-03-24", "https://push2his.eastmoney.com/api/qt/stock/kline/get", {
        "secid": "0.000001", "klt": 101, "fqt": 0, "beg": "20250317", "end": "20250324", "lmt": 10,
        "fields1": "f1,f2,f3,f4,f5,f6", "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61",
    }


def eastmoney_retention_cases():
    endpoint = "https://push2his.eastmoney.com/api/qt/stock/kline/get"
    for name, period, start, end, limit in [
        ("em_min5_long_direct", 5, "20200101", "20261113", 10000),
        ("em_min5_year_ago_direct", 5, "20251114", "20251114", 100),
        ("em_daily_long_direct", 101, "19900101", "20261113", 10000),
    ]:
        yield name, end, endpoint, {
            "secid": "0.000001", "klt": period, "fqt": 0, "beg": start, "end": end, "lmt": limit,
            "fields1": "f1,f2,f3,f4,f5,f6", "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61",
        }


def ths_boundary_cases():
    endpoint = "https://quota-h.10jqka.com.cn/fuyao/common_hq_aggr/quote/v1/single_kline"
    for name, code, market, target, count in [
        ("ths_boundary_sz_400", "000001", "33", "2025-11-14", -400),
        ("ths_boundary_sz_100", "000001", "33", "2025-11-14", -100),
        ("ths_boundary_sh_400", "600000", "17", "2025-11-14", -400),
        ("ths_boundary_sz_1000", "000001", "33", "2026-11-13", -1000),
    ]:
        end = int(datetime.fromisoformat(target + "T15:00:00").replace(tzinfo=ZONE).timestamp() * 1000)
        yield name, target, endpoint, {
            "code_list": [{"codes": [code], "market": market}],
            "trade_class": "intraday", "time_period": "min_5", "trade_date": -1,
            "begin_time": count, "end_time": end, "adjust_type": "actual", "gpid": 1,
            "data_fields": ["1", "7", "8", "9", "11", "13", "19"],
        }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--group", choices=["ths", "eastmoney", "ths-extended", "eastmoney-direct", "eastmoney-retention", "ths-boundary"], required=True)
    parser.add_argument("--output", type=Path, help="A new directory; the issued audit evidence is never overwritten")
    args = parser.parse_args()
    output = args.output or ROOT / ("probe-rerun-" + args.group + "-" + datetime.now(ZONE).strftime("%Y%m%dT%H%M%S%f"))
    output.mkdir(parents=True, exist_ok=False)
    session = requests.Session()
    # A probe uses the host's ordinary network configuration. It does not
    # change system proxy settings, rotate exits, or replay captured cookies.
    session.headers.update({"User-Agent": "Mozilla/5.0 ShadowFlowAudit/1.0"})
    if args.group in {"eastmoney-direct", "eastmoney-retention", "ths-boundary"}:
        session.trust_env = False
    cases = {
        "ths": ths_cases, "eastmoney": eastmoney_cases,
        "ths-extended": ths_extended_cases, "eastmoney-direct": eastmoney_direct_cases,
        "eastmoney-retention": eastmoney_retention_cases, "ths-boundary": ths_boundary_cases,
    }[args.group]()
    results = []
    failures = 0
    for name, target, url, values in cases:
        if results:
            time.sleep(3)
        started = time.monotonic()
        result = {"name": name, "observed_at": datetime.now(ZONE).isoformat(),
                  "url": url, "request": values, "target_date": target,
                  "network_mode": "environment_default" if session.trust_env else "direct_no_env_proxy"}
        try:
            if args.group.startswith("ths"):
                response = session.post(url, json=values, timeout=(5, 12))
            else:
                response = session.get(url, params=values, timeout=(5, 12))
            result["http"] = response.status_code
            result["sha256"] = hashlib.sha256(response.content).hexdigest()
            response.raise_for_status()
            payload = response.json()
            result["business_code"] = payload.get("status_code", payload.get("rc"))
            if result["business_code"] not in (0, None):
                raise ValueError("nonzero business status")
            if args.group.startswith("ths"):
                quotes = (payload.get("data") or {}).get("quote_data") or []
                result["group_count"] = len(quotes)
                quote = quotes[0] if quotes else {}
                rows = quote.get("value") or []
                result["fields"] = quote.get("data_fields")
                result["code"] = quote.get("code")
                result["market"] = quote.get("market")
                result["rows"] = len(rows)
                timestamps = [datetime.fromtimestamp(row[0] / 1000, ZONE).isoformat() for row in rows]
                result["first"] = timestamps[0] if timestamps else None
                result["last"] = timestamps[-1] if timestamps else None
                result["rows_by_date"] = dict(sorted(Counter(t[:10] for t in timestamps).items()))
                result["target_rows"] = sum(t[:10] == target for t in timestamps)
                result["sample_first"] = rows[0] if rows else None
                result["sample_last"] = rows[-1] if rows else None
            else:
                data = payload.get("data") or {}
                rows = data.get("klines", data.get("diff", []))
                result["rows"] = len(rows)
                result["declared_total"] = data.get("total")
                if isinstance(rows, list) and rows and isinstance(rows[0], str):
                    result["first"] = rows[0]
                    result["last"] = rows[-1]
            if not result.get("rows"):
                result["no_rows"] = True
            failures = 0
            (output / (name + ".raw.json")).write_text(json.dumps(payload, ensure_ascii=False) + "\n")
        except Exception as exc:
            result["error"] = type(exc).__name__ + ": " + str(exc)[:250]
            failures += 1
        result["elapsed_ms"] = round(1000 * (time.monotonic() - started))
        results.append(result)
        brief = {k: v for k, v in result.items() if k != "rows_by_date"}
        if "rows_by_date" in result:
            brief["distinct_dates"] = len(result["rows_by_date"])
        print(json.dumps(brief, ensure_ascii=False), flush=True)
        (output / (args.group + "-history-probes.json")).write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n")
        if failures >= 2:
            break


if __name__ == "__main__":
    main()
