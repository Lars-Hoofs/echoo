package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const maxJSONBody = 1 << 20

// apiError is the only error shape the API returns. Code is stable and mapped to Dutch copy
// by the frontend; Message is for developers.
type apiError struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
	// CurrentVersion accompanies version_conflict so the client can retry against it.
	CurrentVersion *int32 `json:"current_version,omitempty"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errBadRequest(msg string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "invalid_request", Message: msg}
}

func errValidation(fields map[string]string) *apiError {
	return &apiError{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: "one or more fields are invalid", Fields: fields}
}

var (
	errUnauthenticated = &apiError{Status: http.StatusUnauthorized, Code: "unauthenticated", Message: "sign in required"}
	errForbidden       = &apiError{Status: http.StatusForbidden, Code: "forbidden", Message: "not allowed"}
	errNotFound        = &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "not found"}
	errCSRF            = &apiError{Status: http.StatusForbidden, Code: "csrf_failed", Message: "missing or invalid CSRF token or origin"}
	errRateLimited     = &apiError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many attempts, try again later"}
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	// The client may have gone away; there is nothing useful to do with a write error.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		slog.ErrorContext(r.Context(), "request failed", "err", err, "request_id", requestIDFrom(r.Context()))
		ae = &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
	}
	writeJSON(w, ae.Status, map[string]*apiError{"error": ae})
}

// decode reads exactly one JSON object with no unknown fields.
func decode(r *http.Request, dst any) error {
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || ct != "application/json" {
		return &apiError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "expected application/json"}
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errBadRequest(fmt.Sprintf("invalid JSON body: %v", err))
	}
	if dec.More() {
		return errBadRequest("body must contain a single JSON object")
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func parseUUID(s string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return id, false
	}
	return id, true
}

func timeOrNil(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func uuidStr(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

// cleanText trims and rejects control characters in short single-line fields.
func cleanText(s string, maxRunes int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > maxRunes {
		return s, false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return s, false
		}
	}
	return s, true
}
