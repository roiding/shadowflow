#!/usr/bin/env python3
"""Off-hours, resumable intraday archival with copy/verify/commit/delete ordering."""

import argparse
from contextlib import closing
from datetime import date, datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import tempfile

from storage_common import (distinct_paths, read_only, storage_lock,
                            sync_directory, sync_file, validate_database)


SHANGHAI = timezone(timedelta(hours=8))
TABLES = ("rank_intraday_work", "raw_response", "collection_run")


def quote(name):
    return '"' + name.replace('"', '""') + '"'


def columns(db, table):
    rows = db.execute("PRAGMA table_info(" + quote(table) + ")").fetchall()
    if not rows:
        raise ValueError(f"required table missing: {table}")
    return [r[1] for r in rows], [r[1] for r in sorted(rows, key=lambda r: r[5]) if r[5]]


def row_digest(db, sql, args=()):
    h, count = hashlib.sha256(), 0
    for row in db.execute(sql, args):
        values = [{"blob_hex": value.hex()} if isinstance(value, bytes) else value for value in row]
        h.update(json.dumps(values, ensure_ascii=True, separators=(",", ":"), allow_nan=False).encode() + b"\n")
        count += 1
    return count, h.hexdigest()


def initialize(cold, main):
    for table in TABLES:
        definition = main.execute("SELECT sql FROM sqlite_schema WHERE type='table' AND name=?", (table,)).fetchone()
        if not definition:
            raise ValueError(f"main schema is missing {table}")
        cold.execute(definition[0])
    for name in ("idx_intraday_series", "idx_collection_run_date"):
        definition = main.execute("SELECT sql FROM sqlite_schema WHERE type='index' AND name=?", (name,)).fetchone()
        if definition:
            cold.execute(definition[0])
    cold.executescript("""CREATE TABLE cold_store_meta (version INTEGER PRIMARY KEY CHECK(version=1));
        INSERT INTO cold_store_meta VALUES (1);
        CREATE TABLE cold_archive_day (
          trade_date TEXT PRIMARY KEY, status TEXT NOT NULL CHECK(status IN ('copied','pruned')),
          work_rows INTEGER NOT NULL, work_sha256 TEXT NOT NULL,
          raw_rows INTEGER NOT NULL, raw_sha256 TEXT NOT NULL,
          verified_at TEXT NOT NULL, pruned_at TEXT);
        PRAGMA journal_mode=DELETE;
        PRAGMA synchronous=FULL;""")


def verify_schema(cold, main):
    if cold.execute("SELECT version FROM cold_store_meta").fetchall() != [(1,)]:
        raise ValueError("cold schema version 1 is required; run upgrade first")
    for table in TABLES:
        names, primary = columns(cold, table)
        expected, expected_primary = columns(main, table)
        if names != expected or primary != expected_primary or not primary:
            raise ValueError(f"cold schema mismatch: {table}")
        if cold.execute("PRAGMA table_info(" + quote(table) + ")").fetchall() != main.execute(
                "PRAGMA table_info(" + quote(table) + ")").fetchall():
            raise ValueError(f"cold column constraints mismatch: {table}")
        definition = "SELECT sql FROM sqlite_schema WHERE type='table' AND name=?"
        if cold.execute(definition, (table,)).fetchone()[0] != main.execute(definition, (table,)).fetchone()[0]:
            raise ValueError(f"cold table constraints mismatch: {table}")


