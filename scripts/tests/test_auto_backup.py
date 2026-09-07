import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from fixtures import ROOT, SCRIPTS


class CalendarBackupTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="shadowflow-calendar-bin-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.collect = os.environ.get("SHADOWFLOW_TEST_COLLECT_BIN", str(Path(cls.temporary.name) / "collect"))
        if "SHADOWFLOW_TEST_COLLECT_BIN" not in os.environ:
            subprocess.run(["go", "build", "-o", cls.collect, "./cmd/collect"], cwd=ROOT / "backend",
                           check=True, timeout=180)

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="shadowflow-calendar-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.calendar = self.root / "calendar with spaces.json"
        self.marker = self.root / "backup-called"
        backup = self.root / "backup.sh"
        backup.write_text('#!/bin/sh\nprintf called > "$TEST_MARKER"\n')
        backup.chmod(0o700)
        date = self.root / "date"
        date.write_text('#!/bin/sh\nprintf "%s\\n" "$TEST_TODAY"\n')
        date.chmod(0o700)
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("SHADOWFLOW_")}
        self.env.update(SHADOWFLOW_CALENDAR_PATH=str(self.calendar), SHADOWFLOW_COLLECT_BIN=self.collect,
                        SHADOWFLOW_BACKUP_SCRIPT=str(backup), TEST_MARKER=str(self.marker), TEST_TODAY="2026-09-07",
                        SHADOWFLOW_DATABASE_PATH=str(self.root / "must-not-exist" / "source.db"),
                        SHADOWFLOW_SQLITE_READ_CONNS="invalid", PATH=str(self.root) + os.pathsep + os.environ["PATH"])

    def wrapper(self, body, expected, shell="/bin/sh"):
        if body is not None:
            self.calendar.write_text(body)
        self.marker.unlink(missing_ok=True)
        result = subprocess.run([shell, str(SCRIPTS / "auto-backup.sh")], env=self.env,
                                text=True, capture_output=True, timeout=20)
        if expected is None:
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse(self.marker.exists())
        else:
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(self.marker.exists(), expected, result.stdout)
        self.assertFalse((self.root / "must-not-exist").exists())

    def test_json_layout_key_order_and_escapes_are_equivalent(self):
        data = {"holidays": ["2026-09-07"], "workdays": []}
        variants = [json.dumps(data), json.dumps(data, indent=2),
                    json.dumps(dict(reversed(list(data.items())))),
                    r'{"holid\u0061ys":["2026-09-\u00307"],"workdays":[]}']
        for shell in dict.fromkeys(["/bin/sh", shutil.which("dash")]):
            if shell is None:
                continue
            for body in variants:
                with self.subTest(shell=shell, body=body):
                    self.wrapper(body, False, shell)

    def test_weekday_weekend_and_workday_override(self):
        self.wrapper('{"holidays":[],"workdays":[]}', True)
        self.env["TEST_TODAY"] = "2026-09-06"
        self.wrapper('{"holidays":[],"workdays":[]}', False)
        self.wrapper('{"holidays":[],"workdays":["2026-09-06"]}', True)

    def test_invalid_or_missing_calendar_fails_closed(self):
        self.wrapper(None, None)
        for body in ("{", "null", "[]", '{"holidays":"2026-09-07"}',
                     '{"holidays":[9]}', '{"workdays":["2026-02-30"]}',
                     '{"holidays":["2026-09-07"],"workdays":["2026-09-07"]}'):
            with self.subTest(body=body):
                self.wrapper(body, None)

    def test_calendar_cli_requires_file_and_valid_date_without_database(self):
        for day in ("2026-09-07", "2026-02-30", "2026-9-7"):
            result = subprocess.run([self.collect, "-task", "is-trading-day", "-date", day], env=self.env,
                                    text=True, capture_output=True, timeout=20)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
        self.calendar.write_text('{"holidays":[],"workdays":[]}')
        result = subprocess.run([self.collect, "-task", "is-trading-day", "-date", "2026-09-07", "-at", "invalid"],
                                env=self.env, text=True, capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "true\n")
        self.assertFalse((self.root / "must-not-exist").exists())


if __name__ == "__main__":
    unittest.main()
