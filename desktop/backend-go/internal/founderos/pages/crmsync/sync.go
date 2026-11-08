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
	mrows, err := pool.Query(ctx, `SELECT m.id::text, m.contact_id, m.title, m.brief, m.starts_at FROM founderos_pod_meetings m
		WHERE m.pod_id = $1 AND m.status = 'booked' AND m.contact_id IS NOT NULL ORDER BY m.starts_at`, podID)
	if err != nil {
		return written, err
	}
	type meet struct {
		id, contact, title, brief string
		at                        *time.Time
	}
	var ms []meet
	for mrows.Next() {
		var m meet
		if err := mrows.Scan(&m.id, &m.contact, &m.title, &m.brief, &m.at); err != nil {
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
	return written, nil
}
