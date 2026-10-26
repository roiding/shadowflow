#!/usr/bin/env python3
"""Verified hot/cold backup bundles; restore only with the service stopped."""

import argparse
from datetime import datetime, timezone
import gzip
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import tarfile
import tempfile
import zlib

from storage_common import (digest, distinct_paths, service_stopped_lock, snapshot, storage_lock,
                            sync_directory, sync_file, validate_database)


VERSION = "shadowflow-hot-cold-backup-v1"
MEMBERS = {"manifest.json", "main.db.gz", "cold.db.gz"}
HISTORY_MEMBER = "calendar-history.json"


def expected_checksum(sidecar):
    lines = Path(sidecar).read_text().splitlines()
    if len(lines) != 1:
        raise ValueError("malformed checksum sidecar")
    match = re.fullmatch(r"\\?([0-9a-fA-F]{64}) [ *].+", lines[0])
    if not match:
        raise ValueError("malformed checksum sidecar")
    return match[1].lower()


def check_checksum(path):
    if digest(path) != expected_checksum(str(path) + ".sha256"):
        raise ValueError("backup checksum mismatch")


def compress(source, target):
    with Path(source).open("rb") as src, gzip.open(target, "wb", compresslevel=6) as dst:
        shutil.copyfileobj(src, dst, 1 << 20)
    sync_file(target)


def inspect_bundle(path, staging=None):
    with tarfile.open(path, "r:") as archive:
        members = archive.getmembers()
        names = {m.name for m in members}
        if len(names) != len(members) or names not in (MEMBERS, MEMBERS | {HISTORY_MEMBER}) or not all(m.isfile() for m in members):
            raise ValueError("unexpected or unsafe backup bundle members")
        if archive.getmember("manifest.json").size > 65536:
            raise ValueError("backup manifest too large")
        with archive.extractfile("manifest.json") as source:
            manifest = json.load(source)
        if not isinstance(manifest, dict) or manifest.get("version") != VERSION or not isinstance(manifest.get("databases"), dict) or set(manifest["databases"]) != {"main", "cold"}:
            raise ValueError("unsupported backup manifest")
        history = manifest.get("calendar_history")
        if (HISTORY_MEMBER in names) != (history is not None):
            raise ValueError("calendar history does not match manifest")
        if history is not None:
            if not isinstance(history, dict) or history.get("member") != HISTORY_MEMBER or archive.getmember(HISTORY_MEMBER).size > (1 << 20):
                raise ValueError("invalid calendar history manifest")
            with archive.extractfile(HISTORY_MEMBER) as source:
                body = source.read()
            import hashlib
            if len(body) != history.get("bytes") or hashlib.sha256(body).hexdigest() != history.get("sha256") or not isinstance(json.loads(body), dict):
                raise ValueError("calendar history checksum/content mismatch")
            if staging:
                (Path(staging) / HISTORY_MEMBER).write_bytes(body)
        if staging:
            sizes = [spec.get("bytes") for spec in manifest["databases"].values() if isinstance(spec, dict)]
            if len(sizes) != 2 or not all(type(size) is int and size >= 0 for size in sizes):
                raise ValueError("invalid database sizes")
            if sum(sizes) + (64 << 20) > shutil.disk_usage(staging).free:
                raise ValueError("insufficient disk space for verified restore snapshots")
        for kind in ("main", "cold"):
            spec = manifest["databases"][kind]
            if not isinstance(spec, dict) or spec.get("member") != kind + ".db.gz" or not isinstance(spec.get("bytes"), int) or spec["bytes"] < 0:
                raise ValueError("invalid database manifest")
            import hashlib
            checksum, count = hashlib.sha256(), 0
            target = (Path(staging) / (kind + ".db")) if staging else None
            with archive.extractfile(spec["member"]) as packed, gzip.GzipFile(fileobj=packed) as source:
                destination = target.open("wb") if target else None
                try:
                    for block in iter(lambda: source.read(1 << 20), b""):
                        count += len(block)
                        if count > spec["bytes"]:
                            raise ValueError("database exceeds manifest size")
                        checksum.update(block)
                        if destination:
                            destination.write(block)
                finally:
                    if destination:
                        destination.close()
            if count != spec["bytes"] or checksum.hexdigest() != spec.get("sha256"):
                raise ValueError(f"{kind} snapshot checksum/size mismatch")
            if target:
                validate_database(target, kind)
    return manifest


