#!/usr/bin/env python3
"""Read-only board close recovery; export isolated data, never publish to production."""

import argparse
from collections import defaultdict
from contextlib import closing
import csv
from datetime import date, datetime, timedelta, timezone
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import subprocess
import sqlite3
import tempfile
import time
from urllib.parse import parse_qs, urlencode, urlsplit


VERSION = "intraday-recovery-v1"
SHANGHAI = timezone(timedelta(hours=8))
KINDS = ("industry", "concept")
CLOCKS = [f"{n // 60:02d}:{n % 60:02d}" for begin, end in ((575, 690), (785, 900))
          for n in range(begin, end + 1, 5)]
CLOSE_FIELDS = ("trade_date", "snapshot_at", "rank_type", "market", "code", "name", "rank",
                "quote_time", "latest_price_raw", "change_pct", "dark_money", "regular_money",
                "main_money_inflow", "dark_activity", "dark_inflow_ratio", "up_count", "down_count",
                "leader_name", "leader_code", "run_id", "fetched_at")
POINT_FIELDS = ("trade_date", "snapshot_at", "rank_type", "market", "code", "name", "rank",
                "quote_time", "dark_money", "regular_money", "main_money_inflow", "money_available",
                "run_id", "fetched_at")
ENDPOINT = "https://push2his.eastmoney.com/api/qt/stock/kline/get"
# A page-origin fetch through the local user's browser is the validated path
# when script clients are refused; it is opt-in, user-observed, and read-only.
USER_BROWSER_COPY_ENDPOINT = "https://push2his.eastmoney.com/api/qt/stock/kline/get"


def iso_date(value):
    if date.fromisoformat(value).isoformat() != value:
        raise ValueError("date must use YYYY-MM-DD")
    return value


def clock_at(row, day):
    at = datetime.fromisoformat(row["snapshot_at"].replace("Z", "+00:00"))
    if at.tzinfo is None:
        raise ValueError("timestamp has no timezone")
    at = at.astimezone(SHANGHAI)
    if at.date().isoformat() != day or at.second or at.microsecond:
        raise ValueError("timestamp is outside the trading date/minute grid")
    return at.strftime("%H:%M")


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def component(db, day, kind):
    existing = db.execute("""SELECT count(*) FROM rank_snapshot WHERE trade_date=?
        AND rank_type=? AND snapshot_kind='daily_close'""", (day, kind)).fetchone()[0]
    result = {"trade_date": day, "rank_type": kind, "existing_close_rows": existing,
              "status": "blocked", "errors": [], "postclose_revised": False}
    if existing:
        result["status"] = "preserved_existing"
        return result, [], []
    buckets = defaultdict(list)
    for record in db.execute("""SELECT * FROM rank_intraday_work WHERE trade_date=?
        AND rank_type=? ORDER BY snapshot_at,market,code""", (day, kind)):
        record = dict(record)
        try:
            clock = clock_at(record, day)
        except ValueError as error:
            result["errors"].append(str(error))
            continue
        buckets[clock].append(record)
    result["intraday_minutes"] = len(buckets)
    result["missing_clocks"] = [clock for clock in CLOCKS if clock not in buckets]
    closes = buckets.get("15:00", [])
    identities = {(r["market"], r["code"]) for r in closes}
    result["close_rows"] = len(closes)
    if not identities or len(identities) != len(closes):
        result["errors"].append("15:00 universe is empty or duplicated")
    if result["missing_clocks"]:
        result["errors"].append("required five-minute timestamps are missing")
    unverified = []
    for clock in CLOCKS:
        rows = buckets.get(clock, [])
        observed = {(r["market"], r["code"]) for r in rows}
        if observed != identities or len(observed) != len(rows):
            result["errors"].append(f"{clock}: code universe differs from 15:00")
        if any(r["money_available"] != 1 for r in rows):
            result["errors"].append(f"{clock}: unavailable money")
        if any(not r["code"] or r["market"] != 90 for r in rows):
            result["errors"].append(f"{clock}: invalid board identity")
        run_ids = {r["run_id"] for r in rows}
        if not rows:
            continue
        run = (db.execute("SELECT * FROM collection_run WHERE run_id=?", (next(iter(run_ids)),)).fetchone()
               if len(run_ids) == 1 else None)
        if (run is None or run["status"] != "success" or run["rank_type"] != kind
                or run["snapshot_kind"] != "minute_work" or run["requested_date"] != day
                or run["actual_trade_date"] != day or run["expected_total"] != len(rows)
                or run["fetched_total"] != len(rows)):
            unverified.append(clock)
        else:
            try:
                if clock_at(dict(run), day) != clock:
                    unverified.append(clock)
            except ValueError:
                unverified.append(clock)
    result["unverified_snapshots"] = unverified
    if unverified:
        result["errors"].append("collection records do not prove complete successful snapshots")
    result["errors"] = sorted(set(result["errors"]))
    if result["errors"]:
        return result, [], []
    result.update(status="recoverable", money_rows=len(identities) * 48,
                  code_set_sha256=hashlib.sha256("".join(f"{m}|{c}\n" for m, c in sorted(identities)).encode()).hexdigest(),
                  quote_available=False,
                  missing_fields=["open_price", "high_price", "low_price", "close_price", "previous_close",
                                  "volume", "turnover", "turnover_rate", "amplitude", "flat_count"],
                  warning="Observed 15:00 funds may lag and do not include post-close revisions.")
    close_rows = [{key: r[key] for key in CLOSE_FIELDS} for r in closes]
    points = []
    for index, clock in enumerate(CLOCKS):
        for r in buckets[clock]:
            points.append(dict({key: r[key] for key in POINT_FIELDS}, minute_index=index))
    for r in close_rows + points:
        r.update(recovery_source=VERSION, funds_postclose_revised=False)
    return result, close_rows, points


