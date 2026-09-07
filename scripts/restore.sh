#!/bin/sh
set -eu
umask 077

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
. "$script_dir/backup-common.sh"

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: restore.sh /backups/shadowflow-YYYYMMDD-HHMMSS.db.gz [--dry-run]" >&2
  exit 2
fi
archive="$1"
mode="${2:-apply}"
database_path="${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}"
case "$mode" in
  (apply|--dry-run) ;;
  (*) echo "second argument must be --dry-run" >&2; exit 2 ;;
esac

if [ ! -f "$archive" ]; then
  echo "backup not found: $archive" >&2
  exit 1
fi
if [ ! -f "$archive.sha256" ]; then
  echo "backup checksum sidecar not found: $archive.sha256" >&2
  exit 1
fi
archive="$(CDPATH= cd "$(dirname "$archive")" && pwd)/$(basename "$archive")"
database_path="$(CDPATH= cd "$(dirname "$database_path")" && pwd)/$(basename "$database_path")"
staging="$(mktemp -d "$(dirname "$database_path")/.shadowflow-restore.XXXXXX")"
restore_path="$staging/restored.db"
cleanup() {
  rm -f "$staging/archive.db.gz" "$restore_path" "$restore_path-wal" "$restore_path-shm" "$restore_path-journal"
  rmdir "$staging" 2>/dev/null || :
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
# Verify the same private copy that is decompressed, even if retention or an
# operator changes the input path while verification is in progress.
cp "$archive" "$staging/archive.db.gz"
verify_backup_checksum "$staging/archive.db.gz" "$archive.sha256"
gzip -t "$staging/archive.db.gz"
gzip -dc "$staging/archive.db.gz" > "$restore_path"
integrity="$(sqlite3 "$restore_path" 'PRAGMA integrity_check;')"
[ "$integrity" = "ok" ] || { echo "restore integrity check failed: $integrity" >&2; exit 1; }
for table in rank_snapshot board_money_5m stock_research_5m stock_kline_source daily_archive_revision schema_migration scheduled_job; do
  sqlite3 "$restore_path" "SELECT count(*) FROM $table;" >/dev/null || { echo "required table missing: $table" >&2; exit 1; }
done
version="$(sqlite3 "$restore_path" 'SELECT count(*) FROM schema_migration WHERE version=1;')"
[ "$version" -ge 1 ] || { echo "schema migration version 1 missing" >&2; exit 1; }

if [ "$mode" = "--dry-run" ]; then
  echo "verified $archive"
  exit 0
fi
if pgrep -x shadowflow >/dev/null 2>&1; then
  echo "stop the shadowflow service before restoring" >&2
  exit 1
fi
if [ -f "$database_path" ]; then
  safety_copy="$(mktemp "${database_path}.pre-restore-$(date '+%Y%m%d-%H%M%S').XXXXXX")"
  cp -p "$database_path" "$safety_copy"
  [ ! -f "$database_path-wal" ] || cp -p "$database_path-wal" "$safety_copy-wal"
  [ ! -f "$database_path-shm" ] || cp -p "$database_path-shm" "$safety_copy-shm"
  echo "preserved existing database at $safety_copy" >&2
fi
mv "$restore_path" "$database_path"
rm -f "$database_path-shm" "$database_path-wal"
echo "$database_path"
