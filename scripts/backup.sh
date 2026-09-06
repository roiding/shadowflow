#!/bin/sh
set -eu

database_path="${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}"
backup_dir="${SHADOWFLOW_BACKUP_DIR:-/backups}"
retention_count="${SHADOWFLOW_BACKUP_RETENTION_DAYS:-3}"

case "$retention_count" in
  ''|*[!0-9]*|0)
    echo "SHADOWFLOW_BACKUP_RETENTION_DAYS must be a positive integer backup count" >&2
    exit 2
    ;;
esac

if [ ! -f "$database_path" ]; then
  echo "database not found: $database_path" >&2
  exit 1
fi

mkdir -p "$backup_dir"
timestamp="$(date '+%Y%m%d-%H%M%S')"
backup_path="$backup_dir/shadowflow-$timestamp.db"
metadata_path="$backup_dir/shadowflow-$timestamp.meta"

sqlite3 "$database_path" ".timeout 10000" ".backup '$backup_path'"
integrity="$(sqlite3 "$backup_path" 'PRAGMA integrity_check;')"
[ "$integrity" = "ok" ] || { echo "backup integrity check failed: $integrity" >&2; rm -f "$backup_path"; exit 1; }

{
  echo "created_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  for table in rank_snapshot board_money_5m stock_research_5m stock_kline_source daily_archive_revision scheduled_job; do
    printf '%s=' "$table"
    sqlite3 "$backup_path" "SELECT count(*) FROM $table;"
  done
} > "$metadata_path"
gzip -f "$backup_path"
gzip -t "$backup_path.gz"
sha256sum "$backup_path.gz" > "$backup_path.gz.sha256"
# Retain the newest N completed compressed backups. Sidecars are removed by
# the same timestamp prefix; files created manually with other names are not
# touched by the automatic retention sweep.
backup_prefixes="$(
  for archive in "$backup_dir"/shadowflow-*.db.gz; do
    [ -f "$archive" ] || continue
    basename "$archive" .db.gz
  done | sort -r | tail -n +$((retention_count + 1))
)"
if [ -n "$backup_prefixes" ]; then
  printf '%s\n' "$backup_prefixes" | while IFS= read -r prefix; do
    [ -n "$prefix" ] || continue
    rm -f "$backup_dir/$prefix.db.gz" "$backup_dir/$prefix.db.gz.sha256" "$backup_dir/$prefix.meta"
  done
fi
echo "$backup_path.gz"
