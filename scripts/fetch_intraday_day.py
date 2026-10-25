#!/usr/bin/env python3
"""Fetch one day's board curves serially with bounded workers and local checkpoints."""

import argparse
import base64
from datetime import date, datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import shlex
import shutil
import subprocess
import tempfile
import time


CLOCKS = [f"{n // 60:02d}:{n % 60:02d}" for start, end in ((575, 690), (785, 900))
          for n in range(start, end + 1, 5)]
KINDS = ("industry", "concept")
MAX_BYTES = 2 << 20


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def stamp(day, clock):
    return (datetime.fromisoformat(day + "T" + clock + ":00")
            .replace(tzinfo=timezone(timedelta(hours=8))).astimezone(timezone.utc)
            .isoformat().replace("+00:00", "Z"))


def validate(payload, day, kind, clock, universe=None):
    if (payload.get("schema_version") != "intraday-single-snapshot-v1"
            or payload.get("trade_date") != day or payload.get("rank_type") != kind
            or payload.get("snapshot_at") != stamp(day, clock) or not payload.get("verified")):
        raise ValueError("snapshot identity or successful collection verification failed")
    if payload.get("source_modified") is not False:
        raise ValueError("snapshot does not declare read-only extraction")
    limits = payload.get("limits", {})
    if (limits.get("memory_bytes") != 128 << 20 or limits.get("cpu_seconds") != 5
            or limits.get("query_seconds") != 8 or limits.get("max_rows") != 500):
        raise ValueError("snapshot worker limits differ from approved limits")
    rows = payload.get("rows", [])
    if not 0 < len(rows) <= 500:
        raise ValueError("snapshot row count outside hard limits")
    keys = {(r["market"], r["code"]) for r in rows}
    if len(keys) != len(rows) or any(market != 90 or not code for market, code in keys):
        raise ValueError("snapshot contains duplicate/invalid board identities")
    if any(r["money_available"] != 1 for r in rows):
        raise ValueError("snapshot contains unavailable money")
    if universe is not None and keys != universe:
        raise ValueError("snapshot code set differs from verified 15:00 universe")
    return keys


