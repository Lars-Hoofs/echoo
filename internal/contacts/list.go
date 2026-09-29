package contacts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

const (
	SortName     = "name"
	SortActivity = "last_activity"
	SortCreated  = "created"

	// MaxPageSize is the largest page an API client may ask for; walking every match (export,
	// segments) may use batches up to MaxBatchSize.
	MaxPageSize  = 100
	MaxBatchSize = 1000
)

var ErrInvalidCursor = errors.New("invalid cursor")

// ListParams describes one page of the contact list. Search matches name, any address and
// the organization name; Filter and OrganizationID narrow further.
type ListParams struct {
	Search         string
	Filter         *Filter
	OrganizationID pgtype.UUID
	Sort           string
	Desc           bool
	Limit          int
	Cursor         string
	// IDsOnly skips the display columns and the conversation count.
	IDsOnly bool
}

type Row struct {
	ID                pgtype.UUID
	Name              string
	Phone             string
	OrganizationID    pgtype.UUID
	OrganizationName  string
	PrimaryEmail      string
	CustomAttributes  []byte
	CreatedAt         time.Time
	LastActivityAt    time.Time
	ConversationCount int32

	// sortText and sortBool are the SQL-side sort key, kept so a cursor never depends on how Go
	// would lower-case a name.
	sortText string
	sortBool bool
}

type cursor struct {
	Sort string   `json:"s"`
	Desc bool     `json:"d"`
	Keys []string `json:"k"`
	ID   string   `json:"i"`
}

func encodeCursor(c cursor) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(s string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, ErrInvalidCursor
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, ErrInvalidCursor
	}
	return c, nil
}

func (r Row) cursorFor(p ListParams) (string, error) {
	c := cursor{Sort: p.Sort, Desc: p.Desc, ID: r.ID.String()}
	switch p.Sort {
	case SortName:
		c.Keys = []string{fmt.Sprint(r.sortBool), r.sortText}
	case SortActivity:
		c.Keys = []string{r.LastActivityAt.UTC().Format(time.RFC3339Nano)}
	default:
		c.Keys = []string{r.CreatedAt.UTC().Format(time.RFC3339Nano)}
	}
	return encodeCursor(c)
}

// NextCursor is the cursor after the last of rows, which must be the page without the extra
// row List returned.
func NextCursor(rows []Row, p ListParams) (string, error) {
	if len(rows) == 0 {
		return "", nil
	}
	return rows[len(rows)-1].cursorFor(p)
}

func (v Viewer) scopeSQL(a *sqlArgs) string {
	if v.Admin {
		return "TRUE"
	}
	return "cv.mailbox_id = ANY (" + a.add(v.mailboxes()) + "::uuid[])"
}

func buildWhere(v Viewer, p ListParams, defs []dbq.CustomAttributeDef, a *sqlArgs) string {
	scope := func() string { return v.scopeSQL(a) }
	clauses := []string{"contact_visible(c.id, " + a.add(v.UserID) + "::uuid, " + a.add(v.Admin) + "::boolean, " + a.add(v.mailboxes()) + "::uuid[])"}
	if s := strings.TrimSpace(p.Search); s != "" {
		pat := a.add(EscapeLike(s))
		clauses = append(clauses, "(c.name ILIKE '%' || "+pat+" || '%'"+
			" OR COALESCE(o.name, '') ILIKE '%' || "+pat+" || '%'"+
			" OR EXISTS (SELECT 1 FROM contact_addresses sa WHERE sa.contact_id = c.id AND sa.email ILIKE '%' || "+pat+" || '%'))")
	}
	if p.OrganizationID.Valid {
		clauses = append(clauses, "c.organization_id = "+a.add(p.OrganizationID)+"::uuid")
	}
	if p.Filter != nil {
		clauses = append(clauses, "("+p.Filter.sql(defs, scope, a)+")")
	}
	return strings.Join(clauses, " AND ")
}

type sortSpec struct {
	keys    string // ROW(...) expression of the sort key without the id
	orderBy string
	casts   []string
}

func sortFor(p ListParams) (sortSpec, error) {
	dir := "ASC"
	if p.Desc {
		dir = "DESC"
	}
	switch p.Sort {
	case SortName:
		return sortSpec{
			keys:    "(c.name = ''), lower(c.name)",
			orderBy: "(c.name = '') " + dir + ", lower(c.name) " + dir + ", c.id " + dir,
			casts:   []string{"boolean", "text"},
		}, nil
	case SortActivity:
		return sortSpec{keys: "c.last_activity_at", orderBy: "c.last_activity_at " + dir + ", c.id " + dir, casts: []string{"timestamptz"}}, nil
	case SortCreated:
		return sortSpec{keys: "c.created_at", orderBy: "c.created_at " + dir + ", c.id " + dir, casts: []string{"timestamptz"}}, nil
	}
	return sortSpec{}, fmt.Errorf("unknown sort %q", p.Sort)
}

func cursorClause(p ListParams, spec sortSpec, a *sqlArgs) (string, error) {
	if p.Cursor == "" {
		return "", nil
	}
	c, err := decodeCursor(p.Cursor)
	if err != nil {
		return "", err
	}
	if c.Sort != p.Sort || c.Desc != p.Desc || len(c.Keys) != len(spec.casts) {
		return "", ErrInvalidCursor
	}
	id, ok := parseID(c.ID)
	if !ok {
		return "", ErrInvalidCursor
	}
	placeholders := make([]string, 0, len(spec.casts)+1)
	for i, typ := range spec.casts {
		var val any = c.Keys[i]
		switch typ {
		case "boolean":
			switch c.Keys[i] {
			case "true":
				val = true
			case "false":
				val = false
			default:
				return "", ErrInvalidCursor
			}
		case "timestamptz":
			t, err := time.Parse(time.RFC3339Nano, c.Keys[i])
			if err != nil {
				return "", ErrInvalidCursor
			}
			val = t
		}
		placeholders = append(placeholders, a.add(val)+"::"+typ)
	}
	placeholders = append(placeholders, a.add(id)+"::uuid")
	op := ">"
	if p.Desc {
		op = "<"
	}
	return "ROW(" + spec.keys + ", c.id) " + op + " ROW(" + strings.Join(placeholders, ", ") + ")", nil
}

