package outbox

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Follow-ups the owner pre-approved (2026-10-22) send without a click. A
// pending row is a safe follow-up only when all of these hold:
//   - it goes to an address this outbox already emailed (never a first contact),
//     with one or two earlier sends (at most two follow-ups),
//   - the latest of those went out at least two days ago,
//   - it is linked to a pipeline contact still at first_touch, so a reply or a
//     booked call (engaged) or a held call (nurtured) stops it,
//   - the owner never rejected an email to that address.
//
// No more than dailyCap follow-ups go out in any 24 hours. The send goes
// through Approve with the agent's own wording, so the claim, guard and
// pipeline touch work exactly as for a click.
const dueFollowUps = `
	SELECT o.id, o.subject, o.body FROM founderos_outbox o
	JOIN founderos_funnel_contacts c ON c.id = o.contact_id
	WHERE o.workspace_id = $1 AND o.status = 'pending'
	  AND c.status = 'first_touch'
	  AND (SELECT count(*) FROM founderos_outbox p WHERE p.workspace_id = o.workspace_id
	       AND lower(p.to_address) = lower(o.to_address) AND p.status = 'sent') BETWEEN 1 AND 2
	  AND (SELECT max(p.sent_at) FROM founderos_outbox p WHERE p.workspace_id = o.workspace_id
	       AND lower(p.to_address) = lower(o.to_address) AND p.status = 'sent') <= now() - interval '2 days'
	  AND NOT EXISTS (SELECT 1 FROM founderos_outbox p WHERE p.workspace_id = o.workspace_id
	       AND lower(p.to_address) = lower(o.to_address) AND p.status = 'rejected')
	ORDER BY o.created_at, o.id`

// followUpsSentToday counts sends in the last 24 hours that followed an
// earlier send to the same address.
const followUpsSentToday = `
	SELECT count(*) FROM founderos_outbox o
	WHERE o.workspace_id = $1 AND o.status = 'sent' AND o.sent_at > now() - interval '24 hours'
	  AND EXISTS (SELECT 1 FROM founderos_outbox p WHERE p.workspace_id = o.workspace_id
	       AND lower(p.to_address) = lower(o.to_address) AND p.status = 'sent' AND p.sent_at < o.sent_at)`

// AutoSendFollowUps sends the safe follow-ups waiting in the outbox, up to
// dailyCap in 24 hours, and returns how many it sent. A failed send is left
// recorded on its row (retryable from /os/outbox) and the pass carries on.
func AutoSendFollowUps(ctx context.Context, pool *pgxpool.Pool, workspaceSlug string, dailyCap int, send SendFunc) (int, error) {
	ws, err := workspaceID(ctx, pool, workspaceSlug)
	if err != nil {
		return 0, err
	}
	var already int
	if err := pool.QueryRow(ctx, followUpsSentToday, ws).Scan(&already); err != nil {
		return 0, err
	}
	if already >= dailyCap {
		return 0, nil
	}
	rows, err := pool.Query(ctx, dueFollowUps, ws)
	if err != nil {
		return 0, err
	}
	type due struct{ id, subject, body string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.subject, &d.body); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	sent := 0
	var errs []error
	for _, d := range list {
		if already+sent >= dailyCap {
			break
		}
		it, err := Approve(ctx, pool, workspaceSlug, d.id, Content{Subject: d.subject, Body: d.body}, send)
		if it != nil && it.Status == "sent" {
			sent++
		}
		if err != nil && !errors.Is(err, ErrNotPending) {
			errs = append(errs, err)
		}
	}
	return sent, errors.Join(errs...)
}
