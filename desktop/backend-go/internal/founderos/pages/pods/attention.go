package pods

// Alerts (Codex open item: TFG OS has no alerts for stale queues). One place
// collects everything about a client's AI SDR that needs a person: a broken
// calendar or CRM connection, emails that failed or may not have gone, notices
// to the rep that could not be delivered, hand-offs not passed on, enquiries
// that could not be tasked, replies needing a check, and stuck AI tasks. Each
// open problem is kept as one alert, pushed to the owner once, and closed when
// it clears.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Alert is one open problem on a pod.
type Alert struct {
	PodID     string    `json:"-"`
	Slug      string    `json:"slug,omitempty"`
	Client    string    `json:"client,omitempty"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	FirstSeen time.Time `json:"firstSeen"`
}

// SetHealth records whether one of a pod's connections works.
func (s *Store) SetHealth(ctx context.Context, podID, check string, ok bool, detail string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_health (pod_id, check_name, ok, detail) VALUES ($1, $2, $3, $4)
		ON CONFLICT (pod_id, check_name) DO UPDATE SET
			since = CASE WHEN founderos_pod_health.ok = EXCLUDED.ok THEN founderos_pod_health.since ELSE now() END,
			ok = EXCLUDED.ok, detail = EXCLUDED.detail, checked_at = now()`, podID, check, ok, clip(detail, 500))
	return err
}

var healthTitles = map[string]string{
	"calendar": "The calendar can't be read, so sending is held",
	"crm":      "The CRM sync is failing",
}

// problems computes a pod's open problems right now.
func (s *Store) problems(ctx context.Context, p Pod) ([]Alert, error) {
	var out []Alert
	add := func(key, title, detail string) {
		out = append(out, Alert{PodID: p.ID, Slug: p.Slug, Client: p.ClientName, Key: key, Title: title, Detail: detail})
	}
	var errs []error

	rows, err := s.Pool.Query(ctx, `SELECT check_name, detail FROM founderos_pod_health WHERE pod_id = $1 AND NOT ok ORDER BY check_name`, p.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var check, detail string
		if err := rows.Scan(&check, &detail); err != nil {
			rows.Close()
			return nil, err
		}
		if (check == "calendar" && p.CalendarProvider != "calcom") || (check == "crm" && (p.CRMProvider == "" || p.CRMProvider == "none")) {
			continue // that connection was switched off since
		}
		title := healthTitles[check]
		if title == "" {
			title = "The " + check + " connection is failing"
		}
		add("health:"+check, title, detail)
	}
	rows.Close()

	rows, err = s.Pool.Query(ctx, `SELECT issue_id, kind, stuck_reason FROM founderos_pod_task_owner
		WHERE pod_id = $1 AND stuck_at IS NOT NULL AND settled_at IS NULL ORDER BY stuck_at`, p.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var issue, kind, reason string
		if err := rows.Scan(&issue, &kind, &reason); err != nil {
			rows.Close()
			return nil, err
		}
		what := stallRules[kind].what
		if what == "" {
			what = kind
		}
		add("task:"+issue, "A "+what+" task is stuck", reason+". Check the task on the board, then mark it dealt with.")
	}
	rows.Close()

	count := func(q string, args ...any) int {
		var n int
		if err := s.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			errs = append(errs, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM founderos_outbox WHERE workspace_id = $1 AND status IN ('failed', 'uncertain')
		AND coalesce(decided_at, created_at) > now() - interval '7 days'`, p.WorkspaceID); n > 0 {
		add("outbox:failed", plural(n, "email", "emails")+" to prospects failed or may not have gone",
			"Check them in the outbox before anything is resent; an uncertain one may already be in the prospect's inbox.")
	}
	if n := count(`SELECT count(*) FROM founderos_outbox WHERE workspace_id = $1 AND status = 'sending'
		AND coalesce(decided_at, created_at) < now() - interval '30 minutes'`, p.WorkspaceID); n > 0 {
		add("outbox:sending", plural(n, "email has", "emails have")+" been sending for over 30 minutes", "The mailbox may be down or the send stopped part way.")
	}
	for _, n := range []struct{ table, prefix, what string }{
		{"founderos_pod_meetings", "brief", "call brief"}, {"founderos_pod_meetings", "coaching", "coaching note"}, {"founderos_pod_weekly", "review", "weekly review"},
	} {
		var failed, missing int
		if err := s.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FILTER (WHERE %[1]s_delivery IN ('failed', 'uncertain')),
			count(*) FILTER (WHERE %[1]s_delivery = 'no_recipient') FROM %[2]s WHERE pod_id = $1 AND %[1]s_sent_at > now() - interval '14 days'`,
			n.prefix, n.table), p.ID).Scan(&failed, &missing); err != nil {
			errs = append(errs, err)
			continue
		}
		if failed > 0 {
			add("notice:"+n.prefix, plural(failed, n.what, n.what+"s")+" could not be emailed to the rep", "They are in the portal; check the rep's address and the mailbox.")
		}
		if missing > 0 {
			add("notice:"+n.prefix+":nobody", plural(missing, n.what, n.what+"s")+" had nobody to go to", "Set the rep's email (hand-off email) on the AI SDR's settings.")
		}
	}
	// Work the AI SDR was asked for over six days ago and never got: a person does it.
	if n := count(`SELECT (SELECT count(*) FROM founderos_pod_meetings WHERE pod_id = $1 AND brief_issue_id IS NULL AND status = 'booked'
			AND starts_at > now() AND brief_task_tried_at < now() - interval '6 days')
		+ (SELECT count(*) FROM founderos_pod_meetings WHERE pod_id = $1 AND coaching_issue_id IS NULL
			AND coaching_task_tried_at < now() - interval '6 days')
		+ (SELECT count(*) FROM founderos_pod_weekly WHERE pod_id = $1 AND review_issue_id IS NULL AND review_task_tried_at < now() - interval '6 days')`, p.ID); n > 0 {
		add("task:expired", plural(n, "brief, coaching note or review", "briefs, coaching notes or reviews")+" could not be given to the AI SDR for six days",
			"Write them by hand, or check the task board: TFG OS will not retry them in case one already exists there.")
	}
	if n := count(`SELECT count(*) FROM founderos_pod_handoffs WHERE pod_id = $1 AND notified_at IS NULL
		AND (coalesce(error, '') <> '' OR created_at < now() - interval '30 minutes')`, p.ID); n > 0 {
		add("handoff", plural(n, "buyer ready for a person has", "buyers ready for a person have")+" not been passed to the rep",
			"Pass them on by hand from the AI SDR page; a warm buyer goes cold fast.")
	}
	if n := count(`SELECT count(*) FROM founderos_pod_inbound WHERE pod_id = $1 AND status <> 'tasked' AND coalesce(error, '') <> ''`, p.ID); n > 0 {
		add("enquiry", plural(n, "website enquiry", "website enquiries")+" could not be given to the AI SDR", "Reply by hand, or fix the AI SDR on the board and it retries.")
	}
	if n := count(`SELECT count(*) FROM founderos_pod_reply_messages WHERE pod_id = $1 AND routed = 'check'`, p.ID); n > 0 {
		add("replies", plural(n, "reply needs", "replies need")+" checking", "TFG OS stopped part way through routing them; check the board so no prospect is answered twice or not at all.")
	}
	return out, errors.Join(errs...)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Alerts returns a pod's open alerts, oldest first.
