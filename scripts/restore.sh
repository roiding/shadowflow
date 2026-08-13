#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: restore.sh /backups/shadowflow-YYYYMMDD-HHMMSS.db.gz" >&2
  exit 2
fi

archive="$1"
database_path="${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}"

if [ ! -f "$archive" ]; then
  echo "backup not found: $archive" >&2
  exit 1
fi
if pgrep -x shadowflow >/dev/null 2>&1; then
  echo "stop the shadowflow service before restoring" >&2
  exit 1
fi

restore_path="$database_path.restore"
gzip -dc "$archive" > "$restore_path"
sqlite3 "$restore_path" "PRAGMA integrity_check;" | grep -qx 'ok'
mv "$restore_path" "$database_path"
rm -f "$database_path-shm" "$database_path-wal"
echo "$database_path"
