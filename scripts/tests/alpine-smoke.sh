#!/bin/sh
set -eu

# Run inside the already-started CI smoke container, never against a deployment.
script_dir="$(CDPATH= cd "$(dirname "$0")/.." && pwd)"
live_database="${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}"
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

# Exercise the paired runtime path as well as the legacy sqlite3 CLI path.
# These are isolated targets, not the running smoke service's database.
PYTHONPATH="$script_dir" python3 - "$live_database" "$work/pair-main.db" "$work/pair-cold.db" <<'PY'
from contextlib import closing
import sqlite3
import sys
from archive_intraday import initialize
from storage_common import snapshot
snapshot(sys.argv[1], sys.argv[2])
with closing(sqlite3.connect(sys.argv[2])) as main, closing(sqlite3.connect(sys.argv[3])) as cold:
    initialize(cold, main)
    cold.commit()
PY
export SHADOWFLOW_DATABASE_PATH="$work/pair-main.db"
export SHADOWFLOW_COLD_DATABASE_PATH="$work/pair-cold.db"
pair="$(/bin/sh "$script_dir/backup.sh")"
/bin/sh "$script_dir/restore.sh" "$pair" --dry-run
printf corrupted >> "$pair"
if /bin/sh "$script_dir/restore.sh" "$pair" --dry-run; then
  echo 'corrupted paired archive was accepted' >&2
  exit 1
fi
unset SHADOWFLOW_COLD_DATABASE_PATH

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
echo 'Alpine single/paired backup and calendar smoke tests passed'