def upgrade(main_path, cold_path):
    main_path, cold_path = distinct_paths(main_path, cold_path)
    with storage_lock(main_path, timeout=120), closing(read_only(main_path)) as main:
        if Path(str(main_path) + ".restore-in-progress").exists():
            raise ValueError("restore is not complete")
        if cold_path.exists():
            with closing(read_only(cold_path)) as cold:
                if cold.execute("SELECT count(*) FROM sqlite_schema WHERE name='cold_store_meta'").fetchone()[0]:
                    verify_schema(cold, main)
                    return {"status": "already_current", "cold": str(cold_path)}
                # Publishing over an active WAL file would strand committed data.
                if cold.execute("PRAGMA journal_mode").fetchone()[0] != "delete":
                    raise ValueError("legacy cold upgrade requires DELETE journaling and no active writers")
        with tempfile.TemporaryDirectory(prefix=".shadowflow-cold-upgrade-", dir=cold_path.parent) as temporary:
            stage = Path(temporary) / "cold.db"
            with closing(sqlite3.connect(stage)) as target:
                initialize(target, main)
                target.execute("PRAGMA cache_size=-8192")
                counts = {}
                if cold_path.exists():
                    with closing(read_only(cold_path)) as source:
                        for table in ("rank_intraday_work", "raw_response"):
                            names, primary = columns(main, table)
                            cols, ordering = ",".join(map(quote, names)), ",".join(map(quote, primary))
                            insert = f"INSERT INTO {quote(table)} ({cols}) VALUES ({','.join('?' for _ in names)})"
                            rows = source.execute(f"SELECT {cols} FROM {quote(table)}")
                            while batch := rows.fetchmany(1024):
                                target.executemany(insert, batch)
                            target.commit()
                            expected = row_digest(source, f"SELECT {cols} FROM {quote(table)} ORDER BY {ordering}")
                            actual = row_digest(target, f"SELECT {cols} FROM {quote(table)} ORDER BY {ordering}")
                            if expected != actual:
                                raise ValueError(f"cold upgrade content mismatch: {table}")
                            counts[table] = {"rows": actual[0], "sha256": actual[1]}
                # Preserve remaining provenance without inventing missing runs.
                names, _ = columns(main, "collection_run")
                cols = ",".join(map(quote, names))
                run_ids = target.execute("SELECT DISTINCT run_id FROM rank_intraday_work")
                for (run_id,) in run_ids:
                    row = main.execute(f"SELECT {cols} FROM collection_run WHERE run_id=?", (run_id,)).fetchone()
                    if row:
                        target.execute(f"INSERT OR IGNORE INTO collection_run ({cols}) VALUES ({','.join('?' for _ in names)})", row)
                target.commit()
            validate_database(stage, "cold")
            sync_file(stage)
            prior = None
            if cold_path.exists():
                prior = cold_path.with_name(cold_path.name + ".pre-upgrade-" + datetime.now().strftime("%Y%m%d-%H%M%S"))
                os.link(cold_path, prior)
            owner = main_path.stat()
            os.chmod(stage, owner.st_mode & 0o777)
            if os.geteuid() == 0:
                os.chown(stage, owner.st_uid, owner.st_gid)
            os.replace(stage, cold_path)
            sync_directory(cold_path.parent)
            return {"status": "upgraded", "cold": str(cold_path), "preserved_prior": str(prior) if prior else None, "content": counts}


def hot_floor(as_of, keep, calendar_path, history_path=None):
    if keep < 1 or keep > 90:
        raise ValueError("keep trading days must be between 1 and 90")
    calendar = json.loads(Path(calendar_path).read_text())
    if as_of > date.fromisoformat(calendar["valid_through"]):
        raise ValueError("trading calendar does not cover the requested date")
    year = date.fromisoformat(calendar["valid_through"]).year
    calendars = {}
    if history_path and Path(history_path).is_file():
        calendars = json.loads(Path(history_path).read_text())
    calendars[str(year)] = calendar
    if history_path:
        history_path = Path(history_path)
        with tempfile.NamedTemporaryFile(dir=history_path.parent, mode="w", delete=False) as output:
            temporary = Path(output.name)
            json.dump(calendars, output)
            output.flush()
            os.fsync(output.fileno())
        try:
            os.replace(temporary, history_path)
            sync_directory(history_path.parent)
        finally:
            temporary.unlink(missing_ok=True)
    day, retained = as_of, 0
    # Keep N completed trading days plus any current-day work. This reproduces
    # the initial 10/12 floor on 10/26 and advances across exchange holidays.
    while retained < keep:
        day -= timedelta(days=1)
        if str(day.year) not in calendars:
            raise ValueError(f"calendar history does not cover {day.year}")
        historical = calendars[str(day.year)]
        holidays, workdays = set(historical["holidays"]), set(historical.get("workdays", []))
        if day.isoformat() in workdays or (day.weekday() < 5 and day.isoformat() not in holidays):
            retained += 1
    return day.isoformat()


