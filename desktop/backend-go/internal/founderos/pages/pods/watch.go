package pods

// Stuck AI tasks (Codex open item: stalled agents are not recovered). Every
// enquiry, reply, call brief, coaching and weekly review task is watched from the
// moment it is created until its work arrives. One that is slow gets a single
// nudge on the board (a comment wakes its agent); one still not done after
// that is marked stuck, logged, and raised as an alert for a person. Nothing
// is re-created automatically: a second copy could email a prospect twice.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// TaskWatch reads and nudges tasks on the board.
type TaskWatch interface {
	TaskStatus(ctx context.Context, issueID string) (string, error)
	Nudge(ctx context.Context, issueID, message string) error
}

type stallRule struct {
	nudge, stuck time.Duration
	what         string
}

// An enquiry promises a reply within minutes; reports have more room.
var stallRules = map[string]stallRule{
	"enquiry":  {20 * time.Minute, time.Hour, "reply to a website enquiry"},
	"reply":    {20 * time.Minute, time.Hour, "reply to a prospect's email"},
	"brief":    {2 * time.Hour, 6 * time.Hour, "call brief"},
	"coaching": {6 * time.Hour, 24 * time.Hour, "call coaching notes"},
	"review":   {6 * time.Hour, 24 * time.Hour, "weekly review"},
}

// briefLead marks a brief stuck when its call is this close and none exists.
const briefLead = time.Hour

type watched struct {
	issue, podID, kind, client string
	created                    time.Time
	nudged                     *time.Time
	done, gone                 bool
	callAt                     *time.Time
}

// WatchTasks checks every open pod task once and returns how many it nudged
// and how many it marked stuck.
func (s *Store) WatchTasks(ctx context.Context, b TaskWatch, now time.Time) (nudged, stuck int, err error) {
	rows, err := s.Pool.Query(ctx, `SELECT o.issue_id, o.pod_id::text, o.kind, p.client_name, o.created_at, o.nudged_at,
		CASE o.kind
			WHEN 'brief' THEN EXISTS (SELECT 1 FROM founderos_pod_meetings m WHERE m.brief_issue_id = o.issue_id AND m.brief <> '')
			WHEN 'coaching' THEN EXISTS (SELECT 1 FROM founderos_pod_meetings m WHERE m.coaching_issue_id = o.issue_id AND m.coaching <> '')
			WHEN 'review' THEN EXISTS (SELECT 1 FROM founderos_pod_weekly w WHERE w.review_issue_id = o.issue_id AND w.review <> '')
			ELSE false END,
		CASE o.kind
			WHEN 'brief' THEN NOT EXISTS (SELECT 1 FROM founderos_pod_meetings m WHERE m.brief_issue_id = o.issue_id AND m.status = 'booked')
			WHEN 'coaching' THEN NOT EXISTS (SELECT 1 FROM founderos_pod_meetings m WHERE m.coaching_issue_id = o.issue_id)
			WHEN 'review' THEN NOT EXISTS (SELECT 1 FROM founderos_pod_weekly w WHERE w.review_issue_id = o.issue_id)
			ELSE EXISTS (SELECT 1 FROM founderos_pod_conversations c WHERE c.issue_id = o.issue_id AND c.taken_over_at IS NOT NULL) END,
		(SELECT m.starts_at FROM founderos_pod_meetings m WHERE o.kind = 'brief' AND m.brief_issue_id = o.issue_id LIMIT 1)
		FROM founderos_pod_task_owner o JOIN founderos_client_pods p ON p.id = o.pod_id
		WHERE o.settled_at IS NULL AND o.stuck_at IS NULL AND o.kind IN ('enquiry', 'reply', 'brief', 'coaching', 'review')
		AND p.status IN ('live', 'dry_run', 'paused')
		ORDER BY o.created_at LIMIT 200`)
	if err != nil {
		return 0, 0, err
	}
	var list []watched
	for rows.Next() {
		var w watched
		if err := rows.Scan(&w.issue, &w.podID, &w.kind, &w.client, &w.created, &w.nudged, &w.done, &w.gone, &w.callAt); err != nil {
			rows.Close()
			return 0, 0, err
		}
		list = append(list, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	var errs []error
	for _, w := range list {
		rule := stallRules[w.kind]
		if w.done || w.gone {
			if err := s.settle(ctx, w.issue); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		age := now.Sub(w.created)
		urgent := w.kind == "brief" && w.callAt != nil && w.callAt.Sub(now) < briefLead
		if age < rule.nudge && !urgent {
			continue
		}
		status, err := b.TaskStatus(ctx, w.issue)
		if err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", w.issue, err))
			continue
		}
		switch status {
		case "cancelled":
			// Someone stopped it on purpose.
			if err := s.settle(ctx, w.issue); err != nil {
				errs = append(errs, err)
			}
			_ = s.Log(ctx, w.podID, "task_watch", "The "+rule.what+" task was cancelled on the board", w.issue)
			continue
		case "done":
			if w.kind == "enquiry" || w.kind == "reply" {
				if err := s.settle(ctx, w.issue); err != nil {
					errs = append(errs, err)
				}
				continue
			}
			// Done on the board but its document never arrived: give the
			// importer until the stuck deadline, then flag it.
			if age >= rule.stuck {
				if s.markStuck(ctx, w, "the agent finished without writing the "+rule.what) == nil {
					stuck++
				}
			}
			continue
		}
		switch {
		case age >= rule.stuck || (urgent && w.nudged != nil):
			reason := fmt.Sprintf("no %s after %s (board status: %s)", rule.what, age.Round(time.Minute), status)
			if urgent {
				reason = "the call starts soon and there is still no brief (board status: " + status + ")"
			}
			if err := s.markStuck(ctx, w, reason); err != nil {
				errs = append(errs, err)
			} else {
				stuck++
			}
		case w.nudged == nil:
			msg := "TFG OS: this " + rule.what + " for " + w.client + " is still waiting. Please finish it now and write the document the task asks for. " +
				"If something stops you, say what in a comment."
			if err := b.Nudge(ctx, w.issue, msg); err != nil {
				errs = append(errs, fmt.Errorf("nudge %s: %w", w.issue, err))
				continue
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_task_owner SET nudged_at = $2 WHERE issue_id = $1`, w.issue, now); err != nil {
				errs = append(errs, err)
				continue
			}
			_ = s.Log(ctx, w.podID, "task_watch", "Nudged the AI SDR: the "+rule.what+" is taking longer than it should", w.issue)
			nudged++
		}
	}
	return nudged, stuck, errors.Join(errs...)
}

func (s *Store) settle(ctx context.Context, issue string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_task_owner SET settled_at = now() WHERE issue_id = $1 AND settled_at IS NULL`, issue)
	return err
}

func (s *Store) markStuck(ctx context.Context, w watched, reason string) error {
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_task_owner SET stuck_at = now(), stuck_reason = $2 WHERE issue_id = $1`, w.issue, reason); err != nil {
		return err
	}
	_ = s.Log(ctx, w.podID, "task_watch", "Stuck: "+reason, w.issue)
	return nil
}

// ResolveStuck clears a stuck task once a person has dealt with it.
func (s *Store) ResolveStuck(ctx context.Context, slug, issue string) error {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_task_owner SET settled_at = now()
		WHERE issue_id = $1 AND pod_id = $2 AND stuck_at IS NOT NULL AND settled_at IS NULL`, issue, p.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
