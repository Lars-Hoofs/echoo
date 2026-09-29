package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/auth"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/reports"
)

func (s *Server) reportRoutes(r chi.Router) {
	// A report runs several aggregate queries; a person paging through periods stays far below this.
	limiter := auth.NewLimiter(120, time.Minute)
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler { return limitByUser(limiter)(next) })
		r.Get("/reports/overview", s.reportOverview)
		r.Get("/reports/agents", s.reportGroup(reports.DimAgent))
		r.Get("/reports/teams", s.reportGroup(reports.DimTeam))
		r.Get("/reports/mailboxes", s.reportGroup(reports.DimMailbox))
		r.Get("/reports/labels", s.reportGroup(reports.DimLabel))
		r.Get("/reports/csat", s.reportCSAT)
		r.Get("/reports/live", s.reportLive)
		r.Get("/reports/{kind}/export", s.reportExport)
	})
}

func (s *Server) getReportSettings(w http.ResponseWriter, r *http.Request) {
	loc, err := s.reports.Timezone(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reports.Settings{Timezone: loc.String()})
}

func (s *Server) putReportSettings(w http.ResponseWriter, r *http.Request) {
	var req reports.Settings
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := reports.LoadLocation(req.Timezone); err != nil {
		writeError(w, r, errValidation(map[string]string{"timezone": "invalid"}))
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := q.UpsertSetting(r.Context(), dbq.UpsertSettingParams{Key: reports.SettingsKey, Value: raw, UpdatedBy: actor.ID}); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{
			Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.ReportSettingsChanged, TargetType: "settings",
			TargetID: reports.SettingsKey, Metadata: map[string]any{"timezone": req.Timezone},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// reportQuery is a parsed and authorized report request.
type reportQuery struct {
	preset reports.Preset
	rng    reports.Range
	filter reports.Filter
	user   dbq.User
	admin  bool
	// scope is every mailbox the user may read, before the mailbox filter.
	scope []pgtype.UUID
}

// ownOnly is the table filter for people who may see only their own numbers.
func (q reportQuery) ownOnly() func(string) bool {
	if q.admin {
		return nil
	}
	own := uuidStr(q.user.ID)
	return func(key string) bool { return key == own }
}

func reportID(v, field string, fields map[string]string) pgtype.UUID {
	if v == "" {
		return pgtype.UUID{}
	}
	id, ok := parseUUID(v)
	if !ok {
		fields[field] = "invalid"
	}
	return id
}

// parseReportQuery applies the report access rules: mailboxes the user cannot read do not exist
// for them, and only admins may look at another agent's numbers.
func (s *Server) parseReportQuery(r *http.Request) (reportQuery, error) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	if !policy.Has(user, policy.ReportsView) {
		return reportQuery{}, errForbidden
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		return reportQuery{}, err
	}
	loc, err := s.reports.Timezone(ctx)
	if err != nil {
		return reportQuery{}, err
	}
	v := r.URL.Query()
	fields := map[string]string{}
	q := reportQuery{user: user, admin: policy.Has(user, policy.ReportsViewAll), scope: scope.Read}
	q.preset = reports.Preset(v.Get("period"))
	if q.preset == "" {
		q.preset = reports.Preset7Days
	}
	q.rng, err = reports.ResolveRange(time.Now(), loc, q.preset, v.Get("from"), v.Get("to"), reports.Group(v.Get("group")))
	if errors.Is(err, reports.ErrInvalidRange) {
		fields["period"] = "invalid"
	} else if err != nil {
		return reportQuery{}, err
	}
	q.filter = reports.Filter{
		Team:  reportID(v.Get("team"), "team", fields),
		Agent: reportID(v.Get("agent"), "agent", fields),
		Label: reportID(v.Get("label"), "label", fields),
	}
	mailbox := reportID(v.Get("mailbox"), "mailbox", fields)
	if len(fields) > 0 {
		return reportQuery{}, errValidation(fields)
	}
	q.filter.Mailboxes = scope.Read
	if mailbox.Valid {
		if !slices.Contains(scope.Read, mailbox) {
			return reportQuery{}, errNotFound
		}
		q.filter.Mailboxes = []pgtype.UUID{mailbox}
	}
	if q.filter.Agent.Valid && !q.admin && q.filter.Agent != user.ID {
		return reportQuery{}, errForbidden
	}
	return q, nil
}

type periodJSON struct {
	Preset   string `json:"preset"`
	From     string `json:"from"`
	To       string `json:"to"`
	Timezone string `json:"timezone"`
	Group    string `json:"group"`
	Days     int    `json:"days"`
}

func (q reportQuery) period() periodJSON {
	return periodJSON{
		Preset: string(q.preset), From: q.rng.From.Format(time.DateOnly), To: q.rng.Last().Format(time.DateOnly),
		Timezone: q.rng.Loc.String(), Group: string(q.rng.Group), Days: q.rng.Days,
	}
}

type durationJSON struct {
	Count         int    `json:"count"`
	MedianSeconds *int64 `json:"median_seconds"`
	P90Seconds    *int64 `json:"p90_seconds"`
}

func toDurationJSON(s reports.Stat) durationJSON {
	if s.Count == 0 {
		return durationJSON{}
	}
	median, p90 := int64(math.Round(s.Median)), int64(math.Round(s.P90))
	return durationJSON{Count: s.Count, MedianSeconds: &median, P90Seconds: &p90}
}

type rateJSON struct {
	Met   int `json:"met"`
	Total int `json:"total"`
}

type metricsJSON struct {
	New              int64        `json:"new_conversations"`
	CustomerMessages int64        `json:"customer_messages"`
	Replies          int64        `json:"replies"`
	Resolved         int64        `json:"resolved"`
	Reopened         int64        `json:"reopened"`
	FirstResponse    durationJSON `json:"first_response"`
	Resolution       durationJSON `json:"resolution"`
	SLAFirstResponse rateJSON     `json:"sla_first_response"`
	SLAResolution    rateJSON     `json:"sla_resolution"`
}

func toMetricsJSON(m reports.Metrics) metricsJSON {
	return metricsJSON{
		New: m.New, CustomerMessages: m.CustomerMessages, Replies: m.Replies, Resolved: m.Resolved, Reopened: m.Reopened,
		FirstResponse: toDurationJSON(m.FirstResponse), Resolution: toDurationJSON(m.Resolution),
		SLAFirstResponse: rateJSON(m.FirstResponseSLA), SLAResolution: rateJSON(m.ResolutionSLA),
	}
}

type csatSummaryJSON struct {
	Sent         int      `json:"sent"`
	Responses    int      `json:"responses"`
	Average      *float64 `json:"average"`
	ResponseRate *float64 `json:"response_rate"`
	Distribution [5]int   `json:"distribution"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func toCSATSummaryJSON(c reports.CSATSummary) csatSummaryJSON {
	out := csatSummaryJSON{Sent: c.Sent, Responses: c.Responses, Distribution: c.Distribution}
	if c.Responses > 0 {
		avg := round1(c.Average)
		out.Average = &avg
	}
	if rate, ok := c.ResponseRate(); ok {
		rate = round1(rate)
		out.ResponseRate = &rate
	}
	return out
}

type statusCountsJSON struct {
	Open    int64 `json:"open"`
	Waiting int64 `json:"waiting"`
	Closed  int64 `json:"closed"`
}

type lifecycleJSON struct {
	Arrived    int64            `json:"arrived"`
	Spam       int64            `json:"spam"`
	Answered   statusCountsJSON `json:"answered"`
	Unanswered statusCountsJSON `json:"unanswered"`
}

type targetJSON struct {
	Count   int64  `json:"count"`
	Seconds *int64 `json:"seconds"`
}

type overviewJSON struct {
	Period              periodJSON      `json:"period"`
	TimeBasis           reports.Basis   `json:"time_basis"`
	Current             metricsJSON     `json:"current"`
	Previous            metricsJSON     `json:"previous"`
	Series              []seriesJSON    `json:"series"`
	OpenNow             int64           `json:"open_now"`
	CSAT                csatSummaryJSON `json:"csat"`
	Lifecycle           lifecycleJSON   `json:"lifecycle"`
	FirstResponseTarget targetJSON      `json:"first_response_target"`
}

type seriesJSON struct {
	Date string `json:"date"`
	metricsJSON
	Unanswered int64 `json:"unanswered"`
}

func toStatusCountsJSON(c reports.StatusCounts) statusCountsJSON {
	return statusCountsJSON{Open: c.Open, Waiting: c.Waiting, Closed: c.Closed}
}

func toTargetJSON(t reports.Target) targetJSON {
	if t.Count == 0 {
		return targetJSON{}
	}
	seconds := int64(math.Round(t.Seconds))
	return targetJSON{Count: t.Count, Seconds: &seconds}
}

func toOverviewJSON(q reportQuery, o reports.Overview) overviewJSON {
	out := overviewJSON{
		Period: q.period(), TimeBasis: o.Basis, Current: toMetricsJSON(o.Current), Previous: toMetricsJSON(o.Previous),
		Series: make([]seriesJSON, len(o.Series)), OpenNow: o.OpenNow, CSAT: toCSATSummaryJSON(o.CSAT),
		Lifecycle: lifecycleJSON{
			Arrived: o.Lifecycle.Arrived(), Spam: o.Lifecycle.Spam,
			Answered: toStatusCountsJSON(o.Lifecycle.Answered), Unanswered: toStatusCountsJSON(o.Lifecycle.Unanswered),
		},
		FirstResponseTarget: toTargetJSON(o.FirstResponseTarget),
	}
	for i, p := range o.Series {
		out.Series[i] = seriesJSON{Date: p.Day, metricsJSON: toMetricsJSON(p.Metrics), Unanswered: p.Unanswered}
	}
	return out
}

func (s *Server) reportOverview(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseReportQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	o, err := s.reports.Overview(r.Context(), q.rng, q.filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOverviewJSON(q, o))
}

type rowJSON struct {
	ID   *string `json:"id"`
	Name string  `json:"name"`
	metricsJSON
}

type groupJSON struct {
	Period    periodJSON    `json:"period"`
	TimeBasis reports.Basis `json:"time_basis"`
	Rows      []rowJSON     `json:"rows"`
}

func nullableID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func (s *Server) reportGroupRows(ctx context.Context, q reportQuery, dim reports.Dim) ([]reports.Row, reports.Basis, error) {
	var keep func(string) bool
	if dim == reports.DimAgent {
		keep = q.ownOnly()
	}
	return s.reports.Groups(ctx, q.rng, q.filter, dim, keep)
}

func (s *Server) reportGroup(dim reports.Dim) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, err := s.parseReportQuery(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		rows, basis, err := s.reportGroupRows(r.Context(), q, dim)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out := groupJSON{Period: q.period(), TimeBasis: basis, Rows: make([]rowJSON, len(rows))}
		for i, row := range rows {
			out.Rows[i] = rowJSON{ID: nullableID(row.ID), Name: row.Name, metricsJSON: toMetricsJSON(row.Metrics)}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type csatPointJSON struct {
	Date string `json:"date"`
	csatSummaryJSON
}

type csatRowJSON struct {
	ID   *string `json:"id"`
	Name string  `json:"name"`
	csatSummaryJSON
}

type csatCommentJSON struct {
	ConversationID string    `json:"conversation_id"`
	Number         int64     `json:"number"`
	Subject        string    `json:"subject"`
	Rating         int       `json:"rating"`
	Comment        string    `json:"comment"`
	At             time.Time `json:"at"`
	Assignee       string    `json:"assignee"`
}

type csatReportJSON struct {
	Period   periodJSON        `json:"period"`
	By       string            `json:"by"`
	Summary  csatSummaryJSON   `json:"summary"`
	Series   []csatPointJSON   `json:"series"`
	Rows     []csatRowJSON     `json:"rows"`
	Comments []csatCommentJSON `json:"comments"`
}

func csatDim(v string) (reports.Dim, bool) {
	switch d := reports.Dim(v); d {
	case "":
		return reports.DimAgent, true
	case reports.DimAgent, reports.DimTeam, reports.DimMailbox, reports.DimLabel:
		return d, true
	}
	return "", false
}

func (s *Server) reportCSATData(ctx context.Context, q reportQuery, dim reports.Dim) (reports.CSATReport, error) {
	var keep func(string) bool
	if dim == reports.DimAgent {
		keep = q.ownOnly()
	}
	return s.reports.CSAT(ctx, q.rng, q.filter, dim, keep)
}

func (s *Server) reportCSAT(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseReportQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	dim, ok := csatDim(r.URL.Query().Get("by"))
	if !ok {
		writeError(w, r, errValidation(map[string]string{"by": "invalid"}))
		return
	}
	rep, err := s.reportCSATData(r.Context(), q, dim)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := csatReportJSON{
		Period: q.period(), By: string(dim), Summary: toCSATSummaryJSON(rep.Summary),
		Series: make([]csatPointJSON, len(rep.Series)), Rows: make([]csatRowJSON, len(rep.Rows)),
		Comments: make([]csatCommentJSON, len(rep.Comments)),
	}
	for i, p := range rep.Series {
		out.Series[i] = csatPointJSON{Date: p.Day, csatSummaryJSON: toCSATSummaryJSON(p.CSATSummary)}
	}
	for i, row := range rep.Rows {
		out.Rows[i] = csatRowJSON{ID: nullableID(row.ID), Name: row.Name, csatSummaryJSON: toCSATSummaryJSON(row.CSATSummary)}
	}
	for i, c := range rep.Comments {
		out.Comments[i] = csatCommentJSON{
			ConversationID: c.ConversationID, Number: c.Number, Subject: c.Subject, Rating: c.Rating,
			Comment: c.Text, At: c.At.UTC(), Assignee: c.Assignee,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type liveAgentJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Open         int64  `json:"open"`
	Waiting      int64  `json:"waiting"`
	SLARisk      int64  `json:"sla_risk"`
	Online       bool   `json:"online"`
	Availability string `json:"availability"`
}

type holderJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type attentionJSON struct {
	Unanswered     int64        `json:"unanswered"`
	Breached       int64        `json:"breached"`
	DueSoon        int64        `json:"due_soon"`
	NextDueSeconds int64        `json:"next_due_seconds"`
	OldestSeconds  int64        `json:"oldest_seconds"`
	Unassigned     int64        `json:"unassigned"`
	Holders        []holderJSON `json:"holders"`
	WindowMinutes  int          `json:"window_minutes"`
}

type liveJSON struct {
	Open        int64           `json:"open"`
	Unassigned  int64           `json:"unassigned"`
	Waiting     int64           `json:"waiting"`
	SLAAtRisk   int64           `json:"sla_at_risk"`
	SLABreached int64           `json:"sla_breached"`
	Agents      []liveAgentJSON `json:"agents"`
	Attention   attentionJSON   `json:"attention"`
	At          time.Time       `json:"at"`
}

func (s *Server) liveData(ctx context.Context, q reportQuery) (reports.Live, error) {
	online := map[string]bool{}
	for _, a := range s.hub.Online() {
		online[a.ID] = true
	}
	return s.reports.Live(ctx, q.filter, online, q.ownOnly())
}

func (s *Server) reportLive(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseReportQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	live, err := s.liveData(r.Context(), q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := liveJSON{
		Open: live.Open, Unassigned: live.Unassigned, Waiting: live.Waiting, SLAAtRisk: live.SLAAtRisk,
		SLABreached: live.SLABreached, Agents: make([]liveAgentJSON, len(live.Agents)), At: time.Now().UTC(),
	}
	for i, a := range live.Agents {
		out.Agents[i] = liveAgentJSON{
			ID: a.ID, Name: a.Name, Open: a.Open, Waiting: a.Waiting, SLARisk: a.SLARisk, Online: a.Online, Availability: a.Availability,
		}
	}
	at := live.Attention
	out.Attention = attentionJSON{
		Unanswered: at.Unanswered, Breached: at.Breached, DueSoon: at.DueSoon,
		NextDueSeconds: int64(at.NextDue.Seconds()), OldestSeconds: int64(at.Oldest.Seconds()),
		Unassigned: at.Unassigned, Holders: make([]holderJSON, len(at.Holders)), WindowMinutes: int(reports.AttentionWindow / time.Minute),
	}
	for i, h := range at.Holders {
		out.Attention.Holders[i] = holderJSON{ID: h.ID, Name: h.Name, Count: h.Count}
	}
	writeJSON(w, http.StatusOK, out)
}

// exportFlushEvery bounds how much CSV is buffered before it goes to the client.
const exportFlushEvery = 500

func seconds(d durationJSON, pick func(durationJSON) *int64) string {
	if v := pick(d); v != nil {
		return strconv.FormatInt(*v, 10)
	}
	return ""
}

func percent(r rateJSON) string {
	if r.Total == 0 {
		return ""
	}
	return strconv.FormatFloat(round1(100*float64(r.Met)/float64(r.Total)), 'f', 1, 64)
}

func medianOf(d durationJSON) *int64 { return d.MedianSeconds }
func p90Of(d durationJSON) *int64    { return d.P90Seconds }

func metricsColumns() []string {
	return []string{
		"nieuwe_gesprekken", "klantberichten", "antwoorden", "opgelost", "heropend",
		"eerste_reactie_mediaan_s", "eerste_reactie_p90_s", "oplostijd_mediaan_s", "oplostijd_p90_s",
		"eerste_reactie_binnen_sla_procent", "oplossing_binnen_sla_procent",
	}
}

func metricsCells(m metricsJSON) []string {
	return []string{
		strconv.FormatInt(m.New, 10), strconv.FormatInt(m.CustomerMessages, 10), strconv.FormatInt(m.Replies, 10),
		strconv.FormatInt(m.Resolved, 10), strconv.FormatInt(m.Reopened, 10),
		seconds(m.FirstResponse, medianOf), seconds(m.FirstResponse, p90Of),
		seconds(m.Resolution, medianOf), seconds(m.Resolution, p90Of),
		percent(m.SLAFirstResponse), percent(m.SLAResolution),
	}
}

func csatCells(c csatSummaryJSON) []string {
	avg, rate := "", ""
	if c.Average != nil {
		avg = strconv.FormatFloat(*c.Average, 'f', 1, 64)
	}
	if c.ResponseRate != nil {
		rate = strconv.FormatFloat(*c.ResponseRate, 'f', 1, 64)
	}
	return []string{strconv.Itoa(c.Sent), strconv.Itoa(c.Responses), rate, avg}
}

func csatColumns() []string {
	return []string{"verstuurd", "beantwoord", "responspercentage", "gemiddelde_beoordeling"}
}

// exportTable is the tabular form of one report.
type exportTable struct {
	header []string
	rows   [][]string
}

func (s *Server) exportOverview(ctx context.Context, q reportQuery) (exportTable, error) {
	o, err := s.reports.Overview(ctx, q.rng, q.filter)
	if err != nil {
		return exportTable{}, err
	}
	t := exportTable{header: append([]string{"datum"}, metricsColumns()...)}
	for _, p := range toOverviewJSON(q, o).Series {
		t.rows = append(t.rows, append([]string{p.Date}, metricsCells(p.metricsJSON)...))
	}
	return t, nil
}

func (s *Server) exportGroup(ctx context.Context, q reportQuery, dim reports.Dim) (exportTable, error) {
	rows, _, err := s.reportGroupRows(ctx, q, dim)
	if err != nil {
		return exportTable{}, err
	}
	t := exportTable{header: append([]string{"naam"}, metricsColumns()...)}
	for _, row := range rows {
		t.rows = append(t.rows, append([]string{csvSafe(row.Name)}, metricsCells(toMetricsJSON(row.Metrics))...))
	}
	return t, nil
}

func (s *Server) exportCSAT(ctx context.Context, q reportQuery, dim reports.Dim) (exportTable, error) {
	rep, err := s.reportCSATData(ctx, q, dim)
	if err != nil {
		return exportTable{}, err
	}
	t := exportTable{header: append([]string{"naam"}, csatColumns()...)}
	for _, row := range rep.Rows {
		t.rows = append(t.rows, append([]string{csvSafe(row.Name)}, csatCells(toCSATSummaryJSON(row.CSATSummary))...))
	}
	return t, nil
}

func (s *Server) exportLive(ctx context.Context, q reportQuery) (exportTable, error) {
	live, err := s.liveData(ctx, q)
	if err != nil {
		return exportTable{}, err
	}
	t := exportTable{header: []string{"naam", "open", "wachtend", "sla_risico", "online", "beschikbaarheid"}}
	for _, a := range live.Agents {
		t.rows = append(t.rows, []string{
			csvSafe(a.Name), strconv.FormatInt(a.Open, 10), strconv.FormatInt(a.Waiting, 10), strconv.FormatInt(a.SLARisk, 10),
			strconv.FormatBool(a.Online), a.Availability,
		})
	}
	return t, nil
}

// reportExport streams one report as CSV. The numbers are computed before the first byte is
// written, so a failure still produces a proper error response instead of a truncated file.
// Text cells are neutralised against spreadsheet formulas.
func (s *Server) reportExport(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseReportQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	kind := chi.URLParam(r, "kind")
	var table exportTable
	switch kind {
	case "overview":
		table, err = s.exportOverview(r.Context(), q)
	case "agents":
		table, err = s.exportGroup(r.Context(), q, reports.DimAgent)
	case "teams":
		table, err = s.exportGroup(r.Context(), q, reports.DimTeam)
	case "mailboxes":
		table, err = s.exportGroup(r.Context(), q, reports.DimMailbox)
	case "labels":
		table, err = s.exportGroup(r.Context(), q, reports.DimLabel)
	case "csat":
		dim, ok := csatDim(r.URL.Query().Get("by"))
		if !ok {
			writeError(w, r, errValidation(map[string]string{"by": "invalid"}))
			return
		}
		table, err = s.exportCSAT(r.Context(), q, dim)
	case "live":
		table, err = s.exportLive(r.Context(), q)
	default:
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = audit.Write(r.Context(), s.q, audit.Entry{
		Actor: q.user.ID, IP: clientFrom(r).IP, Action: audit.ReportExported, TargetType: "report", TargetID: kind,
		Metadata: map[string]any{"from": q.period().From, "to": q.period().To, "rows": len(table.rows)},
	})
	if err != nil {
		writeError(w, r, err)
		return
	}

	p := q.period()
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="rapportage-%s-%s-%s.csv"`, kind, p.From, p.To))
	rc := http.NewResponseController(w)
	out := csv.NewWriter(w)
	_ = out.Write(table.header)
	for i, row := range table.rows {
		_ = out.Write(row)
		if (i+1)%exportFlushEvery == 0 {
			out.Flush()
			if out.Error() != nil {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		}
	}
	out.Flush()
}
