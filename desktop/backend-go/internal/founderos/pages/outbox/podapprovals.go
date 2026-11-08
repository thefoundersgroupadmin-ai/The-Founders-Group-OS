package outbox

// Client approval for pod sends (docs/tfg/CLIENT-PODS.md, M1 step 7). Unless a
// pod's policy is "auto", an email goes out only when:
//
//   - its company's domain is on a target list the client approved, and
//   - the client approved this exact subject and body (per-message policy), or
//   - under the sequences policy, it carries an approved sequence step and its
//     wording is that step's template with only the {placeholders} filled in.
//
// A message that is not covered gets (or re-opens) a pending approval request
// for the portal, keyed by the outbox row and bound to a hash of the wording:
// any edit after approval needs approval again.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ContentHash identifies exact wording.
func ContentHash(subject, body string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(subject) + "\n\n" + strings.TrimSpace(body)))
	return hex.EncodeToString(h[:])
}

var placeholderRe = regexp.MustCompile(`\{[a-z_]{1,40}\}`)

// MatchesTemplate reports whether text is tmpl with each {placeholder} replaced
// by a short phrase (1-80 characters, no line break, no . ! or ?, so a fill
// cannot add a sentence of its own), and nothing else changed.
func MatchesTemplate(tmpl, text string) bool {
	tmpl, text = strings.TrimSpace(tmpl), strings.TrimSpace(text)
	parts := placeholderRe.Split(tmpl, -1)
	var b strings.Builder
	b.WriteString(`^`)
	for i, p := range parts {
		if i > 0 {
			b.WriteString(`[^\n.!?]{1,80}?`)
		}
		b.WriteString(regexp.QuoteMeta(p))
	}
	b.WriteString(`$`)
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(text)
}

// clientApproval returns "" when the send is covered, else why it waits (and
// files or re-opens the approval request in tx).
func clientApproval(ctx context.Context, tx pgx.Tx, r *podRules, it Item, c Content, domain string) (string, error) {
	if r.Policy == "auto" {
		return "", nil
	}
	var listed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM founderos_client_approvals
		WHERE pod_id = $1 AND kind = 'target_list' AND status = 'approved' AND payload->'domains' ? $2)`,
		r.ID, domain).Scan(&listed); err != nil {
		return "", err
	}
	if !listed {
		return domain + " is not on a target list the client approved", nil
	}
	if r.Policy == "client_approves_sequences" && it.SequenceKey != nil && *it.SequenceKey != "" {
		var subj, body string
		err := tx.QueryRow(ctx, `SELECT payload->>'subject', payload->>'body' FROM founderos_client_approvals
			WHERE pod_id = $1 AND kind = 'sequence_step' AND ref = $2 AND status = 'approved'`, r.ID, *it.SequenceKey).Scan(&subj, &body)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if err == nil && MatchesTemplate(subj, c.Subject) && MatchesTemplate(body, c.Body) {
			return "", nil
		}
	}
	hash := ContentHash(c.Subject, c.Body)
	var status, approvedHash string
	err := tx.QueryRow(ctx, `SELECT status, coalesce(payload->>'hash', '') FROM founderos_client_approvals
		WHERE pod_id = $1 AND kind = 'message' AND ref = $2`, r.ID, it.ID).Scan(&status, &approvedHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err == nil && status == "approved" && approvedHash == hash {
		return "", nil
	}
	title := "Email to " + it.To + ": " + c.Subject
	if _, err := tx.Exec(ctx, `INSERT INTO founderos_client_approvals (pod_id, kind, ref, title, payload, status)
		VALUES ($1, 'message', $2, $3, jsonb_build_object('to', $4::text, 'subject', $5::text, 'body', $6::text, 'hash', $7::text), 'pending')
		ON CONFLICT (pod_id, kind, ref) DO UPDATE SET title = EXCLUDED.title, payload = EXCLUDED.payload,
			status = 'pending', client_note = '', decided_by = '', decided_at = NULL
		WHERE founderos_client_approvals.payload->>'hash' IS DISTINCT FROM EXCLUDED.payload->>'hash'
		   OR founderos_client_approvals.status = 'withdrawn'`,
		r.ID, it.ID, title, it.To, c.Subject, c.Body, hash); err != nil {
		return "", err
	}
	if status == "changes_requested" && approvedHash == hash {
		return "the client asked for changes to this email", nil
	}
	return "the client has not approved this email yet", nil
}
