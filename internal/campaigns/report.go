package campaigns

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/contacts"
	"echoo/internal/db/dbq"
)

// ReportBatch is how many report rows are read per page.
const ReportBatch = 500

// WriteReport streams the per-recipient report as CSV. Cells that a spreadsheet would run as a
// formula are neutralised by the CSV writer; names and addresses come from outside. Only
// recipients whose contact v may see are included. beforePage runs before each page, so a
// caller can renew a write deadline during a long export.
func (s *Service) WriteReport(ctx context.Context, campaign pgtype.UUID, v contacts.Viewer, w io.Writer, beforePage func() error) error {
	cw, err := contacts.NewCSVWriter(w)
	if err != nil {
		return err
	}
	if err := cw.Write("E-mailadres", "Naam", "Status", "Reden overgeslagen", "Fout", "In wachtrij gezet", "Afgerond"); err != nil {
		return err
	}
	after := pgtype.UUID{Valid: true}
	for {
		if err := beforePage(); err != nil {
			return err
		}
		rows, err := s.q.CampaignListRecipients(ctx, dbq.CampaignListRecipientsParams{
			CampaignID: campaign, After: after, Admin: v.Admin, UserID: v.UserID, MailboxIds: v.MailboxIDs, Batch: ReportBatch,
		})
		if err != nil {
			return fmt.Errorf("load report page: %w", err)
		}
		for _, r := range rows {
			if err := cw.Write(r.Email, r.Name, r.State, r.SkipReason, r.Error, formatTime(r.QueuedAt), formatTime(r.FinishedAt)); err != nil {
				return err
			}
		}
		if len(rows) < ReportBatch {
			return cw.Flush()
		}
		after = rows[len(rows)-1].ID
	}
}

func formatTime(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}
