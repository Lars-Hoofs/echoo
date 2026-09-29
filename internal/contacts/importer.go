package contacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/webhooks"
)

const (
	DedupeUpdate = "update"
	DedupeSkip   = "skip"

	TargetEmail        = "email"
	TargetName         = "name"
	TargetPhone        = "phone"
	TargetOrganization = "organization"
	targetAttrPrefix   = "attribute:"

	importChunk   = 200
	maxNameLen    = 200
	maxPhoneLen   = 50
	maxEmailLen   = 254
	importTimeout = 30 * time.Minute
)

// ColumnMap sends one CSV column to a contact field or, as "attribute:<key>", to a custom
// attribute.
type ColumnMap struct {
	Index  int    `json:"index"`
	Target string `json:"target"`
}

type ImportOptions struct {
	Delimiter string      `json:"delimiter"`
	Dedupe    string      `json:"dedupe"`
	Columns   []ColumnMap `json:"columns"`
}

// Validate checks the mapping against the parsed header and the attribute definitions.
func (o ImportOptions) Validate(header []string, defs []dbq.CustomAttributeDef) error {
	if o.Dedupe != DedupeUpdate && o.Dedupe != DedupeSkip {
		return errors.New("dedupe must be update or skip")
	}
	seenTarget := map[string]bool{}
	seenIndex := map[int]bool{}
	hasEmail := false
	for _, c := range o.Columns {
		if c.Index < 0 || c.Index >= len(header) {
			return fmt.Errorf("column %d does not exist", c.Index)
		}
		if seenIndex[c.Index] || seenTarget[c.Target] {
			return errors.New("each column and each field can be mapped once")
		}
		seenIndex[c.Index], seenTarget[c.Target] = true, true
		switch c.Target {
		case TargetEmail:
			hasEmail = true
		case TargetName, TargetPhone, TargetOrganization:
		default:
			key, ok := strings.CutPrefix(c.Target, targetAttrPrefix)
			if !ok {
				return fmt.Errorf("unknown target %q", c.Target)
			}
			if _, ok := findDef(defs, EntityContact, key); !ok {
				return fmt.Errorf("unknown attribute %q", key)
			}
		}
	}
	if !hasEmail {
		return errors.New("a column must be mapped to email")
	}
	return nil
}

// PreparedImport is a validated upload, ready to be stored and queued.
type PreparedImport struct {
	Table   Table
	Options ImportOptions
}

// PrepareImport parses data and validates options against it. It has no side effects.
func PrepareImport(data []byte, options ImportOptions, defs []dbq.CustomAttributeDef) (PreparedImport, error) {
	if len(data) > MaxImportBytes {
		return PreparedImport{}, ErrTooManyRows
	}
	table, err := ParseCSV(data, options.Delimiter)
	if err != nil {
		return PreparedImport{}, err
	}
	options.Delimiter = table.Delimiter
	if err := options.Validate(table.Header, defs); err != nil {
		return PreparedImport{}, err
	}
	return PreparedImport{Table: table, Options: options}, nil
}

// cleanCell trims a cell and undoes the apostrophe SafeCell adds, so an exported file can be
// imported again unchanged.
func cleanCell(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1 && s[0] == '\'' && strings.ContainsRune("=+-@", rune(s[1])) {
		return s[1:]
	}
	return s
}

// rowError is why a row was not imported. Code is stable, Detail names the attribute.
type rowError struct{ code, detail string }

func (e rowError) Error() string { return e.code }

type importRow struct {
	email, name, phone, organization string
	attributes                       map[string]any
}