def valid_backup(path):
    try:
        check_checksum(path)
        if path.name.endswith(".bundle.tar"):
            inspect_bundle(path)
        else:
            if not path.with_name(path.name.removesuffix(".db.gz") + ".meta").is_file():
                return False
            with gzip.open(path, "rb") as source:
                while source.read(1 << 20):
                    pass
        return True
    except (ValueError, OSError, EOFError, zlib.error, tarfile.TarError, json.JSONDecodeError):
        return False


def retain(backups, count):
    with storage_lock(backups / ".retention", timeout=120):
        pattern = re.compile(r"shadowflow-\d{8}-\d{6}(?:\.db\.gz|\.bundle\.tar)$")
        completed = [p for p in backups.iterdir() if p.is_file() and pattern.fullmatch(p.name) and valid_backup(p)]
        for expired in sorted(completed, key=lambda p: p.name, reverse=True)[count:]:
            Path(str(expired) + ".sha256").unlink()
            expired.unlink()
            if expired.name.endswith(".db.gz"):
                expired.with_name(expired.name.removesuffix(".db.gz") + ".meta").unlink()
        sync_directory(backups)


def backup(main, cold, backups, retention):
    main, cold = distinct_paths(main, cold)
    if retention < 1:
        raise ValueError("backup retention must be a positive count")
    backups = Path(backups).resolve()
    backups.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".shadowflow-pair-backup-", dir=backups) as temporary:
        stage = Path(temporary)
        with storage_lock(main):
            if Path(str(main) + ".restore-in-progress").exists():
                raise ValueError("restore is not complete")
            snapshot(main, stage / "main.db")
            snapshot(cold, stage / "cold.db")
            history = main.parent / ".shadowflow-calendar-history.json"
            if history.is_file():
                shutil.copyfile(history, stage / HISTORY_MEMBER)
        manifest = {"version": VERSION, "created_at": datetime.now(timezone.utc).isoformat(), "databases": {}}
        if (stage / HISTORY_MEMBER).is_file():
            history = stage / HISTORY_MEMBER
            manifest["calendar_history"] = {"member": HISTORY_MEMBER, "bytes": history.stat().st_size, "sha256": digest(history)}
        for kind in ("main", "cold"):
            source = stage / (kind + ".db")
            validate_database(source, kind)
            packed = stage / (kind + ".db.gz")
            compress(source, packed)
            manifest["databases"][kind] = {"member": packed.name, "bytes": source.stat().st_size, "sha256": digest(source)}
        (stage / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        packed = stage / "bundle.tar"
        with tarfile.open(packed, "w") as archive:
            for member in sorted(MEMBERS | ({HISTORY_MEMBER} if "calendar_history" in manifest else set())):
                archive.add(stage / member, arcname=member, recursive=False)
        sync_file(packed)
        inspect_bundle(packed)
        # A hard-link claim cannot overwrite an existing same-second backup.
        while True:
            path = backups / ("shadowflow-" + datetime.now().strftime("%Y%m%d-%H%M%S") + ".bundle.tar")
            if Path(str(path) + ".sha256").exists():
                import time
                time.sleep(1)
                continue
            try:
                os.link(packed, path)
                break
            except FileExistsError:
                import time
                time.sleep(1)
        try:
            sidecar = stage / "bundle.sha256"
            sidecar.write_text(digest(packed) + "  " + path.name + "\n")
            sync_file(sidecar)
            os.link(sidecar, str(path) + ".sha256")
            sync_directory(backups)
        except BaseException:
            if not Path(str(path) + ".sha256").exists():
                path.unlink(missing_ok=True)
            raise
    retain(backups, retention)
    return path


def restore(archive, main, cold, dry_run=False):
    main, cold = distinct_paths(main, cold)
    main.parent.mkdir(parents=True, exist_ok=True)
    archive = Path(archive).resolve(strict=True)
    expected = expected_checksum(str(archive) + ".sha256")
    with tempfile.TemporaryDirectory(prefix=".shadowflow-pair-restore-", dir=main.parent) as temporary:
        stage = Path(temporary)
        private = stage / "bundle.tar"
        shutil.copyfile(archive, private)
        if digest(private) != expected:
            raise ValueError("backup checksum mismatch")
        inspect_bundle(private, stage)
        if dry_run:
            return "verified " + str(archive)
        if subprocess.run(["pgrep", "-x", "shadowflow"], capture_output=True).returncode == 0:
            raise ValueError("stop the shadowflow service before restoring")
        with service_stopped_lock(main), storage_lock(main):
            safety = Path(tempfile.mkdtemp(prefix="shadowflow-pre-restore-", dir=main.parent))
            for path in (main, cold):
                if path.exists():
                    snapshot(path, safety / path.name)
                    sync_file(safety / path.name)
            history_path = main.parent / ".shadowflow-calendar-history.json"
            if history_path.exists():
                shutil.copyfile(history_path, safety / history_path.name)
                sync_file(safety / history_path.name)
            sync_directory(safety)
            marker = Path(str(main) + ".restore-in-progress")
            marker.write_text(json.dumps({"archive": str(archive), "safety_copy": str(safety)}) + "\n")
            sync_file(marker)
            sync_directory(main.parent)
            # Two renames are not crash-atomic. The durable marker makes the
            # server fail closed until both have finished; safety copies stay.
            for kind, path in (("main", main), ("cold", cold)):
                restored = stage / (kind + ".db")
                owner = path.stat() if path.exists() else path.parent.stat()
                if os.geteuid() == 0:
                    os.chown(restored, owner.st_uid, owner.st_gid)
                sync_file(restored)
                os.replace(restored, path)
                for suffix in ("-wal", "-shm", "-journal"):
                    Path(str(path) + suffix).unlink(missing_ok=True)
            if (stage / HISTORY_MEMBER).is_file():
                if os.geteuid() == 0:
                    owner = main.stat()
                    os.chown(stage / HISTORY_MEMBER, owner.st_uid, owner.st_gid)
                sync_file(stage / HISTORY_MEMBER)
                os.replace(stage / HISTORY_MEMBER, history_path)
            sync_directory(main.parent)
            marker.unlink()
            sync_directory(main.parent)
    return str(main)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("backup")
    restoration = sub.add_parser("restore")
    restoration.add_argument("archive", type=Path)
    restoration.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    if hasattr(os, "nice"):
        os.nice(10)
    main_path = os.environ.get("SHADOWFLOW_DATABASE_PATH", "/data/shadowflow.db")
    cold = os.environ.get("SHADOWFLOW_COLD_DATABASE_PATH", "")
    try:
        if not cold:
            raise ValueError("SHADOWFLOW_COLD_DATABASE_PATH is required for paired backup/restore")
        if args.command == "backup":
            result = backup(main_path, cold, os.environ.get("SHADOWFLOW_BACKUP_DIR", "/backups"),
                            int(os.environ.get("SHADOWFLOW_BACKUP_RETENTION_DAYS", "3")))
        else:
            result = restore(args.archive, main_path, cold, args.dry_run)
        print(result)
    except (ValueError, OSError, TimeoutError, sqlite3.Error, zlib.error, tarfile.TarError, json.JSONDecodeError) as error:
        parser.exit(1, f"paired {args.command} failed: {error}\n")


if __name__ == "__main__":
    main()
