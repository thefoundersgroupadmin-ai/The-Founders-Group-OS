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
