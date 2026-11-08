package outbox

// The pod send gate (docs/tfg/CLIENT-PODS.md, Codex review 8 Nov 2026). Every
// send from a client pod workspace (a click, a follow-up, the campaign) comes
// through Approve, and Approve hands it here. Under one lock per pod, with the
// pod re-read so a pause lands before dispatch, it checks:
//
//   - status: only live sends; dry_run goes to the pod's sink; anything else holds;
//   - identity: the pod's own account only; a row naming another is refused;
//   - suppression: the pod's and the global address/domain lists;
//   - no second email to a person while another to them is sending or uncertain;
//   - the pod's sending window (its timezone, hours, weekends) and daily caps,
//     first emails and follow-ups counted separately, in-flight sends included.
//
// A hold leaves the row pending (it is tried again on a later pass); a refusal
// rejects it with the reason. A dry run is recorded as 'simulated', so it never
// counts as a delivery, against a cap or in "already emailed".

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrHeld: not sent now, still pending (pod not live, outside hours, cap
	// reached, another email to that person in flight).
	ErrHeld = errors.New("held")
	// ErrSuppressed: the address or its domain is on a do-not-email list.
	ErrSuppressed = errors.New("suppressed")
	// ErrForeignAccount: the row names a sending identity that is not the pod's.
	ErrForeignAccount = errors.New("names another sending account")
)

// nowFunc is swapped in tests.
var nowFunc = time.Now

type podRules struct {
	ID, Slug, Status, AccountID, Sink, Timezone string
	Start, End                                  int
	Weekends                                    bool
	FirstCap, FollowCap                         int
}

const podRulesSQL = `SELECT id::text, slug, status, coalesce(sending_account_id, ''), dry_run_inbox, timezone,
	send_window_start, send_window_end, send_weekends, first_email_daily_cap, follow_up_daily_cap
	FROM founderos_client_pods WHERE workspace_id = $1`

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadPodRules(ctx context.Context, q querier, ws string) (*podRules, error) {
	var r podRules
	var s, e int16
	err := q.QueryRow(ctx, podRulesSQL, ws).Scan(&r.ID, &r.Slug, &r.Status, &r.AccountID, &r.Sink, &r.Timezone,
		&s, &e, &r.Weekends, &r.FirstCap, &r.FollowCap)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	r.Start, r.End = int(s), int(e)
	return &r, err
}

// inWindow reports whether now is a sending time for the pod.
func (r *podRules) inWindow(now time.Time) bool {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return false
	}
	t := now.In(loc)
	if !r.Weekends && (t.Weekday() == time.Saturday || t.Weekday() == time.Sunday) {
		return false
	}
	return t.Hour() >= r.Start && t.Hour() < r.End
}

func held(why string) error { return fmt.Errorf("%w: %s", ErrHeld, why) }