def copy_batch(main, cold, table, predicate, args):
    names, primary = columns(main, table)
    cols, keys = ",".join(map(quote, names)), ",".join(map(quote, primary))
    stage = "pending_" + table
    cold.execute("DROP TABLE IF EXISTS temp." + quote(stage))
    cold.execute(f"CREATE TEMP TABLE {quote(stage)} AS SELECT {cols} FROM {quote(table)} WHERE 0")
    rows = main.execute(f"SELECT {cols} FROM {quote(table)} WHERE {predicate}", args)
    insert = f"INSERT INTO temp.{quote(stage)} VALUES ({','.join('?' for _ in names)})"
    while batch := rows.fetchmany(1024):
        cold.executemany(insert, batch)
    updates = ",".join(f"{quote(name)}=excluded.{quote(name)}" for name in names if name not in primary)
    cold.execute(f"INSERT INTO {quote(table)} ({cols}) SELECT {cols} FROM temp.{quote(stage)} WHERE 1 "
                 f"ON CONFLICT ({keys}) DO UPDATE SET {updates}")
    expected = row_digest(cold, f"SELECT {cols} FROM temp.{quote(stage)} ORDER BY {keys}")
    match = " AND ".join(f"stored.{quote(k)}=pending.{quote(k)}" for k in primary)
    selected = ",".join("stored." + quote(n) for n in names)
    order = ",".join("stored." + quote(n) for n in primary)
    actual = row_digest(cold, f"SELECT {selected} FROM {quote(table)} stored JOIN temp.{quote(stage)} pending ON {match} ORDER BY {order}")
    if expected != actual:
        raise ValueError(f"copy verification failed: {table}")
    return expected


def archive_day(main, cold, day, after_copy=None):
    main.execute("BEGIN IMMEDIATE")
    try:
        cold.execute("BEGIN IMMEDIATE")
        work = copy_batch(main, cold, "rank_intraday_work", "trade_date=?", (day,))
        raw = copy_batch(main, cold, "raw_response", "snapshot_kind='research_5m' AND date(snapshot_at,'+8 hours')=?", (day,))
        names, _ = columns(main, "collection_run")
        cols = ",".join(map(quote, names))
        rows = main.execute(f"SELECT {cols} FROM collection_run WHERE run_id IN (SELECT DISTINCT run_id FROM rank_intraday_work WHERE trade_date=?)", (day,))
        cold.executemany(f"INSERT OR REPLACE INTO collection_run ({cols}) VALUES ({','.join('?' for _ in names)})", rows)
        cold.execute("""INSERT INTO cold_archive_day VALUES (?,'copied',?,?,?,?,?,NULL)
            ON CONFLICT(trade_date) DO UPDATE SET status='copied',work_rows=excluded.work_rows,
            work_sha256=excluded.work_sha256,raw_rows=excluded.raw_rows,raw_sha256=excluded.raw_sha256,
            verified_at=excluded.verified_at,pruned_at=NULL""",
                     (day, work[0], work[1], raw[0], raw[1], datetime.now(timezone.utc).isoformat()))
        # FULL synchronous DELETE-journal commit is durable before the hot
        # transaction is allowed to delete anything. A crash leaves duplicates.
        cold.commit()
        if after_copy:
            after_copy()
        main.execute("DELETE FROM rank_intraday_work WHERE trade_date=?", (day,))
        main.execute("DELETE FROM raw_response WHERE snapshot_kind='research_5m' AND date(snapshot_at,'+8 hours')=?", (day,))
        main.commit()
        cold.execute("UPDATE cold_archive_day SET status='pruned',pruned_at=? WHERE trade_date=?",
                     (datetime.now(timezone.utc).isoformat(), day))
        cold.commit()
        return {"trade_date": day, "work_rows": work[0], "work_sha256": work[1], "raw_rows": raw[0], "raw_sha256": raw[1]}
    except BaseException:
        main.rollback()
        cold.rollback()
        raise


