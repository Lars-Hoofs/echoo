# Query plans

`EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)` output for the hot queries, taken on PostgreSQL 18
with 20,000 conversations over 3 mailboxes (20% open, 1 in 7 assigned to one user, 1 in 11 to one team),
after `ANALYZE`. Page size 51 (50 rows plus one to detect a next page). Sources are in
`db/queries/conversations.sql`. Refresh this file when a query or index changes.

## Inbox list, view all/unassigned (`ListConversationPageByMailbox`)

One index scan per readable mailbox, each cut off by `LIMIT`, merged with a top-N sort. The cursor is a
row comparison on `(last_message_at, id)`, which is part of the index condition.

```
Limit (actual rows=51.00 loops=1)
  Buffers: shared hit=52
  ->  Sort (actual rows=51.00 loops=1)
        Sort Key: conversations.last_message_at DESC, conversations.id DESC
        Sort Method: top-N heapsort  Memory: 30kB
        ->  Nested Loop (actual rows=153.00 loops=1)
              ->  Function Scan on unnest mailbox (actual rows=3.00 loops=1)
              ->  Limit (actual rows=51.00 loops=3)
                    ->  Index Only Scan using conversations_mailbox_list on conversations (actual rows=51.00 loops=3)
                          Index Cond: ((mailbox_id = mailbox.id) AND (status = 'open'::text) AND (ROW(last_message_at, id) < ROW('infinity'::timestamp with time zone, 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid)))
                          Heap Fetches: 153
Execution Time: 0.117 ms
```

With a cursor the plan is the same (55 buffers, 0.096 ms). `unassigned` adds `Filter: (assignee_user_id IS NULL)`
on the same index (58 buffers, 0.058 ms). One mailbox: 17 buffers, 0.028 ms.

With `team_id` the planner switches to the new `conversations_team_list` index (migration 00006) and filters on
the mailbox: 459 buffers, 0.158 ms. Without that index this filter would scan the whole mailbox range.

## Inbox list, view mine (`ListConversationPageAssigned`)

Uses `conversations_assignee_list`; the readable-mailbox check is a filter on rows that are already in order.
The scan stops after 51 rows.

```
Limit (actual rows=51.00 loops=1)
  ->  Index Scan using conversations_assignee_list on conversations (actual rows=51.00 loops=1)
        Index Cond: ((assignee_user_id = '...'::uuid) AND (status = 'open'::text) AND (ROW(last_message_at, id) < ROW('infinity'::timestamp with time zone, 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid)))
        Filter: (mailbox_id = ANY ('{...}'::uuid[]))
        Buffers: shared hit=35
Execution Time: 0.026 ms
```

If the user is assigned many conversations in mailboxes they cannot read, the filter discards rows before the
limit is reached (100 rows removed in the single-mailbox case, 0.072 ms). That is bounded by the user's own
assigned open conversations.

## Sidebar counts

`InboxOpenCounts` and `InboxTeamCounts` count all open conversations in scope (bitmap scan on the `status`
prefix, 4,000 rows: 0.72 ms and 0.29 ms). They are linear in the number of open conversations. Replace them
with the per-mailbox counter table from architecture section 8 when that number reaches tens of thousands.

## Search (`SearchConversationPage`) and advanced filters (`ListConversationPageFiltered`)

Both live in `db/queries/search.sql`. Measured on PostgreSQL 18 with a seeded dataset of 100,000
messages (234 MB with indexes) in 30,000 conversations, 20,000 contacts and 3 mailboxes, after `ANALYZE`,
through the real `dbq` client (extended protocol, 60 runs per case, warm cache, Docker on a laptop).
Text is 60 words per message: 20 drawn from a Zipf-like set of 30 common Dutch words, 40 from a long tail.
Page size 21.

| Query | Matching messages | p50 | p95 |
|---|---|---|---|
| Term in 98% of all messages | 98,228 | 36 ms | 40 ms |
| Term in about 30% | 30,401 | 39 ms | 45 ms |
| Term in 2.7% | 2,725 | 40 ms | 46 ms |
| Rare term | 444 | 8 ms | 9 ms |
| Common term and rare term | | 29 ms | 33 ms |
| Quoted phrase | 0 | 2 ms | 3 ms |
| Contact address fragment | 12 conversations | 7 ms | 10 ms |
| Common term, `status:open` | | 42 ms | 52 ms |
| Rare term, `van:@domain` | | 13 ms | 17 ms |

