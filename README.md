# Echoo

Self-hosted shared inbox and helpdesk for email support teams. One Go binary with an embedded
web app, plus PostgreSQL. Licensed under AGPL-3.0.

See [docs/feature-checklist.md](docs/feature-checklist.md) for what exists,
[docs/architecture.md](docs/architecture.md), [SECURITY.md](SECURITY.md),
[DESIGN.md](DESIGN.md), and [docs/operations.md](docs/operations.md) for running it.

## Quick start (Docker Compose)

Requirements: Docker with Compose v2 and a few GB of free disk. The first build takes a few
minutes (frontend and backend are compiled inside Docker); after that it starts in seconds.

```sh
git clone <this repository> echoo && cd echoo
./scripts/setup.sh                 # generates ./conf (database credentials, encryption key)
docker compose up -d --build --wait
docker compose exec echoo /echoo admin create-owner --email you@example.com --name "Your Name"
```

The last command prints a temporary password, valid for 72 hours. Open http://localhost:8080,
sign in, choose your own password, then add a mailbox under Instellingen > Mailboxen. Echoo
listens on 127.0.0.1 only.

Compose runs three database roles: a bootstrap superuser that only initialises the database,
a schema owner used by the one-shot `migrate` service, and a restricted `echoo_app` role that
the server runs as (see [SECURITY.md](SECURITY.md)).

**Back up `conf/encryption_keys` separately from your database backups**: without it, stored
credentials cannot be decrypted.

### Production

Put a TLS-terminating reverse proxy in front (Caddy and Traefik examples are in
[docs/operations.md](docs/operations.md#reverse-proxy)) and put this in a `.env` file next to
`docker-compose.yml`:

```sh
ECHOO_BASE_URL=https://support.example.com
ECHOO_TRUSTED_PROXIES=172.16.0.0/12   # the address range the proxy connects to Echoo from
```

`ECHOO_TRUSTED_PROXIES` is required behind a reverse proxy. If it is empty, Echoo ignores
`X-Forwarded-For`: every user then appears to come from the proxy, shares one login rate limit
and shows the proxy's address in the audit log. Echoo logs a warning at startup when it sees
an https `ECHOO_BASE_URL` with no trusted proxies. For a proxy on the Docker host, the range is
the compose network's subnet (`docker network inspect echoo_default`). Private ranges are never
trusted automatically.

Then read [docs/operations.md](docs/operations.md): upgrades and rollback, `scripts/backup.sh` and
`scripts/restore.sh`, key rotation, data retention, SPF/DKIM/DMARC per mail provider, monitoring
and troubleshooting.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `ECHOO_BASE_URL` | required | Public URL. Must be https, except `http://localhost`. |
| `ECHOO_DATABASE_URL` | required | PostgreSQL connection string. |
| `ECHOO_ENCRYPTION_KEYS` | required | `id:base64key[,id:base64key]`; the first encrypts. `echoo genkey k2` makes a new one; rotation is in [docs/operations.md](docs/operations.md#key-rotation). |
| `ECHOO_LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `ECHOO_TRUSTED_PROXIES` | empty | CIDRs whose `X-Forwarded-For` is trusted. Set it behind a reverse proxy. |
| `ECHOO_STORAGE` | `fs:///data/blobs` | Blob store for raw messages and attachments: a directory, or `s3://bucket?endpoint=host:port&region=r` with `ECHOO_S3_ACCESS_KEY` and `ECHOO_S3_SECRET_KEY`. |
| `ECHOO_MAX_ATTACHMENT_MB` | `25` | Largest single attachment, 1 to 100. The reverse proxy must allow a body about 1 MiB larger. |
| `ECHOO_METRICS_ADDR` | empty (off) | Listen address of the Prometheus endpoint, on a listener of its own, for example `127.0.0.1:9090`. |
| `ECHOO_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

Mail sending for invitations and OAuth mailboxes have their own variables (architecture.md §6).
Every variable can also be read from a file with the `_FILE` suffix. With Compose, put
variables in `.env`; the ones the Compose file passes through are listed in `docker-compose.yml`.

## Development

Requirements: Go (see `go.mod`), Node 24, Docker (integration tests start Postgres with
testcontainers), sqlc 1.31.

```sh
# backend
go test ./...                       # set CGO_ENABLED=0 if your C toolchain is broken
golangci-lint run ./...
sqlc generate                       # after changing db/queries or db/migrations

# frontend
cd web && npm ci && npm run dev     # Vite on :5173, proxies /api to :8080
npm run typecheck && npm run lint && npm test

```

### End-to-end tests

All specs run in one `npx playwright test`, against a fresh stack (the first spec replaces the
owner's temporary password, so a used stack does not work) and the SMTP sink that captures system
mail. This uses its own Compose project and port, so it does not touch a stack you run for
other purposes, and it is what CI does:

```sh
export COMPOSE_PROJECT_NAME=echoo-e2e ECHOO_PORT=18100 ECHOO_BASE_URL=http://localhost:18100 \
       COMPOSE_FILE=docker-compose.yml:web/e2e/compose.smtp.yml E2E_SMTP_PORT=18107
./scripts/setup.sh
(cd web && npm ci && npx playwright install chromium)
(cd web && node e2e/smtp-sink.ts 18107 18108 .e2e/smtp-cert.pem) &      # needs Node 24
docker compose up -d --build --wait
OWNER_PW=$(docker compose exec -T echoo /echoo admin create-owner --email owner@example.com --name Eigenaar | sed -n 's/^Temporary password: //p')
(cd web && ECHOO_E2E_URL=http://localhost:18100 ECHOO_E2E_OWNER_PASSWORD="$OWNER_PW" \
   ECHOO_E2E_COMPOSE_PROJECT=echoo-e2e ECHOO_E2E_SMTP_SINK=http://127.0.0.1:18108 \
   ECHOO_E2E_FAKE_IDP_HOST=host.docker.internal npx playwright test)
docker compose down -v; kill %1                                            # clean up
```

The login endpoint is limited to 10 attempts per minute per address, which one run over all specs
exceeds if every spec signs in again. `web/e2e/session.ts` signs in through the API once and shares
that session between the specs that only need "a signed-in owner"; specs that test the login page
wait for a free slot before each attempt (`loginSlot`). Use one of them in new specs instead of
filling in the login form. CI fails when any spec is skipped.

`ECHOO_E2E_COMPOSE_PROJECT` is required: the specs that seed data with SQL always run
`docker compose -p <project>` and stop with an error when it is missing or equals `echoo` (the
default project name, usually a stack with real data; `ECHOO_E2E_ALLOW_DEFAULT_PROJECT=1` overrides
that).

For local development run the server with `ECHOO_BASE_URL=http://localhost:5173` so the
Vite dev server's origin is accepted.