def parse_daily(body, market, code, start, end):
    text = body.decode("utf-8-sig").strip()
    if not text.startswith("{") and "(" in text and text.rstrip(";").endswith(")"):
        text = text[text.index("(") + 1:text.rindex(")")]
    payload = json.loads(text)
    data = payload.get("data")
    if payload.get("rc") != 0 or not isinstance(data, dict):
        raise ValueError("daily API response unsuccessful")
    if data.get("code") != code or int(data.get("market", -1)) != int(market):
        raise ValueError("daily API identity mismatch")
    bars, seen = {}, set()
    names = ("open_price", "close_price", "high_price", "low_price", "volume", "turnover",
             "amplitude", "change_pct", "change_value", "turnover_rate")
    for raw in data.get("klines", []):
        fields = next(csv.reader([raw]))
        if len(fields) != 11:
            raise ValueError("daily API field count mismatch")
        day = iso_date(fields[0])
        if day in seen:
            raise ValueError("duplicate daily API date")
        seen.add(day)
        numbers = [Decimal(x) for x in fields[1:]]
        if any(not x.is_finite() for x in numbers):
            raise ValueError("non-finite daily API value")
        open_, close, high, low, volume, turnover, amplitude, change, delta, rate = numbers
        if min(open_, close, low) <= 0 or not low <= min(open_, close) <= max(open_, close) <= high:
            raise ValueError("daily OHLC ordering mismatch")
        if min(volume, turnover, amplitude, rate) < 0 or volume != volume.to_integral_value():
            raise ValueError("invalid volume/turnover/rate")
        previous = close - delta
        if previous <= 0 or abs((close / previous - 1) * 100 - change) > Decimal("0.06"):
            raise ValueError("daily percentage/previous close mismatch")
        if start <= day <= end:
            row = {key: str(value) for key, value in zip(names, numbers)}
            for key in ("amplitude", "change_pct", "turnover_rate"):
                row[key] = str(Decimal(row[key]) / 100)
            row.update(previous_close=str(previous), trade_date=day, market=market, code=code,
                       quote_source="eastmoney_daily_kline_fqt0", quote_available=True)
            bars[day] = row
    if not bars:
        raise ValueError("no daily bars in requested interval")
    return bars


