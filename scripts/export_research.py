#!/usr/bin/env python3
"""Export ShadowFlow daily research datasets from SQLite."""

from __future__ import annotations

import argparse
from bisect import bisect_right
from contextlib import closing, contextmanager
import csv
from datetime import date
import json
import os
import sqlite3
import sys
import tempfile
from pathlib import Path
from typing import Any, Iterable


DATASETS = {
    "revisions": (
        "daily_archive_revision",
        """
        SELECT revision.*
        FROM export_revisions AS current
        JOIN daily_archive_revision AS revision ON revision.revision_id=current.revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY revision.trade_date,revision.revision_no
        """,
    ),
    "daily_close": (
        "rank_snapshot",
        """
        SELECT snapshot.*
        FROM rank_snapshot AS snapshot
        JOIN export_revisions AS current ON current.trade_date=snapshot.trade_date
        WHERE current.trade_date BETWEEN ? AND ? AND snapshot.snapshot_kind='daily_close'
        ORDER BY snapshot.trade_date,
          CASE snapshot.rank_type WHEN 'industry' THEN 1 WHEN 'concept' THEN 2 ELSE 3 END,
          snapshot.rank
        """,
    ),
    "daily_features": (
        "daily_feature",
        """
        SELECT feature.*
        FROM export_revisions AS current
        JOIN daily_feature AS feature ON feature.revision_id=current.revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY feature.trade_date,feature.rank_type,feature.code
        """,
    ),
    "future_labels": (
        "future_return_label",
        """
        SELECT label.*
        FROM export_revisions AS current
        JOIN future_return_label AS label ON label.signal_revision_id=current.revision_id
          AND label.signal_date=current.trade_date
        JOIN export_revisions AS target_current
          ON target_current.trade_date=label.target_date
         AND target_current.revision_id=label.target_revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY label.signal_date,label.horizon,label.rank_type,label.code
        """,
    ),
    "future_label_history": (
        "future_return_label",
        """
        SELECT label.*
        FROM export_revisions AS current
        JOIN future_return_label AS label ON label.signal_revision_id=current.revision_id
          AND label.signal_date=current.trade_date
        JOIN export_revisions AS target_current
          ON target_current.trade_date=label.target_date
         AND target_current.revision_id=label.target_revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY label.signal_date,label.horizon,label.rank_type,label.code,label.target_revision_id
        """,
    ),
    "board_money_5m": (
        "board_money_5m",
        """
        SELECT money.*
        FROM board_money_5m AS money
        JOIN export_revisions AS current ON current.trade_date=money.trade_date
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY money.trade_date,money.rank_type,money.code,money.snapshot_at
        """,
    ),
    "stock_research_5m": (
        "stock_research_5m",
        """
        SELECT research.*
        FROM stock_research_5m AS research
        JOIN export_revisions AS current ON current.trade_date=research.trade_date
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY research.trade_date,research.market,research.code,research.minute_index
        """,
    ),
}


def iso_date(value: str) -> str:
    try:
        parsed = date.fromisoformat(value)
    except ValueError as error:
        raise argparse.ArgumentTypeError("dates must use YYYY-MM-DD") from error
    if parsed.isoformat() != value:
        raise argparse.ArgumentTypeError("dates must use YYYY-MM-DD")
    return value


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--database", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--from-date", required=True, type=iso_date)
    parser.add_argument("--to-date", required=True, type=iso_date)
    parser.add_argument("--format", choices=("csv", "parquet", "both"), default="parquet")
    parser.add_argument("--batch-size", type=int, default=20_000)
    return parser.parse_args()


def open_read_only(path: Path) -> sqlite3.Connection:
    if not path.is_file():
        raise SystemExit(f"database not found: {path}")
    connection = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    return connection


@contextmanager
def frozen_database(path: Path):
    # A disk-backed online backup fixes one database version without holding
    # a source read transaction throughout CSV/Parquet generation.
    with tempfile.TemporaryDirectory(prefix="shadowflow-export-db-") as directory:
        with closing(sqlite3.connect(Path(directory) / "snapshot.db")) as snapshot:
            with closing(open_read_only(path)) as source:
                source.backup(snapshot, pages=1024, sleep=0.01)
            snapshot.row_factory = sqlite3.Row
            prepare_export_revisions(snapshot)
            snapshot.commit()
            snapshot.execute("PRAGMA query_only=ON")
            yield snapshot


