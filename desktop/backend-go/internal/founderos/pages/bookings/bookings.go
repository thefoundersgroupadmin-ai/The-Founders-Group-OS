// Package bookings moves pipeline contacts on when they book a call: a
// Cal.com booking whose attendee is a contact still at first_touch makes that
// contact engaged (which also stops automatic follow-ups) and logs a
// "Booked on Cal.com" touch dated the call. Cancelled and rejected bookings
// count for nothing; a contact further along never moves back. Re-running
// changes nothing.
package bookings

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/caldotcom"
)

// MarkBooked applies bookings to the pipeline and returns how many contacts moved.
func MarkBooked(ctx context.Context, pool *pgxpool.Pool, list []caldotcom.Booking) (int, error) {
	moved := 0
	for _, b := range list {
		if b.Status == "cancelled" || b.Status == "rejected" {
			continue
		}
		day := b.Start
		if len(day) >= 10 {
			day = day[:10]
		}
		for _, a := range b.Attendees {
			rows, err := pool.Query(ctx, `
				WITH c AS (SELECT id, workspace_id FROM founderos_funnel_contacts WHERE lower(email) = lower($1)),
				touched AS (
					INSERT INTO founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at)
					SELECT c.id || '-calcom-' || $2, c.workspace_id, c.id,
					       (SELECT coalesce(max(seq), 0) + 1 FROM founderos_funnel_touches t WHERE t.contact_id = c.id),
					       'engaged', 'call', 'Booked on Cal.com: ' || $3, 'calendar', $4::date
					FROM c ON CONFLICT (id) DO NOTHING RETURNING contact_id)
				UPDATE founderos_funnel_contacts f SET status = 'engaged', relationship = 'warm'
				FROM c WHERE f.id = c.id AND f.status = 'first_touch'
				RETURNING f.id`, a.Email, b.UID, b.Title, day)
			if err != nil {
				return moved, err
			}
			for rows.Next() {
				moved++
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return moved, err
			}
		}
	}
	return moved, nil
}
