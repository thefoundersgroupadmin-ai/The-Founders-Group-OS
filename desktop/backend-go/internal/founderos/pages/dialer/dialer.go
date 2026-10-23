// Package dialer is the call queue behind /os/dialer: who to ring next (a
// pipeline contact with a number, not on the do-not-call list, not claimed by
// a teammate, due, and inside their own local calling hours), the call log,
// and what each outcome does to the pipeline.
package dialer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nyaruka/phonenumbers"
)

// ---- numbers and hours ------------------------------------------------------

// E164 normalises a number (US by default) or says why it can't.
func E164(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("no number")
	}
	n, err := phonenumbers.Parse(raw, "US")
	if err != nil || !phonenumbers.IsValidNumber(n) {
		return "", fmt.Errorf("%q is not a valid phone number", raw)
	}
	e := phonenumbers.Format(n, phonenumbers.E164)
	// NANP 555-0100 to 555-0199 is reserved for fiction: never a real person.
	if n.GetCountryCode() == 1 && len(e) == 12 && e[5:9] == "5550" && e[9] == '1' {
		return "", fmt.Errorf("%q is a fictional number", raw)
	}
	return e, nil
}

// Zone is the number's local time zone, or nil when it can't be told.
func Zone(e164 string) *time.Location {
	n, err := phonenumbers.Parse(e164, "US")
	if err != nil {
		return nil
	}
	zones, err := phonenumbers.GetTimezonesForNumber(n)
	if err != nil || len(zones) == 0 {
		return nil
	}
	loc, err := time.LoadLocation(zones[0])
	if err != nil || zones[0] == phonenumbers.UNKNOWN_TIMEZONE {
		return nil
	}
	return loc
}

// Callable: Monday to Friday, 8am to 6pm where the prospect is. A number
// whose zone is unknown is held to noon–4pm Eastern, which is inside
// business hours across the US.
func Callable(now time.Time, loc *time.Location) bool {
	if loc == nil {
		ny, _ := time.LoadLocation("America/New_York")
		t := now.In(ny)
		return weekday(t) && t.Hour() >= 12 && t.Hour() < 16
	}
	t := now.In(loc)
	return weekday(t) && t.Hour() >= 8 && t.Hour() < 18
}

func weekday(t time.Time) bool { return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday }

// ---- the queue --------------------------------------------------------------

// Prospect is one card in the queue.
type Prospect struct {
	ContactID   string     `json:"contactId"`
	Name        string     `json:"name"`
	Person      string     `json:"person"`
	Company     string     `json:"company"`
	Role        string     `json:"role"`
	Email       string     `json:"email"`
	Phone       string     `json:"phone"`
	LocalTime   string     `json:"localTime"`
	Zone        string     `json:"zone"`
	Status      string     `json:"status"`
	Likelihood  int        `json:"likelihood"`
	Attempts    int        `json:"attempts"`
	CallbackDue bool       `json:"callbackDue"`
	LastOutcome string     `json:"lastOutcome,omitempty"`
	LastCallAt  *time.Time `json:"lastCallAt,omitempty"`
	Brief       string     `json:"brief,omitempty"`
}

// candidates: contacts with a number, open in the pipeline, not on the
// do-not-call list, due, and not held by someone else.
const candidates = `
	SELECT c.id, c.name, COALESCE(c.person, ''), COALESCE(c.company, ''), COALESCE(c.role, ''), COALESCE(c.email, ''),
	       c.phone, c.status, c.likelihood, COALESCE(s.attempts, 0), s.next_call_at IS NOT NULL AND s.next_call_at <= $2,
	       COALESCE(last.outcome, ''), last.started_at, COALESCE(s.brief, '')
	FROM founderos_funnel_contacts c
	LEFT JOIN founderos_dial_state s ON s.contact_id = c.id
	LEFT JOIN LATERAL (SELECT outcome, started_at FROM founderos_calls k WHERE k.contact_id = c.id
	                   ORDER BY started_at DESC LIMIT 1) last ON true
	WHERE COALESCE(c.phone, '') <> ''
	  AND c.status IN ('first_touch', 'engaged', 'nurtured')
	  AND (s.next_call_at IS NULL OR s.next_call_at <= $2)
	  AND (s.claimed_until IS NULL OR s.claimed_until < $2 OR s.claimed_by = $1)
	ORDER BY (s.next_call_at IS NOT NULL AND s.next_call_at <= $2) DESC, c.likelihood DESC, COALESCE(s.attempts, 0), c.id
	LIMIT 400`

