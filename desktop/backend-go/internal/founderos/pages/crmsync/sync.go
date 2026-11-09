// Package crmsync writes a client pod's work into the client's own CRM (M5):
// pipeline contacts, every email sent, meetings with their call briefs, and
// qualification notes. A mapping table records what is already there, so a
// pass creates nothing twice. Only the pod's own workspace is read.
package crmsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Contact struct {
	Email, FirstName, LastName, Company, Phone string
}

type Email struct {
	Subject, Body string
	SentAt        time.Time
}

type Meeting struct {
	Title, Body string
	StartsAt    time.Time
}

// CRM is a client's CRM as the sync needs it.
type CRM interface {
	Name() string
	UpsertContact(ctx context.Context, c Contact) (string, error)
	LogEmail(ctx context.Context, contactID string, e Email) error
	LogMeeting(ctx context.Context, contactID string, m Meeting) error
	AddNote(ctx context.Context, contactID, text string) error
}

// perPass bounds how much one pass writes, so a large backlog drains steadily.
const perPass = 50

func mapped(ctx context.Context, pool *pgxpool.Pool, pod, kind, local string) (string, bool, error) {
	var remote string
	err := pool.QueryRow(ctx, `SELECT remote_id FROM founderos_pod_crm_ids WHERE pod_id = $1 AND local_kind = $2 AND local_id = $3`,
		pod, kind, local).Scan(&remote)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return remote, err == nil, err
}

func remember(ctx context.Context, pool *pgxpool.Pool, pod, kind, local, remote string) error {
	_, err := pool.Exec(ctx, `INSERT INTO founderos_pod_crm_ids (pod_id, local_kind, local_id, remote_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (pod_id, local_kind, local_id) DO UPDATE SET remote_id = EXCLUDED.remote_id, synced_at = now()`, pod, kind, local, remote)
	return err
}

func splitName(person string) (string, string) {
	f := strings.Fields(person)
	switch len(f) {
	case 0:
		return "", ""
	case 1:
		return f[0], ""
	}
	return f[0], strings.Join(f[1:], " ")
}