func parseRow(rec []string, cols []ColumnMap, defs []dbq.CustomAttributeDef) (importRow, error) {
	var row importRow
	cell := func(c ColumnMap) string {
		if c.Index >= len(rec) {
			return ""
		}
		return cleanCell(rec[c.Index])
	}
	patch := map[string]any{}
	for _, c := range cols {
		v := cell(c)
		switch c.Target {
		case TargetEmail:
			email, err := normalizeEmail(v)
			if err != nil {
				return row, rowError{code: "invalid_email"}
			}
			row.email = email
		case TargetName:
			if utf8.RuneCountInString(v) > maxNameLen || hasControl(v) {
				return row, rowError{code: "invalid_name"}
			}
			row.name = v
		case TargetPhone:
			if utf8.RuneCountInString(v) > maxPhoneLen || hasControl(v) {
				return row, rowError{code: "invalid_phone"}
			}
			row.phone = v
		case TargetOrganization:
			if utf8.RuneCountInString(v) > maxNameLen || hasControl(v) {
				return row, rowError{code: "invalid_organization"}
			}
			row.organization = v
		default:
			key := strings.TrimPrefix(c.Target, targetAttrPrefix)
			if v != "" {
				patch[key] = v
			}
		}
	}
	if len(patch) > 0 {
		def := func(k string) dbq.CustomAttributeDef { d, _ := findDef(defs, EntityContact, k); return d }
		for key, v := range patch {
			norm, err := Normalize(def(key), v, true)
			if err != nil {
				return row, rowError{code: "invalid_attribute", detail: key}
			}
			patch[key] = norm
		}
		row.attributes = patch
	}
	return row, nil
}

// NormalizeEmail lower-cases and validates a bare address.
func NormalizeEmail(s string) (string, error) { return normalizeEmail(s) }

func normalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > maxEmailLen || hasControl(s) {
		return "", errors.New("invalid email address")
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || !strings.Contains(s[strings.LastIndexByte(s, '@'):], ".") {
		return "", errors.New("invalid email address")
	}
	return s, nil
}

type importCounts struct{ processed, created, updated, skipped, failed int32 }

type failedRow struct {
	line int
	err  rowError
	rec  []string
}

// ImportWorker runs contacts.import jobs.
type ImportWorker struct {
	river.WorkerDefaults[jobs.ContactImport]
	pool  *pgxpool.Pool
	blobs Blobs
	q     *dbq.Queries
	// freeMail lets a row without an organization column pick up the organization of its domain
	// the way incoming mail does.
	isFreeMail func(domain string) bool
}

type ImportDeps struct {
	Pool  *pgxpool.Pool
	Blobs Blobs
	// IsFreeMailDomain reports domains that never identify an organization.
	IsFreeMailDomain func(domain string) bool
}

func NewImportWorker(d ImportDeps) *ImportWorker {
	return &ImportWorker{pool: d.Pool, blobs: d.Blobs, q: dbq.New(d.Pool), isFreeMail: d.IsFreeMailDomain}
}

func (w *ImportWorker) Timeout(*river.Job[jobs.ContactImport]) time.Duration { return importTimeout }

func (w *ImportWorker) Work(ctx context.Context, job *river.Job[jobs.ContactImport]) error {
	id, ok := parseID(job.Args.ImportID)
	if !ok {
		return river.JobCancel(errors.New("invalid import id"))
	}
	imp, err := w.q.GetContactImport(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return river.JobCancel(errors.New("import does not exist"))
		}
		return fmt.Errorf("load import: %w", err)
	}
	if imp.Status == "done" || imp.Status == "failed" {
		return nil
	}
	counts, failures, runErr := w.run(ctx, imp)
	finish := dbq.FinishContactImportParams{
		ID: imp.ID, Status: "done", ProcessedRows: counts.processed, CreatedCount: counts.created,
		UpdatedCount: counts.updated, SkippedCount: counts.skipped, FailedCount: counts.failed,
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return runErr
		}
		finish.Status, finish.Error = "failed", "import_failed"
	} else if len(failures) > 0 {
		key, err := w.storeErrorReport(ctx, imp, failures)
		if err != nil {
			return err
		}
		finish.ErrorKey = key
	}
	sourceKey := imp.SourceKey
	if err := w.q.FinishContactImport(ctx, finish); err != nil {
		return fmt.Errorf("finish import: %w", err)
	}
	// Whatever the outcome, the uploaded file (personal data) is no longer needed.
	if failed := DeleteUnreferenced(ctx, w.q, w.blobs, []string{sourceKey}); failed > 0 {
		return errors.New("delete import file")
	}
	if runErr != nil {
		return river.JobCancel(runErr)
	}
	return nil
}