func approvePod(ctx context.Context, pool *pgxpool.Pool, ws, id string, content Content, send SendFunc) (*Item, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('founderos-pod-send:' || $1))`, ws); err != nil {
		return nil, err
	}
	r, err := loadPodRules(ctx, tx, ws)
	if err != nil || r == nil {
		return nil, fmt.Errorf("pod rules: %v", err)
	}
	it, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM founderos_outbox WHERE workspace_id = $1 AND id = $2
		AND (status IN ('pending', 'failed') OR (status = 'uncertain' AND $3)) FOR UPDATE`, ws, id, content.Force))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notPendingOrMissing(ctx, pool, ws, id)
	}
	if err != nil {
		return nil, err
	}
	refuse := func(cause error, why string) (*Item, error) {
		msg := cause.Error() + ": " + why
		if _, err := tx.Exec(ctx, `UPDATE founderos_outbox SET status = 'rejected', decided_at = now(), error = $3
			WHERE workspace_id = $1 AND id = $2`, ws, id, msg); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO founderos_client_activity (pod_id, kind, summary, ref) VALUES ($1, 'email_refused', $2, $3)`,
			r.ID, "Not sent to "+it.To+": "+why, id); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		it.Status, it.Error = "rejected", &msg
		return &it, fmt.Errorf("%w: %s", cause, why)
	}

	if r.Status != "live" && r.Status != "dry_run" {
		return nil, held("pod is " + strings.ReplaceAll(r.Status, "_", " "))
	}
	if it.AccountID != nil && *it.AccountID != "" && *it.AccountID != r.AccountID {
		return refuse(ErrForeignAccount, fmt.Sprintf("%q is not this pod's account", *it.AccountID))
	}
	addr := strings.ToLower(strings.TrimSpace(it.To))
	domain := addr[strings.LastIndex(addr, "@")+1:]
	var suppressed string
	err = tx.QueryRow(ctx, `SELECT reason FROM founderos_email_suppression
		WHERE (workspace_id = $1 OR workspace_id IS NULL) AND (address = $2 OR domain = $3) LIMIT 1`, ws, addr, domain).Scan(&suppressed)
	if err == nil {
		return refuse(ErrSuppressed, "on the do-not-email list ("+suppressed+")")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var inFlight bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM founderos_outbox WHERE workspace_id = $1 AND id <> $2
		AND lower(to_address) = $3 AND status IN ('sending', 'uncertain'))`, ws, id, addr).Scan(&inFlight); err != nil {
		return nil, err
	}
	if inFlight {
		return nil, held("another email to this person is sending or its delivery is uncertain")
	}

	dry := r.Status == "dry_run"
	if dry && strings.TrimSpace(r.Sink) == "" {
		return nil, held("dry run has no approved sink inbox")
	}
	if !dry {
		if r.AccountID == "" {
			return nil, held("pod has no sending account")
		}
		if !r.inWindow(nowFunc()) {
			return nil, held("outside the pod's sending hours")
		}
		var followUp bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM founderos_outbox WHERE workspace_id = $1
			AND lower(to_address) = $2 AND status = 'sent')`, ws, addr).Scan(&followUp); err != nil {
			return nil, err
		}
		var used int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM founderos_outbox o WHERE o.workspace_id = $1
			AND (o.status = 'sending' OR (o.status = 'sent' AND o.sent_at > now() - interval '24 hours'))
			AND EXISTS (SELECT 1 FROM founderos_outbox p WHERE p.workspace_id = o.workspace_id
				AND lower(p.to_address) = lower(o.to_address) AND p.status = 'sent' AND p.id <> o.id
				AND p.sent_at < coalesce(o.sent_at, now())) = $2`, ws, followUp).Scan(&used); err != nil {
			return nil, err
		}
		limit, kind := r.FirstCap, "first-email"
		if followUp {
			limit, kind = r.FollowCap, "follow-up"
		}
		if used >= limit {
			return nil, held(fmt.Sprintf("the pod's daily %s cap (%d) is reached", kind, limit))
		}
	}

	it, err = scan(tx.QueryRow(ctx, `UPDATE founderos_outbox
		SET status = 'sending', decided_at = now(), error = NULL, attempt = attempt + 1, subject = $3, body = $4
		WHERE workspace_id = $1 AND id = $2 RETURNING `+cols, ws, id, content.Subject, content.Body))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendBudget)
	defer cancel()
	out := it
	if r.AccountID != "" {
		acct := r.AccountID
		out.AccountID, out.RequireAccount = &acct, true
	}
	if dry {
		out.To = r.Sink
		out.Subject = "[DRY RUN → " + it.To + "] " + it.Subject
		out.Body = "DRY RUN for " + r.Slug + ". Nothing was sent to the prospect.\nWould have gone to: " + it.To +
			"\n----------------------------------------\n\n" + it.Body
	}
	if sendErr := send(ctx, out); sendErr != nil {
		status := "failed"
		if errors.Is(sendErr, ErrUncertain) {
			status = "uncertain"
		}
		msg := sendErr.Error()
		if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET status = $3, error = $4
			WHERE workspace_id = $1 AND id = $2 AND attempt = $5`, ws, id, status, msg, it.Attempt); err != nil {
			return nil, err
		}
		it.Status, it.Error = status, &msg
		return &it, sendErr
	}
	final, kind, summary := "sent", "email_sent", "Emailed "+it.To+": "+it.Subject
	if dry {
		final, kind, summary = "simulated", "email_simulated", "Dry run: email to "+it.To+" delivered to "+r.Sink
	}
	if err := pool.QueryRow(ctx, `UPDATE founderos_outbox SET status = $4, sent_at = now()
		WHERE workspace_id = $1 AND id = $2 AND attempt = $3 RETURNING sent_at`, ws, id, it.Attempt, final).Scan(&it.SentAt); err != nil {
		return nil, err
	}
	it.Status = final
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_client_activity (pod_id, kind, summary, ref) VALUES ($1, $2, $3, $4)`,
		r.ID, kind, summary, id); err != nil {
		return &it, fmt.Errorf("sent, but the activity was not recorded: %w", err)
	}
	if !dry && it.ContactID != nil {
		if err := recordTouch(ctx, pool, *it.ContactID, it.Subject); err != nil {
			return &it, fmt.Errorf("sent, but the pipeline touch was not recorded: %w", err)
		}
	}
	return &it, nil
}
