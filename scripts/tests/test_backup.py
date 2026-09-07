from contextlib import closing
import gzip
import hashlib
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import unittest

from fixtures import SCRIPTS, add_day, create_database


class BackupTests(unittest.TestCase):
    shell = "/bin/sh"

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="shadowflow-backup-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.database = self.root / "source with 'quote.db"
        self.backups = self.root / "backups with spaces"
        self.backups.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        create_database(self.database)
        with closing(sqlite3.connect(self.database)) as db, db:
            add_day(db, "2026-09-01")
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("SHADOWFLOW_")}
        self.env.update(SHADOWFLOW_DATABASE_PATH=str(self.database), SHADOWFLOW_BACKUP_DIR=str(self.backups),
                        SHADOWFLOW_BACKUP_RETENTION_DAYS="3", PATH=str(self.bin) + os.pathsep + os.environ["PATH"])
        # Only disposable databases are restored; an unrelated host process
        # must not turn these regression tests into an environment-dependent skip.
        self.shim("pgrep", "exit 1\n")

    def shim(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\nset -eu\n" + body)
        path.chmod(0o700)

    def run_script(self, name, *args, success=True):
        result = subprocess.run([self.shell, str(SCRIPTS / name), *map(str, args)], env=self.env,
                                text=True, capture_output=True, timeout=60)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def backup(self):
        return Path(self.run_script("backup.sh").stdout.strip())

    def seed(self, stamp, valid=True, metadata=True, checksum=True, name=None):
        archive = self.backups / (name or ("shadowflow-" + stamp + ".db.gz"))
        archive.write_bytes(gzip.compress(self.database.read_bytes()) if valid else b"partial gzip")
        if checksum:
            Path(str(archive) + ".sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
        if metadata:
            archive.with_name(archive.name.removesuffix(".db.gz") + ".meta").write_text("created_at=test\n")
        return archive

    def assert_no_staging(self):
        self.assertEqual(list(self.backups.glob(".shadowflow-backup.*")), [])
        self.assertEqual(list(self.root.glob(".shadowflow-restore.*")), [])

    def test_backup_dry_run_and_restore_apply(self):
        archive = self.backup()
        sidecar = Path(str(archive) + ".sha256").read_text()
        self.assertTrue(sidecar.endswith("  " + archive.name + "\n"))
        self.assertNotIn(str(self.backups), sidecar)
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute("UPDATE rank_snapshot SET dark_money=999")
        self.run_script("restore.sh", archive, "--dry-run")
        with closing(sqlite3.connect(self.database)) as db:
            self.assertEqual(db.execute("SELECT dark_money FROM rank_snapshot").fetchone()[0], 999)
        self.run_script("restore.sh", archive)
        with closing(sqlite3.connect(self.database)) as db:
            self.assertEqual(db.execute("SELECT dark_money FROM rank_snapshot").fetchone()[0], 100)
        self.assertEqual(len(list(self.root.glob("*.pre-restore-*"))), 1)
        self.assertEqual(archive.stat().st_mode & 0o077, 0)
        self.assert_no_staging()

    def test_moved_legacy_sidecar_checks_actual_archive(self):
        original = self.seed("20000101-000000")
        moved = self.root / "moved.db.gz"
        shutil.copyfile(original, moved)
        digest = hashlib.sha256(original.read_bytes()).hexdigest()
        Path(str(moved) + ".sha256").write_text(digest + "  " + str(original) + "\n")
        original.unlink()
        self.run_script("restore.sh", moved, "--dry-run")
        shutil.copyfile(moved, original)
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute("UPDATE rank_snapshot SET dark_money=777")
        moved.write_bytes(gzip.compress(self.database.read_bytes()))
        result = self.run_script("restore.sh", moved, "--dry-run", success=False)
        self.assertIn("checksum mismatch", result.stderr)
        self.assert_no_staging()

    def test_malformed_or_missing_sidecars_fail(self):
        archive = self.seed("20000101-000000")
        sidecar = Path(str(archive) + ".sha256")
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        for body in ("", "broken\n", "f" * 63 + "  old\n", digest + "  old\n" + digest + "  other\n"):
            with self.subTest(body=body):
                sidecar.write_text(body)
                self.run_script("restore.sh", archive, "--dry-run", success=False)
        sidecar.unlink()
        self.run_script("restore.sh", archive, "--dry-run", success=False)
        sidecar.write_text("\\" + digest.upper() + "  escaped\\name\n")
        self.run_script("restore.sh", archive, "--dry-run")
        self.assert_no_staging()

    def test_restore_uses_verified_copy_if_input_changes_after_checksum(self):
        archive = self.seed("20000101-000000")
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute("UPDATE rank_snapshot SET dark_money=999")
        replacement = self.root / "replacement.db.gz"
        replacement.write_bytes(gzip.compress(self.database.read_bytes()))
        self.env.update(REAL_GZIP=shutil.which("gzip"), INPUT_ARCHIVE=str(archive), REPLACEMENT=str(replacement))
        self.shim("gzip", 'if [ "$1" = -t ]; then cp "$REPLACEMENT" "$INPUT_ARCHIVE"; fi\nexec "$REAL_GZIP" "$@"\n')
        self.run_script("restore.sh", archive)
        with closing(sqlite3.connect(self.database)) as db:
            self.assertEqual(db.execute("SELECT dark_money FROM rank_snapshot").fetchone()[0], 100)
        self.assertEqual(archive.read_bytes(), replacement.read_bytes())
        self.assert_no_staging()

    def test_retention_ignores_partial_corrupt_missing_and_manual_backups(self):
        old = [self.seed("2000010%d-000000" % index) for index in range(1, 4)]
        ignored = [self.seed("20990101-000000", valid=False),
                   self.seed("20990102-000000", metadata=False),
                   self.seed("20990103-000000", checksum=False),
                   self.seed("20990104-000000"), self.seed("", name="manual.db.gz")]
        Path(str(ignored[3]) + ".sha256").write_text("0" * 64 + "  bad\n")
        new = self.backup()
        self.assertFalse(old[0].exists())
        self.assertFalse(Path(str(old[0]) + ".sha256").exists())
        for archive in old[1:] + [new]:
            self.run_script("restore.sh", archive, "--dry-run")
        for archive in ignored:
            self.assertTrue(archive.exists())
        self.assert_no_staging()

    def test_compression_and_publication_failure_preserve_good_backups(self):
        old = [self.seed("2000010%d-000000" % index) for index in range(1, 4)]
        real_gzip = shutil.which("gzip")
        self.env["REAL_GZIP"] = real_gzip
        self.shim("gzip", 'if [ "$1" = -c ]; then printf partial; exit 9; fi\nexec "$REAL_GZIP" "$@"\n')
        self.run_script("backup.sh", success=False)
        (self.bin / "gzip").unlink()
        self.env["REAL_LN"] = shutil.which("ln")
        for suffix in ("meta", "sha256"):
            self.env["FAIL_SUFFIX"] = suffix
            self.shim("ln", 'case "$2" in (*."$FAIL_SUFFIX") exit 9 ;; esac\nexec "$REAL_LN" "$@"\n')
            self.run_script("backup.sh", success=False)
        self.assertEqual(set(self.backups.glob("*.db.gz")), set(old))
        self.assert_no_staging()

    def test_same_second_collision_does_not_overwrite(self):
        existing = self.seed("20990101-000000")
        before = existing.read_bytes()
        self.env["TEST_COUNTER"] = str(self.root / "counter")
        self.env["REAL_DATE"] = shutil.which("date")
        self.shim("date", 'if [ "$1" = "+%Y%m%d-%H%M%S" ]; then\n'
                  '  if mkdir "$TEST_COUNTER" 2>/dev/null; then printf "20990101-000000\\n"; '
                  'else printf "20990101-000001\\n"; fi\nelse exec "$REAL_DATE" "$@"; fi\n')
        new = self.backup()
        self.assertEqual(new.name, "shadowflow-20990101-000001.db.gz")
        self.assertEqual(existing.read_bytes(), before)
        self.assert_no_staging()

    def test_concurrent_backups_claim_unique_names_and_retain_valid_count(self):
        self.env["SHADOWFLOW_BACKUP_RETENTION_DAYS"] = "5"
        processes = [subprocess.Popen([self.shell, str(SCRIPTS / "backup.sh")], env=self.env,
                                     text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE) for _ in range(3)]
        try:
            outputs = []
            for process in processes:
                stdout, stderr = process.communicate(timeout=60)
                self.assertEqual(process.returncode, 0, stderr)
                outputs.append(stdout.strip())
            self.assertEqual(len(set(outputs)), 3)
            for archive in outputs:
                self.run_script("restore.sh", archive, "--dry-run")
        finally:
            for process in processes:
                if process.poll() is None:
                    process.kill()
                process.wait()
        self.env["SHADOWFLOW_BACKUP_RETENTION_DAYS"] = "2"
        processes = [subprocess.Popen([self.shell, str(SCRIPTS / "backup.sh")], env=self.env,
                                     text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE) for _ in range(3)]
        try:
            for process in processes:
                stdout, stderr = process.communicate(timeout=60)
                self.assertEqual(process.returncode, 0, stderr)
        finally:
            for process in processes:
                if process.poll() is None:
                    process.kill()
                process.wait()
        remaining = list(self.backups.glob("*.db.gz"))
        self.assertEqual(len(remaining), 2)
        for archive in remaining:
            self.run_script("restore.sh", archive, "--dry-run")
        self.assert_no_staging()

    def test_invalid_retention_and_restore_mode_are_rejected(self):
        for value in ("0", "-1", "abc", "1.5"):
            with self.subTest(value=value):
                self.env["SHADOWFLOW_BACKUP_RETENTION_DAYS"] = value
                self.run_script("backup.sh", success=False)
        archive = self.seed("20000101-000000")
        self.run_script("restore.sh", archive, "--force", success=False)
        self.assert_no_staging()


@unittest.skipUnless(shutil.which("dash"), "dash is not available")
class DashBackupTests(BackupTests):
    shell = shutil.which("dash")


if __name__ == "__main__":
    unittest.main()
