#!/bin/sh
# Generates the credential files docker-compose.yml expects in ./conf. Safe to re-run:
# existing files are never overwritten, because a new encryption key would make stored
# mailbox credentials unreadable.
set -eu
cd "$(dirname "$0")/.."
umask 077
mkdir -p conf
chmod 700 conf

rand() { head -c "$1" /dev/urandom | base64 | tr -d '\n'; }

# Three database roles (see docker/postgres-init/01-roles.sh): the bootstrap superuser, the
# schema owner used only for migrations, and the restricted role the server runs as. The
# passwords are URL-safe because they are embedded in connection strings.
for name in db_admin_password db_owner_password db_app_password; do
  if [ ! -f "conf/$name" ]; then
    rand 24 | tr '+/' '-_' | tr -d '=' > "conf/$name"
  fi
done
if [ ! -f conf/database_url ]; then
  printf 'postgres://echoo_app:%s@db:5432/echoo?sslmode=disable' "$(cat conf/db_app_password)" > conf/database_url
fi
if [ ! -f conf/migrate_database_url ]; then
  printf 'postgres://echoo:%s@db:5432/echoo?sslmode=disable' "$(cat conf/db_owner_password)" > conf/migrate_database_url
fi
if [ ! -f conf/encryption_keys ]; then
  printf 'k1:%s' "$(rand 32)" > conf/encryption_keys
fi
# The container runs as UID 65532 and must be able to read the mounted files; the conf
# directory itself stays 0700, so other users on the host still cannot read them.
chmod 644 conf/*
echo "Credentials are in ./conf. Back up conf/encryption_keys separately from your database backups."
