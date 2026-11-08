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

var placeholderRe = regexp.MustCompile(`\{([a-z_]{1,40})\}`)

// TypedFields are the only placeholders an approved sequence step may use: the
// OS fills them from its own records, never from agent text.
var TypedFields = []string{"first_name", "company", "sender_name"}

// RenderTemplate fills tmpl from fields. ok is false if it uses any placeholder
// that is not a typed field, or a typed field with no value.
func RenderTemplate(tmpl string, fields map[string]string) (string, bool) {
	ok := true
	out := placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		v := strings.TrimSpace(fields[m[1:len(m)-1]])
		if v == "" {
			ok = false
		}
		return v
	})
	return strings.TrimSpace(out), ok
}

var fieldRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} &.,'’()\-]{0,59}$`)

// validField reports whether a typed field looks like a name or a company: no
// sentence punctuation, symbols, links or line breaks that could carry a claim.
func validField(v string) bool {
	v = strings.TrimSpace(v)
	// No sentence inside a field: a full stop may only end an abbreviation at the very end ("Ltd.").
	if !fieldRe.MatchString(v) || len(strings.Fields(v)) > 4 || strings.Contains(strings.TrimSuffix(v, "."), ".") {
		return false
	}
	low := strings.ToLower(v)
	for _, w := range []string{"http", "www.", "guarantee", "free", "%"} {
		if strings.Contains(low, w) {
			return false
		}
	}
	return true
}

var alnum = regexp.MustCompile(`[^a-z0-9]+`)

// companyMatchesDomain anchors {company} to the recipient: the company must
// share a word of four or more letters with the first label of their domain.
func companyMatchesDomain(company, domain string) bool {
	label := alnum.ReplaceAllString(strings.ToLower(strings.SplitN(domain, ".", 2)[0]), "")
	for _, w := range strings.Fields(strings.ToLower(company)) {
		w = alnum.ReplaceAllString(w, "")
		if len(w) >= 4 && strings.Contains(label, w) {
			return true
		}
	}
	return false
}

func normalizeSpace(s string) string {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.Join(lines, "\n")
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
		if err == nil {
			// Codex M1 review #5: filled by the OS from the contact's record and
			// the pod's sender, then compared exactly.
			fields := map[string]string{}
			if validField(r.SenderName) {
				fields["sender_name"] = r.SenderName
			}
			if it.ContactID != nil {
				var person, company string
				_ = tx.QueryRow(ctx, `SELECT coalesce(person, ''), coalesce(company, '') FROM founderos_funnel_contacts
					WHERE id = $1 AND workspace_id = (SELECT workspace_id FROM founderos_client_pods WHERE id = $2)`,
					*it.ContactID, r.ID).Scan(&person, &company)
				if validField(company) && companyMatchesDomain(company, domain) {
					fields["company"] = company
				}
				if f := strings.Fields(person); len(f) > 0 && validField(f[0]) {
					fields["first_name"] = f[0]
				}
			}
			ws, okS := RenderTemplate(subj, fields)
			wb, okB := RenderTemplate(body, fields)
			if okS && okB && normalizeSpace(ws) == normalizeSpace(c.Subject) && normalizeSpace(wb) == normalizeSpace(c.Body) {
				return "", nil
			}
		}
	}
	// Bound to who sends it as well as the wording: a new sender needs a new approval.
	hash := ContentHash(r.SenderEmail+"\n"+c.Subject, c.Body)
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
