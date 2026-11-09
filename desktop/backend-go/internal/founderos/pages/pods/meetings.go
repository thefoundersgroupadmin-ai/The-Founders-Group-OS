package pods

// Meetings on a client pod's own calendar and their call briefs (M4). A
// booking engages the contact (stopping their sequence), is recorded once, and
// gets a brief from the pod's AI SDR before the call; the brief is emailed to
// the client's rep and shown in their portal.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Meeting is one booking as read from the pod's calendar.
type Meeting struct {
	UID, Title, Start           string
	AttendeeEmail, AttendeeName string
	Cancelled                   bool
}

// RecordMeetings stores new bookings for the pod and engages their contacts.
// It returns how many were new.
func (s *Store) RecordMeetings(ctx context.Context, slug string, list []Meeting) (int, error) {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, m := range list {
		if m.UID == "" {
			continue
		}
		if m.Cancelled {
			// Codex M2-M4 #6: a cancellation cancels the meeting we have.
			tag, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET status = 'cancelled'
				WHERE pod_id = $1 AND booking_uid = $2 AND status <> 'cancelled'`, p.ID, m.UID)
			if err != nil {
				return added, err
			}
			if tag.RowsAffected() > 0 {
				_ = s.Log(ctx, p.ID, "meeting_cancelled", "Meeting cancelled: "+m.UID, m.UID)
			}
			continue
		}
		if t, err := time.Parse(time.RFC3339, m.Start); err == nil {
			// A reschedule moves the meeting and sends the brief again with the new time.
			tag, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET starts_at = $3, status = 'booked',
				brief_sent_at = CASE WHEN brief <> '' THEN NULL ELSE brief_sent_at END
				WHERE pod_id = $1 AND booking_uid = $2 AND (starts_at IS DISTINCT FROM $3 OR status <> 'booked')`, p.ID, m.UID, t)
			if err != nil {
				return added, err
			}
			if tag.RowsAffected() > 0 {
				_ = s.Log(ctx, p.ID, "meeting_moved", "Meeting moved to "+t.Format("Mon 2 Jan 15:04 MST"), m.UID)
			}
		}
		email := strings.ToLower(strings.TrimSpace(m.AttendeeEmail))
		var start any
		if t, err := time.Parse(time.RFC3339, m.Start); err == nil {
			start = t
		}
		var contact *string
		_ = s.Pool.QueryRow(ctx, `SELECT id FROM founderos_funnel_contacts WHERE workspace_id = $1 AND lower(email) = $2
			ORDER BY id LIMIT 1`, p.WorkspaceID, email).Scan(&contact)
		tag, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_meetings (pod_id, booking_uid, contact_id, attendee_email,
			attendee_name, title, starts_at) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (pod_id, booking_uid) DO NOTHING`,
			p.ID, m.UID, contact, email, clip(m.AttendeeName, 120), clip(m.Title, 200), start)
		if err != nil {
			return added, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		added++
		if contact != nil {
			if _, err := s.Pool.Exec(ctx, `UPDATE founderos_funnel_contacts SET status = 'engaged', relationship = 'warm'
				WHERE id = $1 AND workspace_id = $2 AND status IN ('first_touch', 'nurtured')`, *contact, p.WorkspaceID); err != nil {
				return added, err
			}
			// A booked meeting is a person taking over: the AI stops replying (Codex M2-M4 #2).
			if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_conversations SET taken_over_at = coalesce(taken_over_at, now())
				WHERE workspace_id = $1 AND contact_id = $2`, p.WorkspaceID, *contact); err != nil {
				return added, err
			}
		}
		who := m.AttendeeName
		if who == "" {
			who = email
		}
		when := m.Start
		if t, ok := start.(time.Time); ok {
			when = t.Format("Mon 2 Jan 15:04 MST")
		}
		if err := s.Log(ctx, p.ID, "meeting_booked", fmt.Sprintf("Meeting booked: %s, %s", who, when), m.UID); err != nil {
			return added, err
		}
	}
	return added, nil
}

func briefTask(p Pod, title, name, email, when, contactNotes string) string {
	return "Call brief for " + p.ClientName + "'s rep before \"" + title + "\" with " + name + " <" + email + "> on " + when + ".\n\n" +
		"Write a \"brief\" document on this task, short enough to read in two minutes:\n" +
		"- who they are and their company (public sources only; cite them);\n" +
		"- what they need and why now, from the conversation so far;\n" +
		"- budget, timing and who decides, as far as known;\n" +
		"- what to ask, and the proof points from " + p.ClientName + "'s playbook that fit;\n" +
		"- likely objections and honest answers.\n\n" +
		(func() string {
			if contactNotes != "" {
				return "What the pipeline already knows:\n" + contactNotes + "\n\n"
			}
			return ""
		})() +
		"Work only from this task and your instructions; never use another client's or company's data."
}

