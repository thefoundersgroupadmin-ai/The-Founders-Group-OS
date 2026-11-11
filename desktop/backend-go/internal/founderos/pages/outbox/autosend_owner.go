package outbox

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Mail an agent addresses to the owner's own inbox ("email me the list") is
// not outreach: the owner asked for it and is the only reader, so it sends
// without a click (owner instruction, 2026-11-04). Only exact owner addresses
// qualify (never the whole domain); at most dailyCap such sends in 24 hours
// guard against a looping agent. The send goes through Approve, so the claim,
// guard and audit trail are the same as for a click.
const dueOwnerMail = `
	SELECT id, subject, body FROM founderos_outbox
	WHERE workspace_id = $1 AND status = 'pending' AND lower(to_address) = ANY($2)
	ORDER BY created_at, id`

const ownerMailSentToday = `
	SELECT count(*) FROM founderos_outbox
	WHERE workspace_id = $1 AND status = 'sent' AND sent_at > now() - interval '24 hours'
	  AND lower(to_address) = ANY($2)`

// AutoSendToOwner sends pending outbox mail addressed to one of owners and
// returns how many it sent.
func AutoSendToOwner(ctx context.Context, pool *pgxpool.Pool, workspaceSlug string, owners []string, dailyCap int, send SendFunc) (int, error) {
	var addrs []string
	for _, a := range owners {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 || dailyCap <= 0 {
		return 0, nil
	}
	ws, err := workspaceID(ctx, pool, workspaceSlug)
	if err != nil {
		return 0, err
	}
	var already int
	if err := pool.QueryRow(ctx, ownerMailSentToday, ws, addrs).Scan(&already); err != nil {
		return 0, err
	}
	if already >= dailyCap {
		return 0, nil
	}
	rows, err := pool.Query(ctx, dueOwnerMail, ws, addrs)
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
		if err != nil && !errors.Is(err, ErrNotPending) && !errors.Is(err, ErrSuppressed) {
			errs = append(errs, err)
		}
	}
	return sent, errors.Join(errs...)
}
