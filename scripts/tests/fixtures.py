import json
from pathlib import Path
import sqlite3

ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = ROOT / "scripts"
STAMP = "2026-09-07T01:02:03Z"

# Minimal query-contract fixtures; the Go integration test also exercises the
# exporter against a database sealed by the real repository implementation.
SCHEMA = """
CREATE TABLE daily_archive_manifest (trade_date TEXT PRIMARY KEY, status TEXT, updated_at TEXT);
CREATE TABLE daily_archive_revision (revision_id TEXT PRIMARY KEY, trade_date TEXT,
  revision_no INTEGER, content_sha256 TEXT, created_at TEXT, manifest_json TEXT);
CREATE TABLE daily_archive_current (trade_date TEXT PRIMARY KEY, revision_id TEXT);
CREATE TABLE daily_feature_set (revision_id TEXT, trade_date TEXT, source_revisions_json TEXT);
CREATE TABLE daily_feature (revision_id TEXT, trade_date TEXT, rank_type TEXT, code TEXT, value REAL);
CREATE TABLE rank_snapshot (trade_date TEXT, snapshot_kind TEXT, rank_type TEXT, rank INTEGER, dark_money INTEGER);
CREATE TABLE board_money_5m (trade_date TEXT, rank_type TEXT, code TEXT, snapshot_at TEXT, dark_money INTEGER);
CREATE TABLE stock_research_5m (trade_date TEXT, market INTEGER, code TEXT, minute_index INTEGER, dark_money INTEGER);
CREATE TABLE future_return_label (signal_revision_id TEXT, target_revision_id TEXT, signal_date TEXT,
  target_date TEXT, horizon INTEGER, rank_type TEXT, code TEXT, return_rate REAL);
CREATE TABLE stock_kline_source (code TEXT);
CREATE TABLE scheduled_job (job_key TEXT);
CREATE TABLE schema_migration (version INTEGER);
INSERT INTO schema_migration VALUES (1);
"""


def create_database(path):
    with sqlite3.connect(path) as db:
        db.executescript(SCHEMA)


def add_day(db, day, sealed=True, status="complete", value=100):
    db.execute("INSERT INTO daily_archive_manifest VALUES (?,?,?)", (day, status, STAMP))
    db.execute("INSERT INTO rank_snapshot VALUES (?,'daily_close','stock',1,?)", (day, value))
    db.execute("INSERT INTO board_money_5m VALUES (?,'industry','BK1',?,?)", (day, STAMP, value))
    db.execute("INSERT INTO stock_research_5m VALUES (?,0,'000001',1,?)", (day, value))
    if not sealed:
        return
    revision = "rev-" + day
    manifest = json.dumps({"trade_date": day, "status": "complete", "updated_at": STAMP})
    db.execute("INSERT INTO daily_archive_revision VALUES (?,?,1,?,?,?)",
               (revision, day, "hash-" + day, STAMP, manifest))
    db.execute("INSERT INTO daily_archive_current VALUES (?,?)", (day, revision))
    sources = [{"revision_id": row[0], "trade_date": row[1], "content_sha256": row[2]}
               for row in db.execute("""SELECT revision_id,trade_date,content_sha256
                 FROM daily_archive_revision WHERE trade_date<=? ORDER BY trade_date DESC LIMIT 60""", (day,))]
    db.execute("INSERT INTO daily_feature_set VALUES (?,?,?)", (revision, day, json.dumps(sources)))
    db.execute("INSERT INTO daily_feature VALUES (?,?,'stock','000001',?)", (revision, day, value / 10))


def add_label(db, signal, target, target_revision=None):
    db.execute("INSERT INTO future_return_label VALUES (?,?,?,?,1,'stock','000001',0.1)",
               ("rev-" + signal, target_revision or "rev-" + target, signal, target))
