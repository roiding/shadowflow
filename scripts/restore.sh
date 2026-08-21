#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: restore.sh /backups/shadowflow-YYYYMMDD-HHMMSS.db.gz [--dry-run]" >&2
  exit 2
fi
archive="$1"
mode="${2:-apply}"
database_path="${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}"

if [ ! -f "$archive" ]; then
  echo "backup not found: $archive" >&2
  exit 1
fi
if [ ! -f "$archive.sha256" ]; then
  echo "backup checksum sidecar not found: $archive.sha256" >&2
  exit 1
fi
(cd "$(dirname "$archive")" && sha256sum -c "$(basename "$archive").sha256") >/dev/null
gzip -t "$archive"

restore_path="${database_path}.verify"
trap 'rm -f "$restore_path"' EXIT HUP INT TERM
gzip -dc "$archive" > "$restore_path"
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
[ "$mode" = "apply" ] || { echo "second argument must be --dry-run" >&2; exit 2; }
if pgrep -x shadowflow >/dev/null 2>&1; then
  echo "stop the shadowflow service before restoring" >&2
  exit 1
fi
mv "$restore_path" "$database_path"
rm -f "$database_path-shm" "$database_path-wal"
echo "$database_path"
