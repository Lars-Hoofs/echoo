#!/bin/sh
# Restores a backup made by scripts/backup.sh into the Compose stack in this directory. It
# replaces the database and the blob volume, so it asks for --yes.
#
#   scripts/restore.sh --yes backups/echoo-20260101T030000Z
#
# For a new machine or a wiped stack: run ./scripts/setup.sh only if conf/ is missing, put the
# ORIGINAL conf/encryption_keys back, `docker compose up -d --wait db`, then run this.
# Environment: COMPOSE_PROJECT_NAME selects the project, like for backup.sh.
set -eu
cd "$(dirname "$0")/.."
umask 077

if [ "${1:-}" != "--yes" ] || [ $# -ne 2 ]; then
  echo "usage: scripts/restore.sh --yes <backup-directory>" >&2
  echo "This replaces the database and the blob volume of the running project with the backup." >&2
  exit 2
fi
src=$2
test -f "$src/database.dump" || { echo "$src/database.dump not found" >&2; exit 1; }

echo "Verifying checksums..."
(cd "$src" && if command -v sha256sum >/dev/null 2>&1; then sha256sum -c SHA256SUMS; else shasum -a 256 -c SHA256SUMS; fi)

docker compose stop echoo
docker compose up -d --wait db

echo "Restoring the database..."
# Objects are created as the schema owner, so the restricted application role gets its
# privileges from the default privileges in the dump, exactly as after a normal migration.
docker compose exec -T db pg_restore -U postgres -d echoo --clean --if-exists --no-owner --role=echoo \
  --single-transaction --exit-on-error < "$src/database.dump"

if [ -f "$src/blobs.tar.gz" ]; then
  echo "Restoring the blob volume..."
  project=${COMPOSE_PROJECT_NAME:-$(docker compose config --format json | sed -n 's/^  "name": "\(.*\)",\{0,1\}$/\1/p')}
  volume=$(docker volume ls -q --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.volume=blobs")
  if [ -z "$volume" ]; then
    # A wiped stack has no volume until a container mounts it; creating the echoo container does.
    docker compose create echoo >/dev/null
    volume=$(docker volume ls -q --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.volume=blobs")
  fi
  image=$(docker inspect --format '{{.Config.Image}}' "$(docker compose ps -q db)")
  docker run --rm -i -v "$volume":/data --entrypoint sh "$image" -c 'rm -rf /data/sha256 && tar -xzf - -C /data && chown -R 65532:65532 /data' < "$src/blobs.tar.gz"
else
  echo "No blobs.tar.gz in the backup: restore the object storage bucket with your provider's tools."
fi

echo "Starting Echoo (migrations run first, so a backup from an older version is upgraded)..."
docker compose up -d --wait
echo "Restore complete."
echo "If contacts were erased (GDPR) after this backup was made, repeat those erasures now: the audit log shows which (docs/operations.md)."
