#!/usr/bin/env python3
"""Export versioned ShadowFlow research datasets from SQLite."""

from __future__ import annotations

import argparse
import csv
import json
import sqlite3
import sys
from pathlib import Path
from typing import Any, Iterable


DATASETS = {
    "revisions": (
        "daily_archive_revision",
        """
        SELECT revision.*
        FROM daily_archive_revision AS revision
        WHERE revision.trade_date BETWEEN ? AND ?
        ORDER BY revision.trade_date,revision.revision_no
        """,
    ),
    "daily_close": (
        "rank_snapshot_revision",
        """
        SELECT snapshot.*
        FROM daily_archive_current AS current
        JOIN rank_snapshot_revision AS snapshot ON snapshot.revision_id=current.revision_id
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
        FROM daily_archive_current AS current
        JOIN daily_feature AS feature ON feature.revision_id=current.revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY feature.trade_date,feature.rank_type,feature.code
        """,
    ),
    "future_labels": (
        "future_return_label",
        """
        SELECT label.*
        FROM daily_archive_current AS current
        JOIN future_return_label AS label ON label.signal_revision_id=current.revision_id
        JOIN daily_archive_current AS target_current
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
        FROM future_return_label AS label
        WHERE label.signal_date BETWEEN ? AND ?
        ORDER BY label.signal_date,label.horizon,label.rank_type,label.code,label.target_revision_id
        """,
    ),
    "board_money_5m": (
        "board_money_revision",
        """
        SELECT money.*
        FROM daily_archive_current AS current
        JOIN board_money_revision AS money ON money.revision_id=current.revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY money.trade_date,money.rank_type,money.code,money.snapshot_at
        """,
    ),
    "stock_research_5m": (
        "stock_research_revision",
        """
        SELECT research.*
        FROM daily_archive_current AS current
        JOIN stock_research_revision AS research ON research.revision_id=current.revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY research.trade_date,research.market,research.code,research.minute_index
        """,
    ),
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--database", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--from-date", required=True)
    parser.add_argument("--to-date", required=True)
    parser.add_argument("--format", choices=("csv", "parquet", "both"), default="parquet")
    parser.add_argument("--batch-size", type=int, default=20_000)
    return parser.parse_args()


def open_read_only(path: Path) -> sqlite3.Connection:
    if not path.is_file():
        raise SystemExit(f"database not found: {path}")
    connection = sqlite3.connect(f"file:{path.resolve()}?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    return connection


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
        FROM daily_archive_current AS current
        JOIN daily_archive_revision AS revision ON revision.revision_id=current.revision_id
        WHERE current.trade_date BETWEEN ? AND ?
        ORDER BY current.trade_date
        """,
        (start, end),
    )
    return [dict(row) for row in rows]


def main() -> int:
    args = parse_args()
    if args.from_date > args.to_date:
        raise SystemExit("--from-date must not be after --to-date")
    if args.batch_size < 1:
        raise SystemExit("--batch-size must be positive")
    args.output.mkdir(parents=True, exist_ok=True)
    connection = open_read_only(args.database)
    try:
        revisions = selected_revisions(connection, args.from_date, args.to_date)
        if not revisions:
            raise SystemExit("no current complete archive revisions in the requested range")
        manifest: dict[str, Any] = {
            "database": str(args.database.resolve()),
            "from_date": args.from_date,
            "to_date": args.to_date,
            "revisions": revisions,
            "datasets": {},
        }
        parameters = (args.from_date, args.to_date)
        for name, (table, query) in DATASETS.items():
            counts = []
            if args.format in ("csv", "both"):
                path = args.output / f"{name}.csv"
                counts.append(export_csv(connection, query, parameters, path, args.batch_size))
            if args.format in ("parquet", "both"):
                path = args.output / f"{name}.parquet"
                counts.append(export_parquet(connection, table, query, parameters, path, args.batch_size))
            manifest["datasets"][name] = {"rows": max(counts, default=0)}
        (args.output / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
    finally:
        connection.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
