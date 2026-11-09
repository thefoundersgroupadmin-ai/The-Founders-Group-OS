package pods

// Inbound enquiries (docs/tfg/CLIENT-PODS.md, M3): a client's website form
// posts to the pod's private link; the enquiry becomes an engaged pipeline
// contact at once and, within the tasker's next pass, a task for the pod's own
// AI SDR to research the person and reply with the qualifying questions.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

var ErrRateLimited = errors.New("too many enquiries from this sender; try again later")

// Enquiry is one form submission.
type Enquiry struct {
	Name, Email, Company, Phone, Message string
	Honeypot                             string // a hidden field bots fill in
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte("tfg-pod-inbound:" + token))
	return hex.EncodeToString(h[:])
}

// NewInboundToken makes (or replaces) the pod's form link token. Only its hash is kept.
func (s *Store) NewInboundToken(ctx context.Context, slug string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	n, err := s.lockedExec(ctx, slug, `UPDATE founderos_client_pods SET inbound_token_hash = $2, updated_at = now() WHERE slug = $1`,
		slug, tokenHash(token))
	if err == nil && n == 0 {
		return "", ErrNotFound
	}
	return token, err
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Intake records an enquiry for the pod whose token this is (live or dry-run
// pods only). It returns 0 for a dropped bot submission.
func (s *Store) Intake(ctx context.Context, token string, e Enquiry, source string) (int64, error) {
	if strings.TrimSpace(token) == "" {
		return 0, ErrNotFound
	}
	var podID, ws, slug string
	err := s.Pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, slug FROM founderos_client_pods
		WHERE inbound_token_hash = $1 AND inbound_token_hash <> '' AND status IN ('live', 'dry_run')`, tokenHash(token)).Scan(&podID, &ws, &slug)
	if err != nil {
		return 0, ErrNotFound
	}
	if strings.TrimSpace(e.Honeypot) != "" {
		return 0, nil
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(e.Email))
	if err != nil || !strings.Contains(addr.Address, ".") {
		return 0, fmt.Errorf("%w: a valid email address is required", ErrInvalid)
	}
	email := strings.ToLower(addr.Address)
	src := tokenHash("src:" + source)
	// Count and insert under one per-pod lock, so concurrent posts cannot slip past the limit.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('founderos-pod-inbound:' || $1))`, podID); err != nil {
		return 0, err
	}
	var recent, podHour int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE source_hash = $2 OR email = $3), count(*)
		FROM founderos_pod_inbound WHERE pod_id = $1 AND received_at > now() - interval '1 hour'`, podID, src, email).Scan(&recent, &podHour); err != nil {
		return 0, err
	}
	if recent >= 5 || podHour >= 200 {
		return 0, ErrRateLimited
	}
	if _, err := tx.Exec(ctx, `INSERT INTO founderos_pod_inbound (pod_id, email, source_hash, status, error)
		VALUES ($1, $2, $3, 'failed', 'reservation')`, podID, email, src); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	defer s.Pool.Exec(context.WithoutCancel(ctx), `DELETE FROM founderos_pod_inbound WHERE pod_id = $1 AND email = $2 AND source_hash = $3
		AND status = 'failed' AND error = 'reservation'`, podID, email, src)
	name, company := clip(e.Name, 120), clip(e.Company, 120)
	contactID := "fc-in-" + strings.TrimPrefix(slug, "client-") + "-" + tokenHash(email)[:12]
	label := name
	if label == "" {
		label = email
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, product,
		relationship, email, person, company, next_action, created_at)
		VALUES ($1, $2, $3, 'client', 'engaged', (SELECT client_name FROM founderos_client_pods WHERE id = $4), 'warm', $5, $6, $7,
		'Reply to their enquiry', now())
		ON CONFLICT (id) DO UPDATE SET status = CASE WHEN founderos_funnel_contacts.status = 'first_touch' THEN 'engaged'
			ELSE founderos_funnel_contacts.status END, relationship = 'warm'`,
		contactID, ws, label, podID, email, nullIf(name), nullIf(company)); err != nil {
		return 0, err
	}
	var id int64
	if err := s.Pool.QueryRow(ctx, `INSERT INTO founderos_pod_inbound (pod_id, name, email, company, phone, message, source_hash, contact_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, podID, name, email, company, clip(e.Phone, 40),
		clip(e.Message, 5000), src, contactID).Scan(&id); err != nil {
		return 0, err
	}
	who := label
	if company != "" {
		who += " (" + company + ")"
	}
	return id, s.Log(ctx, podID, "enquiry", "New enquiry from "+who, contactID)
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TaskBoard opens a task for an agent in a project.
type TaskBoard interface {
	CreateTask(ctx context.Context, projectID, agentID, title, description string) (string, error)
}

func inboundBrief(p Pod, e Enquiry) string {
	who := e.Name
	if who == "" {
		who = e.Email
	}
	return "New inbound enquiry for " + p.ClientName + ". Reply within minutes.\n\n" +
		"- **From:** " + who + " <" + e.Email + ">\n- **Company:** " + e.Company + "\n- **Phone:** " + e.Phone + "\n\n" +
		"**Their message:**\n\n> " + strings.ReplaceAll(e.Message, "\n", "\n> ") + "\n\n" +
		"1. Research who they are and their company (public sources only; cite them in a \"research\" document).\n" +
		"2. Draft a personal reply from " + p.SenderName + " that answers what they asked and, naturally, asks what you " +
		"still need to qualify them: budget, timing, and who decides. Hand it over as the \"outbox\" document on this task: " +
		"a JSON array with one {\"to\", \"toName\", \"company\", \"subject\", \"body\"}. TFG OS sends it after its checks.\n" +
		"3. Record what you learned as a \"qualification\" document: {\"budget\", \"timing\", \"decisionMaker\", \"fit\" (0-100), \"notes\"}; " +
		"leave unknowns empty.\n" +
		"4. If they ask to speak to a person, or they are clearly ready to buy, also write a \"handoff\" document for " +
		p.ClientName + "'s rep: who they are, what they asked, what they need, suggested next step. A person takes over from there.\n\n" +
		"Work only from this task and your instructions; never use another client's or company's data."
}

// TaskInbound turns new enquiries into tasks for each pod's own AI SDR and
// records the conversation, so its reply may go out. Returns how many it tasked.
func (s *Store) TaskInbound(ctx context.Context, b TaskBoard) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT i.id, i.name, i.email, i.company, i.phone, i.message, i.contact_id, p.slug,
		a.project_id, a.agent_id FROM founderos_pod_inbound i JOIN founderos_client_pods p ON p.id = i.pod_id
		LEFT JOIN founderos_pod_agents a ON a.pod_id = p.id WHERE i.status = 'new' ORDER BY i.received_at LIMIT 20`)
	if err != nil {
		return 0, err
	}
	type job struct {
		id                      int64
		e                       Enquiry
		contact, slug, proj, ag string
	}
	var jobs []job
	for rows.Next() {
		var j job
		var proj, ag *string
		if err := rows.Scan(&j.id, &j.e.Name, &j.e.Email, &j.e.Company, &j.e.Phone, &j.e.Message, &j.contact, &j.slug, &proj, &ag); err != nil {
			rows.Close()
			return 0, err
		}
		if proj != nil {
			j.proj = *proj
		}
		if ag != nil {
			j.ag = *ag
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	tasked := 0
	var errs []error
	for _, j := range jobs {
		if j.proj == "" || j.ag == "" {
			_, _ = s.Pool.Exec(ctx, `UPDATE founderos_pod_inbound SET error = 'the pod has no AI SDR yet' WHERE id = $1`, j.id)
			continue
		}
		p, err := s.Get(ctx, j.slug)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		who := j.e.Name
		if who == "" {
			who = j.e.Email
		}
		// A buyer a person already took over goes to that person, not the AI
		// (Codex fix review #3: repeat form enquiries).
		var taken bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM founderos_pod_conversations c
			LEFT JOIN founderos_funnel_contacts fc ON fc.id = c.contact_id AND fc.workspace_id = c.workspace_id
			WHERE c.workspace_id = $1 AND c.taken_over_at IS NOT NULL
			AND (c.contact_id = $2 OR lower(fc.email) = lower($3)))`, p.WorkspaceID, j.contact, j.e.Email).Scan(&taken); err != nil {
			errs = append(errs, err)
			continue
		}
		if taken {
			summary := who + " <" + j.e.Email + "> sent another enquiry through your website.\n\n" + clip(j.e.Message, 1500)
			if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_handoffs (pod_id, issue_id, contact_id, summary)
				VALUES ($1, $2, $3, $4) ON CONFLICT (issue_id) DO NOTHING`, p.ID, fmt.Sprintf("form:%d", j.id), j.contact, summary); err != nil {
				errs = append(errs, err)
				continue
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_inbound SET status = 'tasked', issue_id = 'person', error = '' WHERE id = $1`, j.id); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		issue, err := b.CreateTask(ctx, j.proj, j.ag, clip(p.ClientName+": enquiry from "+who, 200), inboundBrief(p, j.e))
		if err != nil {
			_, _ = s.Pool.Exec(ctx, `UPDATE founderos_pod_inbound SET error = $2 WHERE id = $1`, j.id, clip(err.Error(), 300))
			errs = append(errs, err)
			continue
		}
		if err := s.ownTask(ctx, issue, p.ID, "enquiry"); err != nil {
			errs = append(errs, err)
		}

		if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_conversations (issue_id, workspace_id, contact_id, source)
			VALUES ($1, $2, $3, 'form') ON CONFLICT DO NOTHING`, issue, p.WorkspaceID, j.contact); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_inbound SET status = 'tasked', issue_id = $2, error = '' WHERE id = $1`, j.id, issue); err != nil {
			errs = append(errs, err)
			continue
		}
		tasked++
	}
	return tasked, errors.Join(errs...)
}

// Handoff is a buyer ready for a person, waiting to be told to the client's rep.
type Handoff struct {
	ID                int64
	PodID, Client, To string
	Summary           string
}

// PendingHandoffs lists hand-offs not yet emailed.
func (s *Store) PendingHandoffs(ctx context.Context) ([]Handoff, error) {
	rows, err := s.Pool.Query(ctx, `SELECT h.id, p.id::text, p.client_name, p.handoff_email, h.summary FROM founderos_pod_handoffs h
		JOIN founderos_client_pods p ON p.id = h.pod_id WHERE h.notified_at IS NULL AND h.attempts < 5
		AND p.status <> 'handed_over' ORDER BY h.created_at LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Handoff
	for rows.Next() {
		var h Handoff
		if err := rows.Scan(&h.ID, &h.PodID, &h.Client, &h.To, &h.Summary); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// MarkHandoffUncertain records a hand-off whose email may or may not have
// reached the mail server: never resent automatically.
func (s *Store) MarkHandoffUncertain(ctx context.Context, id int64, why string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_handoffs SET notified_at = now(), error = $2 WHERE id = $1`,
		id, clip("uncertain: "+why, 300))
	return err
}

// MarkHandoff records that a hand-off was emailed (why == "") or why it was not.
func (s *Store) MarkHandoff(ctx context.Context, id int64, why string) error {
	var podID, summary string
	// A failure is retried (up to five times); a missing rep email waits for one (Codex M2-M4 #7).
	err := s.Pool.QueryRow(ctx, `UPDATE founderos_pod_handoffs SET notified_at = CASE WHEN $2 = '' THEN now() END, error = $2,
		attempts = attempts + CASE WHEN $2 = '' OR $2 = 'no hand-off email set for this client' THEN 0 ELSE 1 END
		WHERE id = $1 RETURNING pod_id::text, summary`, id, clip(why, 300)).Scan(&podID, &summary)
	if err != nil || why != "" {
		return err
	}
	return s.Log(ctx, podID, "handoff", "Hand-off to your team: "+clip(strings.SplitN(summary, "\n", 2)[0], 160), "")
}
