package pods

// Delivery of client notices (call briefs, coaching notes, weekly reviews) to
// the client's rep (Codex final review #6, #7). Each notice moves through
// sending → sent | retry | failed | uncertain | no_recipient | held:
//   - a claim marks it "sending" first, so it is never emailed twice;
//   - a definite send failure is retried up to three times, then "failed";
//   - a claim that never reported back (a crash mid-send) becomes "uncertain"
//     after 15 minutes and is not retried automatically;
//   - no rep address: "no_recipient" (shown in the OS, in the portal anyway);
//   - a pod paused or handed over between claim and send: released ("held")
//     and sent when it runs again.

import (
	"context"
	"fmt"
)

// notice names one notice kind's columns.
type notice struct{ table, prefix string }

var (
	briefNotice    = notice{"founderos_pod_meetings", "brief"}
	coachingNotice = notice{"founderos_pod_meetings", "coaching"}
	reviewNotice   = notice{"founderos_pod_weekly", "review"}
)

// sweep marks claims that never reported back as uncertain.
func (s *Store) sweep(ctx context.Context, n notice) {
	_, _ = s.Pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s_delivery = 'uncertain'
		WHERE %s_delivery = 'sending' AND %s_sent_at < now() - interval '15 minutes'`, n.table, n.prefix, n.prefix, n.prefix))
}

// finish records how a claimed notice went.
func (s *Store) finish(ctx context.Context, n notice, id int64, why string, retry bool) error {
	var q string
	switch {
	case why == "":
		q = `UPDATE %[1]s SET %[2]s_delivery = 'sent' WHERE id = $1`
	case !retry:
		q = `UPDATE %[1]s SET %[2]s_delivery = 'no_recipient' WHERE id = $1`
	default:
		q = `UPDATE %[1]s SET %[2]s_delivery = CASE WHEN %[2]s_attempts >= 3 THEN 'failed' ELSE 'retry' END,
			%[2]s_sent_at = CASE WHEN %[2]s_attempts >= 3 THEN %[2]s_sent_at ELSE NULL END WHERE id = $1`
	}
	_, err := s.Pool.Exec(ctx, fmt.Sprintf(q, n.table, n.prefix), id)
	return err
}

// release puts a claimed notice back (its pod stopped running meanwhile).
func (s *Store) release(ctx context.Context, n notice, id int64) error {
	_, err := s.Pool.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s SET %[2]s_delivery = 'held', %[2]s_sent_at = NULL,
		%[2]s_attempts = greatest(%[2]s_attempts - 1, 0) WHERE id = $1`, n.table, n.prefix), id)
	return err
}

// PodRunning reports whether the pod may email its client now (live or test run).
func (s *Store) PodRunning(ctx context.Context, podID string) bool {
	var ok bool
	_ = s.Pool.QueryRow(ctx, `SELECT status IN ('live', 'dry_run') FROM founderos_client_pods WHERE id = $1`, podID).Scan(&ok)
	return ok
}

// Release puts back claimed notices whose pod stopped running before the send.
func (s *Store) ReleaseBrief(ctx context.Context, id int64) error {
	return s.release(ctx, briefNotice, id)
}
func (s *Store) ReleaseCoaching(ctx context.Context, id int64) error {
	return s.release(ctx, coachingNotice, id)
}
func (s *Store) ReleaseReview(ctx context.Context, id int64) error {
	return s.release(ctx, reviewNotice, id)
}

// ownTask records which pod a board task belongs to, at creation, so a later
// move of the task never routes its output elsewhere (Codex final review #1).
func (s *Store) ownTask(ctx context.Context, issue, podID, kind string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_task_owner (issue_id, pod_id, kind) VALUES ($1, $2, $3)
		ON CONFLICT (issue_id) DO NOTHING`, issue, podID, kind)
	return err
}
