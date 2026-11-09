package outbox

// DispatchPods is the pod sender's pass (every few minutes): for each pod that
// is live or in a dry run, it walks the pod's waiting emails oldest first
// through the screen and the pod gate (podgate.go), and sends at most one per
// pod per pass so a day's sends spread out. Walking a row also files its
// client approval request, so the portal shows everything awaiting the client.
// Why a row is still waiting is written on the row.

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DispatchResult is one pass.
type DispatchResult struct {
	Sent, Simulated, Waiting int
}

func DispatchPods(ctx context.Context, pool *pgxpool.Pool, ownDomain string, send SendFunc) (DispatchResult, error) {
	return DispatchPodsExcept(ctx, pool, ownDomain, send, nil)
}

// DispatchPodsExcept dispatches every running pod except those in hold (pods
// whose own calendar could not be read this pass, so a fresh booking could be
// missed). One pod's trouble never holds another, nor does TFG's own calendar
// (Codex final review #5).
func DispatchPodsExcept(ctx context.Context, pool *pgxpool.Pool, ownDomain string, send SendFunc, hold map[string]bool) (DispatchResult, error) {
	var res DispatchResult
	rows, err := pool.Query(ctx, `SELECT p.slug, p.sender_name, p.workspace_id::text FROM founderos_client_pods p
		WHERE p.status IN ('live', 'dry_run') ORDER BY p.slug`)
	if err != nil {
		return res, err
	}
	type pod struct{ slug, signer, ws string }
	var list []pod
	for rows.Next() {
		var p pod
		if err := rows.Scan(&p.slug, &p.signer, &p.ws); err != nil {
			rows.Close()
			return res, err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	var errs []error
	for _, p := range list {
		if hold[p.slug] {
			continue
		}
		if err := dispatchPod(ctx, pool, p.slug, p.ws, firstName(p.signer), ownDomain, send, &res); err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

func firstName(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func noteWaiting(ctx context.Context, pool *pgxpool.Pool, ws, id, why string) {
	_, _ = pool.Exec(ctx, `UPDATE founderos_outbox SET error = $3 WHERE workspace_id = $1 AND id = $2 AND status = 'pending'`, ws, id, why)
}

func dispatchPod(ctx context.Context, pool *pgxpool.Pool, slug, ws, signer, ownDomain string, send SendFunc, res *DispatchResult) error {
	// Replies to people who wrote back go first and do not use up the pass's one outreach send.
	rows, err := pool.Query(ctx, `SELECT `+cols+` FROM founderos_outbox WHERE workspace_id = $1 AND status = 'pending'
		ORDER BY (coalesce(sequence_key, '') = 'reply') DESC, created_at, id`, ws)
	if err != nil {
		return err
	}
	var due []Item
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			rows.Close()
			return err
		}
		due = append(due, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, it := range due {
		if signer == "" {
			noteWaiting(ctx, pool, ws, it.ID, "waiting: the pod has no sender name to sign with")
			res.Waiting++
			continue
		}
		if err := ScreenSigned(it, ownDomain, signer); err != nil {
			noteWaiting(ctx, pool, ws, it.ID, "waiting: "+err.Error())
			res.Waiting++
			continue
		}
		got, err := Approve(ctx, pool, slug, it.ID, Content{Subject: it.Subject, Body: it.Body}, send)
		switch {
		case err == nil && got != nil && (got.Status == "sent" || got.Status == "simulated"):
			if got.Status == "sent" {
				res.Sent++
			} else {
				res.Simulated++
			}
			if it.SequenceKey == nil || *it.SequenceKey != "reply" {
				return nil
			}
		case errors.Is(err, ErrHeld), errors.Is(err, ErrAwaitingClient):
			noteWaiting(ctx, pool, ws, it.ID, "waiting: "+err.Error())
			res.Waiting++
		case errors.Is(err, ErrSuppressed), errors.Is(err, ErrForeignAccount), errors.Is(err, ErrNotPending):
			// recorded on the row by the gate
		case err != nil:
			return err
		}
	}
	return nil
}