def archive(main_path, cold_path, floor, *, apply=False, limit=1):
    main_path, cold_path = distinct_paths(main_path, cold_path)
    if limit < 1 or limit > 31:
        raise ValueError("max days must be between 1 and 31")
    if not main_path.is_file() or not cold_path.is_file():
        raise ValueError("main/cold databases must already exist")
    with storage_lock(main_path, timeout=120), closing(sqlite3.connect(main_path, timeout=5)) as main, closing(sqlite3.connect(cold_path, timeout=5)) as cold:
        if Path(str(main_path) + ".restore-in-progress").exists():
            raise ValueError("restore is not complete")
        verify_schema(cold, main)
        for db in (main, cold):
            db.execute("PRAGMA cache_size=-8192")
            db.execute("PRAGMA temp_store=FILE")
        cold.execute("PRAGMA synchronous=FULL")
        if cold.execute("PRAGMA journal_mode").fetchone()[0] != "delete":
            raise ValueError("cold storage must use DELETE journaling")
        days = [r[0] for r in main.execute("""SELECT trade_date FROM rank_intraday_work WHERE trade_date<?
            UNION SELECT date(snapshot_at,'+8 hours') FROM raw_response
            WHERE snapshot_kind='research_5m' AND date(snapshot_at,'+8 hours')<?
            ORDER BY 1 LIMIT ?""", (floor, floor, limit))]
        if not apply:
            return {"status": "dry_run", "hot_floor": floor, "eligible_dates": days, "max_days": limit}
        results = []
        for day in days:
            results.append(archive_day(main, cold, day))
            print(json.dumps({"archived": results[-1]}), flush=True)
        return {"status": "complete", "hot_floor": floor, "days": results}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("upgrade", "archive"))
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--cold-database", type=Path, required=True)
    parser.add_argument("--calendar", type=Path)
    parser.add_argument("--as-of", type=date.fromisoformat, default=datetime.now(SHANGHAI).date())
    parser.add_argument("--keep-trading-days", type=int, default=10)
    parser.add_argument("--max-days", type=int, default=1)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    if hasattr(os, "nice"):
        os.nice(10)
    os.environ.setdefault("SQLITE_TMPDIR", str(args.database.resolve().parent))
    try:
        if args.apply and 6 <= datetime.now(SHANGHAI).hour < 20:
            raise ValueError("apply is restricted to 20:00-06:00 Asia/Shanghai")
        if args.as_of > datetime.now(SHANGHAI).date():
            raise ValueError("as-of must not be in the future")
        if args.command == "upgrade":
            if not args.apply:
                raise ValueError("upgrade requires --apply; preserves the prior cold file")
            result = upgrade(args.database, args.cold_database)
        else:
            if not args.calendar:
                raise ValueError("archive requires --calendar")
            result = archive(args.database, args.cold_database,
                             hot_floor(args.as_of, args.keep_trading_days, args.calendar,
                                       args.database.resolve().parent / ".shadowflow-calendar-history.json"),
                             apply=args.apply, limit=args.max_days)
        print(json.dumps(result, indent=2))
    except (ValueError, OSError, sqlite3.Error, TimeoutError) as error:
        parser.exit(1, f"storage maintenance refused: {error}\n")


if __name__ == "__main__":
    main()