func parseID(s string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return id, false
	}
	return id, true
}

// List returns up to Limit+1 rows: the extra one only tells whether another page follows.
func List(ctx context.Context, db dbq.DBTX, v Viewer, p ListParams, defs []dbq.CustomAttributeDef) ([]Row, error) {
	if p.Limit < 1 || p.Limit > MaxBatchSize {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxBatchSize)
	}
	spec, err := sortFor(p)
	if err != nil {
		return nil, err
	}
	a := &sqlArgs{}
	where := buildWhere(v, p, defs, a)
	if cur, err := cursorClause(p, spec, a); err != nil {
		return nil, err
	} else if cur != "" {
		where += " AND " + cur
	}
	sel := "c.id, '', '', NULL::uuid, '', '', '{}'::jsonb, c.created_at, c.last_activity_at, 0, (c.name = ''), lower(c.name)"
	if !p.IDsOnly {
		sel = "c.id, c.name, c.phone, c.organization_id, COALESCE(o.name, ''), COALESCE(pa.email, ''), c.custom_attributes, " +
			"c.created_at, c.last_activity_at, " +
			"(SELECT count(*) FROM conversations cv WHERE cv.contact_id = c.id AND cv.deleted_at IS NULL AND " + v.scopeSQL(a) + ")::integer, " +
			"(c.name = ''), lower(c.name)"
	}
	sql := "SELECT " + sel + " FROM contacts c" +
		" LEFT JOIN organizations o ON o.id = c.organization_id" +
		" LEFT JOIN LATERAL (SELECT email FROM contact_addresses WHERE contact_id = c.id ORDER BY is_primary DESC, email LIMIT 1) pa ON true" +
		" WHERE " + where + " ORDER BY " + spec.orderBy + " LIMIT " + a.add(p.Limit+1)

	rows, err := db.Query(ctx, sql, a.values...)
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var row Row
		var created, activity pgtype.Timestamptz
		if err := r.Scan(&row.ID, &row.Name, &row.Phone, &row.OrganizationID, &row.OrganizationName, &row.PrimaryEmail,
			&row.CustomAttributes, &created, &activity, &row.ConversationCount, &row.sortBool, &row.sortText); err != nil {
			return Row{}, err
		}
		row.CreatedAt, row.LastActivityAt = created.Time, activity.Time
		return row, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read contacts: %w", err)
	}
	return out, nil
}

// Count is the number of contacts the viewer can see that match p, ignoring paging.
func Count(ctx context.Context, db dbq.DBTX, v Viewer, p ListParams, defs []dbq.CustomAttributeDef) (int64, error) {
	a := &sqlArgs{}
	sql := "SELECT count(*) FROM contacts c LEFT JOIN organizations o ON o.id = c.organization_id WHERE " + buildWhere(v, p, defs, a)
	var n int64
	if err := db.QueryRow(ctx, sql, a.values...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count contacts: %w", err)
	}
	return n, nil
}

// Each walks every match in keyset pages of batch rows and calls fn per page.
func Each(ctx context.Context, db dbq.DBTX, v Viewer, p ListParams, batch int, defs []dbq.CustomAttributeDef, fn func([]Row) error) error {
	p.Limit, p.Cursor = batch, ""
	for {
		rows, err := List(ctx, db, v, p, defs)
		if err != nil {
			return err
		}
		more := len(rows) > batch
		if more {
			rows = rows[:batch]
		}
		if len(rows) > 0 {
			if err := fn(rows); err != nil {
				return err
			}
		}
		if !more {
			return nil
		}
		if p.Cursor, err = NextCursor(rows, p); err != nil {
			return err
		}
	}
}

// ErrSegmentNotFound is returned for a segment that does not exist or is someone else's
// personal segment.
var ErrSegmentNotFound = errors.New("segment not found")

// ResolveSegment calls fn with the ids of the contacts in the segment that v may see, in
// batches. A personal segment resolves only for its owner; a shared one for everybody. It
// is what campaigns use to pick their recipients.
func ResolveSegment(ctx context.Context, q *dbq.Queries, db dbq.DBTX, segmentID pgtype.UUID, v Viewer, batch int, fn func(ids []pgtype.UUID) error) error {
	seg, err := q.GetSegment(ctx, segmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSegmentNotFound
	}
	if err != nil {
		return fmt.Errorf("load segment: %w", err)
	}
	if seg.OwnerUserID != v.UserID && !seg.Shared {
		return ErrSegmentNotFound
	}
	defs, err := q.ListAttributeDefs(ctx, pgtype.Text{})
	if err != nil {
		return fmt.Errorf("load attribute definitions: %w", err)
	}
	filter, err := ParseFilter(seg.Filter, defs)
	if err != nil {
		return fmt.Errorf("segment %s: %w", segmentID, err)
	}
	p := ListParams{Filter: &filter, Sort: SortCreated, IDsOnly: true}
	return Each(ctx, db, v, p, batch, defs, func(rows []Row) error {
		ids := make([]pgtype.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		return fn(ids)
	})
}
