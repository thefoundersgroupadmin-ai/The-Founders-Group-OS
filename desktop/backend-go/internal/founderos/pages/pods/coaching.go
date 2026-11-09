package pods

// Call coaching (M6). After a booked call, its recording's transcript (from the
// client's own Fathom, or pasted by TFG from any recorder such as Plaud) goes
// to the pod's AI SDR, which writes coaching notes for the client's rep. The
// notes are emailed to the rep and shown in the portal.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Recording is one recorded call as read from a recorder.
type Recording struct {
	Ref        string
	Start      time.Time
	Invitees   []string // email addresses
	Transcript string
}

// matchWindow is how far a recording may start from the booked time.
const matchWindow = 3 * time.Hour

// AttachRecordings gives each past, unrecorded meeting of the pod the
// recording that is its call: one started near the booked time with the
// attendee among the invitees. A recording is used once. It returns how many
// meetings got a transcript.
func (s *Store) AttachRecordings(ctx context.Context, slug string, recs []Recording) (int, error) {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return 0, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id, attendee_email, starts_at FROM founderos_pod_meetings
		WHERE pod_id = $1 AND status = 'booked' AND transcript = '' AND outcome <> 'no_show'
		AND starts_at IS NOT NULL AND starts_at < now() AND starts_at > now() - interval '30 days'`, p.ID)
	if err != nil {
		return 0, err
	}
	type open struct {
		id    int64
		email string
		at    time.Time
	}
	var meetings []open
	for rows.Next() {
		var m open
		if err := rows.Scan(&m.id, &m.email, &m.at); err != nil {
			rows.Close()
			return 0, err
		}
		meetings = append(meetings, m)
	}
	rows.Close()
	used := map[string]bool{}
	rs, err := s.Pool.Query(ctx, `SELECT recording_ref FROM founderos_pod_meetings WHERE pod_id = $1 AND recording_ref <> ''`, p.ID)
	if err != nil {
		return 0, err
	}
	for rs.Next() {
		var r string
		_ = rs.Scan(&r)
		used[r] = true
	}
	rs.Close()
	n := 0
	for _, m := range meetings {
		for _, r := range recs {
			if r.Ref == "" || used[r.Ref] || strings.TrimSpace(r.Transcript) == "" {
				continue
			}
			d := r.Start.Sub(m.at)
			if d < -matchWindow || d > matchWindow || !hasInvitee(r.Invitees, m.email) {
				continue
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET transcript = $2, recording_ref = $3, outcome = 'held'
				WHERE id = $1 AND transcript = ''`, m.id, clip(r.Transcript, 200000), r.Ref); err != nil {
				return n, err
			}
			used[r.Ref] = true
			n++
			_ = s.Log(ctx, p.ID, "call_recorded", "Call recording received; coaching notes on the way", r.Ref)
			break
		}
	}
	return n, nil
}

func hasInvitee(list []string, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, e := range list {
		if strings.ToLower(strings.TrimSpace(e)) == email {
			return true
		}
	}
	return false
}