// TaskBriefs asks the pod's AI SDR for a brief on each upcoming meeting.
func (s *Store) TaskBriefs(ctx context.Context, b TaskBoard) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT m.id, p.slug, m.title, m.attendee_name, m.attendee_email, m.starts_at,
		a.project_id, a.agent_id, coalesce(c.next_action, '') || ' ' || coalesce(c.research_evidence, '')
		FROM founderos_pod_meetings m JOIN founderos_client_pods p ON p.id = m.pod_id
		JOIN founderos_pod_agents a ON a.pod_id = p.id AND a.agent_id <> '' AND a.project_id <> ''
		LEFT JOIN founderos_funnel_contacts c ON c.id = m.contact_id
		WHERE m.brief_issue_id IS NULL AND m.starts_at > now() AND m.status = 'booked' ORDER BY m.starts_at LIMIT 20`)
	if err != nil {
		return 0, err
	}
	type job struct {
		id                                      int64
		slug, title, name, email, proj, ag, kno string
		at                                      time.Time
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.slug, &j.title, &j.name, &j.email, &j.at, &j.proj, &j.ag, &j.kno); err != nil {
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
		name := j.name
		if name == "" {
			name = j.email
		}
		when := j.at.Format("Mon 2 Jan 15:04 MST")
		issue, err := b.CreateTask(ctx, j.proj, j.ag, clip(p.ClientName+": call brief for "+name, 200),
			briefTask(p, j.title, name, j.email, when, strings.TrimSpace(j.kno)))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET brief_issue_id = $2 WHERE id = $1`, j.id, issue); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// Brief is a finished call brief waiting to go to the client's rep.
type Brief struct {
	ID                                   int64
	PodID, Client, To, Title, Name, Text string
	At                                   *time.Time
}

// BriefsToSend lists briefs written but not yet sent.
func (s *Store) BriefsToSend(ctx context.Context) ([]Brief, error) {
	rows, err := s.Pool.Query(ctx, `SELECT m.id, p.id::text, p.client_name, p.handoff_email, m.title,
		coalesce(nullif(m.attendee_name, ''), m.attendee_email), m.brief, m.starts_at
		FROM founderos_pod_meetings m JOIN founderos_client_pods p ON p.id = m.pod_id
		WHERE m.brief <> '' AND m.brief_sent_at IS NULL AND m.status = 'booked' ORDER BY m.starts_at LIMIT 20`)
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

// MarkBriefSent records that a brief went to the rep (why == "") or why not.
func (s *Store) MarkBriefSent(ctx context.Context, id int64, why string) error {
	var podID, name string
	err := s.Pool.QueryRow(ctx, `UPDATE founderos_pod_meetings SET brief_sent_at = now()
		WHERE id = $1 RETURNING pod_id::text, coalesce(nullif(attendee_name, ''), attendee_email)`, id).Scan(&podID, &name)
	if err != nil {
		return err
	}
	if why != "" {
		return s.Log(ctx, podID, "brief", "Call brief for "+name+" is in your portal ("+clip(why, 120)+")", "")
	}
	return s.Log(ctx, podID, "brief", "Call brief for "+name+" sent to your team", "")
}

// MeetingRow is a meeting as the portal shows it.
type MeetingRow struct {
	ID                             int64
	Title, Attendee, Brief, Status string
	Outcome, Coaching              string
	StartsAt                       *time.Time
}

// MeetingsForPortal lists the pod's meetings, newest first.
func (s *Store) MeetingsForPortal(ctx context.Context, podID string) ([]MeetingRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, title, coalesce(nullif(attendee_name, ''), attendee_email), brief, status, outcome, coaching, starts_at
		FROM founderos_pod_meetings WHERE pod_id = $1 ORDER BY starts_at DESC NULLS LAST LIMIT 40`, podID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingRow
	for rows.Next() {
		var m MeetingRow
		if err := rows.Scan(&m.ID, &m.Title, &m.Attendee, &m.Brief, &m.Status, &m.Outcome, &m.Coaching, &m.StartsAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