Plan shape for a rare term (11 ms, `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)`, trimmed):

```
Limit (actual rows=21.00 loops=1)
  CTE hits
    ->  Limit (actual rows=444.00 loops=1)
          ->  Sort (actual rows=444.00 loops=1)
                Sort Key: messages.received_at DESC, messages.id DESC
                ->  Bitmap Heap Scan on messages (actual rows=444.00 loops=1)
                      Filter: ((deleted_at IS NULL) AND (mailbox_id = ANY ('{...}'::uuid[])))
                      ->  Bitmap Index Scan on messages_fts (actual rows=444.00 loops=1)
                            Index Cond: (fts @@ '...'::tsquery)
  ->  Sort (actual rows=21.00 loops=1)
        ->  Nested Loop (actual rows=450.00 loops=1)
              ->  HashAggregate (actual rows=450.00 loops=1)     -- message, subject and contact candidates
                    ->  Append
                          ->  Subquery Scan on msg (from hits: best rank per conversation)
                          ->  Bitmap Index Scan on conversations_subject_trgm
                          ->  Bitmap Index Scan on contacts_name_trgm, then conversations_contact
                          ->  Bitmap Index Scan on contact_addresses_email_trgm, then conversations_contact
              ->  Index Scan using conversations_pkey on conversations (actual rows=1.00 loops=450)
                    Filter: ((deleted_at IS NULL) AND (status = ANY (...)))
Execution Time: 11.163 ms
```

For a term found in most of the mail the `hits` scan switches by itself to a backward scan of
`messages_received_at` that stops after 2,000 matches, so the cost is set by that cap and the ranking of
2,000 rows (about 25 ms), not by the number of matches:

```
  CTE hits
    ->  Limit (actual rows=2000.00 loops=1)
          ->  Index Scan using messages_received_at on messages (actual rows=2000.00 loops=1)
                Filter: ((fts @@ '...'::tsquery) AND (mailbox_id = ANY ('{...}'::uuid[])))
                Rows Removed by Filter: 4651
```

Design notes and limits:

- Only the 2,000 newest matching messages are ranked. When a term occurs in more messages than that, the
  newest conversations are found first and older matches only show up once the query is narrowed with more
  words. Filters (status, label and so on) are applied after the cap, so they do not bring older matches back.
  The subject, contact name and contact address sources are capped at 2,000 conversations each, newest first.
- Conversations are fetched by primary key, one lookup per candidate. `OFFSET 0` in that subquery stops
  the planner from flattening it into a scan of the whole `conversations_mailbox_list` index, which grows
  with the number of conversations.
- A quoted phrase matches on the `echoo_simple` config only. The `dutch` config drops stop words
  (`niet`), so "aangekomen niet" would also match text that never contains "niet".
- New indexes (migration 00014): `messages_received_at` (partial, `deleted_at IS NULL`) for the capped scan and
  `messages_from_addr_trgm` (`lower(from_addr)` trigram, partial on email messages) for `van:`. The trigram
  indexes on `contacts.name` and `contact_addresses.email` come from migration 00015.
- `van:@domain` on its own (no text, every status) takes 36 ms with `messages_from_addr_trgm`; without the
  index it seq-scans all messages (128 ms). `aan:` has no index: on its own it scans every message (95 ms at
  100,000 messages, linear in the message count). Combined with text or other filters it checks candidates only.

Advanced list filters use `ListConversationPageFiltered`, the same lateral-per-mailbox shape as the inbox
list, so each mailbox contributes at most one page of rows from `conversations_mailbox_list`. Requests that use
only the original parameters keep using the original queries above. Typical runs on the same dataset:

| Filter (3 mailboxes, page size 51) | Execution | Buffers |
|---|---|---|
| `status=open&priority=high` | 4.7 ms | 413 |
| `status=open&assignee=none` | 3.4 ms | 422 |
| `has_attachment=true`, every status | 5.5 ms | 2,332 |
| `van:@domain` only, via `messages_from_addr_trgm` | 36 ms | 1,678 |

