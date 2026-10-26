#!/usr/bin/env python3
"""Extract one bounded board snapshot, never a day or an entire database."""

import argparse
from contextlib import closing, ExitStack
from datetime import date, datetime, timedelta, timezone
import json
from pathlib import Path
import sqlite3
import sys
import time


MAX_ROWS = 500
MEMORY_LIMIT_BYTES = 128 << 20
CPU_LIMIT_SECONDS = 5
QUERY_LIMIT_SECONDS = 8
SHANGHAI = timezone(timedelta(hours=8))
CLOCKS = {f"{n // 60:02d}:{n % 60:02d}" for begin, end in ((575, 690), (785, 900))
          for n in range(begin, end + 1, 5)}


def apply_limits():
    # Enforce limits in the Linux worker, not in the user's local controller.
    # Do not run an unbounded fallback when this protection is unavailable.
    if sys.platform != "linux":
        raise RuntimeError("worker requires Linux resource limits")
    import os
    import resource
    import signal
    resource.setrlimit(resource.RLIMIT_AS, (MEMORY_LIMIT_BYTES, MEMORY_LIMIT_BYTES))
    resource.setrlimit(resource.RLIMIT_CPU, (CPU_LIMIT_SECONDS, CPU_LIMIT_SECONDS))
    os.nice(19)
    def timeout(signum, frame):
        raise TimeoutError("worker exceeded its wall-clock limit")
    signal.signal(signal.SIGALRM, timeout)
    signal.alarm(QUERY_LIMIT_SECONDS + 2)


def extract(database, trade_date, rank_type, at, cold_database=None):
    if date.fromisoformat(trade_date).isoformat() != trade_date:
        raise ValueError("trade date must use YYYY-MM-DD")
    if rank_type not in ("industry", "concept") or at not in CLOCKS:
        raise ValueError("only one industry/concept five-minute snapshot is allowed")
    database = Path(database).resolve()
    if not database.is_file():
        raise ValueError("source database must already exist")
    stamp = (datetime.fromisoformat(trade_date + "T" + at + ":00")
             .replace(tzinfo=SHANGHAI).astimezone(timezone.utc).isoformat().replace("+00:00", "Z"))
    deadline = time.monotonic() + QUERY_LIMIT_SECONDS
    with ExitStack() as stack:
        connection = stack.enter_context(closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True, timeout=1)))
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA query_only=ON")
        connection.execute("PRAGMA cache_size=-2048")
        connection.set_progress_handler(lambda: int(time.monotonic() > deadline), 1000)
        connection.execute("BEGIN")
        # Exact leading primary-key predicates and a hard limit avoid history
        # scans, full-date sorts, and unbounded fetchall allocations.
        rows = [dict(row) for row in connection.execute("""SELECT * FROM rank_intraday_work
            WHERE trade_date=? AND snapshot_at=? AND rank_type=? LIMIT ?""",
            (trade_date, stamp, rank_type, MAX_ROWS + 1))]
        if len(rows) > MAX_ROWS:
            raise ValueError("snapshot exceeds the hard row limit; no data exported")
        cold = None
        if cold_database:
            path = Path(cold_database).resolve(strict=True)
            cold = stack.enter_context(closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True, timeout=1)))
            cold.row_factory = sqlite3.Row
            cold.execute("PRAGMA query_only=ON")
            cold.execute("PRAGMA cache_size=-2048")
            cold.set_progress_handler(lambda: int(time.monotonic() > deadline), 1000)
            cold_rows = [dict(row) for row in cold.execute("""SELECT * FROM rank_intraday_work
                WHERE trade_date=? AND snapshot_at=? AND rank_type=? LIMIT ?""",
                (trade_date, stamp, rank_type, MAX_ROWS + 1))]
            if len(cold_rows) > MAX_ROWS:
                raise ValueError("cold snapshot exceeds the hard row limit")
            merged = {(r["trade_date"], r["snapshot_at"], r["rank_type"], r["code"]): r for r in cold_rows}
            merged.update({(r["trade_date"], r["snapshot_at"], r["rank_type"], r["code"]): r for r in rows})
            rows = list(merged.values())
            if len(rows) > MAX_ROWS:
                raise ValueError("combined snapshot exceeds the hard row limit")
        run_ids = {row["run_id"] for row in rows}
        if len(run_ids) > 1:
            raise ValueError("mixed collection runs; refusing to infer snapshot completeness")
        run = None
        if run_ids:
            found = connection.execute("SELECT * FROM collection_run WHERE run_id=?",
                                       (next(iter(run_ids)),)).fetchone()
            if not found and cold and cold.execute("SELECT count(*) FROM sqlite_schema WHERE name='collection_run'").fetchone()[0]:
                found = cold.execute("SELECT * FROM collection_run WHERE run_id=?", (next(iter(run_ids)),)).fetchone()
            run = dict(found) if found else None
        verified = bool(rows and run and run["status"] == "success"
                        and run["requested_date"] == trade_date and run["actual_trade_date"] == trade_date
                        and run["rank_type"] == rank_type and run["snapshot_kind"] == "minute_work"
                        and run["expected_total"] == len(rows) and run["fetched_total"] == len(rows)
                        and run["snapshot_at"] == stamp
                        and len({(row["market"], row["code"]) for row in rows}) == len(rows)
                        and all(row["money_available"] == 1 for row in rows))
        connection.rollback()
    return {"schema_version": "intraday-single-snapshot-v1", "trade_date": trade_date,
            "rank_type": rank_type, "snapshot_at": stamp, "rows": rows, "run": run,
            "verified": verified, "source_modified": False,
            "limits": {"max_rows": MAX_ROWS, "memory_bytes": MEMORY_LIMIT_BYTES,
                       "cpu_seconds": CPU_LIMIT_SECONDS, "query_seconds": QUERY_LIMIT_SECONDS}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--cold-database", type=Path)
    parser.add_argument("--trade-date", required=True)
    parser.add_argument("--type", choices=("industry", "concept"), required=True)
    parser.add_argument("--at", required=True)
    args = parser.parse_args()
    try:
        apply_limits()
        print("SNAPSHOT_WORKER_LIMITS_INSTALLED", file=sys.stderr, flush=True)
        result = extract(args.database, args.trade_date, args.type, args.at, args.cold_database)
        print(f"SNAPSHOT_READ_DONE rows={len(result['rows'])} verified={result['verified']}",
              file=sys.stderr, flush=True)
        # No gzip or whole-history assembly. At most MAX_ROWS enter this JSON.
        print(json.dumps(result, ensure_ascii=False))
    except (ValueError, RuntimeError, OSError, MemoryError, sqlite3.Error) as error:
        parser.exit(1, f"snapshot extraction refused: {error}\n")


if __name__ == "__main__":
    main()