// SyncPod runs one pass for the pod and returns how many things it wrote.
func SyncPod(ctx context.Context, pool *pgxpool.Pool, podID string, crm CRM) (int, error) {
	var ws string
	if err := pool.QueryRow(ctx, `SELECT workspace_id::text FROM founderos_client_pods WHERE id = $1`, podID).Scan(&ws); err != nil {
		return 0, err
	}
	written := 0
	budget := func() bool { return written < perPass }

	// 1. Contacts with an address.
	rows, err := pool.Query(ctx, `SELECT id, lower(email), coalesce(person, name, ''), coalesce(company, '')
		FROM founderos_funnel_contacts WHERE workspace_id = $1 AND email IS NOT NULL AND email <> '' ORDER BY created_at, id`, ws)
	if err != nil {
		return 0, err
	}
	type contact struct{ id, email, person, company string }
	var cs []contact
	for rows.Next() {
		var c contact
		if err := rows.Scan(&c.id, &c.email, &c.person, &c.company); err != nil {
			rows.Close()
			return 0, err
		}
		cs = append(cs, c)
	}
	rows.Close()
	remoteOf := map[string]string{}
	for _, c := range cs {
		rid, ok, err := mapped(ctx, pool, podID, "contact", c.id)
		if err != nil {
			return written, err
		}
		if !ok {
			if !budget() {
				break
			}
			first, last := splitName(c.person)
			if rid, err = crm.UpsertContact(ctx, Contact{Email: c.email, FirstName: first, LastName: last, Company: c.company}); err != nil {
				return written, err
			}
			if err := remember(ctx, pool, podID, "contact", c.id, rid); err != nil {
				return written, err
			}
			written++
		}
		remoteOf[c.id] = rid
	}

	// 2. Emails actually sent (never drafts or test runs).
	erows, err := pool.Query(ctx, `SELECT id, contact_id, subject, body, sent_at FROM founderos_outbox
		WHERE workspace_id = $1 AND status = 'sent' AND contact_id IS NOT NULL ORDER BY sent_at, id`, ws)
	if err != nil {
		return written, err
	}
	type sent struct {
		id, contact, subject, body string
		at                         time.Time
	}
	var es []sent
	for erows.Next() {
		var e sent
		if err := erows.Scan(&e.id, &e.contact, &e.subject, &e.body, &e.at); err != nil {
			erows.Close()
			return written, err
		}
		es = append(es, e)
	}
	erows.Close()
	for _, e := range es {
		rid := remoteOf[e.contact]
		if rid == "" || !budget() {
			continue
		}
		if _, ok, err := mapped(ctx, pool, podID, "email", e.id); err != nil || ok {
			if err != nil {
				return written, err
			}
			continue
		}
		if err := crm.LogEmail(ctx, rid, Email{Subject: e.subject, Body: e.body, SentAt: e.at}); err != nil {
			return written, err
		}
		if err := remember(ctx, pool, podID, "email", e.id, rid); err != nil {
			return written, err
		}
		written++
	}

	// 3. Meetings, with their call briefs.
	mrows, err := pool.Query(ctx, `SELECT m.id::text, m.contact_id, m.title, m.brief, m.starts_at, coalesce(p.timezone, 'UTC')
		FROM founderos_pod_meetings m JOIN founderos_client_pods p ON p.id = m.pod_id
		WHERE m.pod_id = $1 AND m.status = 'booked' AND m.contact_id IS NOT NULL ORDER BY m.starts_at`, podID)
	if err != nil {
		return written, err
	}
	type meet struct {
		id, contact, title, brief, tz string
		at                            *time.Time
	}
	var ms []meet
	for mrows.Next() {
		var m meet
		if err := mrows.Scan(&m.id, &m.contact, &m.title, &m.brief, &m.at, &m.tz); err != nil {
			mrows.Close()
			return written, err
		}
		ms = append(ms, m)
	}
	mrows.Close()
	for _, m := range ms {
		rid := remoteOf[m.contact]
		if rid == "" || m.at == nil || !budget() {
			continue
		}
		if _, ok, err := mapped(ctx, pool, podID, "meeting", m.id); err != nil || ok {
			if err != nil {
				return written, err
			}
			continue
		}
		if err := crm.LogMeeting(ctx, rid, Meeting{Title: m.title, Body: m.brief, StartsAt: *m.at}); err != nil {
			return written, err
		}
		if err := remember(ctx, pool, podID, "meeting", m.id, rid); err != nil {
			return written, err
		}
		// What the CRM was just given is the baseline for later change notes
		// (Codex round 4 #5): its time, status and brief, as logged.
		for local, v := range map[string]string{m.id + ":time": whenOf(m.at, m.tz), m.id + ":status": "booked", m.id + ":brief": m.brief} {
			if err := remember(ctx, pool, podID, factKind, local, fingerprint(v)); err != nil {
				return written, err
			}
		}
		written++
	}

	// 4. Qualification notes (each new version once).
	nrows, err := pool.Query(ctx, `SELECT id, next_action FROM founderos_funnel_contacts
		WHERE workspace_id = $1 AND next_action LIKE 'Qualified.%'`, ws)
	if err != nil {
		return written, err
	}
	type note struct{ contact, text string }
	var ns []note
	for nrows.Next() {
		var n note
		if err := nrows.Scan(&n.contact, &n.text); err != nil {
			nrows.Close()
			return written, err
		}
		ns = append(ns, n)
	}
	nrows.Close()
	for _, n := range ns {
		rid := remoteOf[n.contact]
		if rid == "" || !budget() {
			continue
		}
		h := sha256.Sum256([]byte(n.text))
		local := n.contact + ":" + hex.EncodeToString(h[:8])
		if _, ok, err := mapped(ctx, pool, podID, "note", local); err != nil || ok {
			if err != nil {
				return written, err
			}
			continue
		}
		if err := crm.AddNote(ctx, rid, "AI SDR qualification: "+n.text); err != nil {
			return written, err
		}
		if err := remember(ctx, pool, podID, "note", local, rid); err != nil {
			return written, err
		}
		written++
	}

	// 5. Later changes to what is already in the CRM, as notes on the contact:
	// a meeting moved, cancelled, held or missed, a brief or coaching written
	// after the meeting was logged, a contact's stage changing or an opt-out.
	// Each fact is fingerprinted; the first sight is recorded silently, and a
	// note is written only when it changes afterwards.
	n, err := syncChanges(ctx, pool, podID, ws, crm, remoteOf, perPass-written)
	return written + n, err
}

const factKind = "fact"

// whenOf formats a meeting time in the pod's timezone, as notes show it.
func whenOf(at *time.Time, tz string) string {
	if at == nil {
		return ""
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return at.In(loc).Format("Mon 2 Jan 2006, 15:04 MST")
}

// fact returns the stored fingerprint for a fact and whether one exists.
func fact(ctx context.Context, pool *pgxpool.Pool, pod, local string) (string, bool, error) {
	return mapped(ctx, pool, pod, factKind, local)
}

func fingerprint(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:8])
}

