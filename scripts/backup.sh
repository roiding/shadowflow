#!/bin/sh
set -eu

database_path="${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}"
backup_dir="${SHADOWFLOW_BACKUP_DIR:-/backups}"
retention_days="${SHADOWFLOW_BACKUP_RETENTION_DAYS:-30}"

if [ ! -f "$database_path" ]; then
  echo "database not found: $database_path" >&2
  exit 1
fi

mkdir -p "$backup_dir"
timestamp="$(date '+%Y%m%d-%H%M%S')"
backup_path="$backup_dir/shadowflow-$timestamp.db"

sqlite3 "$database_path" ".timeout 10000" ".backup '$backup_path'"
sqlite3 "$backup_path" "PRAGMA integrity_check;" | grep -qx 'ok'
gzip "$backup_path"
find "$backup_dir" -type f -name 'shadowflow-*.db.gz' -mtime "+$retention_days" -delete
echo "$backup_path.gz"
