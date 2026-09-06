#!/bin/sh
set -eu

# Host cron should call this wrapper. Manual operators should call backup.sh
# directly; this wrapper prevents weekend/holiday backups before opening the
# database and delegates the verified backup to the manual script.
calendar_path="${SHADOWFLOW_CALENDAR_PATH:-/app/config/trading_calendar.json}"
backup_script="${SHADOWFLOW_BACKUP_SCRIPT:-/app/scripts/backup.sh}"
timezone="${TZ:-Asia/Shanghai}"
today="$(TZ="$timezone" date '+%Y-%m-%d')"
weekday="$(TZ="$timezone" date '+%u')"

if [ ! -f "$calendar_path" ]; then
  echo "trading calendar not found: $calendar_path" >&2
  exit 1
fi

if ! awk -v today="$today" -v weekday="$weekday" '
  BEGIN { section=""; holiday=0; workday=0 }
  /"holidays"[[:space:]]*:/ { section="holidays" }
  /"workdays"[[:space:]]*:/ { section="workdays" }
  section == "holidays" && $0 ~ "\\\"" today "\\\"" { holiday=1 }
  section == "workdays" && $0 ~ "\\\"" today "\\\"" { workday=1 }
  section != "" && /\]/ { section="" }
  END {
    if (workday || (!holiday && weekday >= 1 && weekday <= 5)) exit 0
    exit 1
  }
' "$calendar_path"; then
  echo "skip backup: $today is not a trading day"
  exit 0
fi

echo "run backup: $today is a trading day"
exec "$backup_script"
