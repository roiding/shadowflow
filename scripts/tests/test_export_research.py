import argparse
from contextlib import closing
import csv
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

from fixtures import SCRIPTS, add_day, add_label, create_database

spec = importlib.util.spec_from_file_location("export_research", SCRIPTS / "export_research.py")
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)


class ExportTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="shadowflow-export-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.database = self.root / "source #? with 'quote.db"
        create_database(self.database)
        self.args = argparse.Namespace(database=self.database, output=self.root / "output",
                                       from_date="2026-09-01", to_date="2026-09-30",
                                       format="csv", batch_size=1)
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute("PRAGMA journal_mode=WAL")
            add_day(db, "2026-09-01")

    def rows(self, dataset):
        with (self.args.output / (dataset + ".csv")).open(encoding="utf-8-sig", newline="") as handle:
            return list(csv.DictReader(handle))

    def manifest(self):
        return json.loads((self.args.output / "manifest.json").read_text())

    def test_snapshot_survives_writer_after_revision_selection(self):
        original = exporter.selected_revisions

        def select_then_write(connection, start, end):
            revisions = original(connection, start, end)
            with closing(sqlite3.connect(self.database)) as writer, writer:
                writer.execute("UPDATE rank_snapshot SET dark_money=999")
                writer.execute("UPDATE daily_archive_revision SET content_sha256='hash-after'")
                add_day(writer, "2026-09-02", sealed=False)
            return revisions

        with mock.patch.object(exporter, "selected_revisions", side_effect=select_then_write):
            exporter.export_bundle(self.args)
        self.assertEqual(self.rows("daily_close")[0]["dark_money"], "100")
        self.assertEqual(self.manifest()["revisions"][0]["content_sha256"], "hash-2026-09-01")
        self.assertEqual(len(self.rows("daily_close")), 1)

    def test_all_datasets_exclude_unsealed_incomplete_and_stale_days(self):
        with closing(sqlite3.connect(self.database)) as db, db:
            add_day(db, "2026-09-02", sealed=False)
            add_day(db, "2026-09-03", status="incomplete")
            add_day(db, "2026-09-04")
            db.execute("UPDATE daily_archive_manifest SET updated_at='new-generation' WHERE trade_date='2026-09-04'")
            add_day(db, "2026-09-05")
            add_label(db, "2026-09-01", "2026-09-05")
        exporter.export_bundle(self.args)
        for dataset in ("daily_close", "board_money_5m", "stock_research_5m", "daily_features"):
            self.assertEqual({row["trade_date"] for row in self.rows(dataset)}, {"2026-09-01"})
        self.assertEqual(self.rows("future_labels"), [])
        self.assertEqual(self.manifest()["excluded_dates"],
                         ["2026-09-02", "2026-09-03", "2026-09-04", "2026-09-05"])

    def test_labels_bind_both_revisions_and_record_out_of_range_targets(self):
        with closing(sqlite3.connect(self.database)) as db, db:
            add_day(db, "2026-09-02")
            add_day(db, "2026-09-03", sealed=False)
            add_label(db, "2026-09-01", "2026-09-02")
            add_label(db, "2026-09-01", "2026-09-02", "obsolete-target")
            add_label(db, "2026-09-01", "2026-09-03")
            add_label(db, "2026-09-03", "2026-09-02")
        self.args.to_date = "2026-09-01"
        exporter.export_bundle(self.args)
        self.assertEqual(len(self.rows("future_labels")), 1)
        self.assertEqual(self.rows("future_label_history"), self.rows("future_labels"))
        self.assertEqual(self.manifest()["label_target_revisions"][0]["trade_date"], "2026-09-02")
        self.assertEqual(len(self.rows("daily_close")), 1)

    def test_stale_source_digest_and_missing_backfill_are_excluded(self):
        with closing(sqlite3.connect(self.database)) as db, db:
            add_day(db, "2026-09-03")
            db.execute("UPDATE daily_feature_set SET source_revisions_json=replace(source_revisions_json, 'hash-2026-09-01', 'old-hash') WHERE trade_date='2026-09-03'")
            add_day(db, "2026-09-04")
            add_day(db, "2026-09-02")
        exporter.export_bundle(self.args)
        self.assertEqual([row["trade_date"] for row in self.rows("revisions")], ["2026-09-01", "2026-09-02"])

    def test_nonempty_output_is_untouched(self):
        self.args.output.mkdir()
        existing = self.args.output / "manifest.json"
        existing.write_text("previous export")
        with self.assertRaises(SystemExit):
            exporter.export_bundle(self.args)
        self.assertEqual(existing.read_text(), "previous export")

    def test_mid_export_failure_publishes_nothing(self):
        original = exporter.export_csv
        calls = 0

        def fail_later(*args):
            nonlocal calls
            calls += 1
            if calls == 3:
                raise OSError("injected write failure")
            return original(*args)

        with mock.patch.object(exporter, "export_csv", side_effect=fail_later):
            with self.assertRaises(OSError):
                exporter.export_bundle(self.args)
        self.assertFalse(self.args.output.exists())
        self.assertEqual(list(self.root.glob(".shadowflow-export-*")), [])

    def test_invalid_dates_empty_selection_and_batch_size(self):
        for value in ("2026-02-30", "2026-9-01", "20260901", "x"):
            with self.subTest(value=value), self.assertRaises(argparse.ArgumentTypeError):
                exporter.iso_date(value)
        self.args.batch_size = 0
        with self.assertRaises(SystemExit):
            exporter.export_bundle(self.args)
        self.args.batch_size = 1
        self.args.from_date = "2027-01-01"
        with self.assertRaises(SystemExit):
            exporter.export_bundle(self.args)
        self.args.to_date = "2027-12-31"
        with self.assertRaises(SystemExit):
            exporter.export_bundle(self.args)
        self.assertFalse(self.args.output.exists())

    def test_corrupt_saved_manifest_fails_without_publication(self):
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute("UPDATE daily_archive_revision SET manifest_json='{bad'")
        with self.assertRaises(ValueError):
            exporter.export_bundle(self.args)
        self.assertFalse(self.args.output.exists())

    @unittest.skipUnless(importlib.util.find_spec("pyarrow"), "Parquet requires pyarrow")
    def test_csv_and_parquet_values_match_from_one_snapshot(self):
        import pyarrow.parquet as pq
        self.args.format = "both"
        self.args.output.mkdir()
        original = exporter.export_csv

        def export_then_write(*args):
            count = original(*args)
            with closing(sqlite3.connect(self.database)) as writer, writer:
                writer.execute("UPDATE rank_snapshot SET dark_money=999")
            return count

        with mock.patch.object(exporter, "export_csv", side_effect=export_then_write):
            exporter.export_bundle(self.args)
        for name in exporter.DATASETS:
            parquet = pq.read_table(self.args.output / (name + ".parquet")).to_pylist()
            as_csv = [{key: "" if value is None else str(value) for key, value in row.items()} for row in parquet]
            self.assertEqual(self.rows(name), as_csv, name)
            self.assertEqual(self.manifest()["datasets"][name]["rows"], len(parquet))


if __name__ == "__main__":
    unittest.main()
