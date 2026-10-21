// Package outbox serves /os/outbox: agent-drafted email waiting for the
// operator (founderos_outbox, migration 166). Nothing here sends on its own:
// Approve claims one waiting row, hands the operator's reviewed subject and
// body to the caller's send func (the email connector, behind
// FOUNDEROS_WRITES and the mail guard) and records the outcome, plus a
// pipeline touch when the row names a funnel contact.
package outbox

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
	// ErrNoWorkspace means the workspace row is missing (bootstrap not run).
	ErrNoWorkspace = errors.New("workspace not found: run founderos-bootstrap")
	ErrNotFound    = errors.New("no such outbox email")
	// ErrNotPending means another approve, a reject or a send already took
	// the row, or it is uncertain and the retry was not forced: do not send.
	ErrNotPending = errors.New("this email is no longer waiting for approval")
	ErrBadContent = errors.New("approve needs the reviewed subject (1-300 characters) and body (1-20000)")
	// ErrUncertain wraps a send that may have been delivered (the server may
	// hold the message). The row becomes 'uncertain' and is never retried
	// without an explicit force.
	ErrUncertain = errors.New("delivery uncertain")
)

// StaleAfter is how long a 'sending' claim may stand before it is treated as
// an interrupted send (process died mid-send) and marked uncertain.
const StaleAfter = 10 * time.Minute

// sendBudget bounds one send plus its bookkeeping, detached from the request
// so a closed browser tab cannot cut a send off or lose its outcome.
const sendBudget = 2 * time.Minute

// Statuses are the founderos_outbox CHECK values.
var Statuses = []string{"pending", "sending", "sent", "rejected", "failed", "uncertain"}

// Item is one outbox row.
type Item struct {
	ID        string     `json:"id"`
	Source    string     `json:"source"`
	SourceRef *string    `json:"sourceRef"`
	ContactID *string    `json:"contactId"`
	AccountID *string    `json:"accountId"`
	To        string     `json:"to"`
	ToName    *string    `json:"toName"`
	Company   *string    `json:"company"`
	Subject   string     `json:"subject"`
	Body      string     `json:"body"`
	Status    string     `json:"status"`
	Error     *string    `json:"error"`
	Attempt   int        `json:"attempt"`
	CreatedAt time.Time  `json:"createdAt"`
	DecidedAt *time.Time `json:"decidedAt"`
	SentAt    *time.Time `json:"sentAt"`
}

// Page is the /os/outbox model: everything needing a decision, then the
// recent outcomes.
type Page struct {
	Pending []Item `json:"pending"`
	Recent  []Item `json:"recent"`
}

// Content is exactly what the operator reviewed; it is what gets sent, so an
// agent revising the draft after the page loaded cannot change the email.
// Force allows a retry of an uncertain row (the operator checked it did not
// arrive).
type Content struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Force   bool   `json:"force"`
}

// SendFunc delivers one approved email; a nil error means it left the bridge.
// Wrap an error with ErrUncertain when the message may have been delivered.
type SendFunc func(ctx context.Context, it Item) error

const cols = `id, source, source_ref, contact_id, account_id, to_address, to_name, company,
	subject, body, status, error, attempt, created_at, decided_at, sent_at`

func scan(row pgx.Row) (Item, error) {
	var it Item
	err := row.Scan(&it.ID, &it.Source, &it.SourceRef, &it.ContactID, &it.AccountID, &it.To, &it.ToName, &it.Company,
		&it.Subject, &it.Body, &it.Status, &it.Error, &it.Attempt, &it.CreatedAt, &it.DecidedAt, &it.SentAt)
	return it, err
}

func workspaceID(ctx context.Context, pool *pgxpool.Pool, slug string) (string, error) {
	var ws string
	err := pool.QueryRow(ctx, `SELECT id::text FROM workspaces WHERE slug = $1`, slug).Scan(&ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w (%s)", ErrNoWorkspace, slug)
	}
	return ws, err
}

// ReconcileStale marks 'sending' claims older than StaleAfter as uncertain:
// the send was interrupted and may or may not have been delivered.
func ReconcileStale(ctx context.Context, pool *pgxpool.Pool, workspaceID string, now time.Time) error {
	_, err := pool.Exec(ctx, `UPDATE founderos_outbox SET status = 'uncertain',
		error = 'Interrupted while sending; delivery unknown. Check the sending log before retrying.'
		WHERE workspace_id = $1 AND status = 'sending' AND decided_at < $2`, workspaceID, now.Add(-StaleAfter))
	return err
}

