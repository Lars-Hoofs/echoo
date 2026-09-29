# SLA, business hours and automation

This describes exactly how deadlines are computed, so that what the timers show can be checked.
Code: `internal/sla` (pure calculator), `internal/automation` (rules, SLA state, assignment).

## Business hours

A schedule (`business_hours`) has an IANA timezone, weekly ranges per weekday, and holidays.

- A range is wall-clock time, `HH:MM` to `HH:MM`; `24:00` is allowed as an end.
- An end before the start is an overnight range (`22:00`-`06:00`): it belongs to the day it starts
  and continues into the next morning.
- Overlapping ranges on a day are merged. A day without ranges is closed.
- A holiday closes a calendar date: every range that starts on it is dropped, including the part of
  an overnight range that would continue into the next day. The morning part of a range that
  started the day before a holiday is kept.
- Business minutes are real elapsed minutes inside those windows. A daylight saving day therefore
  has 23 or 25 hours of wall-clock time, and a `22:00`-`06:00` night is 7 or 9 business hours long.
  A wall-clock time that does not exist (02:30 on the spring change) is resolved by Go's
  `time.Date` to the hour after the gap.
- Exactly one schedule is the workspace default. A mailbox may point to another one. A policy
  without a schedule counts calendar time (24/7).

`Add(t, n)` returns the instant when n business minutes have passed after t; counting starts at the
next opening when t is outside business hours, and a deadline that falls exactly on a closing time
stays there. `Elapsed(a, b)` is the business time between two instants. The two agree:
`Elapsed(t, Add(t, n)) = n`.

## Policies and deadlines

A policy (`sla_policies`) has a first-response target and/or a resolution target in minutes, an
at-risk percentage (default 80) and an optional schedule.

A policy is attached to a conversation in two ways: the mailbox default is applied when the
conversation is created (the `rules.evaluate` job for `conversation_created`, before any rule
runs), and the rule action `apply_sla` applies one at any time (the clocks then start at that
moment). Deadlines are stored on the conversation:

- `first_response_due_at` = start + first-response minutes, in business time.
- `resolution_due_at` = start + resolution minutes, in business time.

Start is `created_at` for the mailbox default and the moment of the action for `apply_sla`.
Changing a policy does not move deadlines of conversations that already have them.

### First response

Met when the first reply by a person is delivered: the send worker sets `first_response_met_at`
(and `first_responded_at`) in the same statement that counts the reply. Automatic replies
(`AutoReplied`, stored as `auto_submitted`) never count. If a policy is applied after a person
already replied, that reply is the first response. A response after the deadline is a breach.

### Resolution

Met when the conversation is closed (`resolved_at`). Closing after the deadline is a breach.
Spam is not a resolution: a spam conversation is finalized without a resolution target.

### Waiting stops the resolution clock

While the status is `waiting` (we wait for the customer) the resolution deadline does not run;
the first-response clock is unaffected. A database trigger stamps `sla_paused_at` when a
conversation enters `waiting` and `sla_resumed_at` when it leaves. The next SLA sweep (within a
minute) turns a finished stay into a later deadline:

    resolution_due_at = Add(resolution_due_at, Elapsed(sla_paused_at, sla_resumed_at))

and clears both stamps. Because this is business time added to the old deadline, five business
hours that were left when the pause began are still five business hours after it.

Limits: a stay shorter than one sweep that ends in `waiting` again overwrites the first stamps, and
the shifted time is rounded down to whole minutes.

### Reopening

When a closed conversation is reopened (by the customer or an agent) and the next
`rules.evaluate` job runs, new clocks start from that moment: new deadlines, `first_response_met_at`
cleared, state recalculated. This needs the previous close to have been finalized by the sweep, so a
conversation reopened within a minute of closing keeps its running clocks.

## State

`conversations.sla_state` is `none` (no policy), `ok`, `at_risk` or `breached`, the worst of the
targets that apply:

- a pending target is `breached` once now is past its deadline;
- it is `at_risk` once the business time left is at most `(100 - at_risk_percent)` percent of the
  target length;
- a met target is `breached` only when it was met late;
- a paused resolution target is ignored.

A job runs every minute (`automation.tick`, one at a time across instances) and updates every
tracked conversation that is not finalized. A closed conversation gets its final state once and is
then finalized (`sla_finalized_at`). When the state worsens it writes a `sla_at_risk` or
`sla_breached` event (`data.target` is `first_response` or `resolution`), notifies the assignee
(notification kind `sla`), wakes open tabs and queues the rules of the same trigger. Each
transition is announced once; going from `breached` back to `at_risk` (after a pause moved the
deadline) is silent.

The list and detail APIs return `sla` (`state` and the due and met timestamps). The web app counts
down to the timestamps itself and re-renders every 30 seconds.

## Rules

Triggers: `conversation_created`, `message_received` (a customer message in an existing
conversation), `conversation_updated` (status, assignment or label change made by a person or a
macro), `sla_at_risk`, `sla_breached`, and `customer_idle` (in status `waiting` for N hours without
a customer reply).

Rules run in order of `position`; `stop_processing` ends the chain when a rule matched. Conditions
are checked against the conversation and the inbound message that caused the trigger. Every
evaluation is recorded in `rule_runs` (30 days), matched or not, with the outcome of each action.

Loop guard: actions run with a rule actor, and only changes by a person queue
`conversation_updated`, so rule actions never trigger rules. Changes made by auto-assignment,
auto-resolve and the SLA sweep are system changes and do not either.

Idle rules look at a conversation once per silence: the run is recorded whether or not the
conditions matched, and a newer message starts a new silence. Conversations more than seven days
past the threshold are not picked up, so purged runs cannot make an old conversation fire again.

### Auto-reply

`auto_reply` sends a canned response or inline text through the normal send queue, marked
`Auto-Submitted: auto-replied`. It is skipped, with the reason on the run, when:

- the message is auto-submitted or bulk/list mail (`Precedence`, `List-Unsubscribe`, `List-Id`);
- the sender looks like `noreply`, `no-reply`, `donotreply`, `mailer-daemon`, `postmaster` or `bounces`;
- the sender is one of our own mailboxes, or the conversation is spam;
- the same rule already replied to the same address in the last 24 hours (claimed atomically).

Variables that cannot be resolved are removed, never sent as `{{placeholders}}`.

## Auto-assignment

Per mailbox: `off`, `round_robin` or `balanced`. New unassigned open conversations are assigned
after the rules ran. Candidates are active, non-readonly members of a team with write access to the
mailbox (limited to the conversation's team when a rule set one), who are `online` and below their
capacity (`users.max_open`, counted over open conversations, empty means no limit). Round robin takes
the agent who was auto-assigned longest ago; balanced takes the fewest open conversations first, then
the same order. Picking and assigning happen under one advisory lock, so capacity holds under
concurrency. When nobody qualifies the conversation stays unassigned and a job retries every
minute for conversations younger than seven days that nobody ever assigned or unassigned.

## Auto-resolve

Setting `auto_resolve_days` (0, the default, is off): conversations in status `waiting` whose last
message and last customer message are older than that many days are closed by the system
(`resolved` event, `data.source` = `system`).

## Macros

A macro is a saved list of the rule actions (not `auto_reply`), personal or workspace-wide. It runs
on a selection with the caller's own mailbox scope, each conversation on its own. Events show the
person and `data.source` = `macro`; one `conversation_updated` evaluation is queued per conversation.
