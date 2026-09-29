package reports

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	svc    *Service
	loc    *time.Location
	box    pgtype.UUID
	other  pgtype.UUID
	agentA pgtype.UUID
	agentB pgtype.UUID
	team   pgtype.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	f := &fixture{t: t, pool: pool, svc: New(pool), loc: amsterdam(t)}
	f.box = f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@shop.example') RETURNING id`)
	f.other = f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Sales', 'sales@shop.example') RETURNING id`)
	f.agentA = f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('a@shop.example', 'Anna', 'agent', 'x') RETURNING id`)
	f.agentB = f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('b@shop.example', 'Bram', 'agent', 'x') RETURNING id`)
	f.team = f.id(`INSERT INTO teams (name) VALUES ('Support') RETURNING id`)
	return f
}

func (f *fixture) id(sql string, args ...any) pgtype.UUID {
	f.t.Helper()
	var id pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

// local builds a time in the workspace timezone.
func (f *fixture) local(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, f.loc)
}

type conv struct {
	mailbox    pgtype.UUID
	created    time.Time
	status     string
	assignee   pgtype.UUID
	team       pgtype.UUID
	firstResp  time.Duration // after created; zero means never
	resolved   time.Duration // after created; zero means not resolved
	outbound   bool          // the conversation began as an outbound message
	campaign   bool          // the conversation was created with the reason "campaign"
	deleted    bool
	policy     pgtype.UUID
	frDue      time.Duration
	resDue     time.Duration
	resolvedBy pgtype.UUID
}

func (f *fixture) conversation(c conv) pgtype.UUID {
	f.t.Helper()
	if !c.mailbox.Valid {
		c.mailbox = f.box
	}
	if c.status == "" {
		c.status = "open"
		if c.resolved > 0 {
			c.status = "closed"
		}
	}
	at := func(d time.Duration) *time.Time {
		if d == 0 {
			return nil
		}
		t := c.created.Add(d)
		return &t
	}
	var deleted *time.Time
	if c.deleted {
		deleted = &c.created
	}
	id := f.id(`INSERT INTO conversations (mailbox_id, subject, status, assignee_user_id, assignee_team_id, created_at, last_message_at,
			first_responded_at, first_response_met_at, first_response_due_at, resolved_at, resolution_due_at, sla_policy_id, deleted_at)
		VALUES ($1, 's', $2, $3, $4, $5, $5, $6, $6, $7, $8, $9, $10, $11) RETURNING id`,
		c.mailbox, c.status, c.assignee, c.team, c.created, at(c.firstResp), at(c.frDue), at(c.resolved), at(c.resDue), c.policy, deleted)
	reason := "new_conversation"
	if c.outbound {
		reason = "outbound"
	}
	if c.campaign {
		reason = "campaign"
	}
	f.event(id, c.mailbox, "created", c.created, pgtype.UUID{}, `{"reason":"`+reason+`"}`)
	if c.resolved > 0 {
		f.event(id, c.mailbox, "resolved", c.created.Add(c.resolved), c.resolvedBy, `{"from":"open"}`)
	}
	return id
}

func (f *fixture) event(conv, mailbox pgtype.UUID, typ string, at time.Time, actor pgtype.UUID, data string) {
	f.t.Helper()
	f.exec(`INSERT INTO conversation_events (conversation_id, mailbox_id, actor_user_id, type, data, created_at) VALUES ($1, $2, $3, $4, $5::jsonb, $6)`,
		conv, mailbox, actor, typ, data, at)
}