// Queue lists who can be rung right now, best first.
func Queue(ctx context.Context, pool *pgxpool.Pool, caller string, now time.Time, limit int) ([]Prospect, error) {
	dnc, err := dncSet(ctx, pool)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, candidates, caller, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Prospect{}
	for rows.Next() {
		var p Prospect
		var raw string
		if err := rows.Scan(&p.ContactID, &p.Name, &p.Person, &p.Company, &p.Role, &p.Email, &raw, &p.Status, &p.Likelihood,
			&p.Attempts, &p.CallbackDue, &p.LastOutcome, &p.LastCallAt, &p.Brief); err != nil {
			return nil, err
		}
		num, err := E164(raw)
		if err != nil || dnc[num] {
			continue
		}
		loc := Zone(num)
		if !Callable(now, loc) {
			continue
		}
		p.Phone = num
		if loc != nil {
			p.Zone = loc.String()
			p.LocalTime = now.In(loc).Format("15:04 Mon")
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func dncSet(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT number FROM founderos_dnc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		set[n] = true
	}
	return set, rows.Err()
}

// ClaimFor is how long a card stays with a caller before others may take it.
const ClaimFor = 10 * time.Minute

// ErrTaken: a teammate has this prospect.
var ErrTaken = errors.New("a teammate is calling this person")

// Claim holds a prospect for a caller.
func Claim(ctx context.Context, pool *pgxpool.Pool, contactID, caller string, now time.Time) error {
	tag, err := pool.Exec(ctx, `INSERT INTO founderos_dial_state (contact_id, claimed_by, claimed_until, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (contact_id) DO UPDATE SET claimed_by = $2, claimed_until = $3, updated_at = $4
		WHERE founderos_dial_state.claimed_until IS NULL OR founderos_dial_state.claimed_until < $4
		   OR founderos_dial_state.claimed_by = $2`, contactID, caller, now.Add(ClaimFor), now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaken
	}
	return nil
}

// Release lets a prospect go back to the queue untouched (a skip).
func Release(ctx context.Context, pool *pgxpool.Pool, contactID, caller string) error {
	_, err := pool.Exec(ctx, `UPDATE founderos_dial_state SET claimed_by = NULL, claimed_until = NULL, updated_at = now()
		WHERE contact_id = $1 AND claimed_by = $2`, contactID, caller)
	return err
}

// SetBrief keeps the 30-second brief for the card.
func SetBrief(ctx context.Context, pool *pgxpool.Pool, contactID, brief string) error {
	_, err := pool.Exec(ctx, `INSERT INTO founderos_dial_state (contact_id, brief) VALUES ($1, $2)
		ON CONFLICT (contact_id) DO UPDATE SET brief = $2, updated_at = now()`, contactID, brief)
	return err
}

// ---- calls ------------------------------------------------------------------

// Call is one placed call.
type Call struct {
	ID         string     `json:"id"`
	ContactID  string     `json:"contactId"`
	Caller     string     `json:"caller"`
	To         string     `json:"to"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	AnsweredAt *time.Time `json:"answeredAt,omitempty"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	DurationS  *int       `json:"durationS,omitempty"`
	Outcome    *string    `json:"outcome,omitempty"`
	Notes      *string    `json:"notes,omitempty"`
	Summary    *string    `json:"summary,omitempty"`
}

const callCols = `id, COALESCE(contact_id, ''), caller_email, to_number, status, started_at, answered_at, ended_at, duration_s, outcome, notes, summary`

func scanCall(row pgx.Row) (Call, error) {
	var c Call
	err := row.Scan(&c.ID, &c.ContactID, &c.Caller, &c.To, &c.Status, &c.StartedAt, &c.AnsweredAt, &c.EndedAt, &c.DurationS, &c.Outcome, &c.Notes, &c.Summary)
	return c, err
}

// ErrDNC: the number is on the do-not-call list.
var ErrDNC = errors.New("that number is on the do-not-call list")

// Start records a call about to be placed, after checking the claim, the
// do-not-call list and the prospect's hours.
func Start(ctx context.Context, pool *pgxpool.Pool, workspaceID, contactID, caller string, now time.Time) (Call, error) {
	var raw, claimedBy string
	err := pool.QueryRow(ctx, `SELECT COALESCE(c.phone, ''), COALESCE(s.claimed_by, '')
		FROM founderos_funnel_contacts c LEFT JOIN founderos_dial_state s ON s.contact_id = c.id WHERE c.id = $1`, contactID).Scan(&raw, &claimedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Call{}, errors.New("no such contact")
	}
	if err != nil {
		return Call{}, err
	}
	if claimedBy != caller {
		if err := Claim(ctx, pool, contactID, caller, now); err != nil {
			return Call{}, err
		}
	}
	to, err := E164(raw)
	if err != nil {
		return Call{}, err
	}
	var listed bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM founderos_dnc WHERE number = $1)`, to).Scan(&listed); err != nil {
		return Call{}, err
	}
	if listed {
		return Call{}, ErrDNC
	}
	if !Callable(now, Zone(to)) {
		return Call{}, errors.New("outside their calling hours (weekdays 8am–6pm their time)")
	}
	id := fmt.Sprintf("call-%d-%s", now.UnixNano(), randHex(4))
	return scanCall(pool.QueryRow(ctx, `INSERT INTO founderos_calls (id, workspace_id, contact_id, caller_email, to_number, started_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+callCols, id, workspaceID, contactID, caller, to, now))
}