func (s *Store) Alerts(ctx context.Context, podID string) ([]Alert, error) {
	rows, err := s.Pool.Query(ctx, `SELECT a.key, a.title, a.detail, a.first_seen, p.slug, p.client_name FROM founderos_pod_alerts a
		JOIN founderos_client_pods p ON p.id = a.pod_id WHERE a.pod_id = $1 AND a.cleared_at IS NULL ORDER BY a.first_seen, a.key`, podID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		a := Alert{PodID: podID}
		if err := rows.Scan(&a.Key, &a.Title, &a.Detail, &a.FirstSeen, &a.Slug, &a.Client); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// OpenAlertCounts returns the number of open alerts per pod slug.
func (s *Store) OpenAlertCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.slug, count(*) FROM founderos_pod_alerts a JOIN founderos_client_pods p ON p.id = a.pod_id
		WHERE a.cleared_at IS NULL GROUP BY p.slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var slug string
		var n int
		if err := rows.Scan(&slug, &n); err != nil {
			return nil, err
		}
		out[slug] = n
	}
	return out, rows.Err()
}

// RefreshAlerts recomputes every running pod's problems, opens new alerts and
// closes cleared ones. It returns the alerts not yet pushed to the owner and
// marks them pushed; the caller pushes them.
func (s *Store) RefreshAlerts(ctx context.Context) ([]Alert, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, p := range list {
		if p.Status != StatusLive && p.Status != StatusDryRun && p.Status != StatusPaused {
			// Set up or handed over: nothing runs, so nothing is open.
			if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_alerts SET cleared_at = now() WHERE pod_id = $1 AND cleared_at IS NULL`, p.ID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		probs, err := s.problems(ctx, p)
		if err != nil {
			// A partial read must not close alerts it could not see.
			errs = append(errs, fmt.Errorf("%s: %w", p.Slug, err))
			continue
		}
		keys := make([]string, 0, len(probs))
		for _, a := range probs {
			keys = append(keys, a.Key)
			if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_alerts (pod_id, key, title, detail) VALUES ($1, $2, $3, $4)
				ON CONFLICT (pod_id, key) DO UPDATE SET title = EXCLUDED.title, detail = EXCLUDED.detail, last_seen = now(),
					first_seen = CASE WHEN founderos_pod_alerts.cleared_at IS NULL THEN founderos_pod_alerts.first_seen ELSE now() END,
					notified_at = CASE WHEN founderos_pod_alerts.cleared_at IS NULL THEN founderos_pod_alerts.notified_at ELSE NULL END,
					cleared_at = NULL`, p.ID, a.Key, a.Title, a.Detail); err != nil {
				errs = append(errs, err)
			}
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_alerts SET cleared_at = now()
			WHERE pod_id = $1 AND cleared_at IS NULL AND NOT (key = ANY($2))`, p.ID, keys); err != nil {
			errs = append(errs, err)
		}
	}
	rows, err := s.Pool.Query(ctx, `UPDATE founderos_pod_alerts a SET notified_at = now() FROM founderos_client_pods p
		WHERE p.id = a.pod_id AND a.cleared_at IS NULL AND a.notified_at IS NULL
		RETURNING a.pod_id::text, p.slug, p.client_name, a.key, a.title, a.detail, a.first_seen`)
	if err != nil {
		return nil, errors.Join(append(errs, err)...)
	}
	defer rows.Close()
	var fresh []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.PodID, &a.Slug, &a.Client, &a.Key, &a.Title, &a.Detail, &a.FirstSeen); err != nil {
			return fresh, err
		}
		fresh = append(fresh, a)
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Slug+fresh[i].Key < fresh[j].Slug+fresh[j].Key })
	return fresh, errors.Join(append(errs, rows.Err())...)
}
