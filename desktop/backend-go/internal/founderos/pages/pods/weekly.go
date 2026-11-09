package pods

// Weekly pipeline review and results (M6). Every Monday TFG OS counts the
// pod's last week (Monday to Sunday in the pod's timezone) from its own
// records, then asks the pod's AI SDR for a review: what happened, what is
// working, and the three things to work on next week, measured against the
// client's month-one baseline. The review is emailed to the client's rep and
// shown, with the numbers, in the portal.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Baseline is where the client started (the Diagnose month), to measure against.
type Baseline struct {
	MeetingsPerMonth int     `json:"meetingsPerMonth"`
	ReplyRatePct     float64 `json:"replyRatePct"`
	Note             string  `json:"note"`
	SetAt            string  `json:"setAt,omitempty"`
}

// SetBaseline records the client's starting numbers.
func (s *Store) SetBaseline(ctx context.Context, slug string, b Baseline) error {
	if b.MeetingsPerMonth < 0 || b.MeetingsPerMonth > 10000 || b.ReplyRatePct < 0 || b.ReplyRatePct > 100 {
		return fmt.Errorf("%w: baseline numbers out of range", ErrInvalid)
	}
	b.Note = clip(b.Note, 1000)
	b.SetAt = time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.Marshal(b)
	tag, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET baseline = $2, updated_at = now() WHERE slug = $1`, slug, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	p, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	return s.Log(ctx, p.ID, "settings", fmt.Sprintf("Baseline set: %d meetings a month, %.1f%% reply rate", b.MeetingsPerMonth, b.ReplyRatePct), "")
}

// GetBaseline reads the client's starting numbers (zero if never set).
func (s *Store) GetBaseline(ctx context.Context, podID string) (Baseline, error) {
	var raw []byte
	var b Baseline
	if err := s.Pool.QueryRow(ctx, `SELECT coalesce(baseline, '{}'::jsonb) FROM founderos_client_pods WHERE id = $1`, podID).Scan(&raw); err != nil {
		return b, err
	}
	_ = json.Unmarshal(raw, &b)
	return b, nil
}

// Metrics is one week of a pod's results, counted from TFG OS's own records.
type Metrics struct {
	Week             string  `json:"week"` // Monday, YYYY-MM-DD
	DryRun           bool    `json:"dryRun"`
	ProspectsReached int     `json:"prospectsReached"`
	EmailsSent       int     `json:"emailsSent"`
	Simulated        int     `json:"simulated"`
	Replies          int     `json:"replies"`
	ReplyRatePct     float64 `json:"replyRatePct"`
	Enquiries        int     `json:"enquiries"`
	AIReplies        int     `json:"aiReplies"`
	Handoffs         int     `json:"handoffs"`
	MeetingsBooked   int     `json:"meetingsBooked"`
	MeetingsHeld     int     `json:"meetingsHeld"`
	NoShows          int     `json:"noShows"`
	OptOuts          int     `json:"optOuts"`
}

// WeekOf returns the Monday 00:00 (in tz) of the week containing t.
func WeekOf(t time.Time, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	l := t.In(loc)
	back := (int(l.Weekday()) + 6) % 7
	return time.Date(l.Year(), l.Month(), l.Day()-back, 0, 0, 0, 0, loc)
}

// WeeklyMetrics counts the pod's results in [from, to).
func (s *Store) WeeklyMetrics(ctx context.Context, p Pod, from, to time.Time) (Metrics, error) {
	m := Metrics{Week: from.Format("2006-01-02"), DryRun: p.Status == StatusDryRun}
	err := s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM (SELECT lower(to_address) a, min(sent_at) f FROM founderos_outbox
			WHERE workspace_id = $1 AND status = 'sent' AND coalesce(sequence_key, '') <> 'reply' GROUP BY 1) x WHERE f >= $2 AND f < $3),
		(SELECT count(*) FROM founderos_outbox WHERE workspace_id = $1 AND status = 'sent' AND coalesce(sequence_key, '') <> 'reply' AND sent_at >= $2 AND sent_at < $3),
		(SELECT count(*) FROM founderos_outbox WHERE workspace_id = $1 AND status = 'simulated' AND coalesce(decided_at, created_at) >= $2 AND coalesce(decided_at, created_at) < $3),
		(SELECT count(*) FROM founderos_pod_conversations WHERE workspace_id = $1 AND source = 'email' AND created_at >= $2 AND created_at < $3),
		(SELECT count(*) FROM founderos_pod_inbound WHERE pod_id = $4 AND received_at >= $2 AND received_at < $3),
		(SELECT count(*) FROM founderos_outbox WHERE workspace_id = $1 AND status = 'sent' AND sequence_key = 'reply' AND sent_at >= $2 AND sent_at < $3),
		(SELECT count(*) FROM founderos_pod_handoffs WHERE pod_id = $4 AND created_at >= $2 AND created_at < $3),
		(SELECT count(*) FROM founderos_pod_meetings WHERE pod_id = $4 AND status <> 'cancelled' AND created_at >= $2 AND created_at < $3),
		(SELECT count(*) FROM founderos_pod_meetings WHERE pod_id = $4 AND status = 'booked' AND outcome = 'held' AND starts_at >= $2 AND starts_at < $3),
		(SELECT count(*) FROM founderos_pod_meetings WHERE pod_id = $4 AND status = 'booked' AND outcome = 'no_show' AND starts_at >= $2 AND starts_at < $3),
		(SELECT count(*) FROM founderos_email_suppression WHERE workspace_id = $1 AND created_at >= $2 AND created_at < $3)`,
		p.WorkspaceID, from, to, p.ID).Scan(&m.ProspectsReached, &m.EmailsSent, &m.Simulated, &m.Replies, &m.Enquiries,
		&m.AIReplies, &m.Handoffs, &m.MeetingsBooked, &m.MeetingsHeld, &m.NoShows, &m.OptOuts)
	if err != nil {
		return m, err
	}
	if m.ProspectsReached > 0 {
		m.ReplyRatePct = float64(int(float64(m.Replies)/float64(m.ProspectsReached)*1000+0.5)) / 10
	}
	return m, nil
}