def fetch_daily(market, code, start, end):
    # A single-day repair uses the same modest limit as the validated probe.
    limit = max(100, (date.fromisoformat(end) - date.fromisoformat(start)).days + 1)
    params = {"secid": f"{market}.{code}", "ut": "fa5fd1943c7b386f172d6893dbfba10b",
              "fields1": "f1,f2,f3,f4,f5,f6", "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61",
              "klt": "101", "fqt": "0", "beg": start.replace("-", ""),
              "end": end.replace("-", ""), "lmt": str(limit)}
    url = ENDPOINT + "?" + urlencode(params)
    # System curl uses the installed CA store; never disable TLS verification.
    result = subprocess.run(["curl", "-sS", "--connect-timeout", "5", "--max-time", "12",
        "--max-filesize", str(2 << 20),
        "-w", "\n__STATUS__%{http_code}", url], capture_output=True, timeout=15)
    if result.returncode:
        raise ValueError(f"curl exit {result.returncode}: {result.stderr.decode().strip()[:160]}")
    body, separator, status = result.stdout.rpartition(b"\n__STATUS__")
    if not separator or status != b"200" or len(body) > 2 << 20:
        raise ValueError("daily API HTTP status/size mismatch")
    return body, url


def browser_copy_daily():
    """Print instructions for the user-approved browser fallback path."""
    raise ValueError(
        "script client refused by upstream. Use the browser fallback: open the "
        f"{USER_BROWSER_COPY_ENDPOINT} URL for each requested board in the user's browser, "
        "save the response as data/recovery-20261023/api-cache/90.<code>.json plus a receipt, "
        "then rerun with --cache-directory. This tool does not drive the browser or bypass "
        "any upstream control.")


def cached_daily(directory, market, code, start, end):
    if not code.isalnum():
        raise ValueError("unsafe daily cache identity")
    raw = directory / f"{market}.{code}.json"
    receipt = raw.with_suffix(".receipt.json")
    if not raw.exists() and not receipt.exists():
        return None
    if not raw.exists() or not receipt.exists():
        raise ValueError("incomplete daily cache pair; refusing replacement")
    metadata = json.loads(receipt.read_text())
    if sha256(raw) != metadata["sha256"]:
        raise ValueError("daily cache checksum mismatch")
    contract = metadata["contract"]
    if (contract.get("secid") != f"{market}.{code}" or str(contract.get("klt")) != "101"
            or str(contract.get("fqt")) != "0" or metadata.get("endpoint") != ENDPOINT):
        raise ValueError("daily cache request contract mismatch")
    if contract.get("beg", "9") > start.replace("-", "") or contract.get("end", "0") < end.replace("-", ""):
        return None
    body = raw.read_bytes()
    parse_daily(body, market, code, start, end)
    return body, metadata.get("url")


def save_daily_cache(directory, market, code, body, url):
    directory.mkdir(parents=True, exist_ok=True)
    raw = directory / f"{market}.{code}.json"
    receipt = raw.with_suffix(".receipt.json")
    if raw.exists() or receipt.exists():
        # Never overwrite a successful cache just to widen its date coverage.
        return
    query = {key: values[0] for key, values in parse_qs(urlsplit(url).query).items()}
    metadata = {"endpoint": ENDPOINT, "url": url, "contract": query,
                "sha256": hashlib.sha256(body).hexdigest()}
    raw.write_bytes(body)
    receipt.write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n")


def append_csv(path, rows):
    if not rows:
        return 0
    existing = path.exists()
    with path.open("a", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(rows[0]))
        if not existing:
            writer.writeheader()
        writer.writerows(rows)
    return len(rows)


