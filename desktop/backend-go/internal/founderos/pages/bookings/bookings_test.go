package bookings

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/caldotcom"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestABookedCallMovesTheContactOnOnce(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "vantage")
	for _, c := range []struct{ id, email, status string }{
		{"fc-pc-basalt", "ben@basalt.health", "first_touch"},
		{"fc-pc-won", "amy@won.example", "converted"},
		{"fc-pc-quiet", "q@quiet.example", "first_touch"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, email, created_at)
			VALUES ($1, $2, $1, 'vantage', $3, $4, now())`, c.id, ws, c.status, c.email); err != nil {
			t.Fatal(err)
		}
	}
	list := []caldotcom.Booking{
		{UID: "b1", Title: "Discovery call", Status: "accepted", Start: "2026-10-27T15:00:00Z",
			Attendees: []caldotcom.Attendee{{Email: "ben@basalt.health"}, {Email: "nader@thefoundersgroup.net"}}},
		{UID: "b2", Title: "Kickoff", Status: "accepted", Start: "2026-10-28T15:00:00Z", Attendees: []caldotcom.Attendee{{Email: "amy@won.example"}}},
		{UID: "b3", Title: "Cancelled", Status: "cancelled", Start: "2026-10-29T15:00:00Z", Attendees: []caldotcom.Attendee{{Email: "q@quiet.example"}}},
	}
	for i := 0; i < 2; i++ { // the second pass changes nothing
		if _, err := MarkBooked(ctx, pool, list); err != nil {
			t.Fatal(err)
		}
	}
	status := func(id string) string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM founderos_funnel_contacts WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status("fc-pc-basalt"); s != "engaged" {
		t.Fatalf("basalt = %s, want engaged", s)
	}
	if s := status("fc-pc-won"); s != "converted" {
		t.Fatalf("a later stage must not move back: %s", s)
	}
	if s := status("fc-pc-quiet"); s != "first_touch" {
		t.Fatalf("a cancelled booking must not count: %s", s)
	}
	var n int
	var label, at string
	if err := pool.QueryRow(ctx, `SELECT count(*), max(label), max(at::text) FROM founderos_funnel_touches WHERE contact_id = 'fc-pc-basalt'`).Scan(&n, &label, &at); err != nil {
		t.Fatal(err)
	}
	if n != 1 || label != "Booked on Cal.com: Discovery call" || at != "2026-10-27" {
		t.Fatalf("touches = %d %q %s", n, label, at)
	}
}

// Codex review, 8 Nov 2026: TFG's own calendar must never move a client pod's
// contacts, and a pod's calendar moves only that pod's.
func TestBookingsStayInsideTheirOwnWorkspaces(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	hq := catalogdb.Workspace(t, pool, "vantage")
	podA := catalogdb.Workspace(t, pool, "client-alpha")
	podB := catalogdb.Workspace(t, pool, "client-beta")
	for _, w := range []struct{ ws, slug string }{{podA, "client-alpha"}, {podB, "client-beta"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_client_pods (workspace_id, slug, client_name) VALUES ($1, $2, $2)`, w.ws, w.slug); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ id, ws, venture string }{{"c-hq", hq, "vantage"}, {"c-a", podA, "client"}, {"c-b", podB, "client"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, email, created_at)
			VALUES ($1, $2, $1, $3, 'first_touch', 'priya@brightvale.example', now())`, c.id, c.ws, c.venture); err != nil {
			t.Fatal(err)
		}
	}
	list := []caldotcom.Booking{{UID: "b1", Title: "Call", Status: "accepted", Start: "2026-11-09T15:00:00Z",
		Attendees: []caldotcom.Attendee{{Email: "priya@brightvale.example"}}}}
	if n, err := MarkBooked(ctx, pool, list); err != nil || n != 1 {
		t.Fatalf("HQ calendar moved %d (%v)", n, err)
	}
	if n, err := MarkBookedIn(ctx, pool, podA, list); err != nil || n != 1 {
		t.Fatalf("pod A calendar moved %d (%v)", n, err)
	}
	st := func(id string) (s string) {
		_ = pool.QueryRow(ctx, `SELECT status FROM founderos_funnel_contacts WHERE id = $1`, id).Scan(&s)
		return
	}
	if st("c-hq") != "engaged" || st("c-a") != "engaged" || st("c-b") != "first_touch" {
		t.Fatalf("hq=%s a=%s b=%s", st("c-hq"), st("c-a"), st("c-b"))
	}
}