def prepare_export_revisions(connection: sqlite3.Connection) -> None:
    connection.execute("""CREATE TEMP TABLE export_revisions (
        trade_date TEXT PRIMARY KEY, revision_id TEXT UNIQUE, revision_no INTEGER,
        content_sha256 TEXT, created_at TEXT)""")
    candidates = {}
    for row in connection.execute("""
        SELECT revision.revision_id,current.trade_date,revision.revision_no,
               revision.content_sha256,revision.created_at,revision.manifest_json,manifest.updated_at
        FROM daily_archive_current AS current
        JOIN daily_archive_revision AS revision
          ON revision.revision_id=current.revision_id AND revision.trade_date=current.trade_date
        JOIN daily_archive_manifest AS manifest ON manifest.trade_date=current.trade_date
        WHERE manifest.status='complete' ORDER BY current.trade_date
    """):
        saved = json.loads(row["manifest_json"])
        if (not isinstance(saved, dict) or saved.get("status") != "complete"
                or saved.get("trade_date") != row["trade_date"]
                or saved.get("updated_at") != row["updated_at"] or not row["content_sha256"]):
            continue
        candidates[row["revision_id"]] = tuple(row[column] for column in (
            "trade_date", "revision_id", "revision_no", "content_sha256", "created_at"))

    current = list(connection.execute(
        "SELECT trade_date,revision_id FROM daily_archive_current ORDER BY trade_date"))
    current_dates = [row["trade_date"] for row in current]
    # A later day's rolling features can depend on a currently unsealed
    # correction. Do not publish those stale derived rows as a complete day.
    for row in connection.execute("SELECT revision_id,trade_date,source_revisions_json FROM daily_feature_set"):
        candidate = candidates.get(row["revision_id"])
        if candidate is None or candidate[0] != row["trade_date"]:
            continue
        sources = json.loads(row["source_revisions_json"])
        if not isinstance(sources, list) or not sources:
            continue
        end = bisect_right(current_dates, row["trade_date"])
        # Match the full window, including newly backfilled dates. The window
        # size follows maxFeatureHistory in the backend analytics builder.
        expected = [ref["revision_id"] for ref in reversed(current[max(0, end - 60):end])]
        if [source.get("revision_id") if isinstance(source, dict) else None for source in sources] != expected:
            continue
        valid = True
        for source in sources:
            known = candidates.get(source.get("revision_id")) if isinstance(source, dict) else None
            if known is None or known[0] != source.get("trade_date") or known[3] != source.get("content_sha256"):
                valid = False
                break
        if valid:
            connection.execute("INSERT INTO export_revisions VALUES (?,?,?,?,?)", candidate)


def table_schema(connection: sqlite3.Connection, table: str):
    try:
        import pyarrow as pa
    except ImportError as error:
        raise SystemExit("Parquet export requires pyarrow: python3 -m pip install pyarrow") from error
    fields = []
    for row in connection.execute(f"PRAGMA table_info({table})"):
        declared = str(row["type"]).upper()
        if "INT" in declared:
            arrow_type = pa.int64()
        elif "REAL" in declared or "FLOA" in declared or "DOUB" in declared:
            arrow_type = pa.float64()
        elif "BLOB" in declared:
            arrow_type = pa.binary()
        else:
            arrow_type = pa.string()
        fields.append(pa.field(row["name"], arrow_type, nullable=not bool(row["notnull"])))
    return pa.schema(fields)


def batches(cursor: sqlite3.Cursor, batch_size: int) -> Iterable[list[sqlite3.Row]]:
    while True:
        batch = cursor.fetchmany(batch_size)
        if not batch:
            return
        yield batch


def export_csv(
    connection: sqlite3.Connection,
    query: str,
    parameters: tuple[str, str],
    output: Path,
    batch_size: int,
) -> int:
    cursor = connection.execute(query, parameters)
    columns = [item[0] for item in cursor.description]
    count = 0
    with output.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.writer(handle)
        writer.writerow(columns)
        for batch in batches(cursor, batch_size):
            writer.writerows(tuple(row[column] for column in columns) for row in batch)
            count += len(batch)
    return count


