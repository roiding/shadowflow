#!/bin/sh
set -eu

# Run inside the already-started CI smoke container, never against a deployment.
script_dir="$(CDPATH= cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export SHADOWFLOW_BACKUP_DIR="$work/backups"
export SHADOWFLOW_BACKUP_RETENTION_DAYS=2
mkdir "$SHADOWFLOW_BACKUP_DIR"
partial="$SHADOWFLOW_BACKUP_DIR/shadowflow-20990101-000000.db.gz"
printf 'incomplete gzip' > "$partial"
first="$(/bin/sh "$script_dir/backup.sh")"
second="$(/bin/sh "$script_dir/backup.sh")"
third="$(/bin/sh "$script_dir/backup.sh")"
test ! -f "$first"
test -f "$second"
test -f "$third"
test -f "$partial"
export SHADOWFLOW_DATABASE_PATH="$work/restore-target.db"
mv "$third" "$work/moved.db.gz"
mv "$third.sha256" "$work/moved.db.gz.sha256"
/bin/sh "$script_dir/restore.sh" "$work/moved.db.gz" --dry-run
printf corrupted >> "$work/moved.db.gz"
if /bin/sh "$script_dir/restore.sh" "$work/moved.db.gz" --dry-run; then
  echo 'corrupted archive was accepted' >&2
  exit 1
fi

export SHADOWFLOW_CALENDAR_PATH="$work/calendar.json"
printf '%s\n' '{"workdays":["2026-09-06"],"holidays":["2026-09-07"]}' > "$SHADOWFLOW_CALENDAR_PATH"
test "$(/app/collect -task is-trading-day -date 2026-09-07)" = false
test "$(/app/collect -task is-trading-day -date 2026-09-06)" = true
today="$(date '+%Y-%m-%d')"
printf '{"holidays":["%s"],"workdays":[]}\n' "$today" > "$SHADOWFLOW_CALENDAR_PATH"
SHADOWFLOW_BACKUP_SCRIPT=/does-not-exist /bin/sh "$script_dir/auto-backup.sh"
printf malformed > "$SHADOWFLOW_CALENDAR_PATH"
if /bin/sh "$script_dir/auto-backup.sh"; then
  echo 'malformed calendar was accepted' >&2
  exit 1
fi
echo 'Alpine backup and calendar smoke tests passed'