// Get reads one call.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (Call, error) {
	return scanCall(pool.QueryRow(ctx, `SELECT `+callCols+` FROM founderos_calls WHERE id = $1`, id))
}

// Twilio's call states, in the order a call can move through them.
var progress = map[string]int{"dialing": 0, "queued": 1, "initiated": 1, "ringing": 2, "in-progress": 3, "answered": 3,
	"completed": 4, "busy": 4, "no-answer": 4, "failed": 4, "canceled": 4}

// Track records what Twilio says about the phone leg; it never moves a call
// backwards (callbacks can arrive out of order).
func Track(ctx context.Context, pool *pgxpool.Pool, id, sid, status string, durationS int, now time.Time) error {
	status = strings.ToLower(strings.TrimSpace(status))
	rank, known := progress[status]
	if !known {
		return nil
	}
	var cur string
	if err := pool.QueryRow(ctx, `SELECT status FROM founderos_calls WHERE id = $1`, id).Scan(&cur); err != nil {
		return err
	}
	if rank < progress[cur] {
		return nil
	}
	var dur any
	if rank == 4 && durationS >= 0 {
		dur = durationS
	}
	_, err := pool.Exec(ctx, `UPDATE founderos_calls SET status = $2,
		twilio_sid = COALESCE(twilio_sid, NULLIF($3, '')),
		answered_at = CASE WHEN $4 = 3 AND answered_at IS NULL THEN $5 ELSE answered_at END,
		ended_at = CASE WHEN $4 = 4 AND ended_at IS NULL THEN $5 ELSE ended_at END,
		duration_s = COALESCE($6, duration_s)
		WHERE id = $1`, id, status, sid, rank, now, dur)
	return err
}

// Outcomes a caller can pick, and what each does.
var Outcomes = map[string]struct {
	Label string
	Retry time.Duration // 0: not called again
	Stage string        // pipeline stage it moves the contact to ("" keeps it)
	Final bool
}{
	"meeting":        {"Meeting booked", 0, "engaged", true},
	"interested":     {"Interested, follow up", 0, "engaged", true},
	"callback":       {"Call back", 0, "", false},
	"gatekeeper":     {"Gatekeeper", 2 * 24 * time.Hour, "", false},
	"voicemail":      {"Left a voicemail", 2 * 24 * time.Hour, "", false},
	"no_answer":      {"No answer", 24 * time.Hour, "", false},
	"not_interested": {"Not interested", 0, "", true},
	"wrong_number":   {"Wrong number", 0, "", true},
	"do_not_call":    {"Asked not to be called", 0, "", true},
}

// MaxAttempts: after this many unanswered tries a prospect leaves the queue.
const MaxAttempts = 6

