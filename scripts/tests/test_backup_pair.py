from contextlib import closing
import gzip
import hashlib
import io
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import fcntl

from fixtures import SCRIPTS, add_day, create_database

sys.path.insert(0, str(SCRIPTS))
import backup_pair


class PairedBackupTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="shadowflow-pair-test-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.main = self.root / "main with 'quote.db"
        self.cold = self.root / "cold with spaces.db"
        self.backups = self.root / "backups"
        self.backups.mkdir()
        create_database(self.main)
        with closing(sqlite3.connect(self.main)) as db, db:
            add_day(db, "2026-10-23")
        with closing(sqlite3.connect(self.cold)) as db, db:
            db.executescript("CREATE TABLE rank_intraday_work (trade_date TEXT, dark_money INTEGER);"
                             "CREATE TABLE raw_response (body BLOB);"
                             "INSERT INTO rank_intraday_work VALUES ('2026-10-09',123);")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        pgrep = self.bin / "pgrep"
        pgrep.write_text("#!/bin/sh\nexit 1\n")
        pgrep.chmod(0o700)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("SHADOWFLOW_")}
        self.env.update(SHADOWFLOW_DATABASE_PATH=str(self.main), SHADOWFLOW_COLD_DATABASE_PATH=str(self.cold),
                        SHADOWFLOW_BACKUP_DIR=str(self.backups), SHADOWFLOW_BACKUP_RETENTION_DAYS="3",
                        PATH=str(self.bin) + os.pathsep + os.environ["PATH"])

    def run_script(self, name, *args, success=True):
        result = subprocess.run(["/bin/sh", str(SCRIPTS / name), *map(str, args)],
                                env=self.env, capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def backup(self):
        return Path(self.run_script("backup.sh").stdout.strip())

    def assert_values(self, main=100, cold=123):
        with closing(sqlite3.connect(self.main)) as db:
            self.assertEqual(db.execute("SELECT dark_money FROM rank_snapshot").fetchone()[0], main)
        with closing(sqlite3.connect(self.cold)) as db:
            self.assertEqual(db.execute("SELECT dark_money FROM rank_intraday_work").fetchone()[0], cold)

    def test_verified_pair_round_trip_and_safety_copy(self):
        archive = self.backup()
        self.assertTrue(archive.name.endswith(".bundle.tar"))
        self.assertEqual(archive.stat().st_mode & 0o077, 0)
        with closing(sqlite3.connect(self.main)) as db, db:
            db.execute("UPDATE rank_snapshot SET dark_money=999")
        with closing(sqlite3.connect(self.cold)) as db, db:
            db.execute("UPDATE rank_intraday_work SET dark_money=888")
        self.run_script("restore.sh", archive, "--dry-run")
        self.assert_values(999, 888)
        self.run_script("restore.sh", archive)
        self.assert_values()
        safety = list(self.root.glob("shadowflow-pre-restore-*"))
        self.assertEqual(len(safety), 1)
        self.assertTrue((safety[0] / self.main.name).exists())
        self.assertTrue((safety[0] / self.cold.name).exists())
        self.assertFalse(Path(str(self.main) + ".restore-in-progress").exists())

    def test_calendar_history_is_verified_and_restored_with_pair(self):
        history = self.root / ".shadowflow-calendar-history.json"
        history.write_text('{"2026":{"holidays":[],"workdays":[],"valid_through":"2026-12-31"}}')
        expected = history.read_bytes()
        archive = self.backup()
        history.write_text('{}')
        self.run_script("restore.sh", archive)
        self.assertEqual(history.read_bytes(), expected)

    def test_missing_cold_or_same_file_publishes_nothing(self):
        for path in (str(self.root / "missing.db"), str(self.main)):
            self.env["SHADOWFLOW_COLD_DATABASE_PATH"] = path
            self.run_script("backup.sh", success=False)
        self.assertEqual(list(self.backups.glob("*.bundle.tar")), [])
        self.assertFalse((self.root / "missing.db").exists())

    def test_bad_checksum_does_not_modify_either_database(self):
        archive = self.backup()
        Path(str(archive) + ".sha256").write_text("0" * 64 + "  wrong\n")
        self.run_script("restore.sh", archive, success=False)
        self.assert_values()

    def test_restore_detects_service_in_another_pid_namespace(self):
        archive = self.backup()
        with Path(str(self.main) + ".service.lock").open("a+b") as lease:
            fcntl.flock(lease, fcntl.LOCK_SH)
            self.run_script("restore.sh", archive, success=False)
            self.run_script("restore.sh", archive, "--dry-run")
        self.assert_values()

    def test_unsafe_tar_member_is_rejected_before_writes(self):
        archive = self.backup()
        with tarfile.open(archive, "a") as packed:
            member = tarfile.TarInfo("../../outside")
            member.size = 4
            packed.addfile(member, io.BytesIO(b"oops"))
        Path(str(archive) + ".sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  bad\n")
        self.run_script("restore.sh", archive, success=False)
        self.assert_values()

    def test_missing_partner_and_legacy_restore_fail_closed(self):
        legacy = self.backups / "shadowflow-20000101-000000.db.gz"
        legacy.write_bytes(gzip.compress(self.main.read_bytes()))
        Path(str(legacy) + ".sha256").write_text(hashlib.sha256(legacy.read_bytes()).hexdigest() + "  legacy\n")
        self.run_script("restore.sh", legacy, success=False)

    def test_mixed_retention_keeps_valid_pairs_and_ignores_partial(self):
        legacy = self.backups / "shadowflow-20000101-000000.db.gz"
        legacy.write_bytes(gzip.compress(self.main.read_bytes()))
        Path(str(legacy) + ".sha256").write_text(hashlib.sha256(legacy.read_bytes()).hexdigest() + "  legacy\n")
        legacy.with_name(legacy.name.removesuffix(".db.gz") + ".meta").write_text("created_at=test\n")
        partial = self.backups / "shadowflow-20990101-000000.bundle.tar"
        partial.write_bytes(b"partial")
        self.env["SHADOWFLOW_BACKUP_RETENTION_DAYS"] = "1"
        archive = self.backup()
        self.assertFalse(legacy.exists())
        self.assertTrue(partial.exists())
        self.run_script("restore.sh", archive, "--dry-run")

    def test_interrupted_pair_restore_leaves_startup_marker_and_safety(self):
        archive = self.backup()
        real_replace = os.replace

        def fail_second(source, target):
            if Path(target).resolve() == self.cold.resolve():
                raise OSError("simulated second rename failure")
            return real_replace(source, target)

        with patch.dict(os.environ, self.env), patch.object(backup_pair.os, "replace", side_effect=fail_second):
            with self.assertRaises(OSError):
                backup_pair.restore(archive, self.main, self.cold)
        self.assertTrue(Path(str(self.main) + ".restore-in-progress").exists())
        self.assertEqual(len(list(self.root.glob("shadowflow-pre-restore-*"))), 1)
        self.run_script("restore.sh", archive)
        self.assertFalse(Path(str(self.main) + ".restore-in-progress").exists())
        self.assert_values()


if __name__ == "__main__":
    unittest.main()
