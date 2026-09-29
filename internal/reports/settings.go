package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SettingsKey is the row of the settings table that holds Settings.
const SettingsKey = "reports"

// Settings are the workspace-wide reporting settings.
type Settings struct {
	Timezone string `json:"timezone"`
}

// Timezone is the workspace timezone that cuts days, or DefaultTimezone when none was chosen.
func (s *Service) Timezone(ctx context.Context) (*time.Location, error) {
	set := Settings{Timezone: DefaultTimezone}
	raw, err := s.q.GetSetting(ctx, SettingsKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("load report settings: %w", err)
	default:
		if err := json.Unmarshal(raw, &set); err != nil {
			return nil, fmt.Errorf("decode report settings: %w", err)
		}
	}
	return LoadLocation(set.Timezone)
}