// Log records the caller's outcome and moves the pipeline on.
func Log(ctx context.Context, pool *pgxpool.Pool, callID, caller, outcome, notes string, callbackAt *time.Time, now time.Time) (Call, error) {
	o, ok := Outcomes[outcome]
	if !ok {
		return Call{}, fmt.Errorf("unknown outcome %q", outcome)
	}
	if outcome == "callback" && (callbackAt == nil || callbackAt.Before(now)) {
		return Call{}, errors.New("pick when to call back")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Call{}, err
	}
	defer tx.Rollback(ctx)
	c, err := scanCall(tx.QueryRow(ctx, `UPDATE founderos_calls SET outcome = $3, notes = NULLIF($4, ''), callback_at = $5,
		ended_at = COALESCE(ended_at, $6) WHERE id = $1 AND caller_email = $2 RETURNING `+callCols, callID, caller, outcome, strings.TrimSpace(notes), callbackAt, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return Call{}, errors.New("not your call, or no such call")
	}
	if err != nil {
		return Call{}, err
	}
	var next any
	switch {
	case outcome == "callback":
		next = *callbackAt
	case o.Final:
		next = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC) // never again
	default:
		next = now.Add(o.Retry)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO founderos_dial_state (contact_id, attempts, next_call_at, updated_at) VALUES ($1, 1, $2, $3)
		ON CONFLICT (contact_id) DO UPDATE SET attempts = founderos_dial_state.attempts + 1, next_call_at = $2,
		  claimed_by = NULL, claimed_until = NULL, updated_at = $3`, c.ContactID, next, now); err != nil {
		return Call{}, err
	}
	// Out of tries: off the queue.
	if _, err := tx.Exec(ctx, `UPDATE founderos_dial_state SET next_call_at = '9999-01-01'
		WHERE contact_id = $1 AND attempts >= $2 AND $3`, c.ContactID, MaxAttempts, !o.Final && outcome != "callback"); err != nil {
		return Call{}, err
	}
	if outcome == "do_not_call" || outcome == "wrong_number" {
		if _, err := tx.Exec(ctx, `INSERT INTO founderos_dnc (number, reason, added_by) VALUES ($1, $2, $3) ON CONFLICT (number) DO NOTHING`,
			c.To, o.Label, caller); err != nil {
			return Call{}, err
		}
	}
	if o.Stage != "" {
		if _, err := tx.Exec(ctx, `UPDATE founderos_funnel_contacts SET status = $2 WHERE id = $1 AND status = 'first_touch'`, c.ContactID, o.Stage); err != nil {
			return Call{}, err
		}
	}
	label := "Call: " + o.Label
	if n := strings.TrimSpace(notes); n != "" {
		label += " — " + n
	}
	if r := []rune(label); len(r) > 200 {
		label = string(r[:200])
	}
	if _, err := tx.Exec(ctx, `INSERT INTO founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at)
		SELECT c.id || '-call-' || (COALESCE(MAX(t.seq), 0) + 1), c.workspace_id, c.id, COALESCE(MAX(t.seq), 0) + 1,
		       c.status, 'call', $2, 'manual', CURRENT_DATE
		FROM founderos_funnel_contacts c LEFT JOIN founderos_funnel_touches t ON t.contact_id = c.id
		WHERE c.id = $1 GROUP BY c.id, c.workspace_id, c.status`, c.ContactID, label); err != nil {
		return Call{}, err
	}
	return c, tx.Commit(ctx)
}

// SetSummary keeps the tidied notes.
func SetSummary(ctx context.Context, pool *pgxpool.Pool, callID, summary string) error {
	_, err := pool.Exec(ctx, `UPDATE founderos_calls SET summary = $2 WHERE id = $1`, callID, summary)
	return err
}

// AddDNC puts a number on the do-not-call list.
func AddDNC(ctx context.Context, pool *pgxpool.Pool, raw, reason, by string) (string, error) {
	n, err := E164(raw)
	if err != nil {
		return "", err
	}
	_, err = pool.Exec(ctx, `INSERT INTO founderos_dnc (number, reason, added_by) VALUES ($1, $2, $3)
		ON CONFLICT (number) DO UPDATE SET reason = $2`, n, reason, by)
	return n, err
}

// ---- stats ------------------------------------------------------------------

// Stats is a caller's day.
type Stats struct {
	Dials    int `json:"dials"`
	Connects int `json:"connects"`
	Meetings int `json:"meetings"`
	TalkS    int `json:"talkSeconds"`
}

// Today counts a caller's calls since midnight (in loc).
func Today(ctx context.Context, pool *pgxpool.Pool, caller string, now time.Time, loc *time.Location) (Stats, error) {
	y, m, d := now.In(loc).Date()
	since := time.Date(y, m, d, 0, 0, 0, 0, loc)
	var s Stats
	err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE answered_at IS NOT NULL),
		count(*) FILTER (WHERE outcome = 'meeting'), COALESCE(sum(duration_s) FILTER (WHERE answered_at IS NOT NULL), 0)
		FROM founderos_calls WHERE caller_email = $1 AND started_at >= $2 AND started_at < $3`, caller, since, since.AddDate(0, 0, 1)).Scan(&s.Dials, &s.Connects, &s.Meetings, &s.TalkS)
	return s, err
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
