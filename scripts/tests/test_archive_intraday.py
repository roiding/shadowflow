from contextlib import closing
from datetime import date
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

from fixtures import SCRIPTS

sys.path.insert(0, str(SCRIPTS))
import archive_intraday as archive


SCHEMA = """
CREATE TABLE rank_intraday_work (run_id TEXT NOT NULL, trade_date TEXT NOT NULL,
  snapshot_at TEXT NOT NULL,rank_type TEXT NOT NULL CHECK(rank_type IN ('industry','concept')),
  code TEXT NOT NULL,dark_money INTEGER NOT NULL,money_available INTEGER NOT NULL,
  PRIMARY KEY(trade_date,snapshot_at,rank_type,code));
CREATE INDEX idx_intraday_series ON rank_intraday_work(trade_date,rank_type,code,snapshot_at);
CREATE TABLE raw_response (run_id TEXT NOT NULL,snapshot_at TEXT NOT NULL,snapshot_kind TEXT NOT NULL,
  rank_type TEXT NOT NULL,page INTEGER NOT NULL,body BLOB NOT NULL,
  PRIMARY KEY(snapshot_kind,snapshot_at,rank_type,page));
CREATE TABLE collection_run (run_id TEXT PRIMARY KEY,status TEXT NOT NULL);
"""


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="shadowflow-archive-test-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.main, self.cold = self.root / "main.db", self.root / "cold.db"
        with closing(sqlite3.connect(self.main)) as db, db:
            db.executescript(SCHEMA)
            db.execute("INSERT INTO collection_run VALUES ('one','success')")
            db.execute("INSERT INTO rank_intraday_work VALUES ('one','2026-10-09','2026-10-09T01:36:00Z','industry','BK0448',123,1)")
            db.execute("INSERT INTO raw_response VALUES ('one','2026-10-09T01:36:00Z','research_5m','industry',1,?)", (b"raw\x00response",))
        self.calendar = self.root / "calendar.json"
        self.calendar.write_text(json.dumps({"valid_through": "2026-12-31", "holidays": [
            "2026-10-01", "2026-10-02", "2026-10-05", "2026-10-06", "2026-10-07"], "workdays": []}))

    def make_current(self):
        archive.upgrade(self.main, self.cold)

    def test_rolling_window_uses_completed_exchange_days(self):
        self.assertEqual(archive.hot_floor(date(2026, 10, 26), 10, self.calendar), "2026-10-12")
        self.assertEqual(archive.hot_floor(date(2026, 10, 27), 10, self.calendar), "2026-10-13")
        self.assertEqual(archive.hot_floor(date(2026, 10, 12), 2, self.calendar), "2026-10-08")
        with self.assertRaises(ValueError):
            archive.hot_floor(date(2027, 1, 4), 10, self.calendar)

    def test_rolling_window_crosses_year_with_preserved_annual_calendars(self):
        history = self.root / "history.json"
        archive.hot_floor(date(2026, 10, 26), 10, self.calendar, history)
        self.calendar.write_text(json.dumps({"valid_through": "2027-12-31", "holidays": ["2027-01-01"], "workdays": []}))
        self.assertEqual(archive.hot_floor(date(2027, 1, 4), 2, self.calendar, history), "2026-12-30")
        with self.assertRaisesRegex(ValueError, "history"):
            archive.hot_floor(date(2027, 1, 4), 2, self.calendar)

    def test_copy_verify_delete_and_provenance_round_trip(self):
        self.make_current()
        result = archive.archive(self.main, self.cold, "2026-10-12", apply=True)
        self.assertEqual(result["days"][0]["work_rows"], 1)
        with closing(sqlite3.connect(self.main)) as main, closing(sqlite3.connect(self.cold)) as cold:
            self.assertEqual(main.execute("SELECT count(*) FROM rank_intraday_work").fetchone()[0], 0)
            self.assertEqual(main.execute("SELECT count(*) FROM raw_response").fetchone()[0], 0)
            self.assertEqual(cold.execute("SELECT dark_money,money_available FROM rank_intraday_work").fetchone(), (123, 1))
            self.assertEqual(cold.execute("SELECT body FROM raw_response").fetchone()[0], b"raw\x00response")
            self.assertEqual(cold.execute("SELECT status FROM collection_run").fetchone()[0], "success")
            self.assertEqual(cold.execute("SELECT status FROM cold_archive_day").fetchone()[0], "pruned")
        self.assertEqual(archive.archive(self.main, self.cold, "2026-10-12", apply=True)["days"], [])

    def test_crash_after_durable_copy_preserves_hot_then_resumes(self):
        self.make_current()
        with closing(sqlite3.connect(self.main)) as main, closing(sqlite3.connect(self.cold)) as cold:
            def crash():
                raise RuntimeError("simulated crash after cold commit")
            with self.assertRaises(RuntimeError):
                archive.archive_day(main, cold, "2026-10-09", after_copy=crash)
            self.assertEqual(main.execute("SELECT count(*) FROM rank_intraday_work").fetchone()[0], 1)
            self.assertEqual(cold.execute("SELECT count(*) FROM rank_intraday_work").fetchone()[0], 1)
            self.assertEqual(cold.execute("SELECT status FROM cold_archive_day").fetchone()[0], "copied")
        archive.archive(self.main, self.cold, "2026-10-12", apply=True)
        with closing(sqlite3.connect(self.cold)) as cold:
            self.assertEqual(cold.execute("SELECT count(*) FROM rank_intraday_work").fetchone()[0], 1)

    def test_hot_correction_updates_cold_without_losing_other_minutes(self):
        self.make_current()
        archive.archive(self.main, self.cold, "2026-10-12", apply=True)
        with closing(sqlite3.connect(self.main)) as db, db:
            db.execute("INSERT INTO rank_intraday_work VALUES ('one','2026-10-09','2026-10-09T01:36:00Z','industry','BK0448',999,1)")
            db.execute("INSERT INTO rank_intraday_work VALUES ('one','2026-10-09','2026-10-09T01:37:00Z','industry','BK0448',456,1)")
        archive.archive(self.main, self.cold, "2026-10-12", apply=True)
        with closing(sqlite3.connect(self.cold)) as db:
            self.assertEqual(db.execute("SELECT dark_money FROM rank_intraday_work ORDER BY snapshot_at").fetchall(), [(999,), (456,)])

    def test_upgrade_preserves_content_keys_constraints_and_old_copy(self):
        with closing(sqlite3.connect(self.main)) as main, closing(sqlite3.connect(self.cold)) as cold, cold:
            cold.execute("ATTACH DATABASE ? AS source", (str(self.main),))
            for table in ("rank_intraday_work", "raw_response"):
                cold.execute(f"CREATE TABLE {table} AS SELECT * FROM source.{table}")
        result = archive.upgrade(self.main, self.cold)
        self.assertEqual(result["content"]["rank_intraday_work"]["rows"], 1)
        self.assertTrue(Path(result["preserved_prior"]).is_file())
        with closing(sqlite3.connect(self.cold)) as db:
            primary = [r for r in db.execute("PRAGMA table_info(rank_intraday_work)") if r[5]]
            self.assertEqual(len(primary), 4)
            with self.assertRaises(sqlite3.IntegrityError):
                db.execute("INSERT INTO rank_intraday_work SELECT * FROM rank_intraday_work")
        self.assertEqual(archive.upgrade(self.main, self.cold)["status"], "already_current")

    def test_invalid_legacy_duplicate_aborts_without_replacing_original(self):
        with closing(sqlite3.connect(self.cold)) as cold, cold:
            cold.execute("ATTACH DATABASE ? AS source", (str(self.main),))
            for table in ("rank_intraday_work", "raw_response"):
                cold.execute(f"CREATE TABLE {table} AS SELECT * FROM source.{table}")
            cold.execute("INSERT INTO rank_intraday_work SELECT * FROM rank_intraday_work")
        before = self.cold.read_bytes()
        with self.assertRaises(sqlite3.IntegrityError):
            archive.upgrade(self.main, self.cold)
        self.assertEqual(self.cold.read_bytes(), before)

    def test_dry_run_and_missing_file_do_not_move_or_create_data(self):
        self.make_current()
        result = archive.archive(self.main, self.cold, "2026-10-12")
        self.assertEqual(result["eligible_dates"], ["2026-10-09"])
        with closing(sqlite3.connect(self.main)) as db:
            self.assertEqual(db.execute("SELECT count(*) FROM rank_intraday_work").fetchone()[0], 1)
        missing = self.root / "missing.db"
        with self.assertRaises(ValueError):
            archive.archive(self.main, missing, "2026-10-12", apply=True)
        self.assertFalse(missing.exists())


if __name__ == "__main__":
    unittest.main()
