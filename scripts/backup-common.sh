#!/bin/sh

# Only the digest is trusted from a sidecar. Legacy absolute paths and moved
# backups use the same check, always against the actual supplied archive.
verify_backup_checksum() (
  archive_path="$1"
  sidecar_path="${2:-$1.sha256}"
  [ -f "$archive_path" ] && [ -f "$sidecar_path" ] || {
    echo "archive or checksum sidecar is missing: $archive_path" >&2
    exit 1
  }
  expected="$(awk '
    NR == 1 {
      digest=$1
      sub(/^\\/, "", digest)
      if (length(digest) != 64 || digest ~ /[^[:xdigit:]]/) bad=1
    }
    NR > 1 { bad=1; exit }
    END { if (NR != 1 || bad) exit 1; print tolower(digest) }
  ' "$sidecar_path")" || {
    echo "invalid checksum sidecar: $sidecar_path" >&2
    exit 1
  }
  actual="$(sha256sum < "$archive_path")" || exit 1
  actual="${actual%% *}"
  [ "$actual" = "$expected" ] || {
    echo "checksum mismatch: $archive_path" >&2
    exit 1
  }
)

automatic_backup_prefix() {
  case "$1" in
    (shadowflow-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9].db.gz)
      printf '%s\n' "${1%.db.gz}"
      ;;
    (*) return 1 ;;
  esac
}
