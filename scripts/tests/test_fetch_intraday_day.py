import argparse
from contextlib import redirect_stdout
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


path = Path(__file__).resolve().parents[1] / "fetch_intraday_day.py"
spec = importlib.util.spec_from_file_location("fetch_intraday_day", path)
fetcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fetcher)


def payload(kind, clock, code="BK001"):
    return {"schema_version": "intraday-single-snapshot-v1", "trade_date": "2026-10-22",
            "rank_type": kind, "snapshot_at": fetcher.stamp("2026-10-22", clock),
            "verified": True, "source_modified": False,
            "rows": [{"market": 90, "code": code, "money_available": 1}],
            "limits": {"memory_bytes": 128 << 20, "cpu_seconds": 5, "query_seconds": 8, "max_rows": 500}}


class FakeSSH:
    calls = []
    exited = False
    fail_at = None

    def __init__(self, args):
        self.worker_hash = "test-worker"

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.__class__.exited = True

    def fetch(self, day, kind, clock, output):
        self.__class__.calls.append((kind, clock))
        if len(self.calls) == self.fail_at:
            raise ValueError("first simulated failure")
        data = payload(kind, clock)
        output.write_text(json.dumps(data))
        return data


class BoundedDayTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.seed = self.root / "seed"
        self.seed.mkdir()
        for kind in fetcher.KINDS:
            source = self.seed / f"{kind}-20261022-1500-bounded.json"
            source.write_text(json.dumps(payload(kind, "15:00")))
            source.with_name(source.stem + "-receipt.json").write_text(json.dumps({
                "file": source.name, "sha256": hashlib.sha256(source.read_bytes()).hexdigest()}))
        self.args = argparse.Namespace(trade_date="2026-10-22", output=self.root / "output",
                                       seed_directory=self.seed, interval=2)
        FakeSSH.calls, FakeSSH.exited, FakeSSH.fail_at = [], False, None

    def collect(self):
        with mock.patch.object(fetcher, "SSHSnapshots", FakeSSH), mock.patch.object(fetcher.time, "sleep"), \
             redirect_stdout(io.StringIO()):
            return fetcher.collect(self.args)

    def test_seeded_day_requires_exactly_94_serial_calls(self):
        report = self.collect()
        self.assertEqual(report["status"], "complete")
        self.assertEqual(report["completed_snapshots"], 96)
        self.assertEqual(len(FakeSSH.calls), 94)
        self.assertEqual(len(set(FakeSSH.calls)), 94)
        self.assertTrue(FakeSSH.exited)
        self.assertNotIn(("industry", "15:00"), FakeSSH.calls)

    def test_first_failure_stops_without_retry_and_preserves_checkpoints(self):
        FakeSSH.fail_at = 3
        with self.assertRaisesRegex(ValueError, "first simulated failure"):
            self.collect()
        report = json.loads((self.args.output / "progress.json").read_text())
        self.assertEqual(len(FakeSSH.calls), 3)
        self.assertEqual(report["status"], "stopped")
        self.assertEqual(report["completed_snapshots"], 4)
        self.assertTrue(FakeSSH.exited)
        self.assertEqual(list((self.args.output / "snapshots").glob("*.part")), [])

    def test_explicit_resume_skips_previously_verified_reads(self):
        FakeSSH.fail_at = 3
        with self.assertRaises(ValueError):
            self.collect()
        done = FakeSSH.calls[:2]
        FakeSSH.calls, FakeSSH.fail_at = [], None
        self.collect()
        self.assertEqual(len(FakeSSH.calls), 92)
        self.assertFalse(any(item in FakeSSH.calls for item in done))

    def test_completed_day_sends_no_further_queries(self):
        self.collect()
        FakeSSH.calls = []
        self.collect()
        self.assertEqual(FakeSSH.calls, [])

    def test_corrupt_seed_stops_before_authentication(self):
        (self.seed / "industry-20261022-1500-bounded.json").write_text("corrupt")
        with mock.patch.object(fetcher, "SSHSnapshots") as transport:
            with self.assertRaisesRegex(ValueError, "checksum"):
                fetcher.collect(self.args)
        transport.assert_not_called()

    def test_relaxed_worker_limits_refused(self):
        data = payload("industry", "15:00")
        data["limits"]["memory_bytes"] *= 2
        with self.assertRaisesRegex(ValueError, "approved limits"):
            fetcher.validate(data, "2026-10-22", "industry", "15:00")

    def test_changed_universe_refused(self):
        with self.assertRaisesRegex(ValueError, "code set"):
            fetcher.validate(payload("concept", "09:35", "BK002"), "2026-10-22", "concept", "09:35", {(90, "BK001")})


if __name__ == "__main__":
    unittest.main()