// SetCallOutcome records how a call went, by hand: held (optionally with a
// pasted transcript from any recorder) or no-show.
func (s *Store) SetCallOutcome(ctx context.Context, slug string, meetingID int64, outcome, transcript string) error {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	if outcome != "held" && outcome != "no_show" {
		return fmt.Errorf("%w: outcome must be held or no_show", ErrInvalid)
	}
	transcript = clip(transcript, 200000)
	if outcome == "no_show" && transcript != "" {
		return fmt.Errorf("%w: a no-show has no transcript", ErrInvalid)
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET outcome = $3,
		transcript = CASE WHEN $4 <> '' AND coaching_issue_id IS NULL THEN $4 ELSE transcript END,
		recording_ref = CASE WHEN $4 <> '' AND coaching_issue_id IS NULL AND recording_ref = '' THEN 'pasted' ELSE recording_ref END
		WHERE id = $1 AND pod_id = $2 AND status = 'booked'`, meetingID, p.ID, outcome, transcript)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	summary := "Call marked as held"
	if outcome == "no_show" {
		summary = "Call marked as a no-show"
	} else if transcript != "" {
		summary = "Call transcript added; coaching notes on the way"
	}
	return s.Log(ctx, p.ID, "call_outcome", summary, "")
}

func coachingTask(p Pod, title, name, when, brief, transcript string) string {
	b := "Coaching notes for " + p.ClientName + "'s rep on the call \"" + title + "\" with " + name + " on " + when + ".\n\n" +
		"Write a \"coaching\" document on this task that the rep can read in three minutes:\n" +
		"- the outcome and the agreed next step (who does what by when);\n" +
		"- what went well, quoting the moments from the transcript;\n" +
		"- what to do differently: questions not asked, objections not handled, talking too much, unclear next step;\n" +
		"- qualification as heard on the call (need, budget, timing, who decides), and what is still unknown;\n" +
		"- the follow-up email the rep should send today, in " + p.ClientName + "'s voice.\n\n"
	if brief != "" {
		b += "The call brief written before the call:\n" + clip(brief, 6000) + "\n\n"
	}
	return b + "Transcript:\n" + clip(transcript, 60000) + "\n\n" +
		"Judge against " + p.ClientName + "'s playbook and qualification rules in your instructions. " +
		"Work only from this task and your instructions; never use another client's or company's data."
}

// TaskCoaching asks the pod's AI SDR for coaching notes on each recorded call.
func (s *Store) TaskCoaching(ctx context.Context, b TaskBoard) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT m.id, p.slug, m.title, coalesce(nullif(m.attendee_name, ''), m.attendee_email),
		m.starts_at, m.brief, m.transcript, a.project_id, a.agent_id
		FROM founderos_pod_meetings m JOIN founderos_client_pods p ON p.id = m.pod_id
		JOIN founderos_pod_agents a ON a.pod_id = p.id AND a.agent_id <> '' AND a.project_id <> ''
		WHERE m.coaching_issue_id IS NULL AND m.transcript <> '' AND m.outcome = 'held' AND m.status = 'booked'
		ORDER BY m.starts_at LIMIT 10`)
	if err != nil {
		return 0, err
	}
	type job struct {
		id                                  int64
		slug, title, name, brief, tr, pr, a string
		at                                  *time.Time
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.slug, &j.title, &j.name, &j.at, &j.brief, &j.tr, &j.pr, &j.a); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	n := 0
	var errs []error
	for _, j := range jobs {
		p, err := s.Get(ctx, j.slug)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		when := "the booked time"
		if j.at != nil {
			when = j.at.Format("Mon 2 Jan 15:04 MST")
		}
		issue, err := b.CreateTask(ctx, j.pr, j.a, clip(p.ClientName+": coaching notes for the call with "+j.name, 200),
			coachingTask(p, j.title, j.name, when, j.brief, j.tr))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET coaching_issue_id = $2 WHERE id = $1`, j.id, issue); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// CoachingToSend lists coaching notes written but not yet sent to the rep.
func (s *Store) CoachingToSend(ctx context.Context) ([]Brief, error) {
	rows, err := s.Pool.Query(ctx, `SELECT m.id, p.id::text, p.client_name, p.handoff_email, m.title,
		coalesce(nullif(m.attendee_name, ''), m.attendee_email), m.coaching, m.starts_at
		FROM founderos_pod_meetings m JOIN founderos_client_pods p ON p.id = m.pod_id
		WHERE m.coaching <> '' AND m.coaching_sent_at IS NULL ORDER BY m.starts_at LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Brief
	for rows.Next() {
		var b Brief
		if err := rows.Scan(&b.ID, &b.PodID, &b.Client, &b.To, &b.Title, &b.Name, &b.Text, &b.At); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// MarkCoachingSent records that coaching notes went to the rep (why == "") or why not.
func (s *Store) MarkCoachingSent(ctx context.Context, id int64, why string) error {
	var podID, name string
	err := s.Pool.QueryRow(ctx, `UPDATE founderos_pod_meetings SET coaching_sent_at = now()
		WHERE id = $1 RETURNING pod_id::text, coalesce(nullif(attendee_name, ''), attendee_email)`, id).Scan(&podID, &name)
	if err != nil {
		return err
	}
	if why != "" {
		return s.Log(ctx, podID, "coaching", "Coaching notes for the call with "+name+" are in your portal ("+clip(why, 120)+")", "")
	}
	return s.Log(ctx, podID, "coaching", "Coaching notes for the call with "+name+" sent to your team", "")
}
