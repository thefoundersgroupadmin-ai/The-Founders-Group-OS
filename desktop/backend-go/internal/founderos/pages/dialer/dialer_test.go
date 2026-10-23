package dialer

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func contact(t *testing.T, pool *pgxpool.Pool, ws, id, phone, status string, likelihood int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO founderos_funnel_contacts
		(id, workspace_id, name, venture, status, likelihood, phone, company, person, created_at)
		VALUES ($1, $2, $1, 'vantage', $3, $4, $5, 'Co '||$1, 'Pat '||$1, now())`, id, ws, status, likelihood, phone); err != nil {
		t.Fatal(err)
	}
}

func ids(ps []Prospect) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.ContactID)
	}
	return out
}

func TestNumbersAndHours(t *testing.T) {
	if n, err := E164("(415) 555-0132"); err == nil || n != "" {
		// 555-01xx is a fictional range: not a valid number
		t.Fatalf("fictional number accepted: %q", n)
	}
	n, err := E164("(212) 736-5000")
	if err != nil || n != "+12127365000" {
		t.Fatalf("%q %v", n, err)
	}
	ny := Zone(n)
	if ny == nil || ny.String() != "America/New_York" {
		t.Fatalf("zone %v", ny)
	}
	sf := Zone("+14155992671")
	if sf == nil || sf.String() != "America/Los_Angeles" {
		t.Fatalf("zone %v", sf)
	}
	// Thursday 23 Oct 2026, 14:00 UTC = 10:00 New York, 07:00 San Francisco.
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	if !Callable(at, ny) || Callable(at, sf) {
		t.Fatal("10am NY is callable, 7am SF is not")
	}
	if Callable(time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC), ny) {
		t.Fatal("Saturday is not callable")
	}
	if !Callable(time.Date(2026, 10, 22, 17, 0, 0, 0, time.UTC), nil) || Callable(at, nil) {
		t.Fatal("an unknown zone only gets noon–4pm Eastern")
	}
}

func TestTheQueueSkipsWhatShouldNotBeRung(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "vantage")
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC) // 11:00 NY, 08:00 SF
	contact(t, pool, ws, "hot", "+12127365000", "first_touch", 90)
	contact(t, pool, ws, "warm", "+12125550199", "first_touch", 60) // fictional: invalid
	contact(t, pool, ws, "sf", "+14155992671", "engaged", 70)
	contact(t, pool, ws, "nophone", "", "first_touch", 99)
	contact(t, pool, ws, "won", "+12127365001", "converted", 99)
	contact(t, pool, ws, "dnc", "+12127365002", "first_touch", 95)
	contact(t, pool, ws, "theirs", "+12127365003", "first_touch", 95)
	contact(t, pool, ws, "later", "+12127365004", "first_touch", 95)
	if _, err := AddDNC(ctx, pool, "+12127365002", "asked", "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := Claim(ctx, pool, "theirs", "karim@x", at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_dial_state (contact_id, next_call_at) VALUES ('later', $1)`, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	q, err := Queue(ctx, pool, "nader@x", at, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(q); len(got) != 2 || got[0] != "hot" || got[1] != "sf" {
		t.Fatalf("queue %v", got)
	}
	if q[0].Phone != "+12127365000" || q[0].LocalTime != "11:00 Thu" || q[0].Zone != "America/New_York" {
		t.Fatalf("card %+v", q[0])
	}
	// The teammate's claim keeps it out of my queue, but not theirs.
	q, _ = Queue(ctx, pool, "karim@x", at, 10)
	if got := ids(q); len(got) != 3 || got[0] != "theirs" && got[1] != "theirs" {
		t.Fatalf("karim's queue %v", got)
	}
	if err := Claim(ctx, pool, "theirs", "nader@x", at); err != ErrTaken {
		t.Fatalf("claimed a teammate's prospect: %v", err)
	}
	// A claim runs out.
	if err := Claim(ctx, pool, "theirs", "nader@x", at.Add(ClaimFor+time.Second)); err != nil {
		t.Fatalf("expired claim: %v", err)
	}
}

func TestOutcomesMoveThePipeline(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "vantage")
	fos := catalogdb.Workspace(t, pool, "founderos")
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	contact(t, pool, ws, "a", "+12127365000", "first_touch", 90)
	contact(t, pool, ws, "b", "+12127365001", "first_touch", 80)
	contact(t, pool, ws, "c", "+12127365002", "first_touch", 70)

	call, err := Start(ctx, pool, fos, "a", "nader@x", at)
	if err != nil || call.To != "+12127365000" || call.Status != "dialing" {
		t.Fatalf("%+v %v", call, err)
	}
	if err := Track(ctx, pool, call.ID, "CA1", "in-progress", 0, at.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := Track(ctx, pool, call.ID, "CA1", "completed", 95, at.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := Track(ctx, pool, call.ID, "CA1", "ringing", 0, at.Add(101*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _ := Get(ctx, pool, call.ID)
	if got.Status != "completed" || got.DurationS == nil || *got.DurationS != 95 || got.AnsweredAt == nil {
		t.Fatalf("tracked %+v", got)
	}
	if _, err := Log(ctx, pool, call.ID, "karim@x", "meeting", "", nil, at); err == nil {
		t.Fatal("logged someone else's call")
	}
	if _, err := Log(ctx, pool, call.ID, "nader@x", "meeting", "Booked Tue 2pm with the CEO", nil, at); err != nil {
		t.Fatal(err)
	}
	var stage, touch string
	_ = pool.QueryRow(ctx, `SELECT status FROM founderos_funnel_contacts WHERE id = 'a'`).Scan(&stage)
	_ = pool.QueryRow(ctx, `SELECT label FROM founderos_funnel_touches WHERE contact_id = 'a' AND channel = 'call'`).Scan(&touch)
	if stage != "engaged" || touch != "Call: Meeting booked — Booked Tue 2pm with the CEO" {
		t.Fatalf("pipeline %q %q", stage, touch)
	}

	// Do not call: on the list, never queued again.
	c2, _ := Start(ctx, pool, fos, "b", "nader@x", at)
	if _, err := Log(ctx, pool, c2.ID, "nader@x", "do_not_call", "", nil, at); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, pool, fos, "b", "nader@x", at.Add(time.Hour)); err != ErrDNC {
		t.Fatalf("dialled a do-not-call number: %v", err)
	}

	// No answer comes back tomorrow; a callback at its time.
	c3, _ := Start(ctx, pool, fos, "c", "nader@x", at)
	if _, err := Log(ctx, pool, c3.ID, "nader@x", "no_answer", "", nil, at); err != nil {
		t.Fatal(err)
	}
	if q, _ := Queue(ctx, pool, "nader@x", at.Add(time.Hour), 10); len(q) != 0 {
		t.Fatalf("no-answer came straight back: %v", ids(q))
	}
	if q, _ := Queue(ctx, pool, "nader@x", at.Add(24*time.Hour), 10); len(ids(q)) != 1 || ids(q)[0] != "c" {
		t.Fatalf("no-answer not back the next day: %v", ids(q))
	}
	c4, _ := Start(ctx, pool, fos, "c", "nader@x", at.Add(24*time.Hour))
	if _, err := Log(ctx, pool, c4.ID, "nader@x", "callback", "", nil, at); err == nil {
		t.Fatal("a callback needs a time")
	}
	when := at.Add(27 * time.Hour) // Friday 2pm in New York
	if _, err := Log(ctx, pool, c4.ID, "nader@x", "callback", "after the board meeting", &when, at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	q, _ := Queue(ctx, pool, "nader@x", when, 10)
	if len(q) != 1 || !q[0].CallbackDue || q[0].LastOutcome != "callback" {
		t.Fatalf("callback card %+v", q)
	}
	s, _ := Today(ctx, pool, "nader@x", at.Add(time.Minute), time.UTC)
	if s.Dials != 3 || s.Connects != 1 || s.Meetings != 1 || s.TalkS != 95 {
		t.Fatalf("stats %+v", s)
	}
}
