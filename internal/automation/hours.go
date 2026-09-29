package automation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/sla"
)

// DefinitionFromRow converts a stored schedule into the calculator's input.
func DefinitionFromRow(row dbq.BusinessHour) (sla.Definition, error) {
	def := sla.Definition{Timezone: row.Timezone}
	if err := json.Unmarshal(row.Weekly, &def.Weekly); err != nil {
		return def, fmt.Errorf("decode weekly hours of %s: %w", row.ID.String(), err)
	}
	for _, h := range row.Holidays {
		if h.Valid {
			def.Holidays = append(def.Holidays, sla.Date{Year: h.Time.Year(), Month: h.Time.Month(), Day: h.Time.Day()})
		}
	}
	return def, nil
}

func scheduleFromRow(row dbq.BusinessHour) (*sla.Schedule, error) {
	def, err := DefinitionFromRow(row)
	if err != nil {
		return nil, err
	}
	s, err := sla.New(def)
	if err != nil {
		return nil, fmt.Errorf("business hours %s: %w", row.ID.String(), err)
	}
	return s, nil
}

// schedule loads one schedule; an invalid id means calendar time (nil).
func (e *Engine) schedule(ctx context.Context, id pgtype.UUID) (*sla.Schedule, error) {
	if !id.Valid {
		return nil, nil
	}
	row, err := e.q.AutoGetBusinessHours(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load business hours: %w", err)
	}
	return scheduleFromRow(row)
}