def enrich_closes(stage, closes, args):
    grouped = defaultdict(list)
    for row in closes:
        if not args.codes or row["code"] in args.codes:
            grouped[(row["market"], row["code"])].append(row)
    outcomes, total, failures = [], 0, 0
    raw_dir = stage / "raw_daily"
    raw_dir.mkdir()
    cache = getattr(args, "cache_directory", None)
    # Reuse all existing responses before sending new requests, so a later
    # upstream failure cannot hide already available historical OHLC.
    ordered = sorted(grouped, key=lambda key: (
        not (cache and (cache / f"{key[0]}.{key[1]}.json").exists()), key))
    for market, code in ordered:
        rows = grouped[(market, code)]
        start, end = min(r["trade_date"] for r in rows), max(r["trade_date"] for r in rows)
        outcome = {"market": market, "code": code, "requested_dates": sorted({r["trade_date"] for r in rows})}
        cache_hit = False
        try:
            if args.raw_directory:
                body, url = (args.raw_directory / f"{market}.{code}.json").read_bytes(), None
            else:
                cached = cached_daily(cache, market, code, start, end) if cache else None
                if cached:
                    body, url = cached
                    cache_hit = True
                else:
                    body, url = fetch_daily(market, code, start, end)
            raw_path = raw_dir / f"{market}.{code}.json"
            raw_path.write_bytes(body)
            outcome.update(raw_file=str(raw_path.relative_to(stage)), raw_sha256=sha256(raw_path), url=url)
            outcome["fqt_verified_by_request"] = cache_hit or url is not None
            outcome["from_cache"] = cache_hit
            bars = parse_daily(body, market, code, start, end)
            if cache and not cache_hit and url is not None:
                save_daily_cache(cache, market, code, body, url)
            merged = []
            for row in rows:
                if row["trade_date"] not in bars:
                    continue
                item = dict(row)
                item["intraday_change_pct"] = item.pop("change_pct")
                item.update(bars[row["trade_date"]])
                item["ohlc_raw_sha256"] = outcome["raw_sha256"]
                merged.append(item)
            total += append_csv(stage / "recovered_board_close_with_ohlc.csv", merged)
            missing = sorted({r["trade_date"] for r in rows} - bars.keys())
            outcome.update(status="partial" if missing else "success", missing_dates=missing)
            failures = failures + 1 if missing else 0
        except (OSError, ValueError, ArithmeticError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
            outcome.update(status="failed", error_type=type(error).__name__, error=str(error)[:240])
            failures += 1
        outcomes.append(outcome)
        if failures >= args.max_failures:
            break
        if not args.raw_directory and not cache_hit:
            time.sleep(args.interval)
    return {"requested_identities": len(grouped), "attempted_identities": len(outcomes),
            "enriched_close_rows": total, "daily_contract": "klt=101,fqt=0", "outcomes": outcomes,
            "offline_contract_asserted_by_caller": bool(args.raw_directory)}


def export(args):
    iso_date(args.from_date)
    iso_date(args.to_date)
    if args.from_date > args.to_date:
        raise ValueError("from-date follows to-date")
    if args.overwrite_stale_report and args.output.is_file() and not any(args.output.iterdir()):
        # Only a previously interrupted, zero-byte report file may be replaced;
        # real recovery bundles are always preserved.
        args.output.unlink()
    if not args.database.is_file() or args.output.exists() or not args.output.parent.is_dir():
        raise ValueError("database must exist and output must be a new directory with an existing parent")
    if args.interval < 1 or args.max_failures < 1:
        raise ValueError("interval and max-failures must be positive")
    wants_ohlc = (args.ohlc or args.raw_directory or args.cache_directory or args.browser_fallback)
    report = {"schema_version": VERSION, "source_database": str(args.database.resolve()),
              "from_date": args.from_date, "to_date": args.to_date, "source_modified": False,
              "snapshot_method": "SQLite online backup from mode=ro source",
              "funds_postclose_revised": False, "days": [], "files": [],
              "production_publication": False,
              "limitations": ["Recovered funds are not post-close revised.",
                              "Missing stock five-minute K-lines are not reconstructed.",
                              "This bundle does not seal or update production archives."]}
    with tempfile.TemporaryDirectory(prefix=".intraday-recovery-", dir=args.output.parent) as temporary:
        temporary = Path(temporary)
        stage = temporary / "bundle"
        stage.mkdir()
        counts, all_closes = {}, []
        with closing(sqlite3.connect(temporary / "snapshot.db")) as db:
            with closing(sqlite3.connect(args.database.resolve().as_uri() + "?mode=ro", uri=True)) as source:
                source.execute("PRAGMA query_only=ON")
                deadline = time.monotonic() + 120
                def progress(status, remaining, total):
                    if time.monotonic() > deadline:
                        raise ValueError("snapshot exceeded 120 seconds; retry using a stable copy")
                source.backup(db, pages=1024, progress=progress, sleep=.01)
            db.row_factory = sqlite3.Row
            db.execute("PRAGMA query_only=ON")
            dates = [r[0] for r in db.execute("""SELECT trade_date FROM daily_archive_manifest
                WHERE trade_date BETWEEN ? AND ? UNION SELECT trade_date FROM rank_intraday_work
                WHERE trade_date BETWEEN ? AND ? ORDER BY trade_date""",
                (args.from_date, args.to_date, args.from_date, args.to_date))]
            for day in dates:
                manifest = db.execute("SELECT * FROM daily_archive_manifest WHERE trade_date=?", (day,)).fetchone()
                entry = {"trade_date": day, "original_manifest": dict(manifest) if manifest else None, "components": []}
                for kind in KINDS:
                    audit, closes, points = component(db, day, kind)
                    entry["components"].append(audit)
                    for name, rows in (("recovered_board_close.csv", closes), ("recovered_board_money_5m.csv", points)):
                        count = append_csv(stage / name, rows)
                        if count:
                            counts[name] = counts.get(name, 0) + count
                    if wants_ohlc:
                        all_closes.extend(closes)
                report["days"].append(entry)
        if wants_ohlc:
            report["ohlc"] = enrich_closes(stage, all_closes, args)
            if report["ohlc"]["enriched_close_rows"]:
                counts["recovered_board_close_with_ohlc.csv"] = report["ohlc"]["enriched_close_rows"]
        report["recoverable_components"] = sum(c["status"] == "recoverable" for d in report["days"] for c in d["components"])
        report["files"] = [{"name": name, "rows": count, "sha256": sha256(stage / name)} for name, count in counts.items()]
        (stage / "manifest.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        if args.output.exists():
            raise ValueError("output appeared during export; refusing replacement")
        stage.rename(args.output)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--from-date", required=True)
    parser.add_argument("--to-date", required=True)
    parser.add_argument("--ohlc", action="store_true", help="fetch daily OHLC separately from recovered funds")
    parser.add_argument("--raw-directory", type=Path, help="offline daily responses named market.code.json")
    parser.add_argument("--browser-fallback", action="store_true",
                        help="explain the user-observed browser fallback instead of failing silently")
    parser.add_argument("--cache-directory", type=Path, help="reuse validated responses and cache new successes")
    parser.add_argument("--codes", nargs="+", help="limit daily OHLC fetches to these board codes")
    parser.add_argument("--interval", type=float, default=3)
    parser.add_argument("--max-failures", type=int, default=2)
    parser.add_argument("--overwrite-stale-report", action="store_true",
                        help="replace only an empty leftover file at --output before exporting")
    args = parser.parse_args()
    if args.browser_fallback and not args.raw_directory and not args.cache_directory:
        try:
            browser_copy_daily()
        except ValueError as message:
            print(message)
            return
    try:
        report = export(args)
    except (OSError, ValueError, sqlite3.Error) as error:
        parser.exit(1, f"recovery failed: {error}\n")
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