func (w *ImportWorker) run(ctx context.Context, imp dbq.ContactImport) (importCounts, []failedRow, error) {
	var counts importCounts
	rc, err := w.blobs.Open(ctx, imp.SourceKey)
	if err != nil {
		return counts, nil, fmt.Errorf("open import file: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(rc, MaxImportBytes+1))
	if closeErr := rc.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return counts, nil, fmt.Errorf("read import file: %w", err)
	}
	var opts ImportOptions
	if err := json.Unmarshal(imp.Mapping, &opts); err != nil {
		return counts, nil, fmt.Errorf("decode mapping: %w", err)
	}
	table, err := ParseCSV(data, imp.Delimiter)
	if err != nil {
		return counts, nil, err
	}
	defs, err := w.q.ListAttributeDefs(ctx, pgtype.Text{String: EntityContact, Valid: true})
	if err != nil {
		return counts, nil, fmt.Errorf("load attribute definitions: %w", err)
	}
	importer, err := w.q.GetUser(ctx, imp.CreatedBy)
	if err != nil {
		return counts, nil, fmt.Errorf("load importer: %w", err)
	}
	viewer, err := ViewerFor(ctx, w.q, importer)
	if err != nil {
		return counts, nil, err
	}
	if err := w.q.StartContactImport(ctx, imp.ID); err != nil {
		return counts, nil, fmt.Errorf("start import: %w", err)
	}

	var failures []failedRow
	for start := 0; start < len(table.Rows); start += importChunk {
		end := min(start+importChunk, len(table.Rows))
		chunkFailures, chunkCounts, err := w.chunk(ctx, imp, viewer, opts, defs, table.Rows[start:end], start)
		if err != nil {
			return counts, failures, err
		}
		failures = append(failures, chunkFailures...)
		counts.processed += chunkCounts.processed
		counts.created += chunkCounts.created
		counts.updated += chunkCounts.updated
		counts.skipped += chunkCounts.skipped
		counts.failed += chunkCounts.failed
		err = w.q.UpdateContactImportProgress(ctx, dbq.UpdateContactImportProgressParams{
			ID: imp.ID, ProcessedRows: counts.processed, CreatedCount: counts.created,
			UpdatedCount: counts.updated, SkippedCount: counts.skipped, FailedCount: counts.failed,
		})
		if err != nil {
			return counts, failures, fmt.Errorf("update progress: %w", err)
		}
	}
	return counts, failures, nil
}

// chunk imports rows in one transaction. Each row runs in a savepoint, so a bad row is
// reported and the rest still commit.
func (w *ImportWorker) chunk(ctx context.Context, imp dbq.ContactImport, viewer Viewer, opts ImportOptions, defs []dbq.CustomAttributeDef, recs [][]string, offset int) (failures []failedRow, counts importCounts, err error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, counts, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rbErr)
		}
	}()
	for i, rec := range recs {
		counts.processed++
		outcome, rowErr := w.row(ctx, tx, imp, viewer, opts, defs, rec)
		var re rowError
		switch {
		case errors.As(rowErr, &re):
			counts.failed++
			failures = append(failures, failedRow{line: offset + i + 2, err: re, rec: rec})
			continue
		case rowErr != nil:
			return nil, counts, rowErr
		}
		switch outcome {
		case outcomeCreated:
			counts.created++
		case outcomeUpdated:
			counts.updated++
		default:
			counts.skipped++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, counts, fmt.Errorf("commit: %w", err)
	}
	return failures, counts, nil
}

type rowOutcome int

const (
	outcomeSkipped rowOutcome = iota
	outcomeCreated
	outcomeUpdated
)

