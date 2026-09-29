package search

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

const maxListValues = 20

// Filters is the structured form of the list filters. It is what a saved view stores and what
// the list endpoint's query parameters translate to. The schema is closed: unknown fields are rejected.
type Filters struct {
	Status         string   `json:"status,omitempty"`
	MailboxIDs     []string `json:"mailbox_ids,omitempty"`
	LabelIDs       []string `json:"label_ids,omitempty"`
	TeamIDs        []string `json:"team_ids,omitempty"`
	Assignee       string   `json:"assignee,omitempty"`
	Priorities     []string `json:"priorities,omitempty"`
	HasAttachment  bool     `json:"has_attachment,omitempty"`
	After          string   `json:"after,omitempty"`
	Before         string   `json:"before,omitempty"`
	ContactID      string   `json:"contact_id,omitempty"`
	OrganizationID string   `json:"organization_id,omitempty"`
}

var (
	listStatuses = []string{"open", "waiting", "closed", "spam", "snoozed"}
	priorities   = []string{"none", "low", "normal", "high", "urgent"}
)

// DecodeFilters reads stored filters strictly.
func DecodeFilters(raw []byte) (Filters, error) {
	var f Filters
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Filters{}, fmt.Errorf("decode filters: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Filters{}, errors.New("decode filters: trailing data")
	}
	return f, nil
}

// Validate returns the invalid fields, keyed by JSON name, or nil.
func (f Filters) Validate() map[string]string {
	bad := map[string]string{}
	if f.Status != "" && !slices.Contains(listStatuses, f.Status) {
		bad["status"] = "invalid"
	}
	for name, ids := range map[string][]string{"mailbox_ids": f.MailboxIDs, "label_ids": f.LabelIDs, "team_ids": f.TeamIDs} {
		if len(ids) > maxListValues || !allUUIDs(ids) {
			bad[name] = "invalid"
		}
	}
	if f.Assignee != "" && f.Assignee != "me" && f.Assignee != "none" && !isUUID(f.Assignee) {
		bad["assignee"] = "invalid"
	}
	if len(f.Priorities) > len(priorities) || slices.ContainsFunc(f.Priorities, func(p string) bool { return !slices.Contains(priorities, p) }) {
		bad["priorities"] = "invalid"
	}
	for name, v := range map[string]string{"after": f.After, "before": f.Before} {
		if v == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, v); err != nil {
			bad[name] = "invalid"
		}
	}
	for name, v := range map[string]string{"contact_id": f.ContactID, "organization_id": f.OrganizationID} {
		if v != "" && !isUUID(v) {
			bad[name] = "invalid"
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return bad
}

func isUUID(s string) bool {
	var id pgtype.UUID
	return id.Scan(s) == nil
}

func allUUIDs(list []string) bool {
	return !slices.ContainsFunc(list, func(s string) bool { return !isUUID(s) })
}
