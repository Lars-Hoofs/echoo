package api

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

func TestToUserJSONMaxOpen(t *testing.T) {
	if got := toUserJSON(dbq.User{}).MaxOpen; got != nil {
		t.Fatalf("unset capacity: got %d, want nil", *got)
	}
	got := toUserJSON(dbq.User{MaxOpen: pgtype.Int4{Int32: 25, Valid: true}}).MaxOpen
	if got == nil || *got != 25 {
		t.Fatalf("capacity 25: got %v", got)
	}
}