// Week is one stored week with its review.
type Week struct {
	Start  string
	M      Metrics
	Review string
}

// Weeks lists the pod's stored weeks, newest first.
func (s *Store) Weeks(ctx context.Context, podID string, limit int) ([]Week, error) {
	rows, err := s.Pool.Query(ctx, `SELECT week_start::text, metrics, review FROM founderos_pod_weekly
		WHERE pod_id = $1 ORDER BY week_start DESC LIMIT $2`, podID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Week
	for rows.Next() {
		var w Week
		var raw []byte
		if err := rows.Scan(&w.Start, &raw, &w.Review); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &w.M)
		out = append(out, w)
	}
	return out, rows.Err()
}

func metricLines(m Metrics) string {
	mode := ""
	if m.DryRun {
		mode = " (dry run: nothing reached prospects)"
	}
	return fmt.Sprintf("Week of %s%s\n- prospects reached for the first time: %d\n- outreach emails sent: %d (dry-run drafts: %d)\n"+
		"- replies: %d (reply rate %.1f%%)\n- website enquiries: %d\n- AI replies sent: %d\n- handed to a person: %d\n"+
		"- meetings booked: %d; held: %d; no-shows: %d\n- opt-outs: %d\n",
		m.Week, mode, m.ProspectsReached, m.EmailsSent, m.Simulated, m.Replies, m.ReplyRatePct, m.Enquiries, m.AIReplies,
		m.Handoffs, m.MeetingsBooked, m.MeetingsHeld, m.NoShows, m.OptOuts)
}

func reviewTask(p Pod, m Metrics, prev []Week, b Baseline, pipeline string) string {
	var sb strings.Builder
	sb.WriteString("Weekly pipeline review for " + p.ClientName + ".\n\n")
	sb.WriteString("Write a \"review\" document on this task, readable in five minutes by " + p.ClientName + "'s team:\n" +
		"- what happened this week, in plain words, using only the numbers below;\n" +
		"- results against where they started (the baseline) and the trend over the last weeks;\n" +
		"- what is working and what is not (messages, segments, objections heard);\n" +
		"- the three things to work on next week, each with who does it;\n" +
		"- deals that need a person this week.\n" +
		"Do not invent numbers; if something is not counted below, say it is not measured.\n\n")
	sb.WriteString("This week (counted by TFG OS):\n" + metricLines(m) + "\n")
	if len(prev) > 0 {
		sb.WriteString("Previous weeks:\n")
		for _, w := range prev {
			sb.WriteString(fmt.Sprintf("- %s: reached %d, replies %d (%.1f%%), meetings booked %d, held %d\n",
				w.Start, w.M.ProspectsReached, w.M.Replies, w.M.ReplyRatePct, w.M.MeetingsBooked, w.M.MeetingsHeld))
		}
		sb.WriteString("\n")
	}
	if b.SetAt != "" {
		sb.WriteString(fmt.Sprintf("Baseline (before TFG): %d meetings a month, %.1f%% reply rate. %s\n\n", b.MeetingsPerMonth, b.ReplyRatePct, b.Note))
	} else {
		sb.WriteString("Baseline: not recorded yet; compare week to week only.\n\n")
	}
	if pipeline != "" {
		sb.WriteString("Open pipeline now: " + pipeline + "\n\n")
	}
	sb.WriteString("Work only from this task and your instructions; never use another client's or company's data.")
	return sb.String()
}

