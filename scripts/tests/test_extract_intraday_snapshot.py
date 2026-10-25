from contextlib import closing
import importlib.util
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "extract_intraday_snapshot.py"
spec = importlib.util.spec_from_file_location("extract_intraday_snapshot", SCRIPT)
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)

SCHEMA = """
CREATE TABLE rank_intraday_work (
 trade_date TEXT, snapshot_at TEXT, rank_type TEXT, market INTEGER, code TEXT,
 money_available INTEGER, run_id TEXT,
 PRIMARY KEY(trade_date,snapshot_at,rank_type,code));
CREATE TABLE collection_run (
 run_id TEXT PRIMARY KEY, status TEXT, requested_date TEXT, actual_trade_date TEXT,
 rank_type TEXT, snapshot_kind TEXT, snapshot_at TEXT,
 expected_total INTEGER, fetched_total INTEGER);
"""


class SingleSnapshotTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.database = Path(temporary.name) / "source.db"
        with closing(sqlite3.connect(self.database)) as db:
            db.executescript(SCHEMA)
            db.execute("INSERT INTO collection_run VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                       ("run", "success", "2026-10-22", "2026-10-22", "concept",
                        "minute_work", "2026-10-22T07:00:00Z", 2, 2))
            for code in ("BK001", "BK002"):
                db.execute("INSERT INTO rank_intraday_work VALUES (?, ?, ?, ?, ?, ?, ?)",
                           ("2026-10-22", "2026-10-22T07:00:00Z", "concept", 90, code, 1, "run"))
            db.commit()

    def extract(self):
        return worker.extract(self.database, "2026-10-22", "concept", "15:00")

    def change(self, sql):
        with closing(sqlite3.connect(self.database)) as db:
            db.execute(sql)
            db.commit()

    def test_read_only_and_verified_counts(self):
        before = self.database.read_bytes()
        result = self.extract()
        self.assertTrue(result["verified"])
        self.assertEqual(len(result["rows"]), 2)
        self.assertEqual(before, self.database.read_bytes())
        self.assertEqual(result["limits"]["memory_bytes"], 128 << 20)

    def test_invalid_scope_refused(self):
        for kind, clock in (("stock", "15:00"), ("concept", "all"), ("concept", "14:56")):
            with self.subTest(kind=kind, clock=clock), self.assertRaises(ValueError):
                worker.extract(self.database, "2026-10-22", kind, clock)

    def test_row_limit_is_hard_not_truncation(self):
        with closing(sqlite3.connect(self.database)) as db:
            db.executemany("INSERT INTO rank_intraday_work VALUES (?, ?, ?, ?, ?, ?, ?)",
                           [("2026-10-22", "2026-10-22T07:00:00Z", "concept", 90,
                             f"BK{i:04d}", 1, "run") for i in range(3, 503)])
            db.commit()
        with self.assertRaisesRegex(ValueError, "hard row limit"):
            self.extract()

    def test_missing_or_inconsistent_metadata_unverified(self):
        self.change("UPDATE collection_run SET expected_total=3")
        self.assertFalse(self.extract()["verified"])
        self.change("DELETE FROM collection_run")
        self.assertFalse(self.extract()["verified"])

    def test_mixed_runs_rejected(self):
        self.change("UPDATE rank_intraday_work SET run_id='other' WHERE code='BK002'")
        with self.assertRaisesRegex(ValueError, "mixed"):
            self.extract()

    def test_query_uses_exact_snapshot_index(self):
        with closing(sqlite3.connect(self.database)) as db:
            plan = db.execute("""EXPLAIN QUERY PLAN SELECT * FROM rank_intraday_work
                WHERE trade_date=? AND snapshot_at=? AND rank_type=? LIMIT ?""",
                ("2026-10-22", "2026-10-22T07:00:00Z", "concept", 501)).fetchall()
        self.assertTrue(any("SEARCH" in row[3] for row in plan))
        self.assertFalse(any("SCAN rank_intraday_work" in row[3] for row in plan))

    def test_unsupported_platform_fails_closed(self):
        with mock.patch.object(worker.sys, "platform", "darwin"):
            with self.assertRaisesRegex(RuntimeError, "Linux resource limits"):
                worker.apply_limits()

    def test_linux_limits_and_alarm_are_installed(self):
        resource = mock.Mock(RLIMIT_AS=1, RLIMIT_CPU=2)
        with mock.patch.object(worker.sys, "platform", "linux"), \
             mock.patch.dict(sys.modules, {"resource": resource}), \
             mock.patch("os.nice") as nice, mock.patch("signal.signal") as handler, \
             mock.patch("signal.alarm") as alarm:
            worker.apply_limits()
        resource.setrlimit.assert_any_call(1, (128 << 20, 128 << 20))
        resource.setrlimit.assert_any_call(2, (5, 5))
        nice.assert_called_once_with(19)
        handler.assert_called_once()
        alarm.assert_called_once_with(10)


if __name__ == "__main__":
    unittest.main()
