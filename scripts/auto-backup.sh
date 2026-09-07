#!/bin/sh
set -eu

# Host cron should call this wrapper. Manual operators should call backup.sh
# directly; this wrapper prevents weekend/holiday backups before opening the
# database and delegates the verified backup to the manual script.
calendar_path="${SHADOWFLOW_CALENDAR_PATH:-/app/config/trading_calendar.json}"
backup_script="${SHADOWFLOW_BACKUP_SCRIPT:-/app/scripts/backup.sh}"
collect_bin="${SHADOWFLOW_COLLECT_BIN:-/app/collect}"
timezone="${TZ:-Asia/Shanghai}"
today="$(TZ="$timezone" date '+%Y-%m-%d')"

if [ ! -f "$calendar_path" ]; then
  echo "trading calendar not found: $calendar_path" >&2
  exit 1
fi

trading_day="$(SHADOWFLOW_CALENDAR_PATH="$calendar_path" "$collect_bin" -task is-trading-day -date "$today")"
case "$trading_day" in
  (true) ;;
  (false) echo "skip backup: $today is not a trading day"; exit 0 ;;
  (*) echo "invalid trading-day result: $trading_day" >&2; exit 1 ;;
esac

echo "run backup: $today is a trading day"
exec "$backup_script"