// row imports one record inside a savepoint. A rowError leaves the savepoint rolled back;
// any other error aborts the chunk.
func (w *ImportWorker) row(ctx context.Context, tx pgx.Tx, imp dbq.ContactImport, viewer Viewer, opts ImportOptions, defs []dbq.CustomAttributeDef, rec []string) (rowOutcome, error) {
	parsed, err := parseRow(rec, opts.Columns, defs)
	if err != nil {
		return outcomeSkipped, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return outcomeSkipped, fmt.Errorf("savepoint: %w", err)
	}
	outcome, err := w.apply(ctx, dbq.New(sp), imp, viewer, opts, defs, parsed)
	if err != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			return outcomeSkipped, errors.Join(err, rbErr)
		}
		return outcomeSkipped, err
	}
	if err := sp.Commit(ctx); err != nil {
		return outcomeSkipped, fmt.Errorf("release savepoint: %w", err)
	}
	return outcome, nil
}

func (w *ImportWorker) apply(ctx context.Context, q *dbq.Queries, imp dbq.ContactImport, viewer Viewer, opts ImportOptions, defs []dbq.CustomAttributeDef, row importRow) (rowOutcome, error) {
	// Same lock and order as mail ingest: contact first, then organization.
	if err := q.IngestAdvisoryLock(ctx, "contact:"+row.email); err != nil {
		return outcomeSkipped, fmt.Errorf("lock contact: %w", err)
	}
	existing, err := q.FindContactByEmail(ctx, row.email)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return outcomeSkipped, fmt.Errorf("find contact: %w", err)
	}
	// An import must not read or overwrite contacts its owner cannot see.
	if found {
		visible, err := viewer.ContactVisible(ctx, q, existing)
		if err != nil {
			return outcomeSkipped, fmt.Errorf("check visibility: %w", err)
		}
		if !visible {
			return outcomeSkipped, rowError{code: "contact_not_visible"}
		}
	}
	if found && opts.Dedupe == DedupeSkip {
		return outcomeSkipped, nil
	}
	orgID, err := w.organizationFor(ctx, q, viewer, imp.CreatedBy, row)
	if err != nil {
		return outcomeSkipped, err
	}
	if !found {
		return outcomeCreated, w.create(ctx, q, imp.CreatedBy, defs, row, orgID)
	}
	cur, err := q.LockContact(ctx, existing)
	if err != nil {
		return outcomeSkipped, fmt.Errorf("lock contact row: %w", err)
	}
	attrs, err := DecodeAttributes(cur.CustomAttributes)
	if err != nil {
		return outcomeSkipped, err
	}
	merged, problems := ApplyPatch(defs, attrs, row.attributes, true)
	if len(problems) > 0 {
		return outcomeSkipped, rowError{code: "invalid_attribute"}
	}
	encoded, err := merged.Encode()
	if err != nil {
		return outcomeSkipped, err
	}
	params := dbq.UpdateContactParams{
		ID: existing, Name: cur.Name, Phone: cur.Phone, OrganizationID: cur.OrganizationID, CustomAttributes: encoded,
	}
	if row.name != "" {
		params.Name = row.name
	}
	if row.phone != "" {
		params.Phone = row.phone
	}
	if orgID.Valid {
		params.OrganizationID = orgID
	}
	if err := q.UpdateContact(ctx, params); err != nil {
		return outcomeSkipped, fmt.Errorf("update contact: %w", err)
	}
	return outcomeUpdated, nil
}

func (w *ImportWorker) create(ctx context.Context, q *dbq.Queries, by pgtype.UUID, defs []dbq.CustomAttributeDef, row importRow, orgID pgtype.UUID) error {
	merged, problems := ApplyPatch(defs, Attributes{}, row.attributes, true)
	if len(problems) > 0 {
		return rowError{code: "invalid_attribute"}
	}
	encoded, err := merged.Encode()
	if err != nil {
		return err
	}
	id, err := q.InsertContact(ctx, dbq.InsertContactParams{
		Name: row.name, Phone: row.phone, OrganizationID: orgID, CustomAttributes: encoded, CreatedBy: by,
	})
	if err != nil {
		return fmt.Errorf("create contact: %w", err)
	}
	if err := q.InsertContactAddress(ctx, dbq.InsertContactAddressParams{ContactID: id, Email: row.email, IsPrimary: true}); err != nil {
		return fmt.Errorf("create contact address: %w", err)
	}
	return webhooks.Record(ctx, q, webhooks.Event{Type: webhooks.ContactCreated, ContactID: id})
}

