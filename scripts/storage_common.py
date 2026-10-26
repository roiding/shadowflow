"""Shared, disk-backed SQLite snapshot and hot/cold maintenance primitives."""

from contextlib import closing, contextmanager
import fcntl
import hashlib
import os
from pathlib import Path
import sqlite3
import time


def read_only(path):
    path = Path(path).resolve(strict=True)
    if not path.is_file():
        raise ValueError(f"not a database file: {path}")
    db = sqlite3.connect(path.as_uri() + "?mode=ro", uri=True, timeout=5)
    db.execute("PRAGMA query_only=ON")
    db.execute("PRAGMA cache_size=-8192")
    return db


@contextmanager
def storage_lock(main_path, *, timeout=30):
    # flock is released by the kernel on exit/crash; a leftover file is not
    # evidence of a running job. Backups and archivers use the same lock.
    path = Path(str(Path(main_path).resolve()) + ".archive.lock")
    with path.open("a+b") as handle:
        if os.geteuid() == 0:
            owner = Path(main_path).stat() if Path(main_path).exists() else path.parent.stat()
            os.fchown(handle.fileno(), owner.st_uid, owner.st_gid)
        deadline = time.monotonic() + timeout
        while True:
            try:
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise TimeoutError("another storage maintenance operation is running")
                time.sleep(0.2)
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)


def snapshot(source_path, destination, *, timeout=3600):
    deadline = time.monotonic() + timeout

    def progress(status, remaining, total):
        if time.monotonic() >= deadline:
            raise TimeoutError("SQLite online backup exceeded its time budget")

    with closing(read_only(source_path)) as source, closing(sqlite3.connect(destination)) as target:
        source.backup(target, pages=1024, progress=progress, sleep=0.01)


@contextmanager
def service_stopped_lock(main_path):
    # Unlike pgrep, a file lease also sees a live service in another container
    # PID namespace. The server holds a shared lease for its entire lifetime.
    path = Path(str(Path(main_path).resolve()) + ".service.lock")
    with path.open("a+b") as handle:
        if os.geteuid() == 0:
            owner = Path(main_path).stat() if Path(main_path).exists() else path.parent.stat()
            os.fchown(handle.fileno(), owner.st_uid, owner.st_gid)
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError("stop the service/collectors using this database before restoring") from error
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)


def digest(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def sync_file(path):
    with Path(path).open("rb") as handle:
        os.fsync(handle.fileno())


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def distinct_paths(main_path, cold_path):
    main, cold = Path(main_path).resolve(), Path(cold_path).resolve()
    if main == cold or (main.exists() and cold.exists() and os.path.samefile(main, cold)):
        raise ValueError("main and cold databases must be different files")
    if main.parent != cold.parent:
        raise ValueError("main and cold must share a directory for coordinated maintenance/restore")
    return main, cold


def validate_database(path, kind):
    required = ("rank_snapshot", "board_money_5m", "stock_research_5m", "stock_kline_source",
                "daily_archive_revision", "schema_migration", "scheduled_job") if kind == "main" else (
                    "rank_intraday_work", "raw_response")
    with closing(read_only(path)) as db:
        errors = [row[0] for row in db.execute("PRAGMA integrity_check")]
        if errors != ["ok"]:
            raise ValueError(f"{kind} integrity check failed: {errors[:5]}")
        for table in required:
            db.execute(f'SELECT * FROM "{table}" LIMIT 0').close()
        if kind == "main" and not db.execute(
                "SELECT count(*) FROM schema_migration WHERE version=1").fetchone()[0]:
            raise ValueError("main schema migration version 1 missing")


def merge_cold_snapshot(db, snapshot_path, from_date, to_date):
    # Only mutate the isolated recovery snapshot. Hot corrections win on the
    # original primary keys, including during an interrupted archival copy.
    db.execute("ATTACH DATABASE ? AS cold_snapshot", (str(snapshot_path),))
    names = [row[1] for row in db.execute("PRAGMA main.table_info(rank_intraday_work)")]
    quoted = ",".join('"' + name.replace('"', '""') + '"' for name in names)
    db.execute(f"INSERT OR IGNORE INTO main.rank_intraday_work ({quoted}) SELECT {quoted} "
               "FROM cold_snapshot.rank_intraday_work WHERE trade_date BETWEEN ? AND ?", (from_date, to_date))
    if db.execute("SELECT count(*) FROM cold_snapshot.sqlite_schema WHERE name='collection_run'").fetchone()[0]:
        names = [row[1] for row in db.execute("PRAGMA main.table_info(collection_run)")]
        quoted = ",".join('"' + name.replace('"', '""') + '"' for name in names)
        db.execute(f"INSERT OR IGNORE INTO main.collection_run ({quoted}) SELECT {quoted} FROM cold_snapshot.collection_run "
                   "WHERE run_id IN (SELECT DISTINCT run_id FROM main.rank_intraday_work WHERE trade_date BETWEEN ? AND ?)",
                   (from_date, to_date))
    db.commit()
    db.execute("DETACH DATABASE cold_snapshot")