Saved view counts run the same query with a page size of 1,000 and count the rows, so a count never reads
more than 1,000 index entries per mailbox. The UI shows 999+ from 1,000.

## Reports (`db/queries/reports.sql`, migration 00016)

Measured on PostgreSQL 18 (Docker Desktop VM shared with other containers, default configuration),
with 100,000 conversations spread evenly over 90 days (about 1,100 a day), 3 mailboxes, 21 agents,
10 labels, one inbound and one outbound email each, 80% resolved, 30% labelled, after `ANALYZE`.
The whole period therefore holds all 100,000 conversations, which is the densest case a 90 d report
can meet. Times are wall clock from the service call, all queries of the report included (five
metric queries, first response, resolution, and for the overview the same again for the comparison
period, open now and satisfaction). They vary by about a factor of two between runs on this shared
machine, so ranges are given.

| Report | Vandaag | 7 d | 30 d | 90 d |
|---|---|---|---|---|
| Overview | 30-50 ms | 35-50 ms | 85-115 ms | 115-230 ms |
| Table per agent | 5-10 ms | 18-27 ms | 40-80 ms | 100-195 ms |
| Table per label | 6-9 ms | 22-32 ms | 45-80 ms | 85-120 ms |

Everything up to 30 days stays below 200 ms. The 90-day overview exceeds it now and then on this
machine; the volume, first-response and resolution queries are CPU bound (about 40-65 ms each at
this density), run in parallel and are capped at five in flight service-wide. A daily rollup table
for closed days was built and measured and then removed: it cut the volume queries to 1-3 ms, but
the first-response and resolution queries (about 100 ms together) cannot be rolled up without
storing every duration, so the 90-day overview improved by less than the noise between runs, at the
price of snapshot semantics (a reassignment or relabelling would not reach closed days). Reconsider
when a 90-day report at this density is slower than a second, by rolling up durations as well.

Two indexes from migration 00016 matter most: `conversations_report_created` and
`messages_report_in`/`messages_report_out` turn a 7-day period into range scans, and the partial
index `conversation_events_outbound_created` (a few rows, only conversations that began as an
outbound message) replaces a scan of every event for the first-response anti-join (96 ms to 42 ms
for that query at 90 days).

### Volume, new conversations, 7 days and 90 days

A bitmap scan on `conversations_report_created` for 7 days (7,783 rows, 4.4 ms); for 90 days the
period is the whole table, so the planner reads it sequentially (40.9 ms, 2,223 buffers).

```
HashAggregate (actual rows=1331.00 loops=1)
  Group Key: ((created_at AT TIME ZONE 'Europe/Amsterdam'::text))::date, mailbox_id, assignee_team_id, assignee_user_id
  ->  Seq Scan on conversations c (actual rows=100000.00 loops=1)
        Filter: ((deleted_at IS NULL) AND (status <> 'spam'::text) AND (mailbox_id = ANY (...)) AND ...)
Execution Time: 40.923 ms
```

### Volume, customer messages, 7 days

Bitmap scan on `messages_report_in` for the period, hash-joined to the conversations that are not
spam or deleted (17.2 ms; 61 ms for 90 days). Replies use `messages_report_out` in the same way and
resolutions `conversation_events_report` (16 ms for 7 days, 77 ms for 90 days).

```
HashAggregate (actual rows=154.00 loops=1)
  ->  Hash Join (actual rows=7783.00 loops=1)
        Hash Cond: (c.id = m.conversation_id)
        ->  Seq Scan on conversations c (actual rows=100000.00 loops=1)
              Filter: ((deleted_at IS NULL) AND (status <> 'spam'::text))
        ->  Hash (actual rows=7783.00 loops=1)
              ->  Bitmap Heap Scan on messages m (actual rows=7783.00 loops=1)
                    ->  Bitmap Index Scan on messages_report_in (actual rows=7783.00 loops=1)
                          Index Searches: 3
Execution Time: 17.151 ms
```

### First response and resolution

One pass over the conversations of the period, grouped per day (or per period for tables) with the
durations gathered in arrays that the service turns into medians and percentiles, since business
time needs the schedule (`internal/sla`). About 50-100 ms at 100,000 conversations, of which the
first-response query is the larger.