// organizationFor resolves the organization column by name (creating it when new), or, when
// the file has none for this row, the organization that already owns the address's domain. An
// existing organization the importer cannot see is not attached: that would reveal it and let
// the import change what its owner's colleagues see; the contact is left without one.
func (w *ImportWorker) organizationFor(ctx context.Context, q *dbq.Queries, viewer Viewer, by pgtype.UUID, row importRow) (pgtype.UUID, error) {
	if row.organization != "" {
		if err := q.IngestAdvisoryLock(ctx, "organization-name:"+strings.ToLower(row.organization)); err != nil {
			return pgtype.UUID{}, fmt.Errorf("lock organization: %w", err)
		}
		id, err := q.FindOrganizationByName(ctx, row.organization)
		if err == nil {
			return w.visibleOrganization(ctx, q, viewer, id)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, fmt.Errorf("find organization: %w", err)
		}
		id, err = q.InsertOrganization(ctx, dbq.InsertOrganizationParams{
			Name: row.organization, Domains: []string{}, CustomAttributes: []byte("{}"), CreatedBy: by,
		})
		if err != nil {
			return pgtype.UUID{}, fmt.Errorf("create organization: %w", err)
		}
		return id, nil
	}
	domain := row.email[strings.LastIndexByte(row.email, '@')+1:]
	if w.isFreeMail != nil && w.isFreeMail(domain) {
		return pgtype.UUID{}, nil
	}
	id, err := q.IngestFindOrganizationByDomain(ctx, domain)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, nil
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("find organization by domain: %w", err)
	}
	return w.visibleOrganization(ctx, q, viewer, id)
}

func (w *ImportWorker) visibleOrganization(ctx context.Context, q *dbq.Queries, viewer Viewer, id pgtype.UUID) (pgtype.UUID, error) {
	visible, err := viewer.OrganizationVisible(ctx, q, id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("check organization visibility: %w", err)
	}
	if !visible {
		return pgtype.UUID{}, nil
	}
	return id, nil
}

var rowErrorText = map[string]string{
	"invalid_email":        "Ongeldig e-mailadres",
	"invalid_name":         "Ongeldige naam",
	"invalid_phone":        "Ongeldig telefoonnummer",
	"invalid_organization": "Ongeldige organisatie",
	"invalid_attribute":    "Ongeldige waarde voor een aangepast veld",
	"contact_not_visible":  "Dit e-mailadres hoort bij een contact waar je geen toegang toe hebt",
}

func (w *ImportWorker) storeErrorReport(ctx context.Context, imp dbq.ContactImport, failures []failedRow) (string, error) {
	var opts ImportOptions
	if err := json.Unmarshal(imp.Mapping, &opts); err != nil {
		return "", fmt.Errorf("decode mapping: %w", err)
	}
	var buf bytes.Buffer
	cw, err := NewCSVWriter(&buf)
	if err != nil {
		return "", err
	}
	width := 0
	for _, f := range failures {
		width = max(width, len(f.rec))
	}
	header := []string{"Regel", "Fout"}
	for i := range width {
		header = append(header, fmt.Sprintf("Kolom %d", i+1))
	}
	if err := cw.Write(header...); err != nil {
		return "", err
	}
	for _, f := range failures {
		text := rowErrorText[f.err.code]
		if f.err.detail != "" {
			text += " (" + f.err.detail + ")"
		}
		cells := slices.Concat([]string{fmt.Sprint(f.line), text}, f.rec)
		if err := cw.Write(cells...); err != nil {
			return "", err
		}
	}
	if err := cw.Flush(); err != nil {
		return "", err
	}
	key, _, err := w.blobs.Put(ctx, buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("store error report: %w", err)
	}
	return key, nil
}