def save_json(path, value):
    with tempfile.NamedTemporaryFile(dir=path.parent, mode="w", encoding="utf-8", delete=False) as handle:
        temporary = Path(handle.name)
        try:
            json.dump(value, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
            handle.flush()
            temporary.replace(path)
        finally:
            if temporary.exists():
                temporary.unlink()


class SSHSnapshots:
    def __init__(self, args):
        self.args = args
        self.temporary = None
        self.socket = None
        self.source = (Path(__file__).parent / "extract_intraday_snapshot.py").read_bytes()
        self.worker_hash = hashlib.sha256(self.source).hexdigest()

    def base(self):
        return ["ssh", "-T", "-o", "ConnectTimeout=5", "-o", "ConnectionAttempts=1",
                "-p", str(self.args.port)]

    def __enter__(self):
        # The private, short-lived local master separates authentication from
        # query timing. No database command runs during this step.
        self.temporary = tempfile.TemporaryDirectory(prefix="sf-ssh-", dir="/tmp")
        self.socket = str(Path(self.temporary.name) / "control")
        host = f"{self.args.user}@{self.args.host}"
        try:
            subprocess.run(self.base() + ["-M", "-N", "-f", "-S", self.socket,
                           "-o", "ControlPersist=no", "-o", "NumberOfPasswordPrompts=1", host], check=True)
            subprocess.run(self.base() + ["-S", self.socket, "-O", "check", host],
                           check=True, timeout=5, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        except BaseException:
            self.__exit__(None, None, None)
            raise
        print("SSH authenticated; starting strictly serial bounded workers.", flush=True)
        return self

    def __exit__(self, *ignored):
        if self.socket:
            try:
                subprocess.run(self.base() + ["-S", self.socket, "-O", "exit",
                               f"{self.args.user}@{self.args.host}"], timeout=5,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            except (OSError, subprocess.TimeoutExpired):
                pass
        if self.temporary:
            self.temporary.cleanup()

    def fetch(self, day, kind, clock, output):
        program = "import base64;exec(base64.b64decode(" + repr(base64.b64encode(self.source).decode()) + "))"
        command = " ".join(shlex.quote(x) for x in ["python3", "-c", program,
            "--database", self.args.database, "--trade-date", day, "--type", kind, "--at", clock])
        with output.open("wb") as handle:
            result = subprocess.run(self.base() + ["-S", self.socket, "-o", "ControlMaster=no",
                "-o", "BatchMode=yes", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=1",
                f"{self.args.user}@{self.args.host}", command], stdout=handle, stderr=subprocess.PIPE, timeout=15)
        if result.returncode:
            raise ValueError(f"bounded worker exit {result.returncode}: {result.stderr.decode()[-300:]}")
        if b"SNAPSHOT_WORKER_LIMITS_INSTALLED" not in result.stderr or b"SNAPSHOT_READ_DONE" not in result.stderr:
            raise ValueError("missing worker protection/completion acknowledgement")
        if output.stat().st_size > MAX_BYTES:
            raise ValueError("bounded snapshot response exceeds 2 MiB")
        return json.loads(output.read_text())


def collect(args):
    if date.fromisoformat(args.trade_date).isoformat() != args.trade_date or args.interval < 2:
        raise ValueError("one YYYY-MM-DD date and an interval of at least 2 seconds are required")
    args.output.mkdir(parents=True, exist_ok=True)
    snapshots = args.output / "snapshots"
    snapshots.mkdir(exist_ok=True)
    report_path = args.output / "progress.json"
    report = {"schema_version": "bounded-day-extraction-v1", "trade_date": args.trade_date,
              "status": "pending", "expected_snapshots": 96, "completed_snapshots": 0,
              "completed": [], "interval_seconds": args.interval, "production_modified": False,
              "automatic_retry": False, "full_daily_archive_complete": False}
    if report_path.exists():
        previous = json.loads(report_path.read_text())
        if previous.get("trade_date") != args.trade_date:
            raise ValueError("checkpoint directory belongs to another trade date")
    universes = {}
    pending = []
    plan = [(kind, clock) for kind in KINDS for clock in ["15:00"] + CLOCKS[:-1]]
    for kind, clock in plan:
        path = snapshots / f"{kind}-{clock.replace(':', '')}.json"
        receipt_path = path.with_suffix(".receipt.json")
        if not path.exists() and args.seed_directory and clock == "15:00":
            source = args.seed_directory / f"{kind}-{args.trade_date.replace('-', '')}-1500-bounded.json"
            source_receipt = source.with_name(source.stem + "-receipt.json")
            if source.exists():
                receipt = json.loads(source_receipt.read_text())
                if digest(source) != receipt["sha256"]:
                    raise ValueError("seed checksum mismatch")
                validate(json.loads(source.read_text()), args.trade_date, kind, clock)
                shutil.copyfile(source, path)
                save_json(receipt_path, dict(receipt, file=path.name))
        if path.exists():
            receipt = json.loads(receipt_path.read_text())
            if digest(path) != receipt["sha256"]:
                raise ValueError("checkpoint checksum mismatch; no query sent")
            payload = json.loads(path.read_text())
            keys = validate(payload, args.trade_date, kind, clock, universes.get(kind))
            if clock == "15:00":
                universes[kind] = keys
            report["completed"].append({"rank_type": kind, "at": clock, "rows": len(payload["rows"]),
                                        "file": str(path.relative_to(args.output)), "sha256": receipt["sha256"]})
        else:
            pending.append((kind, clock, path, receipt_path))
    report["completed_snapshots"] = len(report["completed"])
    save_json(report_path, report)
    if pending:
        with SSHSnapshots(args) as transport:
            for kind, clock, path, receipt_path in pending:
                temporary = path.with_suffix(".part")
                try:
                    payload = transport.fetch(args.trade_date, kind, clock, temporary)
                    keys = validate(payload, args.trade_date, kind, clock, universes.get(kind))
                    if clock == "15:00":
                        universes[kind] = keys
                    if path.exists():
                        raise ValueError("checkpoint appeared during read; refusing overwrite")
                    temporary.rename(path)
                    receipt = {"file": path.name, "sha256": digest(path), "worker_sha256": transport.worker_hash,
                               "verified": True, "rows": len(payload["rows"]), "production_modified": False}
                    save_json(receipt_path, receipt)
                    report["completed"].append({"rank_type": kind, "at": clock, "rows": len(payload["rows"]),
                        "file": str(path.relative_to(args.output)), "sha256": receipt["sha256"]})
                    report["completed_snapshots"] = len(report["completed"])
                    save_json(report_path, report)
                    print(f"{kind} {clock}: {len(payload['rows'])} rows; {report['completed_snapshots']}/96", flush=True)
                except (OSError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
                    report.update(status="stopped", failed_snapshot={"rank_type": kind, "at": clock},
                                  error=f"{type(error).__name__}: {str(error)[:350]}")
                    save_json(report_path, report)
                    raise
                finally:
                    if temporary.exists():
                        temporary.unlink()
                time.sleep(args.interval)
    report["status"] = "complete"
    save_json(report_path, report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--trade-date", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--seed-directory", type=Path)
    parser.add_argument("--host", default="192.168.1.100")
    parser.add_argument("--port", type=int, default=2233)
    parser.add_argument("--user", default="root")
    parser.add_argument("--database", default="/mnt/data/shadowflow/data/shadowflow.db")
    parser.add_argument("--interval", type=float, default=2)
    try:
        report = collect(parser.parse_args())
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        parser.exit(1, f"extraction stopped without retry: {error}\n")
    print(json.dumps({"status": report["status"], "snapshots": report["completed_snapshots"]}))


if __name__ == "__main__":
    main()