func syncChanges(ctx context.Context, pool *pgxpool.Pool, podID, ws string, crm CRM, remoteOf map[string]string, room int) (int, error) {
	written := 0
	type change struct{ local, value, contact, note string }
	var cs []change

	// Meetings already logged in the CRM, whatever their status now.
	rows, err := pool.Query(ctx, `SELECT m.id::text, m.contact_id, m.title, m.status, m.starts_at, m.outcome, m.brief, m.coaching, coalesce(p.timezone, 'UTC')
		FROM founderos_pod_meetings m JOIN founderos_client_pods p ON p.id = m.pod_id
		JOIN founderos_pod_crm_ids c ON c.pod_id = m.pod_id AND c.local_kind = 'meeting' AND c.local_id = m.id::text
		WHERE m.pod_id = $1 AND m.contact_id IS NOT NULL ORDER BY m.starts_at`, podID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id, contact, title, status, outcome, brief, coaching, tz string
		var at *time.Time
		if err := rows.Scan(&id, &contact, &title, &status, &at, &outcome, &brief, &coaching, &tz); err != nil {
			rows.Close()
			return 0, err
		}
		when := whenOf(at, tz)
		name := strings.TrimSpace(title)
		if name == "" {
			name = "Meeting"
		}
		cs = append(cs, change{id + ":time", when, contact, name + " moved to " + when + "."})
		statusNote := name + " (" + when + ") was cancelled."
		if status != "cancelled" {
			statusNote = name + " (" + when + ") is booked again."
		}
		cs = append(cs, change{id + ":status", status, contact, statusNote})
		switch outcome {
		case "held":
			cs = append(cs, change{id + ":outcome", outcome, contact, name + " (" + when + ") was held."})
		case "no_show":
			cs = append(cs, change{id + ":outcome", outcome, contact, name + " (" + when + "): the prospect did not show."})
		default:
			cs = append(cs, change{id + ":outcome", outcome, contact, ""})
		}
		cs = append(cs, change{id + ":brief", brief, contact, "Call brief for " + name + " (" + when + "):\n\n" + brief})
		cs = append(cs, change{id + ":coaching", coaching, contact, "Call coaching for " + name + " (" + when + "):\n\n" + coaching})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Contacts already in the CRM: their stage and whether they opted out.
	crows, err := pool.Query(ctx, `SELECT f.id, coalesce(f.status, ''),
		EXISTS (SELECT 1 FROM founderos_email_suppression s WHERE (s.workspace_id::text = f.workspace_id::text OR s.workspace_id IS NULL)
			AND (lower(s.address) = lower(f.email) OR (coalesce(s.domain, '') <> '' AND lower(f.email) LIKE '%@' || lower(s.domain))))
		FROM founderos_funnel_contacts f WHERE f.workspace_id = $1 AND f.email IS NOT NULL AND f.email <> '' ORDER BY f.created_at, f.id`, ws)
	if err != nil {
		return 0, err
	}
	for crows.Next() {
		var id, status string
		var optedOut bool
		if err := crows.Scan(&id, &status, &optedOut); err != nil {
			crows.Close()
			return 0, err
		}
		if remoteOf[id] == "" {
			continue
		}
		cs = append(cs, change{id + ":stage", status, id, "AI SDR stage: " + strings.ReplaceAll(status, "_", " ") + "."})
		opt := "no"
		if optedOut {
			opt = "yes"
		}
		optNote := "Asked not to be contacted again. The AI SDR will not email them."
		if !optedOut {
			optNote = "" // lifted: recorded silently, never a false opt-out note (Codex round 4 #6)
		}
		cs = append(cs, change{id + ":optout", opt, id, optNote})
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return 0, err
	}

	// A meeting's facts are first recorded with what the CRM was given when it
	// was logged: its time, status and brief. Anything else seen first counts as
	// a change only if it carries information (a brief, coaching or outcome
	// added after the meeting was logged).
	for _, c := range cs {
		rid := remoteOf[c.contact]
		if rid == "" {
			continue
		}
		fp := fingerprint(c.value)
		have, ok, err := fact(ctx, pool, podID, c.local)
		if err != nil {
			return written, err
		}
		if ok && have == fp {
			continue
		}
		silent := !ok && !informative(c.local, c.value)
		if !silent && (c.note == "" || c.value == "") {
			// Cleared or uninteresting (outcome reset, brief removed): just record it.
			silent = true
		}
		if !silent {
			if written >= room {
				continue // noted on a later pass; silent baselines still recorded
			}
			if err := crm.AddNote(ctx, rid, c.note); err != nil {
				return written, err
			}
			written++
		}
		if err := remember(ctx, pool, podID, factKind, c.local, fp); err != nil {
			return written, err
		}
	}
	return written, nil
}

// informative reports whether a fact seen for the first time is news for the
// CRM: an outcome or coaching (never part of the logged meeting), or an
// opt-out. A meeting's time, status and brief went in with the meeting, and a
// contact's stage is recorded silently the first time.
func informative(local, value string) bool {
	switch {
	// A meeting logged before baselines were recorded at log time: it went in
	// booked, perhaps without a brief, so a cancellation or brief is news.
	case strings.HasSuffix(local, ":status"):
		return value != "booked"
	case strings.HasSuffix(local, ":brief"):
		return value != ""
	case strings.HasSuffix(local, ":outcome"), strings.HasSuffix(local, ":coaching"):
		return value != ""
	case strings.HasSuffix(local, ":optout"):
		return value == "yes"
	}
	return false
}