// TaskWeeklyReviews records last week's numbers for every running pod and
// asks its AI SDR for the review, once per week.
func (s *Store) TaskWeeklyReviews(ctx context.Context, b TaskBoard, now time.Time) (int, error) {
	list, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, p := range list {
		if p.Status != StatusLive && p.Status != StatusDryRun && p.Status != StatusPaused {
			continue
		}
		var proj, agent string
		if err := s.Pool.QueryRow(ctx, `SELECT project_id, agent_id FROM founderos_pod_agents WHERE pod_id = $1
			AND project_id <> '' AND agent_id <> ''`, p.ID).Scan(&proj, &agent); err != nil {
			continue // not provisioned yet
		}
		to := WeekOf(now, p.Timezone)
		from := to.AddDate(0, 0, -7)
		if !p.CreatedAt.Before(to) {
			continue // the pod did not exist for any of last week
		}
		m, err := s.WeeklyMetrics(ctx, p, from, to)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		raw, _ := json.Marshal(m)
		var id int64
		err = s.Pool.QueryRow(ctx, `INSERT INTO founderos_pod_weekly (pod_id, week_start, metrics) VALUES ($1, $2, $3)
			ON CONFLICT (pod_id, week_start) DO NOTHING RETURNING id`, p.ID, from.Format("2006-01-02"), raw).Scan(&id)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) { // no row: this week is already recorded
				errs = append(errs, err)
			}
			continue
		}
		prev, _ := s.Weeks(ctx, p.ID, 6)
		var older []Week
		for _, w := range prev {
			if w.Start != m.Week {
				older = append(older, w)
			}
		}
		base, _ := s.GetBaseline(ctx, p.ID)
		issue, err := b.CreateTask(ctx, proj, agent, clip(p.ClientName+": weekly pipeline review, week of "+m.Week, 200),
			reviewTask(p, m, older, base, s.pipelineSummary(ctx, p)))
		if err != nil {
			// Undo so the next pass retries the whole week.
			_, _ = s.Pool.Exec(ctx, `DELETE FROM founderos_pod_weekly WHERE id = $1`, id)
			errs = append(errs, err)
			continue
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_weekly SET review_issue_id = $2 WHERE id = $1`, id, issue); err != nil {
			errs = append(errs, err)
			continue
		}
		_ = s.Log(ctx, p.ID, "weekly", "Weekly results counted for the week of "+m.Week+"; review on the way", "")
		n++
	}
	return n, errors.Join(errs...)
}

func (s *Store) pipelineSummary(ctx context.Context, p Pod) string {
	rows, err := s.Pool.Query(ctx, `SELECT status, count(*) FROM founderos_funnel_contacts WHERE workspace_id = $1
		GROUP BY status ORDER BY 2 DESC`, p.WorkspaceID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var st string
		var c int
		if rows.Scan(&st, &c) == nil {
			parts = append(parts, fmt.Sprintf("%s %d", strings.ReplaceAll(st, "_", " "), c))
		}
	}
	var up int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM founderos_pod_meetings WHERE pod_id = $1 AND status = 'booked' AND starts_at > now()`, p.ID).Scan(&up)
	if up > 0 {
		parts = append(parts, fmt.Sprintf("upcoming meetings %d", up))
	}
	return strings.Join(parts, ", ")
}

// Review is a finished weekly review waiting to go to the client.
type Review struct {
	ID                      int64
	PodID, Client, To, Week string
	Text                    string
}

// ReviewsToSend lists weekly reviews written but not yet sent.
func (s *Store) ReviewsToSend(ctx context.Context) ([]Review, error) {
	rows, err := s.Pool.Query(ctx, `SELECT w.id, p.id::text, p.client_name, p.handoff_email, w.week_start::text, w.review
		FROM founderos_pod_weekly w JOIN founderos_client_pods p ON p.id = w.pod_id
		WHERE w.review <> '' AND w.review_sent_at IS NULL ORDER BY w.week_start LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.PodID, &r.Client, &r.To, &r.Week, &r.Text); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkReviewSent records that a weekly review went to the client (why == "") or why not.
func (s *Store) MarkReviewSent(ctx context.Context, id int64, why string) error {
	var podID, week string
	if err := s.Pool.QueryRow(ctx, `UPDATE founderos_pod_weekly SET review_sent_at = now() WHERE id = $1
		RETURNING pod_id::text, week_start::text`, id).Scan(&podID, &week); err != nil {
		return err
	}
	if why != "" {
		return s.Log(ctx, podID, "weekly", "Weekly review for the week of "+week+" is in your portal ("+clip(why, 120)+")", "")
	}
	return s.Log(ctx, podID, "weekly", "Weekly review for the week of "+week+" sent to your team", "")
}

// WeeklyText renders a review email body.
func WeeklyText(r Review, m Metrics) string {
	return "Your weekly AI SDR review, week of " + r.Week + ".\n\n" + metricLines(m) + "\n" + r.Text +
		"\n\nAlso in your portal: https://thefoundersgroup.net/portal\n\nThe Founders Group"
}

// WeekMetrics reads one stored week's numbers.
func (s *Store) WeekMetrics(ctx context.Context, id int64) (Metrics, error) {
	var raw []byte
	var m Metrics
	if err := s.Pool.QueryRow(ctx, `SELECT metrics FROM founderos_pod_weekly WHERE id = $1`, id).Scan(&raw); err != nil {
		return m, err
	}
	return m, json.Unmarshal(raw, &m)
}
