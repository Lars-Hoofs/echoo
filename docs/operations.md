# Operating Echoo

For the person who runs the server. Everything here assumes the Docker Compose setup in this
repository; each section says what changes for other setups. The security background is in
[SECURITY.md](../SECURITY.md), the design in [architecture.md](architecture.md).

Contents: [install](#install), [configuration](#configuration), [reverse proxy](#reverse-proxy),
[upgrade](#upgrade), [backups and restore](#backups-and-restore),
[key rotation](#key-rotation), [retention and privacy](#retention-and-privacy),
[blob hygiene](#blob-hygiene), [SPF, DKIM and DMARC](#spf-dkim-and-dmarc),
[sizing](#resource-sizing), [monitoring](#monitoring), [troubleshooting](#troubleshooting).

## Install

Requirements: Docker with Compose v2, about 1 GB RAM free, a DNS name and a TLS-terminating
reverse proxy for production (below).

```sh
git clone <this repository> echoo && cd echoo
./scripts/setup.sh                       # ./conf: database passwords and the encryption key
docker compose up -d --build --wait
docker compose exec echoo /echoo admin create-owner --email you@example.com --name "Your Name"
```

`--wait` returns when the database, the one-shot `migrate` service and Echoo are healthy. The
last command prints a temporary password that is valid for 72 hours; sign in at
`http://localhost:8080` and choose your own. Then add a mailbox under Instellingen > Mailboxen.

What `setup.sh` created, and what to do with it:

| File in `./conf` | Used by | Handling |
|---|---|---|
| `encryption_keys` | Echoo | **Back it up separately from database backups.** Without it stored mailbox passwords, OAuth tokens, 2FA secrets and webhook secrets cannot be read. |
| `database_url` | Echoo | The restricted `echoo_app` role. |
| `migrate_database_url`, `db_owner_password` | `migrate` service, database init | Schema owner. Do not copy to other systems. |
| `db_admin_password` | database init | Bootstrap superuser; only the init script uses it. |

`setup.sh` never overwrites existing files, so re-running it is safe. Configuration goes in a
`.env` file next to `docker-compose.yml` (Compose reads it automatically), for example:

```sh
ECHOO_BASE_URL=https://support.example.com
ECHOO_TRUSTED_PROXIES=172.18.0.0/16
ECHOO_MAX_ATTACHMENT_MB=25
```

`ECHOO_PORT` (default 8080) sets the port Echoo publishes on 127.0.0.1.

## Configuration

The complete variable list is in the README and architecture.md §6. The ones that matter to
operations:

| Variable | Default | Notes |
|---|---|---|
| `ECHOO_BASE_URL` | `http://localhost:8080` | The public https URL. Cookies, CSRF checks and links in mail depend on it. |
| `ECHOO_TRUSTED_PROXIES` | empty | CIDRs of the proxy as Echoo sees it. Required behind a proxy. |
| `ECHOO_MAX_ATTACHMENT_MB` | 25 | 1 to 100. The proxy must allow bodies about 1 MiB larger. |
| `ECHOO_STORAGE` | `fs:///data/blobs` | Or `s3://bucket?endpoint=host:port&region=r`, with `ECHOO_S3_ACCESS_KEY` and `ECHOO_S3_SECRET_KEY`. |
| `ECHOO_METRICS_ADDR` | empty (off) | Address of the Prometheus listener, for example `127.0.0.1:9090`. See [monitoring](#monitoring). |
| `ECHOO_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `ECHOO_APNS_KEY`, `_KEY_ID`, `_TEAM_ID`, `_TOPIC`, `_ENVIRONMENT` | empty (off) | Push to the iOS app. See [docs/mobile.md](mobile.md). |
| `ECHOO_FCM_CREDENTIALS` | empty (off) | Firebase service account key JSON, for push to the Android app. See [docs/mobile.md](mobile.md). |

Any variable can be read from a file with the `_FILE` suffix.

## Reverse proxy

Echoo speaks plain HTTP on 127.0.0.1 only. Put a proxy in front that terminates TLS and:

1. sets `X-Forwarded-For` to the real client address,
2. does **not** buffer responses, because `/api/v1/events` is a long-lived server-sent event
   stream,
3. allows request bodies of `ECHOO_MAX_ATTACHMENT_MB` plus about 1 MiB (multipart overhead;
   Echoo itself cuts a larger body off at exactly that size),
4. gives large uploads time: Echoo extends its own read deadline to 5 minutes for uploads, so
   the proxy's request timeout should be at least that.

### ECHOO_TRUSTED_PROXIES

Echoo only honors `X-Forwarded-For` from addresses in `ECHOO_TRUSTED_PROXIES`, and private ranges
are never trusted automatically. Set it to the address range Echoo sees the proxy connect from:

* Proxy on the Docker host, talking to the published `127.0.0.1:8080`: Echoo sees the Compose
  network's gateway. Find it with
  `docker network inspect echoo_default --format '{{(index .IPAM.Config 0).Subnet}}'` and use
  that subnet (for example `172.18.0.0/16`).
* Proxy as a container on the Compose network: the subnet of that network.
* A CDN or second proxy in front of yours: list your own proxy here, and configure the CDN's
  trusted ranges on your proxy (Caddy `trusted_proxies`, Traefik `forwardedHeaders.trustedIPs`).

Left empty behind a proxy, every user shares one login rate limit and the audit log records the
proxy's address. Echoo logs a warning at startup when the base URL is https and no proxy is
trusted. Check it works: sign in, then look at the address in Auditlog or Instellingen >
Beveiliging > Sessies; it must be yours, not the proxy's.

### Caddy

```caddyfile
support.example.com {
	encode zstd gzip

	# 25 MiB attachments plus multipart overhead: Echoo allows ECHOO_MAX_ATTACHMENT_MB + 1 MiB.
	request_body {
		max_size 27MB
	}

	reverse_proxy 127.0.0.1:8080 {
		# Do not buffer: the event stream (/api/v1/events) must reach the browser immediately.
		flush_interval -1
		# Caddy sets X-Forwarded-For, -Proto and -Host itself and replaces any value a client
		# sent, unless the client is in servers.trusted_proxies.
	}
}
```

Caddy obtains and renews the certificate itself. Validated with `caddy validate` (Caddy 2).

### Traefik (v3, file provider)

Static configuration, `traefik.yml`:

```yaml
entryPoints:
  web:
    address: ":80"
    http:
      redirections:
        entryPoint: { to: websecure, scheme: https }
  websecure:
    address: ":443"
    transport:
      respondingTimeouts:
        # Large attachments on slow links: the default of 60 s would cut them off.
        readTimeout: 5m
    # Only add forwardedHeaders.trustedIPs when another proxy or CDN sits in front of Traefik;
    # then Traefik keeps its X-Forwarded-For instead of replacing it.
certificatesResolvers:
  letsencrypt:
    acme:
      email: you@example.com
      storage: /letsencrypt/acme.json     # mount a volume here
      httpChallenge: { entryPoint: web }
providers:
  file:
    filename: /etc/traefik/dynamic.yml
```

Dynamic configuration, `dynamic.yml`:

```yaml
http:
  routers:
    echoo:
      rule: Host(`support.example.com`)
      entryPoints: [websecure]
      tls: { certResolver: letsencrypt }
      service: echoo
    echoo-uploads:
      # Reject oversized attachments before they reach Echoo. Only this router buffers: the
      # buffering middleware also buffers responses, which would stall the event stream.
      rule: Host(`support.example.com`) && Path(`/api/v1/uploads`)
      entryPoints: [websecure]
      tls: { certResolver: letsencrypt }
      middlewares: [attachment-limit]
      service: echoo
      priority: 100
  middlewares:
    attachment-limit:
      buffering:
        maxRequestBodyBytes: 28311552 # (ECHOO_MAX_ATTACHMENT_MB + 2) MiB = 27 MiB for the default 25
        memRequestBodyBytes: 1048576
  services:
    echoo:
      loadBalancer:
        servers:
          - url: http://echoo:8080     # Traefik on the same Compose network
        # Send bytes as they arrive: the event stream is long-lived.
        responseForwarding:
          flushInterval: 1ms
```

Traefik must reach Echoo on the network: attach it to the `echoo_default` network, or use
`http://host.docker.internal:8080` with an `extra_hosts` entry. Set `ECHOO_TRUSTED_PROXIES` to
that network's subnet. The two files were loaded by Traefik v3 without configuration errors
(apart from the ACME account, which needs the mounted `/letsencrypt` volume).

Other size-limited endpoints (contact CSV import, help center images) enforce their limits in
Echoo itself.

After the proxy is up, set `ECHOO_BASE_URL=https://support.example.com` and
`ECHOO_TRUSTED_PROXIES`, then `docker compose up -d`.

## Upgrade

```sh
scripts/backup.sh                         # always first: migrations only go forward
git pull
docker compose up -d --build --wait
docker compose logs migrate               # what was applied
```

The Compose file runs a one-shot `migrate` service before Echoo starts. It applies pending
schema migrations as the schema owner and exits; Echoo only starts when it completed
successfully, and refuses to start against a schema that is behind. Downtime is the time the
migrations take plus the restart, usually seconds. `/readyz` answers 503 `schema out of date`
until the schema matches the binary.

**Rollback.** Migrations are forward-only: an older Echoo against a newer schema is not
supported, and there are no automatic down migrations. To go back, restore the backup you made
before the upgrade ([restore](#restore)) and start the older version. Anything that happened
after the backup is lost, so check the new version before you tell users it is live.

Pin what you deploy: the base images in the Dockerfile and Compose file are pinned by digest,
and Renovate or Dependabot updates them in pull requests.

## Backups and restore

What holds state: the PostgreSQL database (everything, including the job queue) and the blob
store (raw messages and attachments). Blobs are content-addressed and written once, so a blob
copy taken after the database dump contains everything the dump refers to.

### Backup

```sh
scripts/backup.sh                         # writes ./backups/echoo-<UTC timestamp>/
scripts/backup.sh /mnt/backup/echoo       # another directory
ECHOO_BACKUP_KEEP=14 scripts/backup.sh    # keep the newest 14 (default 7)
```

Each backup directory holds `database.dump` (`pg_dump -Fc`), `blobs.tar.gz` (the blob volume) and
`SHA256SUMS`. The script writes to a `.partial` directory and renames it when complete, so an
interrupted run never looks like a backup, and afterwards deletes the oldest backups beyond
`ECHOO_BACKUP_KEEP`. Run it from cron on the host, for example nightly:

```cron
15 3 * * *  cd /srv/echoo && scripts/backup.sh /mnt/backup/echoo >> /var/log/echoo-backup.log 2>&1
```

Then get the directory off the machine (rsync, restic, your backup tool). Things to know:

* **Encryption keys are not in the backup.** `conf/encryption_keys` must be stored separately,
  and a restore needs the same file. Database dumps contain mail, contact data and credentials
  in encrypted form; treat them as personal data.
* **Encrypt backups you copy elsewhere**, for example with [age](https://age-encryption.org):
  `age -r <public key> -o database.dump.age database.dump`, or `gpg --encrypt --recipient <id>`.
  Echoo does not require it and does not do it for you.
* **S3 storage.** With `ECHOO_STORAGE=s3://...` there is no blob volume; run the backup with
  `ECHOO_BACKUP_BLOBS=skip` and back up the bucket with the provider's tools (bucket versioning
  or replication), or copy it with rclone: `rclone sync s3remote:echoo-bucket /mnt/backup/blobs`,
  ideally right after the database dump.
* **Retention purges and backups.** A restored backup contains whatever was deleted since. See
  [retention and privacy](#retention-and-privacy).

### Restore

`scripts/restore.sh --yes <backup directory>` replaces the database and the blob volume of the
running project with the backup, verifies the checksums first, and starts everything again. It
runs the migrations, so restoring a backup made by an older version upgrades it.

To restore on a new or wiped machine:

```sh
git clone <this repository> echoo && cd echoo
mkdir -p conf && cp /safe/place/conf-backup/* conf/    # at least the ORIGINAL encryption_keys
                                                          # (run ./scripts/setup.sh only if conf/ is missing)
docker compose up -d --build --wait db                 # a new empty database, roles included
scripts/restore.sh --yes /mnt/backup/echoo/echoo-20260101T031500Z
```

Afterwards: sign in, check the inbox, and compare the newest message with what you expect.

* A restore brings back data that was erased after the backup was made. If contacts were erased
  (GDPR) or retention purges ran since, repeat the erasures now: the audit log in the restored
  database lists erasures up to the backup time, and your own record of requests covers the rest.
  This is also why backups should not be kept longer than your retention promise allows.
* Test a restore at least quarterly, on a scratch machine with the same steps.

CI runs this round trip after the end-to-end tests (backup, `docker compose down -v`, a new empty
database, restore, `/readyz`, the same number of conversations, messages and users), so a change
that breaks it fails the build.

## Key rotation

Stored secrets (mailbox IMAP and SMTP passwords, OAuth tokens, inbound webhook secrets, 2FA
secrets, webhook signing secrets, the SSO client secret) are encrypted with AES-256-GCM. Each
value records the id of the key that sealed it, so several keys can be configured at once: the
first entry of `ECHOO_ENCRYPTION_KEYS` encrypts new values, all entries decrypt. Rotate after a
suspected leak of the key file, when someone with access to it leaves, or on a schedule.

1. **Make a new key** and put it first in `conf/encryption_keys`, keeping the old one:

   ```sh
   docker compose run --rm --no-deps echoo genkey k2      # prints k2:<base64>
   # conf/encryption_keys becomes:  k2:<new>,k1:<old>
   ```

   Key ids are 1 to 16 lowercase letters and digits.
2. **Restart Echoo** so it reads the file: `docker compose restart echoo`. New and changed
   secrets are now sealed with `k2`; everything else can still be read with `k1`.
3. **Re-encrypt everything else:**

   ```sh
   docker compose exec echoo /echoo admin rotate-keys
   ```

   It goes through every encrypted column in batches, re-encrypts values that are not under the
   active key, prints a line per column, and ends by checking each retired key. It is idempotent
   and safe to interrupt and run again. A value it cannot decrypt (wrong key, damaged, copied
   from another row) is left as it was and reported; the command then exits non-zero.
4. **Confirm.** The last lines say `old key k1 no longer used: it can be removed from
   ECHOO_ENCRYPTION_KEYS`. The owner sees the same counts under Instellingen > Privacy en
   retentie, and `GET /api/v1/admin/keys` returns them.
5. **Retire the old key**: remove `k1` from `conf/encryption_keys`, `docker compose restart
   echoo`, and destroy old copies of the key. Wait a day first: an invitation or reset mail that
   is still being retried (up to 8 attempts) holds its content sealed with the key that was
   active when it was queued, and fails when that key is gone; resend it from the user's page.

Things to know before rotating:

* **Backups made before the rotation need the old key.** Keep `k1` in the offline key backup for
  as long as you keep backups from before the rotation.
* **Signed links and ids follow the configured keys.** Satisfaction survey links (valid 30
  days), the tag in outbound Message-IDs (so customers' replies to old mail still thread) and
  the knowledge base feedback token are signed with a key derived from the active key and
  verified against keys derived from every key in `ECHOO_ENCRYPTION_KEYS`. They keep working
  through a rotation for as long as the old key stays in the list; removing it (step 5)
  invalidates the survey links and Message-ID tags made under it, so wait until surveys sent
  under it have expired (30 days) if you want those to keep working. Replies to old mail still
  thread through the stored Message-ID of the message they answer. Message preview URLs (one
  hour) are reissued on the next page view.
* The rotation is audited (`keys.rotated`, with counts and the new key id, never key material).

## Retention and privacy

Settings live under Instellingen > Privacy en retentie (permission `settings.manage`), with a
preview that counts what would be deleted before you save. A period left empty keeps that kind
of data forever.

| What | Setting | Deletes |
|---|---|---|
| Closed conversations | per workspace or per mailbox, months | conversations whose last activity, or resolution if later, is older; with messages, notes, events, attachments and raw copies. Conversations with mail still waiting to be sent, or locked by an agent at that moment, are skipped until the next run. |
| Attachments only | per workspace or per mailbox, months | attachment files of older messages, and the raw copy of those messages (it contains the files). The message text stays; the search index keeps working. |
| Spam | per workspace or per mailbox, days | spam conversations, like closed ones |
| Trash | per workspace or per mailbox, days (default 30) | conversations that went into the trash that long ago, like closed ones. Agents with `conversations.delete` can also delete from the trash by hand. |
| Audit log | workspace, months (default 12, minimum 3) | entries older than that, through a database function that logs its own count first |
| Webhook delivery log | fixed 14 days | |
| Rule runs | fixed 30 days | |
| Composer uploads never attached | fixed 24 hours | files of abandoned uploads |

A mailbox with its own periods ignores the workspace periods entirely, so it can opt out of one.

How it runs: a daily job (`retention.purge`, also once at startup) deletes in batches of 1000
rows with `FOR UPDATE SKIP LOCKED`, so it never blocks agents. Every batch writes an audit entry
(`retention.purged`) with counts and no names, addresses or subjects, in the same transaction as
the deletion. Files are removed through the reference-checked deletion queue: a blob that another
attachment, raw message or upload still uses stays. A file that cannot be deleted stays queued
and is retried hourly; the job then reports a failure in the "Taken" page. The last run's counts
are shown on the settings page.

The audit log can only be shortened through the database function `audit_log_purge`, which
refuses ages under 60 days. The application role cannot delete audit rows itself and cannot get
around this (SECURITY.md, G). A hand-written setup with a different application role name has to
`GRANT EXECUTE ON FUNCTION audit_log_purge(interval, integer)` to that role; the migration only
does it for `echoo_app`.

Limits worth stating to your customers: backups keep purged data until they age out;
conversations deleted by retention leave a blanked raw-message row so IMAP sync does not
re-import them, but if the mail server resets its UIDVALIDITY the mail is fetched again.

## Blob hygiene

Two things keep the blob store honest, both automatic: expired composer uploads are purged
hourly, and everything deleted by erasure or retention goes through the deletion queue.

For a manual check, for example after a restore or a storage incident:

```sh
docker compose exec echoo /echoo admin blobs --check
```

It lists blob keys the database refers to that the store does not have (`MISSING`, with the table
and column that refers to it), and, for filesystem storage, files that nothing refers to
(`ORPHAN`). Files younger than 24 hours are skipped, because a file is written a moment before
the row that refers to it. It exits non-zero when it finds anything, so it can run from cron.
Missing files mean data loss: restore them from the blob backup. For S3 storage only the missing
check runs.

```sh
docker compose exec echoo /echoo admin blobs --delete-orphans          # lists, deletes nothing
docker compose exec echoo /echoo admin blobs --delete-orphans --yes    # deletes
```

Deletion goes through the reference-checked queue, so a file that gained a reference since the
scan stays, and it is audited (`blobs.orphans_deleted`).

## SPF, DKIM and DMARC

Echoo sends customer replies through the mailbox's own SMTP server or API, so the mail is signed
and authorized by that provider. There is nothing to add for Echoo's IP address. What you must do
is make the provider's authentication cover the domain you send from, and publish a DMARC policy.
Without them, replies land in spam or are rejected, which shows up as bounces and `uncertain`
sends.

Check a domain after each change by sending a reply to a mailbox you can read and looking at the
`Authentication-Results` header: you want `spf=pass`, `dkim=pass` and `dmarc=pass`, with the
signing domain equal to the From domain.

**Microsoft 365**

* SPF: one TXT record on the domain: `v=spf1 include:spf.protection.outlook.com -all` (merge
  other senders into the same record; a domain may have only one SPF record).
* DKIM: Microsoft 365 Defender > Email & collaboration > Policies > Threat policies > DKIM. Choose
  the domain, publish the two CNAME records it shows (`selector1._domainkey` and
  `selector2._domainkey`), then switch signing on.
* DMARC: see below.
* Connection: connect the mailbox with Microsoft OAuth (docs/mail-oauth.md) rather than a
  password; basic authentication is being retired by Microsoft.

**Google Workspace**

* SPF: `v=spf1 include:_spf.google.com ~all` (use `-all` once you are sure nothing else sends).
* DKIM: Admin console > Apps > Google Workspace > Gmail > Authenticate email. Generate a 2048-bit
  key for the domain, publish the TXT record on `google._domainkey`, then click Start
  authentication.
* Connection: OAuth (docs/mail-oauth.md), or an app password with IMAP enabled.

**Other providers (generic SMTP)**

* SPF: the `include:` mechanism your provider documents.
* DKIM: the provider's records, usually CNAMEs or a TXT on a selector under `_domainkey`. If the
  provider offers no DKIM for your domain, DMARC can only pass through SPF, which is fragile.
* Alignment: DMARC passes only when SPF or DKIM passes **for the domain in the From address**. A
  provider that signs with its own domain (`d=provider.example`) does not align.

**DMARC** (all providers): a TXT record on `_dmarc.<domain>`. Start in monitoring mode and tighten
once the reports look clean:

```
v=DMARC1; p=none; rua=mailto:dmarc-reports@example.com; adkim=r; aspf=r
```

After a few weeks with only legitimate sources in the reports, move to `p=quarantine`, later
`p=reject`.

**System mail** (invitations, password resets, satisfaction surveys) goes through
`ECHOO_SMTP_URL` or `ECHOO_SYSTEM_MAILBOX`. The domain in its From address needs the same records,
for that relay.

## Resource sizing

These are starting points from the architecture's budgets, not measured guarantees; watch the
metrics and adjust.

| Team | Echoo | PostgreSQL | Disk |
|---|---|---|---|
| Up to 10 agents, 5 mailboxes, a few hundred mails a day | 1 vCPU, 256 MB (idle RSS is under 100 MB) | 1 vCPU, 1 GB | 20 GB, mostly blobs |
| Up to 50 agents, 20 mailboxes, a few thousand mails a day | 2 vCPU, 512 MB | 2 vCPU, 4 GB, `shared_buffers` 1 GB | 100 GB and growing with attachments |

Rules of thumb:

* Echoo holds one connection per mailbox to the mail server (IMAP IDLE) and a pool of at most
  10 database connections. Keep PostgreSQL's `max_connections` well above that, per Echoo
  instance.
* Password hashing (argon2id, 19 MiB per hash, at most 2 at a time) is the largest burst of memory.
* Storage growth is the size of your mail. Estimate from your mail server's mailbox sizes, and
  use the attachment retention setting to bound it.
* PostgreSQL needs regular `VACUUM` (autovacuum is on by default) and its own disk alerts; the job
  queue and conversation tables churn.
* Several Echoo instances against one database are supported: an advisory lock makes exactly one
  of them sync each mailbox. The blob store must then be shared (S3).

## Monitoring

**Endpoints**

* `/healthz`: the process is up; trivial on purpose. The container healthcheck uses it.
* `/readyz`: the database answers, the schema version matches the binary, and the blob store
  accepts a write (the storage and schema checks are cached for 30 seconds, 5 seconds after a
  failure). 503 with a reason: `database unavailable`, `schema out of date`, `schema unreadable`,
  `storage not writable`. Use it for load balancer checks and uptime monitoring.

**Metrics.** Off by default. Set `ECHOO_METRICS_ADDR` to open a Prometheus endpoint on a listener
of its own, so it is never reachable through the public address:

* Bare metal or host network: `ECHOO_METRICS_ADDR=127.0.0.1:9090`.
* Docker Compose: `ECHOO_METRICS_ADDR=0.0.0.0:9090` inside the container and no `ports:` entry for
  it. Prometheus on the same Compose network scrapes `http://echoo:9090/metrics`. (Do not publish
  the port to a public interface: the endpoint has no authentication.)

```yaml
# prometheus.yml
scrape_configs:
  - job_name: echoo
    static_configs: [{ targets: ["echoo:9090"] }]
```

| Metric | Meaning |
|---|---|
| `echoo_http_requests_total{route,class}` | Requests per route pattern (`/api/v1/conversations/{id}`) and status class (`2xx`). |
| `echoo_http_request_duration_seconds{route,class}` | Latency histogram. Event stream requests last until the browser leaves. |
| `echoo_sse_streams_open` | Open event streams on this instance. |
| `echoo_imap_mailboxes{sync_state}` | Enabled mailboxes per sync state. |
| `echoo_jobs{kind,state}` | Job queue rows per kind and state (`available`, `running`, `retryable`, `discarded`, `completed`, ...), refreshed every 30 seconds. |
| `echoo_outbound_messages{status}` | Outgoing mail per status, including `uncertain`, `failed` and `bounced`. |
| `echoo_raw_messages{parse_status}` | Raw messages waiting (`pending`) or failed to parse (`failed`). |
| `echoo_webhook_deliveries{status}` | Webhook deliveries of the last 14 days per status. |
| `echoo_db_pool_*` | Connection pool: total, idle, in use, max, acquires, waits. |
| `echoo_metrics_collect_errors_total` | Failed refreshes of the database-derived numbers. |
| `go_*`, `process_*` | Go runtime and process. |

Labels only ever hold route patterns, states, statuses and job kinds: never addresses, subjects
or user names.

Suggested alerts:

```yaml
- alert: EchooMailboxNeedsAttention
  expr: echoo_imap_mailboxes{sync_state="auth_failed"} > 0
  for: 10m
- alert: EchooUncertainSend
  expr: echoo_outbound_messages{status="uncertain"} > 0
- alert: EchooJobsFailing
  expr: echoo_jobs{state="retryable"} > 20 or echoo_jobs{state="discarded"} > 0
  for: 15m
- alert: EchooParseFailures
  expr: echoo_raw_messages{parse_status="failed"} > 0
  for: 30m
- alert: EchooDbPoolExhausted
  expr: rate(echoo_db_pool_acquires_waited_total[5m]) > 1
- alert: EchooDown
  expr: probe_success{job="echoo-readyz"} == 0     # blackbox exporter on /readyz
  for: 3m
```

**Logs.** One JSON object per line on stderr (`docker compose logs -f echoo`). Every line written
while handling a request carries `request_id`; the same id is in the `X-Request-Id` response
header, so a user can quote it. Access log lines hold the route pattern, method, status and
duration, never the path, query string or client address. Log values are scrubbed of email
addresses as a last line of defense (`[address]`), and message content and subjects are never
logged. Ship them wherever you keep logs; the audit log in Echoo covers who did what.

## Troubleshooting

**Mailbox states** (Instellingen > Mailboxen, and `echoo_imap_mailboxes`):

| State | Meaning | What to do |
|---|---|---|
| `connected` | IMAP IDLE is active; new mail arrives at once. | Nothing. |
| `polling` | The server has no IDLE, or IDLE failed three times; Echoo checks every 60 seconds and retries IDLE later. | Fine, slightly slower. Persistent: check the provider's IDLE limits. |
| `backoff` | The connection failed (network, TLS, server error); retrying with a growing delay up to 5 minutes. | Read the error in the mailbox settings and `docker compose logs echoo` (`mailbox sync failed`). Usually a firewall, DNS or provider outage; it recovers by itself. |
| `auth_failed` | The provider rejected the credentials. | Password mailbox: enter the current password (an app password if the account has 2FA). OAuth mailbox: press "Opnieuw verbinden" and sign in with the same account (`oauth_reauth_required` means the provider no longer honors the refresh token, for example after a password change or when an administrator revoked access). |
| `disabled` | The mailbox is switched off, or has not synced yet. | Enable it. |

Echoo never deletes or moves mail on the server, so a failed sync loses nothing: it continues
from the last UID it processed, and resyncs the inbox when the server reports a changed
UIDVALIDITY.

**Outgoing mail** (`outbound` statuses): `queued`, `sending`, `retry` (temporary error, backing
off), `sent`, `failed` (permanent error or retries exhausted; the server's answer is shown on the
message), `bounced` (a delivery report came back), `cancelled` (undo send).

* **`uncertain`** means Echoo crashed or lost its database between the SMTP server accepting a
  message and recording that, and could not find the message in the Sent folder. Echoo never
  resends on its own. Look in the mailbox's Sent folder or ask the recipient; if the mail did not
  go out, use "Opnieuw versturen" on the message. Alert on this state.
* Many `failed` or `bounced` with `550` and "SPF" or "DMARC" in the response: fix
  [SPF, DKIM and DMARC](#spf-dkim-and-dmarc).

**Mail does not arrive.** Check the mailbox state first. Then Instellingen > Taken: messages that
failed to parse are listed with the reason and can be retried after a fix; `mail.parse` jobs that
keep failing appear there too. `echoo_raw_messages{parse_status="pending"}` staying above zero
means the job queue is not running.

**Everyone is rate limited or the audit log shows one address.** `ECHOO_TRUSTED_PROXIES` is empty
or wrong ([reverse proxy](#reverse-proxy)).

**`/readyz` returns 503.**

* `schema out of date`: the migrations did not run. `docker compose up -d --wait` runs them;
  `docker compose logs migrate` shows errors.
* `storage not writable`: the blob volume is full, read-only or mounted with the wrong owner (the
  container runs as UID 65532); check `df` on the volume and its ownership.
* `database unavailable`: the database container or its credentials.

**Echoo does not start: "database schema is not up to date; run `echoo migrate`".** Run the
`migrate` service (`docker compose up -d --wait` does). If migrations fail, do not edit the
database by hand; restore the backup made before the upgrade.

**Stored passwords stopped working after a restore or a change of `conf/encryption_keys`.** The
key that sealed them is missing. The owner sees `unknown` keys under Instellingen > Privacy en
retentie. Put the original key back; if it is lost, mailbox passwords, OAuth connections and 2FA
secrets must be entered again (users reset 2FA through an admin).

**Recover the owner.** `docker compose exec echoo /echoo admin reset-password --email <owner>`.

**Disk is filling up.** `docker system df -v` and the size of the `blobs` volume against the
database. Set an attachment retention period (above), and run `echoo admin blobs --check`.

**Finding what happened.** Instellingen > Auditlog for who changed what; `docker compose logs
echoo | grep <request id>` for one request; Instellingen > Taken for failed background work.