def export_parquet(
    connection: sqlite3.Connection,
    table: str,
    query: str,
    parameters: tuple[str, str],
    output: Path,
    batch_size: int,
) -> int:
    try:
        import pyarrow as pa
        import pyarrow.parquet as pq
    except ImportError as error:
        raise SystemExit("Parquet export requires pyarrow: python3 -m pip install pyarrow") from error
    schema = table_schema(connection, table)
    cursor = connection.execute(query, parameters)
    count = 0
    writer = pq.ParquetWriter(output, schema=schema, compression="zstd")
    try:
        for batch in batches(cursor, batch_size):
            rows = [{column: row[column] for column in schema.names} for row in batch]
            writer.write_table(pa.Table.from_pylist(rows, schema=schema))
            count += len(batch)
    finally:
        writer.close()
    return count


def selected_revisions(connection: sqlite3.Connection, start: str, end: str) -> list[dict[str, Any]]:
    rows = connection.execute(
        """
        SELECT revision.revision_id,revision.trade_date,revision.revision_no,
               revision.content_sha256,revision.created_at
        FROM export_revisions AS current
        JOIN daily_archive_revision AS revision ON revision.revision_id=current.revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY current.trade_date
        """,
        (start, end),
    )
    return [dict(row) for row in rows]


def export_bundle(args: argparse.Namespace) -> None:
    if args.from_date > args.to_date:
        raise SystemExit("--from-date must not be after --to-date")
    if args.batch_size < 1:
        raise SystemExit("--batch-size must be positive")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    if args.output.exists() and (not args.output.is_dir() or any(args.output.iterdir())):
        raise SystemExit("output must be a new or empty directory; existing exports are not overwritten")
    with frozen_database(args.database) as connection, tempfile.TemporaryDirectory(
        prefix=".shadowflow-export-", dir=args.output.parent
    ) as directory:
        staging = Path(directory)
        revisions = selected_revisions(connection, args.from_date, args.to_date)
        if not revisions:
            raise SystemExit("no current complete archive revisions in the requested range")
        manifest: dict[str, Any] = {
            "database": str(args.database.resolve()),
            "from_date": args.from_date,
            "to_date": args.to_date,
            "revisions": revisions,
            "datasets": {},
            "snapshot": "sqlite-online-backup",
            "scope": "current-complete-sealed-archives",
            "future_label_history_scope": "alias-of-current-future-labels",
        }
        manifest["label_target_revisions"] = [dict(row) for row in connection.execute("""
            SELECT DISTINCT target.* FROM export_revisions AS signal
            JOIN future_return_label AS label ON label.signal_revision_id=signal.revision_id
              AND label.signal_date=signal.trade_date
            JOIN export_revisions AS target ON target.revision_id=label.target_revision_id
              AND target.trade_date=label.target_date
            WHERE signal.trade_date BETWEEN ? AND ? ORDER BY target.trade_date
        """, (args.from_date, args.to_date))]
        selected_dates = {item["trade_date"] for item in revisions}
        present_dates = {row[0] for row in connection.execute("""
            SELECT trade_date FROM rank_snapshot WHERE snapshot_kind='daily_close' AND trade_date BETWEEN ? AND ?
            UNION SELECT trade_date FROM daily_archive_current WHERE trade_date BETWEEN ? AND ?
        """, (args.from_date, args.to_date, args.from_date, args.to_date))}
        manifest["excluded_dates"] = sorted(present_dates - selected_dates)
        parameters = (args.from_date, args.to_date)
        for name, (table, query) in DATASETS.items():
            counts = []
            if args.format in ("csv", "both"):
                path = staging / f"{name}.csv"
                counts.append(export_csv(connection, query, parameters, path, args.batch_size))
            if args.format in ("parquet", "both"):
                path = staging / f"{name}.parquet"
                counts.append(export_parquet(connection, table, query, parameters, path, args.batch_size))
            if len(set(counts)) > 1:
                raise RuntimeError(f"CSV/Parquet row counts differ for {name}")
            manifest["datasets"][name] = {"rows": counts[0]}
        (staging / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        # Publish the package only after every dataset succeeds. Replacing a
        # non-empty directory fails, including a competing export's output.
        os.replace(staging, args.output)


def main() -> int:
    export_bundle(parse_args())
    return 0


if __name__ == "__main__":
    sys.exit(main())
