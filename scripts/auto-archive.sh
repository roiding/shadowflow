#!/bin/sh
set -eu
umask 077

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
if [ -z "${SHADOWFLOW_COLD_DATABASE_PATH:-}" ]; then
  echo "skip intraday archival: cold storage is disabled"
  exit 0
fi
exec python3 "$script_dir/archive_intraday.py" archive \
  --database "${SHADOWFLOW_DATABASE_PATH:-/data/shadowflow.db}" \
  --cold-database "$SHADOWFLOW_COLD_DATABASE_PATH" \
  --calendar "${SHADOWFLOW_CALENDAR_PATH:-/app/config/trading_calendar.json}" \
  --keep-trading-days "${SHADOWFLOW_HOT_TRADING_DAYS:-10}" \
  --max-days "${SHADOWFLOW_ARCHIVE_MAX_DAYS:-1}" --apply
