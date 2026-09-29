#!/bin/sh
# Backs up the database and the blob volume of a running Compose stack into a timestamped
# directory, then removes the oldest backups beyond ECHOO_BACKUP_KEEP.
#
#   scripts/backup.sh [backup-dir]        default ./backups
#
# Environment:
#   ECHOO_BACKUP_KEEP     number of backups to keep in the directory (default 7)
#   ECHOO_BACKUP_BLOBS    "skip" to leave the blob volume out, for ECHOO_STORAGE=s3://... where
#                         the bucket is backed up by the provider or with rclone (docs/operations.md)
#   COMPOSE_PROJECT_NAME  the Compose project to back up (default: the one in docker-compose.yml)
#
# The database goes first and the blobs second: blobs are written once and never changed, so
# every blob the dump refers to is in the archive. conf/encryption_keys is deliberately not
# included: keep it apart from the backups (docs/operations.md).
set -eu
cd "$(dirname "$0")/.."

dir=${1:-backups}
keep=${ECHOO_BACKUP_KEEP:-7}
case "$keep" in ''|*[!0-9]*|0) echo "ECHOO_BACKUP_KEEP must be a whole number of at least 1" >&2; exit 2;; esac

# Backups hold personal data: nothing is created group- or world-readable, not even for a moment.
umask 077
mkdir -p "$dir"
chmod 700 "$dir"

stamp=$(date -u +%Y%m%dT%H%M%SZ)
final="$dir/echoo-$stamp"
work="$final.partial"
mkdir "$work"
trap 'rm -rf "$work"' EXIT

echo "Dumping the database..."
docker compose exec -T db pg_dump -U postgres -d echoo --format=custom --no-owner > "$work/database.dump"
test -s "$work/database.dump"

if [ "${ECHOO_BACKUP_BLOBS:-}" = "skip" ]; then
  echo "Skipping the blob volume (ECHOO_BACKUP_BLOBS=skip)."
else
  echo "Archiving the blob volume..."
  project=${COMPOSE_PROJECT_NAME:-$(docker compose config --format json | sed -n 's/^  "name": "\(.*\)",\{0,1\}$/\1/p')}
  volume=$(docker volume ls -q --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.volume=blobs")
  if [ -z "$volume" ]; then
    echo "Could not find the blobs volume of project '$project'." >&2
    exit 1
  fi
  # The postgres image is already pulled and ships busybox tar; the Echoo image has no shell.
  image=$(docker inspect --format '{{.Config.Image}}' "$(docker compose ps -q db)")
  docker run --rm -v "$volume":/data:ro --entrypoint tar "$image" -C /data -czf - . > "$work/blobs.tar.gz"
fi

# The file list is expanded before the redirect creates SHA256SUMS, so it does not list itself.
(cd "$work" && set -- * && for f in "$@"; do
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$f"; else shasum -a 256 "$f"; fi
done > SHA256SUMS)

trap - EXIT
mv "$work" "$final"
echo "Backup written to $final"

# Keep the newest $keep backups; names sort by their UTC timestamp.
find "$dir" -maxdepth 1 -type d -name 'echoo-????????T??????Z' | sort -r | tail -n +"$((keep + 1))" | while read -r old; do
  echo "Removing old backup $old"
  rm -rf "$old"
done
echo "Reminder: conf/encryption_keys is not part of the backup; store it separately."