func (f *fixture) message(conv, mailbox pgtype.UUID, direction string, at time.Time, opts msg) {
	f.t.Helper()
	var sentAt *time.Time
	if direction == "out" && !opts.unsent {
		sentAt = &at
	}
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, received_at, sent_at, auto_submitted, is_bounce, author_user_id)
		VALUES ($1, $2, 'email', $3, 'm-' || uuidv7()::text, $4, $5, $6, $7, $8)`,
		conv, mailbox, direction, at, sentAt, opts.auto, opts.bounce, opts.author)
}

type msg struct {
	auto, bounce, unsent bool
	author               pgtype.UUID
}

func (f *fixture) filter(mailboxes ...pgtype.UUID) Filter {
	if len(mailboxes) == 0 {
		mailboxes = []pgtype.UUID{f.box}
	}
	return Filter{Mailboxes: mailboxes}
}

func (f *fixture) rng(preset Preset, from, to string) Range {
	f.t.Helper()
	now := f.local(2026, time.September, 28, 12, 0)
	r, err := ResolveRange(now, f.loc, preset, from, to, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func seconds(m time.Duration) float64 { return m.Seconds() }

func TestFirstResponseAndResolutionTimes(t *testing.T) {
	f := newFixture(t)
	base := f.local(2026, time.September, 15, 9, 0)
	for i, m := range []time.Duration{10, 20, 30, 40, 50} {
		f.conversation(conv{created: base.Add(time.Duration(i) * time.Hour), firstResp: m * time.Minute, resolved: time.Duration(i+1) * time.Hour})
	}
	// The fifth resolution takes ten hours instead of five, to make the percentile interesting.
	f.exec(`UPDATE conversations SET resolved_at = created_at + interval '10 hours' WHERE created_at = $1`, base.Add(4*time.Hour))

	// None of these count towards response or resolution times.
	f.conversation(conv{created: base, status: "spam", firstResp: time.Minute})
	f.conversation(conv{created: base, deleted: true, firstResp: time.Minute})
	f.conversation(conv{created: base, mailbox: f.other, firstResp: time.Minute})
	f.conversation(conv{created: base, outbound: true, firstResp: time.Second})
	f.conversation(conv{created: base, campaign: true, firstResp: time.Second})
	f.conversation(conv{created: base})

	o, err := f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-09-15", "2026-09-15"), f.filter())
	if err != nil {
		t.Fatal(err)
	}
	c := o.Current
	if c.New != 6 {
		t.Errorf("new conversations: %d, want 6 (the five answered ones and the unanswered one; outbound and campaign conversations are nobody writing in)", c.New)
	}
	if fr := c.FirstResponse; fr.Count != 5 || fr.Median != seconds(30*time.Minute) || !near(fr.P90, seconds(46*time.Minute)) {
		t.Errorf("first response: %+v", fr)
	}
	// 1, 2, 3, 4 and 10 hours: median 3 h; p90 sits 60% of the way from 4 h to 10 h.
	if rs := c.Resolution; rs.Count != 5 || rs.Median != seconds(3*time.Hour) || !near(rs.P90, seconds(4*time.Hour)+0.6*seconds(6*time.Hour)) {
		t.Errorf("resolution: %+v", rs)
	}
	if o.Basis != BasisWallClock {
		t.Errorf("basis %s", o.Basis)
	}
}

func TestVolumeCountsOnlyWhatIsDefined(t *testing.T) {
	f := newFixture(t)
	at := f.local(2026, time.September, 16, 10, 0)
	c := f.conversation(conv{created: at})
	for _, m := range []msg{{}, {}, {auto: true}, {bounce: true}} {
		f.message(c, f.box, "in", at, m)
	}
	for _, m := range []msg{{author: f.agentA}, {author: f.agentB}, {auto: true}, {unsent: true, author: f.agentA}} {
		f.message(c, f.box, "out", at.Add(time.Hour), m)
	}
	f.message(c, f.other, "in", at, msg{})
	f.message(c, f.box, "in", at.AddDate(0, 0, -30), msg{})

	o, err := f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-09-16", "2026-09-16"), f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if o.Current.CustomerMessages != 2 || o.Current.Replies != 2 {
		t.Errorf("customer messages %d, replies %d; want 2 and 2", o.Current.CustomerMessages, o.Current.Replies)
	}

	rows, _, err := f.svc.Groups(context.Background(), f.rng(PresetCustom, "2026-09-16", "2026-09-16"), f.filter(), DimAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Name] = r.Replies
	}
	if got["Anna"] != 1 || got["Bram"] != 1 {
		t.Errorf("replies per author: %v", got)
	}
}

func TestDayBucketsFollowTheWorkspaceTimezoneAcrossDST(t *testing.T) {
	f := newFixture(t)
	utc := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, time.UTC) }
	for _, at := range []time.Time{
		utc(2026, 3, 28, 22, 30), // 23:30 CET on the 28th
		utc(2026, 3, 28, 23, 30), // 00:30 CET on the 29th
		utc(2026, 3, 29, 21, 30), // 23:30 CEST on the 29th, in a day of 23 hours
		utc(2026, 3, 29, 22, 30), // 00:30 CEST on the 30th
		utc(2026, 3, 30, 21, 59), // 23:59 CEST on the 30th
		utc(2026, 3, 30, 22, 0),  // midnight into the 31st: outside the range
	} {
		f.conversation(conv{created: at})
	}
	o, err := f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-03-28", "2026-03-30"), f.filter())
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, p := range o.Series {
		got = append(got, p.New)
	}
	if want := []int64{1, 2, 2}; !equalInts(got, want) {
		t.Errorf("new per day %v, want %v", got, want)
	}
	if o.Current.New != 5 {
		t.Errorf("total %d, want 5", o.Current.New)
	}

	// Clocks go back on 25 October 2026: both instants below fall on the same 25-hour day.
	for _, at := range []time.Time{utc(2026, 10, 24, 22, 30), utc(2026, 10, 25, 22, 30)} {
		f.conversation(conv{created: at})
	}
	o, err = f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-10-24", "2026-10-26"), f.filter())
	if err != nil {
		t.Fatal(err)
	}
	got = got[:0]
	for _, p := range o.Series {
		got = append(got, p.New)
	}
	if want := []int64{0, 2, 0}; !equalInts(got, want) {
		t.Errorf("new per day in October %v, want %v", got, want)
	}
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBusinessHoursDurations(t *testing.T) {
	f := newFixture(t)
	hours := f.id(`INSERT INTO business_hours (name, timezone, weekly) VALUES ('Kantoor', 'Europe/Amsterdam',
		'{"mon":[{"start":"09:00","end":"17:00"}],"tue":[{"start":"09:00","end":"17:00"}],"wed":[{"start":"09:00","end":"17:00"}],"thu":[{"start":"09:00","end":"17:00"}],"fri":[{"start":"09:00","end":"17:00"}]}') RETURNING id`)
	policy := f.id(`INSERT INTO sla_policies (name, first_response_minutes, business_hours_id) VALUES ('Kantoor', 240, $1) RETURNING id`, hours)

	// Friday 16:00 to Monday 10:00 is two business hours, and 66 hours of wall-clock time.
	fri := f.local(2026, time.September, 18, 16, 0)
	f.conversation(conv{created: fri, firstResp: 66 * time.Hour, policy: policy})

	r := f.rng(PresetCustom, "2026-09-18", "2026-09-18")
	o, err := f.svc.Overview(context.Background(), r, f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if fr := o.Current.FirstResponse; fr.Count != 1 || fr.Median != seconds(2*time.Hour) {
		t.Errorf("business time: %+v", fr)
	}
	if o.Basis != BasisBusinessHours {
		t.Errorf("basis %s", o.Basis)
	}

	f.conversation(conv{created: fri, firstResp: 90 * time.Minute})
	o, err = f.svc.Overview(context.Background(), r, f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if fr := o.Current.FirstResponse; fr.Count != 2 || fr.Median != seconds(105*time.Minute) {
		t.Errorf("mixed: %+v", fr)
	}
	if o.Basis != BasisMixed {
		t.Errorf("basis %s", o.Basis)
	}
}

func TestSLAAchievement(t *testing.T) {
	f := newFixture(t)
	policy := f.id(`INSERT INTO sla_policies (name, first_response_minutes, resolution_minutes) VALUES ('P', 60, 480) RETURNING id`)
	base := f.local(2026, time.September, 17, 9, 0)
	// Answered in time, answered late, never answered but overdue, and not yet due.
	f.conversation(conv{created: base, policy: policy, frDue: time.Hour, firstResp: 30 * time.Minute, resDue: 8 * time.Hour, resolved: 7 * time.Hour})
	f.conversation(conv{created: base, policy: policy, frDue: time.Hour, firstResp: 90 * time.Minute, resDue: 8 * time.Hour, resolved: 9 * time.Hour})
	f.conversation(conv{created: base, policy: policy, frDue: time.Hour})
	f.conversation(conv{created: time.Now().Add(-time.Minute), policy: policy, frDue: 60 * time.Minute})
	f.conversation(conv{created: base})

	r := f.rng(PresetCustom, "2026-09-17", "2026-09-17")
	o, err := f.svc.Overview(context.Background(), r, f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Current.FirstResponseSLA; got != (Rate{Met: 1, Total: 3}) {
		t.Errorf("first response SLA %+v, want 1 of 3", got)
	}
	if got := o.Current.ResolutionSLA; got != (Rate{Met: 1, Total: 2}) {
		t.Errorf("resolution SLA %+v, want 1 of 2", got)
	}
}

func TestReopenedCountsOnlyReopenedResolutions(t *testing.T) {
	f := newFixture(t)
	at := f.local(2026, time.September, 18, 9, 0)
	c := f.conversation(conv{created: at, resolved: time.Hour})
	f.event(c, f.box, "reopened", at.Add(2*time.Hour), pgtype.UUID{}, `{"to":"open"}`)
	f.event(c, f.box, "status_changed", at.Add(3*time.Hour), pgtype.UUID{}, `{"from":"open","to":"waiting"}`)
	// A customer reply to a waiting conversation is recorded as reopened too, but was not resolved.
	f.event(c, f.box, "reopened", at.Add(4*time.Hour), pgtype.UUID{}, `{}`)
	f.event(c, f.box, "resolved", at.Add(5*time.Hour), pgtype.UUID{}, `{"from":"open"}`)
	f.event(c, f.box, "reopened", at.Add(6*time.Hour), pgtype.UUID{}, `{}`)

	o, err := f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-09-18", "2026-09-18"), f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if o.Current.Reopened != 2 || o.Current.Resolved != 2 {
		t.Errorf("reopened %d, resolved %d; want 2 and 2", o.Current.Reopened, o.Current.Resolved)
	}
}

func TestFiltersAndGroups(t *testing.T) {
	f := newFixture(t)
	label := f.id(`INSERT INTO labels (name, color_token) VALUES ('Facturen', 'blue') RETURNING id`)
	at := f.local(2026, time.September, 19, 9, 0)
	a1 := f.conversation(conv{created: at, assignee: f.agentA, team: f.team, firstResp: 10 * time.Minute, resolved: time.Hour, resolvedBy: f.agentA})
	f.conversation(conv{created: at, assignee: f.agentA, firstResp: 20 * time.Minute})
	f.conversation(conv{created: at, assignee: f.agentB, team: f.team, firstResp: 40 * time.Minute})
	f.conversation(conv{created: at})
	f.conversation(conv{created: at, mailbox: f.other, assignee: f.agentB})
	f.exec(`INSERT INTO conversation_labels (conversation_id, label_id) VALUES ($1, $2)`, a1, label)

	r := f.rng(PresetCustom, "2026-09-19", "2026-09-19")
	ctx := context.Background()

	rows, _, err := f.svc.Groups(ctx, r, f.filter(f.box, f.other), DimAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Row{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if len(rows) != 3 || byName["Anna"].New != 2 || byName["Bram"].New != 2 || byName[""].New != 1 || byName["Anna"].Resolved != 1 {
		t.Errorf("agents: %+v", rows)
	}
	if fr := byName["Anna"].FirstResponse; fr.Median != seconds(15*time.Minute) {
		t.Errorf("Anna's median: %+v", fr)
	}

	// An agent who may see only their own row.
	mine := f.agentA.String()
	rows, _, err = f.svc.Groups(ctx, r, f.filter(f.box, f.other), DimAgent, func(key string) bool { return key == mine })
	if err != nil || len(rows) != 1 || rows[0].Name != "Anna" {
		t.Errorf("own row only: %+v %v", rows, err)
	}

	rows, _, err = f.svc.Groups(ctx, r, f.filter(f.box, f.other), DimMailbox, nil)
	if err != nil || len(rows) != 2 || rows[0].Name != "Support" || rows[0].New != 4 || rows[1].Name != "Sales" || rows[1].New != 1 {
		t.Errorf("mailboxes: %+v %v", rows, err)
	}
	rows, _, err = f.svc.Groups(ctx, r, f.filter(), DimTeam, nil)
	if err != nil || len(rows) != 2 || rows[0].Name != "Support" || rows[0].New != 2 || rows[1].ID != "" || rows[1].New != 2 {
		t.Errorf("teams: %+v %v", rows, err)
	}
	rows, _, err = f.svc.Groups(ctx, r, f.filter(), DimLabel, nil)
	if err != nil || len(rows) != 2 || rows[0].ID != "" || rows[0].New != 3 || rows[1].Name != "Facturen" || rows[1].New != 1 {
		t.Errorf("labels: %+v %v", rows, err)
	}

	for name, fl := range map[string]Filter{
		"agent": {Mailboxes: []pgtype.UUID{f.box}, Agent: f.agentB},
		"team":  {Mailboxes: []pgtype.UUID{f.box}, Team: f.team},
	} {
		o, err := f.svc.Overview(ctx, r, fl)
		if err != nil || o.Current.New != 1+map[string]int64{"agent": 0, "team": 1}[name] {
			t.Errorf("%s filter: %d %v", name, o.Current.New, err)
		}
	}
	o, err := f.svc.Overview(ctx, r, Filter{Mailboxes: []pgtype.UUID{f.box}, Label: label})
	if err != nil || o.Current.New != 1 {
		t.Errorf("label filter: %d %v", o.Current.New, err)
	}
	o, err = f.svc.Overview(ctx, r, Filter{})
	if err != nil || o.Current.New != 0 || len(o.Series) != 1 {
		t.Errorf("no readable mailbox: %+v %v", o.Current, err)
	}
}

func TestOverviewComparesWithThePreviousPeriod(t *testing.T) {
	f := newFixture(t)
	for _, day := range []int{14, 15, 16, 17} {
		f.conversation(conv{created: f.local(2026, time.September, day, 9, 0)})
	}
	f.conversation(conv{created: f.local(2026, time.September, 15, 10, 0)})
	o, err := f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-09-16", "2026-09-17"), f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if o.Current.New != 2 || o.Previous.New != 3 {
		t.Errorf("current %d, previous %d; want 2 and 3", o.Current.New, o.Previous.New)
	}
}

func TestLive(t *testing.T) {
	f := newFixture(t)
	at := time.Now().Add(-time.Hour)
	f.conversation(conv{created: at, assignee: f.agentA})
	f.conversation(conv{created: at, assignee: f.agentA})
	f.conversation(conv{created: at})
	f.conversation(conv{created: at, status: "waiting", assignee: f.agentB})
	f.conversation(conv{created: at, resolved: time.Minute})
	f.conversation(conv{created: at, mailbox: f.other})
	snoozed := f.conversation(conv{created: at, assignee: f.agentB})
	f.exec(`UPDATE conversations SET snoozed_until = now() + interval '1 day' WHERE id = $1`, snoozed)
	f.exec(`UPDATE conversations SET sla_state = 'at_risk' WHERE assignee_user_id = $1 AND status = 'open'`, f.agentA)
	f.exec(`UPDATE conversations SET sla_state = 'breached' WHERE status = 'waiting'`)

	live, err := f.svc.Live(context.Background(), f.filter(), map[string]bool{f.agentB.String(): true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if live.Open != 3 || live.Unassigned != 1 || live.Waiting != 1 || live.SLAAtRisk != 2 || live.SLABreached != 1 {
		t.Errorf("counts: %+v", live)
	}
	if len(live.Agents) != 2 {
		t.Fatalf("agents: %+v", live.Agents)
	}
	if a := live.Agents[0]; a.Name != "Anna" || a.Open != 2 || a.SLARisk != 2 || a.Online {
		t.Errorf("first agent: %+v", a)
	}
	if b := live.Agents[1]; b.Name != "Bram" || b.Open != 0 || b.Waiting != 1 || !b.Online {
		t.Errorf("second agent: %+v", b)
	}

	own := f.agentA.String()
	live, err = f.svc.Live(context.Background(), f.filter(), nil, func(id string) bool { return id == own })
	if err != nil || len(live.Agents) != 1 || live.Agents[0].Name != "Anna" || live.Open != 3 {
		t.Errorf("own row only: %+v %v", live, err)
	}
}

func TestWeeklySeriesGroupsByMondayAndKeepsMedians(t *testing.T) {
	f := newFixture(t)
	f.conversation(conv{created: f.local(2026, time.September, 14, 9, 0), firstResp: 10 * time.Minute})   // Monday
	f.conversation(conv{created: f.local(2026, time.September, 20, 23, 0), firstResp: 30 * time.Minute})  // Sunday
	f.conversation(conv{created: f.local(2026, time.September, 21, 0, 30), firstResp: 100 * time.Minute}) // next Monday
	r, err := ResolveRange(f.local(2026, time.September, 28, 12, 0), f.loc, PresetCustom, "2026-09-14", "2026-09-27", GroupWeek)
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.svc.Overview(context.Background(), r, f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Series) != 2 || o.Series[0].Day != "2026-09-14" || o.Series[1].Day != "2026-09-21" {
		t.Fatalf("series: %+v", o.Series)
	}
	if a, b := o.Series[0], o.Series[1]; a.New != 2 || b.New != 1 ||
		a.FirstResponse.Median != seconds(20*time.Minute) || b.FirstResponse.Median != seconds(100*time.Minute) {
		t.Errorf("weeks: %+v %+v", a, b)
	}
	if o.Current.New != 3 || o.Current.FirstResponse.Median != seconds(30*time.Minute) {
		t.Errorf("totals: %+v", o.Current)
	}
}

func TestSatisfactionReport(t *testing.T) {
	f := newFixture(t)
	sent := f.local(2026, time.September, 22, 10, 0)
	survey := func(c conv, rating int, comment string, at time.Time) {
		id := f.conversation(c)
		f.exec(`INSERT INTO csat_requests (conversation_id, mailbox_id, token_hash, sent_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
			id, f.box, id.Bytes[:], at, at.Add(30*24*time.Hour))
		if rating > 0 {
			f.exec(`INSERT INTO csat_responses (conversation_id, mailbox_id, rating, comment, token_hash) VALUES ($1, $2, $3, $4, $5)`,
				id, f.box, rating, comment, id.Bytes[:])
		}
	}
	anna, bram := conv{created: sent.Add(-time.Hour), assignee: f.agentA}, conv{created: sent.Add(-time.Hour), assignee: f.agentB}
	survey(anna, 5, "Top", sent)
	survey(anna, 4, "", sent)
	survey(anna, 0, "", sent)
	survey(bram, 2, "Traag", sent)
	survey(bram, 0, "", sent)
	// Sent outside the period, in another mailbox and for a deleted conversation: none count.
	survey(anna, 1, "buiten periode", sent.AddDate(0, 0, -10))
	deleted := anna
	deleted.deleted = true
	survey(deleted, 1, "verwijderd", sent)
	elsewhere := anna
	elsewhere.mailbox = f.other
	other := f.conversation(elsewhere)
	f.exec(`INSERT INTO csat_requests (conversation_id, mailbox_id, token_hash, sent_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		other, f.other, other.Bytes[:], sent, sent.Add(30*24*time.Hour))

	r := f.rng(PresetCustom, "2026-09-22", "2026-09-22")
	rep, err := f.svc.CSAT(context.Background(), r, f.filter(), DimAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Summary
	if s.Sent != 5 || s.Responses != 3 || !near(s.Average, 11.0/3) || s.Distribution != [5]int{0, 1, 0, 1, 1} {
		t.Errorf("summary: %+v", s)
	}
	if rate, ok := s.ResponseRate(); !ok || !near(rate, 60) {
		t.Errorf("response rate %v %v", rate, ok)
	}
	if len(rep.Rows) != 2 || rep.Rows[0].Name != "Anna" || rep.Rows[0].Sent != 3 || !near(rep.Rows[0].Average, 4.5) ||
		rep.Rows[1].Name != "Bram" || rep.Rows[1].Responses != 1 || !near(rep.Rows[1].Average, 2) {
		t.Errorf("rows: %+v", rep.Rows)
	}
	if len(rep.Comments) != 2 || rep.Comments[0].Text == "" {
		t.Errorf("comments: %+v", rep.Comments)
	}
	if len(rep.Series) != 1 || rep.Series[0].Sent != 5 {
		t.Errorf("series: %+v", rep.Series)
	}

	own := f.agentB.String()
	rep, err = f.svc.CSAT(context.Background(), r, f.filter(), DimAgent, func(key string) bool { return key == own })
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Name != "Bram" {
		t.Errorf("own row only: %+v %v", rep.Rows, err)
	}
	o, err := f.svc.Overview(context.Background(), r, f.filter())
	if err != nil || o.CSAT.Sent != 5 || o.CSAT.Responses != 3 {
		t.Errorf("overview summary: %+v %v", o.CSAT, err)
	}
}

func TestLifecycleAddsUpToTheNewConversations(t *testing.T) {
	f := newFixture(t)
	day1 := f.local(2026, time.September, 15, 9, 0)
	day2 := f.local(2026, time.September, 16, 9, 0)
	// Day 1: answered and closed, answered and waiting, answered and open again, unanswered and open.
	f.conversation(conv{created: day1, firstResp: 10 * time.Minute, resolved: time.Hour})
	f.conversation(conv{created: day1, firstResp: 10 * time.Minute, status: "waiting"})
	f.conversation(conv{created: day1, firstResp: 10 * time.Minute})
	f.conversation(conv{created: day1})
	// Day 2: closed without a reply, waiting without a reply, spam, and two open unanswered.
	f.conversation(conv{created: day2, resolved: time.Hour})
	f.conversation(conv{created: day2, status: "waiting"})
	f.conversation(conv{created: day2, status: "spam"})
	f.conversation(conv{created: day2})
	f.conversation(conv{created: day2})
	// Not part of the cohort: deleted, outbound, campaign, other mailbox, outside the period.
	f.conversation(conv{created: day2, deleted: true})
	f.conversation(conv{created: day2, outbound: true})
	f.conversation(conv{created: day2, campaign: true})
	f.conversation(conv{created: day2, mailbox: f.other})
	f.conversation(conv{created: day1.AddDate(0, 0, -5)})

	o, err := f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-09-15", "2026-09-16"), f.filter())
	if err != nil {
		t.Fatal(err)
	}
	l := o.Lifecycle
	if l.Spam != 1 || l.Answered != (StatusCounts{Open: 1, Waiting: 1, Closed: 1}) || l.Unanswered != (StatusCounts{Open: 3, Waiting: 1, Closed: 1}) {
		t.Errorf("lifecycle: %+v", l)
	}
	if l.Arrived() != 9 {
		t.Errorf("arrived %d, want 9", l.Arrived())
	}
	if got := l.Answered.Total() + l.Unanswered.Total(); got != o.Current.New {
		t.Errorf("answered + unanswered = %d, new conversations = %d; the flow must add up", got, o.Current.New)
	}
	if len(o.Series) != 2 || o.Series[0].Unanswered != 1 || o.Series[1].Unanswered != 2 {
		t.Errorf("unanswered per day: %+v", o.Series)
	}
}

func TestFirstResponseTargetIsTheMedianOfThePolicies(t *testing.T) {
	f := newFixture(t)
	at := f.local(2026, time.September, 15, 9, 0)
	target := func(minutes int) pgtype.UUID {
		return f.id(`INSERT INTO sla_policies (name, first_response_minutes) VALUES ($1, $2) RETURNING id`, "P"+time.Duration(minutes).String(), minutes)
	}
	p30, p60, p240 := target(30), target(60), target(240)
	for _, p := range []pgtype.UUID{p30, p60, p60, p240, p240} {
		f.conversation(conv{created: at, policy: p})
	}
	resolutionOnly := f.id(`INSERT INTO sla_policies (name, resolution_minutes) VALUES ('Oplossing', 480) RETURNING id`)
	f.conversation(conv{created: at, policy: resolutionOnly})
	f.conversation(conv{created: at})
	f.conversation(conv{created: at, policy: p30, status: "spam"})

	r := f.rng(PresetCustom, "2026-09-15", "2026-09-15")
	o, err := f.svc.Overview(context.Background(), r, f.filter())
	if err != nil {
		t.Fatal(err)
	}
	if got := o.FirstResponseTarget; got.Count != 5 || got.Seconds != seconds(60*time.Minute) {
		t.Errorf("target: %+v, want 5 conversations and 60 minutes", got)
	}
	o, err = f.svc.Overview(context.Background(), f.rng(PresetCustom, "2026-09-20", "2026-09-20"), f.filter())
	if err != nil || o.FirstResponseTarget.Count != 0 {
		t.Errorf("no policies in range: %+v %v", o.FirstResponseTarget, err)
	}
}

func TestLiveAttentionAndFilters(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	created := now.Add(-3 * time.Hour)
	// The due time is relative to creation: frDue is what lies between created and due.
	due := func(fromNow time.Duration) time.Duration { return now.Add(fromNow).Sub(created) }
	f.conversation(conv{created: created, assignee: f.agentA, team: f.team, frDue: due(-30 * time.Minute)})
	f.conversation(conv{created: created, assignee: f.agentA, frDue: due(20 * time.Minute)})
	f.conversation(conv{created: created, assignee: f.agentA, frDue: due(45 * time.Minute)})
	f.conversation(conv{created: created, assignee: f.agentB, frDue: due(-5 * time.Minute)})
	f.conversation(conv{created: created, frDue: due(30 * time.Minute)})
	// Unanswered but not urgent, so it counts as unanswered only.
	f.conversation(conv{created: created, assignee: f.agentB, frDue: due(5 * time.Hour)})
	f.conversation(conv{created: created})
	// Never counted: answered, waiting, snoozed, other mailbox.
	f.conversation(conv{created: created, assignee: f.agentA, firstResp: time.Minute, frDue: due(-time.Hour)})
	f.conversation(conv{created: created, assignee: f.agentA, status: "waiting", frDue: due(-time.Hour)})
	snoozed := f.conversation(conv{created: created, assignee: f.agentA, frDue: due(-time.Hour)})
	f.exec(`UPDATE conversations SET snoozed_until = now() + interval '1 day' WHERE id = $1`, snoozed)
	f.conversation(conv{created: created, mailbox: f.other, assignee: f.agentA, frDue: due(-time.Hour)})

	live, err := f.svc.Live(context.Background(), f.filter(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := live.Attention
	if a.Unanswered != 7 || a.Breached != 2 || a.DueSoon != 3 || a.Unassigned != 1 {
		t.Errorf("attention: %+v", a)
	}
	if a.NextDue < 19*time.Minute || a.NextDue > 20*time.Minute {
		t.Errorf("next due in %s, want about 20 minutes", a.NextDue)
	}
	if a.Oldest < 3*time.Hour || a.Oldest > 3*time.Hour+time.Minute {
		t.Errorf("oldest %s, want about 3 hours", a.Oldest)
	}
	if len(a.Holders) != 2 || a.Holders[0].Name != "Anna" || a.Holders[0].Count != 3 || a.Holders[1].Name != "Bram" || a.Holders[1].Count != 1 {
		t.Errorf("holders: %+v", a.Holders)
	}

	own := f.agentB.String()
	live, err = f.svc.Live(context.Background(), f.filter(), nil, func(id string) bool { return id == own })
	if err != nil || len(live.Attention.Holders) != 1 || live.Attention.Holders[0].Name != "Bram" || live.Attention.Breached != 2 {
		t.Errorf("own holder only, totals unchanged: %+v %v", live.Attention, err)
	}

	byTeam := f.filter()
	byTeam.Team = f.team
	live, err = f.svc.Live(context.Background(), byTeam, nil, nil)
	if err != nil || live.Open != 1 || live.Attention.Unanswered != 1 || live.Attention.Breached != 1 || live.Attention.DueSoon != 0 {
		t.Errorf("team filter: %+v %v", live, err)
	}

	quiet := f.filter(f.other)
	quiet.Agent = f.agentB
	live, err = f.svc.Live(context.Background(), quiet, nil, nil)
	if err != nil || live.Attention.Unanswered != 0 || live.Attention.Breached != 0 || len(live.Attention.Holders) != 0 {
		t.Errorf("nothing needs attention: %+v %v", live.Attention, err)
	}
}
