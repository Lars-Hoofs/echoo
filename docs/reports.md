# Reports and customer satisfaction

Definitions of every number on the Rapportage pages. The code is in `internal/reports` and the
queries in `db/queries/reports.sql`; the tests in `internal/reports/service_test.go` pin each
definition on crafted data. Change a definition here and there together.

## Period, timezone and buckets

- A period is a whole number of local calendar days: Vandaag (today), 7 d, 30 d and 90 d (each
  ending today, so 7 d is today and the six days before it) or a custom first and last day
  (inclusive, at most 366 days). Ranges are half-open in time: midnight of the first day up to
  midnight after the last day.
- Days are cut in the **workspace timezone**, setting `reports.timezone` (default
  `Europe/Amsterdam`, changed by admins under Instellingen, audited). A day with a clock change is
  23 or 25 hours long; nothing is shifted to make it 24. Timestamps are stored in UTC.
- Charts group by day, or by week (Monday to Sunday, the first bucket may start before the
  period) when the period is longer than 92 days or `group=week` is given.
- Every number has a comparison with the period of the same length directly before it (`previous`
  in the API, the percentage in the KPI strip). No percentage is shown when the earlier value is 0.

## Scope

- Admins and owners see every mailbox and every agent.
- Everyone else sees only the mailboxes their teams can read (`policy.MailboxScope`). A mailbox
  filter for another mailbox answers 404, like every other resource.
- Non-admins see their own row in the per-agent table (reports, satisfaction and live) and cannot
  filter on another agent (403). Totals still cover everything in their readable mailboxes.
- Spam and deleted conversations are never counted, except where a definition says otherwise.

## Metrics

| Metric | Definition |
|---|---|
| Nieuwe gesprekken | Conversations with `created_at` in the period, not spam, not deleted. Conversations that began as an outbound message or a campaign (created event reason `outbound` or `campaign`) are left out: nobody wrote in. |
| Klantberichten | Email messages received (`direction = in`) in the period by `received_at`, excluding bounces and automatic mail (`auto_submitted`). |
| Antwoorden | Email messages sent (`direction = out`) by `sent_at`, which is set when the mail was delivered to the mail server; queued, cancelled and failed messages do not count. Automatic replies (rules, satisfaction surveys) are excluded. |
| Opgelost | `resolved` events in the period (a conversation resolved twice counts twice). |
| Heropend | `reopened` events in the period whose previous status event of that conversation was a `resolved`. A customer reply to a *waiting* conversation is also recorded as `reopened` by ingest; it does not count here. |
| Eerste reactie | For conversations created in the period: `first_responded_at` minus `created_at`, taken as **median** and **p90** over all answered conversations. `first_responded_at` is the first non-automatic reply that was delivered. Conversations that began as an outbound message or a campaign (created event reason `outbound` or `campaign`) are left out, since nobody was waiting. Unanswered conversations are not in the median. |
| Oplostijd | For conversations currently closed whose `resolved_at` falls in the period: `resolved_at` minus `created_at`, median and p90. A conversation that was reopened and resolved again counts with its latest resolution and the original creation time. |
| Binnen SLA (eerste reactie) | Of the conversations created in the period with a first-response target: those answered by `first_response_due_at`, out of those answered or already past the target. Conversations still inside their target are not yet judged. |
| Binnen SLA (oplossing) | Of the conversations resolved in the period that have `resolution_due_at`: those resolved by then. The due time already shifts for waiting periods (the SLA sweep moves it). |
| Open nu | Open conversations that are not snoozed, right now, within the filters. |
| Tevredenheid | See below. |

### Lifecycle and what is drawn on Overzicht

The overview also answers where the conversations of the period stand now. The cohort is the
conversations created in the period that are not deleted and did not begin as an outbound message
or a campaign, **spam included**. Each one is counted once by two facts: whether we sent a first
reply (`first_responded_at`) and its current status.

| Field (`lifecycle`) | Definition |
|---|---|
| `answered.open / waiting / closed` | First reply sent; status now open, waiting (on the customer) or closed. |
| `unanswered.open` | No first reply, status open: **the customer is waiting for us**. This is the one number the accent colour marks. |
| `unanswered.waiting / closed` | No first reply, but put on waiting or closed anyway (for example closed without a reply). |
| `spam` | Marked as spam. |
| `arrived` | `spam` plus every answered and unanswered conversation. |

`answered` plus `unanswered` always equals "Nieuwe gesprekken"; the flow diagram and the volume chart
are drawn from these counts and add up by construction (a test pins it). `series[].unanswered` is
`unanswered.open` per chart bucket, by creation day, which is the solid bar of "Volume per dag".
Because it follows the current status, a day's solid part shrinks when its conversations are
answered later.

`first_response_target` is the **median of the first-response targets** (`first_response_minutes`
of the SLA policy) over the conversations that count for the first-response time and have a policy
with such a target; `count` says how many. Without one, `seconds` is null. Targets of
business-hours policies are business minutes, like the times they are compared with.

### Percentiles

Median and p90 interpolate linearly between the two closest ranks (the same as PostgreSQL's
`percentile_cont`): for the sorted values 10, 20, 30, 40, 50 the median is 30 and the p90 is 46.

