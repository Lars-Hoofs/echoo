package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/audit"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

const (
	jobListLimit = 100
	maxErrorText = 300
)

// urlCredentials matches user:password@ in a URL, which some library errors echo back.
var urlCredentials = regexp.MustCompile(`://[^/\s@]*@`)

// safeErrorText shortens a job error and removes URL credentials before it is shown.
func safeErrorText(s string) string {
	s = urlCredentials.ReplaceAllString(strings.Join(strings.Fields(s), " "), "://***@")
	if r := []rune(s); len(r) > maxErrorText {
		s = string(r[:maxErrorText]) + "…"
	}
	return s
}

type jobJSON struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	State       string    `json:"state"`
	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	LastError   string    `json:"last_error"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeError(w, r, errors.New("job queue is not configured"))
		return
	}
	res, err := s.jobs.JobList(r.Context(), river.NewJobListParams().
		States(rivertype.JobStateRetryable, rivertype.JobStateDiscarded).
		OrderBy(river.JobListOrderByID, river.SortOrderDesc).
		First(jobListLimit))
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]jobJSON, len(res.Jobs))
	for i, j := range res.Jobs {
		out[i] = jobJSON{
			ID: j.ID, Kind: j.Kind, State: string(j.State), Attempt: j.Attempt, MaxAttempts: j.MaxAttempts,
			ScheduledAt: j.ScheduledAt.UTC(),
		}
		if n := len(j.Errors); n > 0 {
			out[i].LastError = safeErrorText(j.Errors[n-1].Error)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeError(w, r, errors.New("job queue is not configured"))
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, r, errNotFound)
		return
	}
	job, err := s.jobs.JobRetry(r.Context(), id)
	if errors.Is(err, rivertype.ErrNotFound) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = audit.Write(r.Context(), s.q, audit.Entry{
		Actor: sessionFrom(r.Context()).User.ID, IP: clientFrom(r).IP, Action: audit.JobRetried,
		TargetType: "job", TargetID: strconv.FormatInt(id, 10), Metadata: map[string]any{"kind": job.Kind},
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": jobJSON{ID: job.ID, Kind: job.Kind, State: string(job.State), Attempt: job.Attempt, MaxAttempts: job.MaxAttempts, ScheduledAt: job.ScheduledAt.UTC()}})
}

func (s *Server) listRawMessageProblems(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListProblemRawMessages(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	type rawJSON struct {
		ID          string    `json:"id"`
		MailboxID   string    `json:"mailbox_id"`
		MailboxName string    `json:"mailbox_name"`
		Status      string    `json:"parse_status"`
		Reason      string    `json:"reason"`
		SizeBytes   int32     `json:"size_bytes"`
		ReceivedAt  time.Time `json:"received_at"`
	}
	out := make([]rawJSON, len(rows))
	for i, m := range rows {
		out[i] = rawJSON{
			ID: uuidStr(m.ID), MailboxID: uuidStr(m.MailboxID), MailboxName: m.MailboxName, Status: m.ParseStatus,
			Reason: safeErrorText(m.ParseError), SizeBytes: m.SizeBytes, ReceivedAt: m.ReceivedAt.Time.UTC(),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"raw_messages": out})
}

// retryRawMessage puts the message back to pending and enqueues a new parse job in one
// transaction, so it is never left pending without a job.
func (s *Server) retryRawMessage(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeError(w, r, errors.New("job queue is not configured"))
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		n, err := q.ResetRawMessage(r.Context(), id)
		if err != nil {
			return err
		}
		if n == 0 {
			return pgx.ErrNoRows
		}
		if _, err := s.jobs.InsertTx(r.Context(), tx, jobs.ParseRaw{RawMessageID: id.String()}, nil); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{
			Actor: sessionFrom(r.Context()).User.ID, IP: clientFrom(r).IP, Action: audit.RawMessageRetried,
			TargetType: "raw_message", TargetID: id.String(),
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