// Load reads the outbox: rows needing a decision oldest first (the order
// agents filed them), then the 50 most recent outcomes.
func Load(ctx context.Context, pool *pgxpool.Pool, workspaceSlug string) (*Page, error) {
	ws, err := workspaceID(ctx, pool, workspaceSlug)
	if err != nil {
		return nil, err
	}
	if err := ReconcileStale(ctx, pool, ws, time.Now()); err != nil {
		return nil, err
	}
	page := &Page{Pending: []Item{}, Recent: []Item{}}
	for _, q := range []struct {
		dst *[]Item
		sql string
	}{
		{&page.Pending, `SELECT ` + cols + ` FROM founderos_outbox WHERE workspace_id = $1
			AND status IN ('pending', 'sending', 'failed', 'uncertain') ORDER BY created_at, id`},
		{&page.Recent, `SELECT ` + cols + ` FROM founderos_outbox WHERE workspace_id = $1
			AND status IN ('sent', 'rejected') ORDER BY COALESCE(sent_at, decided_at) DESC, id LIMIT 50`},
	} {
		rows, err := pool.Query(ctx, q.sql, ws)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			it, err := scan(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			*q.dst = append(*q.dst, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (c Content) normalized() (Content, bool) {
	c.Subject, c.Body = strings.TrimSpace(c.Subject), strings.TrimSpace(c.Body)
	s, b := len([]rune(c.Subject)), len([]rune(c.Body))
	return c, s > 0 && s <= 300 && b > 0 && b <= 20000
}

// Approve claims a waiting row (pending or failed; uncertain only when
// forced), stores the reviewed content, sends exactly that and records sent,
// failed or uncertain. The claim is one conditional UPDATE, so concurrent
// approves send at most once.
func Approve(ctx context.Context, pool *pgxpool.Pool, workspaceSlug, id string, content Content, send SendFunc) (*Item, error) {
	content, ok := content.normalized()
	if !ok {
		return nil, ErrBadContent
	}
	ws, err := workspaceID(ctx, pool, workspaceSlug)
	if err != nil {
		return nil, err
	}
	it, err := scan(pool.QueryRow(ctx, `UPDATE founderos_outbox
		SET status = 'sending', decided_at = now(), error = NULL, attempt = attempt + 1, subject = $3, body = $4
		WHERE workspace_id = $1 AND id = $2
		  AND (status IN ('pending', 'failed') OR (status = 'uncertain' AND $5))
		RETURNING `+cols, ws, id, content.Subject, content.Body, content.Force))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notPendingOrMissing(ctx, pool, ws, id)
	}
	if err != nil {
		return nil, err
	}

	// From the claim on, the request may go away; the send and its record must not.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendBudget)
	defer cancel()
	if sendErr := send(ctx, it); sendErr != nil {
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
	if err := pool.QueryRow(ctx, `UPDATE founderos_outbox SET status = 'sent', sent_at = now()
		WHERE workspace_id = $1 AND id = $2 AND attempt = $3 RETURNING sent_at`, ws, id, it.Attempt).Scan(&it.SentAt); err != nil {
		return nil, err
	}
	it.Status = "sent"
	if it.ContactID != nil {
		if err := recordTouch(ctx, pool, *it.ContactID, it.Subject); err != nil {
			return &it, fmt.Errorf("sent, but the pipeline touch was not recorded: %w", err)
		}
	}
	return &it, nil
}

// Reject drops a row that is waiting, failed or uncertain, without sending.
func Reject(ctx context.Context, pool *pgxpool.Pool, workspaceSlug, id string) (*Item, error) {
	ws, err := workspaceID(ctx, pool, workspaceSlug)
	if err != nil {
		return nil, err
	}
	it, err := scan(pool.QueryRow(ctx, `UPDATE founderos_outbox SET status = 'rejected', decided_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status IN ('pending', 'failed', 'uncertain') RETURNING `+cols, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notPendingOrMissing(ctx, pool, ws, id)
	}
	return &it, err
}

func notPendingOrMissing(ctx context.Context, pool *pgxpool.Pool, ws, id string) error {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM founderos_outbox WHERE workspace_id = $1 AND id = $2)`,
		ws, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrNotPending
	}
	return ErrNotFound
}

// recordTouch logs the sent email on the contact's pipeline history, in the
// contact's own workspace. The stage stays where it is: a send is not a reply.
func recordTouch(ctx context.Context, pool *pgxpool.Pool, contactID, subject string) error {
	label := "Outreach sent: " + subject
	if r := []rune(label); len(r) > 200 {
		label = string(r[:200])
	}
	_, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at)
		SELECT c.id || '-email-' || (COALESCE(MAX(t.seq), 0) + 1), c.workspace_id, c.id, COALESCE(MAX(t.seq), 0) + 1,
		       c.status, 'email', $2, 'manual', CURRENT_DATE
		FROM founderos_funnel_contacts c LEFT JOIN founderos_funnel_touches t ON t.contact_id = c.id
		WHERE c.id = $1 GROUP BY c.id, c.workspace_id, c.status`, contactID, label)
	return err
}
