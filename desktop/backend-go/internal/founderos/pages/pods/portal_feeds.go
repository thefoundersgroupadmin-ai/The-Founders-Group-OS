package pods

// What the client portal shows beyond approvals and meetings: each pod's campaigns (one per
// sending sequence) with their numbers, and its pipeline, one row per person, with a stage the
// client understands. Also the portal hold: sending waits when the portal's controls (pause,
// resume) haven't been read recently, so a client's pause is never missed.

import (
	"context"
	"strings"
)

// PortalFreshness is how recent a successful portal sync must be for a linked pod to send.
const PortalFreshness = "15 minutes"

// PortalHold lists sending pods linked to a portal client whose last portal sync failed,
// is older than PortalFreshness, or never happened.
func (s *Store) PortalHold(ctx context.Context) (map[string]bool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.slug FROM founderos_client_pods p
		LEFT JOIN founderos_pod_health h ON h.pod_id = p.id AND h.check_name = 'portal'
		WHERE p.status IN ('live', 'dry_run') AND coalesce(p.portal_client_id::text, '') <> ''
		  AND (h.pod_id IS NULL OR NOT h.ok OR h.checked_at < now() - interval '`+PortalFreshness+`')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out[slug] = true
	}
	return out, rows.Err()
}

// ResetPortalCursor makes the next sync send the pod's whole activity history again (after relinking).
func (s *Store) ResetPortalCursor(ctx context.Context, podID string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM founderos_portal_sync WHERE pod_id = $1`, podID)
	return err
}

// CampaignRow is one sending sequence of a pod and its counts.
type CampaignRow struct {
	Key, Name, StartedOn                      string
	Sent, Bounced, Replies, OptOuts, Meetings int
}

// CampaignsForPortal counts each sequence's sent emails, hard bounces, replying people, opt-outs and meetings.
func (s *Store) CampaignsForPortal(ctx context.Context, p Pod) ([]CampaignRow, error) {
	rows, err := s.Pool.Query(ctx, `
		WITH sent AS (
		  SELECT coalesce(nullif(o.sequence_key, ''), 'main') AS k, lower(o.to_address) AS addr, o.contact_id, o.sent_at
		    FROM founderos_outbox o WHERE o.workspace_id = $1 AND o.status = 'sent'
		)
		SELECT k, to_char(min(sent_at), 'YYYY-MM-DD'), count(*)::int,
		  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM founderos_email_suppression x WHERE lower(x.address) = sent.addr
		       AND (x.workspace_id = $1 OR x.workspace_id IS NULL) AND x.reason = 'hard bounce'))::int,
		  count(DISTINCT sent.contact_id) FILTER (WHERE EXISTS (SELECT 1 FROM founderos_pod_reply_messages r
		       WHERE r.pod_id = $2 AND r.contact_id = sent.contact_id AND r.routed NOT IN ('auto', 'opt_out')))::int,
		  count(DISTINCT sent.addr) FILTER (WHERE EXISTS (SELECT 1 FROM founderos_email_suppression x WHERE lower(x.address) = sent.addr
		       AND x.workspace_id = $1 AND x.reason LIKE 'opted out%'))::int,
		  count(DISTINCT sent.addr) FILTER (WHERE EXISTS (SELECT 1 FROM founderos_pod_meetings m
		       WHERE m.pod_id = $2 AND lower(m.attendee_email) = sent.addr AND m.status <> 'cancelled'))::int
		FROM sent GROUP BY k ORDER BY min(sent_at)`, p.WorkspaceID, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CampaignRow
	for rows.Next() {
		var c CampaignRow
		if err := rows.Scan(&c.Key, &c.StartedOn, &c.Sent, &c.Bounced, &c.Replies, &c.OptOuts, &c.Meetings); err != nil {
			return nil, err
		}
		c.Name = campaignName(c.Key)
		out = append(out, c)
	}
	return out, rows.Err()
}

func campaignName(key string) string {
	if key == "" || key == "main" {
		return "Main sequence"
	}
	k := strings.NewReplacer("-", " ", "_", " ").Replace(key)
	return strings.ToUpper(k[:1]) + k[1:]
}

// ProspectRow is one person in a pod's pipeline.
type ProspectRow struct {
	ID, Company, Person, Role, Stage, LastTouch, Next, Campaign string
}

// ProspectsForPortal lists the pod's people with the furthest stage each has reached.
func (s *Store) ProspectsForPortal(ctx context.Context, p Pod, limit int) ([]ProspectRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, coalesce(c.company, ''), coalesce(nullif(c.person, ''), c.name), coalesce(nullif(c.buyer_role, ''), c.role, ''),
		  CASE
		    WHEN EXISTS (SELECT 1 FROM founderos_email_suppression x WHERE x.workspace_id = $1 AND lower(x.address) = lower(c.email)) THEN 'opted_out'
		    WHEN EXISTS (SELECT 1 FROM founderos_pod_handoffs h WHERE h.pod_id = $2 AND h.contact_id = c.id) THEN 'handed_over'
		    WHEN EXISTS (SELECT 1 FROM founderos_pod_meetings m WHERE m.pod_id = $2 AND lower(m.attendee_email) = lower(c.email) AND m.status <> 'cancelled') THEN 'meeting'
		    WHEN EXISTS (SELECT 1 FROM founderos_pod_reply_messages r WHERE r.pod_id = $2 AND r.contact_id = c.id AND r.routed NOT IN ('auto')) THEN 'replied'
		    WHEN EXISTS (SELECT 1 FROM founderos_outbox o WHERE o.workspace_id = $1 AND o.status = 'sent' AND (o.contact_id = c.id OR lower(o.to_address) = lower(c.email))) THEN 'contacted'
		    ELSE 'researched' END,
		  coalesce(to_char((SELECT max(t.at) FROM founderos_funnel_touches t WHERE t.contact_id = c.id), 'YYYY-MM-DD'), ''),
		  coalesce(c.next_action, ''),
		  coalesce((SELECT coalesce(nullif(o.sequence_key, ''), 'main') FROM founderos_outbox o WHERE o.workspace_id = $1 AND o.contact_id = c.id
		            AND o.status = 'sent' ORDER BY o.sent_at LIMIT 1), '')
		FROM founderos_funnel_contacts c WHERE c.workspace_id = $1
		ORDER BY c.created_at DESC LIMIT $3`, p.WorkspaceID, p.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProspectRow
	for rows.Next() {
		var r ProspectRow
		if err := rows.Scan(&r.ID, &r.Company, &r.Person, &r.Role, &r.Stage, &r.LastTouch, &r.Next, &r.Campaign); err != nil {
			return nil, err
		}
		if r.Campaign != "" {
			r.Campaign = campaignName(r.Campaign)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StaleAgents lists provisioned pods whose AI agent was last updated before a change to their
// documents (edit or approval) or their sender and sending rules. Their agents need refreshing,
// and sending holds until they are, so the agent never works from old targeting, pricing or voice.
func (s *Store) StaleAgents(ctx context.Context) ([]string, error) { return s.agentsBehind(ctx, false) }

// AgentsDueRefresh is StaleAgents plus running pods whose agent is over 20 hours old, so each day's
// routine tasks carry a fresh pipeline snapshot. Only StaleAgents holds sending.
func (s *Store) AgentsDueRefresh(ctx context.Context) ([]string, error) {
	return s.agentsBehind(ctx, true)
}

func (s *Store) agentsBehind(ctx context.Context, daily bool) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.slug FROM founderos_client_pods p
		JOIN founderos_pod_agents a ON a.pod_id = p.id AND a.agent_id <> ''
		WHERE ($1 AND p.status IN ('live', 'dry_run') AND coalesce(a.synced_at, 'epoch') < now() - interval '20 hours')
		   OR coalesce(a.synced_at, 'epoch') < greatest(
		        coalesce(p.config_changed_at, 'epoch'),
		        coalesce((SELECT max(greatest(d.updated_at, coalesce(d.approved_at, 'epoch'))) FROM founderos_client_pod_docs d WHERE d.pod_id = p.id), 'epoch'))
		ORDER BY p.slug`, daily)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}
