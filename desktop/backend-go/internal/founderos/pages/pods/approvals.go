package pods

// Client approvals the OS asks for (docs/tfg/CLIENT-PODS.md, M2): documents
// and target lists. Each request is bound to a hash of exactly what the client
// will see; an edit re-opens it, and a decision on older wording is ignored.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"regexp"
	"sort"
	"strings"
)

// fileApproval opens (or re-opens, when the wording changed) a pending request.
func (s *Store) fileApproval(ctx context.Context, podID, kind, ref, title string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO founderos_client_approvals (pod_id, kind, ref, title, payload, status)
		VALUES ($1, $2, $3, $4, $5, 'pending')
		ON CONFLICT (pod_id, kind, ref) DO UPDATE SET title = EXCLUDED.title, payload = EXCLUDED.payload,
			status = 'pending', client_note = '', decided_by = '', decided_at = NULL
		WHERE founderos_client_approvals.payload->>'hash' IS DISTINCT FROM EXCLUDED.payload->>'hash'
		   OR founderos_client_approvals.status = 'withdrawn'`, podID, kind, ref, title, raw)
	if err != nil || tag.RowsAffected() == 0 || kind != "doc" {
		return err
	}
	return s.Log(ctx, podID, "approval_requested", title+" (ready for your approval)", kind+":"+ref)
}

func (s *Store) fileDocApproval(ctx context.Context, slug, key, body string) error {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	label := key
	return s.fileApproval(ctx, p.ID, "doc", key, "Approve the "+label+" document",
		map[string]any{"key": key, "body": body, "hash": hashOf("doc", key, body)})
}

var domainRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// ProposeTargetList asks the client to approve a list of company domains.
func (s *Store) ProposeTargetList(ctx context.Context, slug, title string, domains []string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("%w: the list needs a name", ErrInvalid)
	}
	seen := map[string]bool{}
	var list []string
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if !domainRe.MatchString(d) {
			return fmt.Errorf("%w: %q is not a domain", ErrInvalid, d)
		}
		if !seen[d] {
			seen[d] = true
			list = append(list, d)
		}
	}
	if len(list) == 0 || len(list) > 5000 {
		return fmt.Errorf("%w: a list needs 1-5000 domains", ErrInvalid)
	}
	sort.Strings(list)
	p, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	ref := strings.TrimPrefix(SlugFor(title), "client-")
	if err := s.fileApproval(ctx, p.ID, "target_list", ref, "Target list: "+title,
		map[string]any{"title": title, "domains": list, "hash": hashOf("target_list", title, strings.Join(list, ","))}); err != nil {
		return err
	}
	return s.Log(ctx, p.ID, "target_list_proposed", fmt.Sprintf("Target list %q (%d companies) sent for approval", title, len(list)), ref)
}

// ApplyClientDecision records the client's decision on a request, but only if
// hash is still the request's current wording. It reports whether it applied.
// Everything happens in one transaction under the pod's row lock: the request's
// status and, for an approved document, the document's approval change together,
// and only when the document's current wording is exactly what the client saw.
// A retry after a crash finishes a half-applied decision (Codex 11 Nov #6).
func (s *Store) ApplyClientDecision(ctx context.Context, podID, kind, ref, hash, decision, by, note string) (bool, error) {
	if decision != "approved" && decision != "changes_requested" {
		return false, fmt.Errorf("%w: decision must be approved or changes_requested", ErrInvalid)
	}
	who := "client:" + strings.TrimSpace(by)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var slug string
	if err := tx.QueryRow(ctx, `SELECT slug FROM founderos_client_pods WHERE id = $1 FOR UPDATE`, podID).Scan(&slug); err != nil {
		return false, err
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM founderos_client_approvals
		WHERE pod_id = $1 AND kind = $2 AND ref = $3 AND payload->>'hash' = $4 FOR UPDATE`, podID, kind, ref, hash).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // the wording changed after the client looked
	}
	if err != nil {
		return false, err
	}
	switch status {
	case "pending":
	case decision: // already recorded: finish anything left undone below
	default:
		return false, nil
	}
	if kind == "doc" && decision == "approved" {
		var body string
		var approved bool
		err := tx.QueryRow(ctx, `SELECT body, approved_at IS NOT NULL FROM founderos_client_pod_docs
			WHERE pod_id = $1 AND key = $2 FOR UPDATE`, podID, ref).Scan(&body, &approved)
		if err != nil {
			return false, err
		}
		if hashOf("doc", ref, body) != hash {
			return false, nil // the document was edited since: the client must look again
		}
		if !approved {
			if _, err := tx.Exec(ctx, `UPDATE founderos_client_pod_docs SET approved_at = now() WHERE pod_id = $1 AND key = $2`, podID, ref); err != nil {
				return false, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO founderos_client_activity (pod_id, kind, summary, ref) VALUES ($1, 'doc_approved', $2, $3)`,
				podID, fmt.Sprintf("%s approved by %s (portal)", ref, who), ref); err != nil {
				return false, err
			}
		}
	}
	if status == "pending" {
		if _, err := tx.Exec(ctx, `UPDATE founderos_client_approvals SET status = $5, decided_by = $6, client_note = $7, decided_at = now()
			WHERE pod_id = $1 AND kind = $2 AND ref = $3 AND payload->>'hash' = $4`,
			podID, kind, ref, hash, decision, who, strings.TrimSpace(note)); err != nil {
			return false, err
		}
		summary := fmt.Sprintf("%s %s: %s", strings.TrimSpace(by), strings.ReplaceAll(decision, "_", " "), ref)
		if _, err := tx.Exec(ctx, `INSERT INTO founderos_client_activity (pod_id, kind, summary, ref) VALUES ($1, 'client_decision', $2, $3)`,
			podID, summary, kind+":"+ref); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// LinkPortal ties the pod to the client's record in the client portal.
func (s *Store) LinkPortal(ctx context.Context, slug, portalClientID string) error {
	id := strings.ToLower(strings.TrimSpace(portalClientID))
	var v any
	if id != "" {
		if !uuidRe.MatchString(id) {
			return fmt.Errorf("%w: the portal client id must be a UUID", ErrInvalid)
		}
		v = id
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET portal_client_id = $2, updated_at = now() WHERE slug = $1`, slug, v)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ApprovalRow is one approval request as the portal sync reads it.
type ApprovalRow struct {
	Kind, Ref, Title, Status, Hash string
	Payload                        json.RawMessage
}

// Approvals lists the pod's approval requests.
func (s *Store) Approvals(ctx context.Context, podID string) ([]ApprovalRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT kind, ref, title, status, coalesce(payload->>'hash', ''), payload
		FROM founderos_client_approvals WHERE pod_id = $1 ORDER BY created_at`, podID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalRow
	for rows.Next() {
		var a ApprovalRow
		if err := rows.Scan(&a.Kind, &a.Ref, &a.Title, &a.Status, &a.Hash, &a.Payload); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActivityRow is one activity line with its id, for incremental pushes.
type ActivityRow struct {
	ID                 int64
	At                 string
	Kind, Summary, Ref string
}

// ActivityAfter returns activity with id > after, oldest first.
func (s *Store) ActivityAfter(ctx context.Context, podID string, after int64, limit int) ([]ActivityRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, to_char(at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), kind, summary, ref
		FROM founderos_client_activity WHERE pod_id = $1 AND id > $2 ORDER BY id LIMIT $3`, podID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityRow
	for rows.Next() {
		var a ActivityRow
		if err := rows.Scan(&a.ID, &a.At, &a.Kind, &a.Summary, &a.Ref); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PortalCursor and SetPortalCursor track how far activity was pushed.
func (s *Store) PortalCursor(ctx context.Context, podID string) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT coalesce((SELECT last_activity_id FROM founderos_portal_sync WHERE pod_id = $1), 0)`, podID).Scan(&n)
	return n, err
}

func (s *Store) SetPortalCursor(ctx context.Context, podID string, n int64) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO founderos_portal_sync (pod_id, last_activity_id, synced_at) VALUES ($1, $2, now())
		ON CONFLICT (pod_id) DO UPDATE SET last_activity_id = greatest(founderos_portal_sync.last_activity_id, $2), synced_at = now()`, podID, n)
	return err
}