### Time basis: wall clock or business hours

Response and resolution times are **business time** for a conversation whose SLA policy has a
business-hours schedule (`sla_policies.business_hours_id`), computed with the same calculator as
the SLA (`sla.Schedule.Elapsed`): only the minutes inside opening hours count, holidays are closed.
Conversations without a policy, or with a policy without business hours, are measured in **wall
clock** time. A mixed period mixes both in one median; the API reports `time_basis` as
`wall_clock`, `business_hours` or `mixed`, and the page says which.

Waiting periods are **not** deducted from the resolution time. The SLA only keeps the latest pause
(`sla_paused_at`, `sla_resumed_at`), so the full history would have to be rebuilt from status
events; the SLA percentage for resolution does account for them because its due time is shifted.

### Attribution in tables

- Per agent: new conversations, customer messages, reopenings and both time metrics belong to the
  conversation's **current assignee** (a conversation with no assignee is the "Niet toegewezen"
  row, admins only). Replies belong to the message's author, resolutions to the person who
  resolved. Conversations resolved by a rule have no person.
- Per team and per mailbox: the conversation's current team and mailbox.
- Per label: every label on the conversation; a conversation with two labels counts under both,
  so label rows do not add up to the total. Conversations without a label form one row.
- Attribution follows the current state, not the state at the time; reassigning a conversation
  moves its history with it.

## Live view

Open (not snoozed), unassigned open, waiting, and the conversations whose SLA state is `at_risk`
or `breached` right now (open and waiting ones); the page shows the sum as "SLA-risico" and the
breached ones apart. Per agent: open, waiting and SLA-risk conversations assigned to them. The
mailbox, team, agent and label filters all narrow the live numbers.

`attention` is what the insight card on Overzicht states. It covers open, not snoozed
conversations **without a first reply from us**: `unanswered` (all of them), `breached` (first-response
due time passed), `due_soon` (due within the next 60 minutes, `window_minutes`), `next_due_seconds`
(until the first of those), `oldest_seconds` (age of the longest-waiting one), `unassigned` and
`holders` (people holding a breached or due soon conversation, largest first; a non-admin sees only
themselves in this list, the counts stay complete). Conversations without an SLA policy have no due
time, so they appear in `unanswered` only.

**Online** means the person has an event stream (the app open) on this server,
from the realtime hub; **beschikbaarheid** is the status they set themselves. With several server
instances only the local streams are seen. The page refetches every 30 seconds.

## Satisfaction (CSAT)

- Off by default, per mailbox (Instellingen, Mailboxen). Settings: on/off and a delay of 0 to 24
  hours after resolving. Switching it on starts counting from that moment: conversations resolved
  earlier are never surveyed, and neither are conversations resolved more than three days (plus the
  delay) before the sweep looks at them.
- A sweep runs every minute (`csat.sweep`). For each conversation that is closed, past its delay and
  without a survey row, it queues **one** email through the normal send path as an automated message
  (`auto_submitted`, so it never counts as a reply or a first response), in the branded layout, in
  the same thread, with five rating links. A conversation is surveyed at most once, even if it is
  reopened and resolved again.
- Nothing is sent, and the conversation is marked with the reason, when: the customer's last
  message was an automatic reply, bounce or bulk mail; the sender address is a no-reply,
  mailer-daemon or one of our own mailboxes; or the conversation has no customer message at all.
  Spam conversations are not `closed` and never qualify.
- The links go to `ECHOO_BASE_URL/tevredenheid/<token>?r=<1-5>`. Opening a link records nothing (mail
  scanners follow links); the page shows the rating preselected with an optional comment and a
  confirm button. The token is `conversation id + expiry` under an HMAC-SHA256 (key derived from the
  encryption keyring), valid for 30 days, and its SHA-256 must match the one stored with the
  survey. One answer exists per conversation; the customer can change it for 7 days after the first
  answer.
- Reports count surveys in the period they were **sent**: *verstuurd* is the number sent,
  *beantwoord* the number with an answer, *responspercentage* their ratio, *gemiddelde* the mean
  rating of the answers. Ratings appear in the conversation ("Tevredenheid: 4 van 5") and in the
  Tevredenheid tab with the latest comments.
- A rating of 1 or 2 creates an in-app notification for the conversation's assignee: on the first
  low answer, and again if a better answer is changed into a low one (not for one low answer
  replaced by another).

## Export

`GET /api/v1/reports/{kind}/export` streams a CSV with the same filters as the page. Headers are
Dutch snake case, durations are whole seconds, rates are percentages with one decimal, and text
cells starting with `=`, `+`, `-`, `@`, tab or carriage return get a leading apostrophe so
spreadsheets do not evaluate them. An export is written to the audit log. The numbers are computed
before the first byte is sent, so a failure gives a normal error instead of a truncated file.

## Performance

Reports read `conversations`, `conversation_events` and `messages` directly, through the indexes of
migration 00016, and the numbers are always current (nothing is pre-aggregated). See
`docs/queries.md` for the measurements at 100,000 conversations and why no rollup table exists.
