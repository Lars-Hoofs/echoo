#!/bin/sh
# Runs once, as the bootstrap superuser, when the data directory is first initialised.
#
#   echoo      owns the database and the schema; used only by `echoo migrate`.
#   echoo_app  what the server connects as: data access on the tables echoo creates, and
#              nothing else. It owns nothing, so it cannot ALTER a table, drop or disable the
#              audit_log triggers, or grant itself anything.
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v owner_password="$(cat /run/secrets/db_owner_password)" \
  -v app_password="$(cat /run/secrets/db_app_password)" <<'SQL'
CREATE ROLE echoo LOGIN PASSWORD :'owner_password';
CREATE ROLE echoo_app LOGIN PASSWORD :'app_password';

ALTER DATABASE echoo OWNER TO echoo;
REVOKE ALL ON DATABASE echoo FROM PUBLIC;
GRANT CONNECT ON DATABASE echoo TO echoo_app;

-- Tables and sequences that future migrations create get data-access grants automatically.
-- TRUNCATE, TRIGGER and REFERENCES are deliberately not among them.
ALTER DEFAULT PRIVILEGES FOR ROLE echoo IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO echoo_app;
ALTER DEFAULT PRIVILEGES FOR ROLE echoo IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO echoo_app;
SQL
