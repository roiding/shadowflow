#!/bin/sh
set -eu
umask 077

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
. "$script_dir/backup-common.sh"

database_path="${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}"
backup_dir="${SHADOWFLOW_BACKUP_DIR:-/backups}"
retention_count="${SHADOWFLOW_BACKUP_RETENTION_DAYS:-3}"

case "$retention_count" in
  (''|*[!0-9]*)
    echo "SHADOWFLOW_BACKUP_RETENTION_DAYS must be a positive integer backup count" >&2
    exit 2
    ;;
esac
if ! [ "$retention_count" -gt 0 ] 2>/dev/null; then
  echo "SHADOWFLOW_BACKUP_RETENTION_DAYS must be a positive integer backup count" >&2
  exit 2
fi

if [ ! -f "$database_path" ]; then
  echo "database not found: $database_path" >&2
  exit 1
fi

mkdir -p "$backup_dir"
database_path="$(CDPATH= cd "$(dirname "$database_path")" && pwd)/$(basename "$database_path")"
backup_dir="$(CDPATH= cd "$backup_dir" && pwd)"
staging="$(mktemp -d "$backup_dir/.shadowflow-backup.XXXXXX")"
published=""
metadata_published=0
cleanup() {
  # The sidecar is the commit marker. Never roll back a published backup;
  # another backup's retention pass may already have counted it.
  if [ -n "$published" ] && [ ! -f "$published.db.gz.sha256" ]; then
    rm -f "$published.db.gz"
    if [ "$metadata_published" = 1 ]; then rm -f "$published.meta"; fi
  fi
  rm -f "$staging/snapshot.db" "$staging/snapshot.db-wal" "$staging/snapshot.db-shm" "$staging/snapshot.db-journal" \
    "$staging/archive.db.gz" "$staging/archive.sha256" "$staging/backup.meta" \
    "$staging/completed" "$staging/sorted" "$staging/expired"
  rmdir "$staging" 2>/dev/null || :
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

(cd "$staging" && sqlite3 "$database_path" ".timeout 10000" ".backup snapshot.db")
integrity="$(sqlite3 "$staging/snapshot.db" 'PRAGMA integrity_check;')"
[ "$integrity" = "ok" ] || { echo "backup integrity check failed: $integrity" >&2; exit 1; }

{
  echo "created_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  for table in rank_snapshot board_money_5m stock_research_5m stock_kline_source daily_archive_revision scheduled_job; do
    printf '%s=' "$table"
    sqlite3 "$staging/snapshot.db" "SELECT count(*) FROM $table;"
  done
} > "$staging/backup.meta"
gzip -c "$staging/snapshot.db" > "$staging/archive.db.gz"
gzip -t "$staging/archive.db.gz"
checksum="$(sha256sum < "$staging/archive.db.gz")"
checksum="${checksum%% *}"
sync

# A hard-link claim cannot replace another writer's archive. On a same-second
# collision choose a new timestamp, preserving the existing filename format.
while :; do
  prefix="shadowflow-$(date '+%Y%m%d-%H%M%S')"
  if [ -e "$backup_dir/$prefix.meta" ] || [ -e "$backup_dir/$prefix.db.gz.sha256" ]; then
    sleep 1
    continue
  fi
  if ln "$staging/archive.db.gz" "$backup_dir/$prefix.db.gz" 2>/dev/null; then
    published="$backup_dir/$prefix"
    break
  fi
  [ -e "$backup_dir/$prefix.db.gz" ] || { echo "cannot publish backup in $backup_dir" >&2; exit 1; }
  sleep 1
done
ln "$staging/backup.meta" "$published.meta"
metadata_published=1
printf '%s  %s.db.gz\n' "$checksum" "$prefix" > "$staging/archive.sha256"
ln "$staging/archive.sha256" "$published.db.gz.sha256"
sync

# Only complete, checksum-valid compressed backups participate. Half files,
# corrupt archives and manually named files are neither counted nor removed.
: > "$staging/completed"
for archive in "$backup_dir"/shadowflow-*.db.gz; do
  [ -f "$archive" ] || continue
  prefix="$(automatic_backup_prefix "$(basename "$archive")")" || continue
  [ -f "$backup_dir/$prefix.meta" ] || continue
  if verify_backup_checksum "$archive" 2>/dev/null && gzip -t "$archive" 2>/dev/null; then
    printf '%s\n' "$prefix" >> "$staging/completed"
  fi
done
LC_ALL=C sort -r "$staging/completed" > "$staging/sorted"
awk -v keep="$retention_count" 'NR > keep' "$staging/sorted" > "$staging/expired"
while IFS= read -r prefix; do
  # Retire the marker first so an interrupted deletion is not a complete backup.
  rm -f "$backup_dir/$prefix.db.gz.sha256" "$backup_dir/$prefix.db.gz" "$backup_dir/$prefix.meta"
done < "$staging/expired"
echo "$published.db.gz"
