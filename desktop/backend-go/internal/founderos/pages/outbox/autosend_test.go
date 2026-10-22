package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// row inserts an outbox row to `to`; sentAgo > 0 makes it a past send.
func row(t *testing.T, pool *pgxpool.Pool, ws, id, to, contact, status string, sentAgo time.Duration) {
	t.Helper()
	var sentAt any
	if sentAgo > 0 {
		sentAt = time.Now().Add(-sentAgo)
	}
	var contactID any
	if contact != "" {
		contactID = contact
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO founderos_outbox
		(id, workspace_id, source, source_ref, contact_id, to_address, subject, body, status, sent_at)
		VALUES ($1, $2, 'paperclip', 'THE-20', $3, $4, 'Following up', 'Body', $5, $6)`,
		id, ws, contactID, to, status, sentAt); err != nil {
		t.Fatal(err)
	}
}

func contact(t *testing.T, pool *pgxpool.Pool, ws, id, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, created_at)
		VALUES ($1, $2, $1, 'vantage', $3, now())`, id, ws, status); err != nil {
		t.Fatal(err)
	}
}

const day = 24 * time.Hour

func TestAutoSendSendsOnlyFollowUpsThatAreSafeToSend(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	venture := catalogdb.Workspace(t, pool, "vantage")
	contact(t, pool, venture, "fc-quiet", "first_touch")
	contact(t, pool, venture, "fc-replied", "engaged")

	// Due: one earlier send 3 days ago, no reply.
	row(t, pool, ws, "sent-a", "a@quiet.example", "fc-quiet", "sent", 3*day)
	row(t, pool, ws, "due", "a@quiet.example", "fc-quiet", "pending", 0)
	// First contact: never emailed before.
	row(t, pool, ws, "first", "new@quiet.example", "fc-quiet", "pending", 0)
	// They replied.
	row(t, pool, ws, "sent-b", "b@replied.example", "fc-replied", "sent", 3*day)
	row(t, pool, ws, "replied", "b@replied.example", "fc-replied", "pending", 0)
	// Last send was yesterday: too soon.
	row(t, pool, ws, "sent-c", "c@quiet.example", "fc-quiet", "sent", 1*day)
	row(t, pool, ws, "soon", "c@quiet.example", "fc-quiet", "pending", 0)
	// Already a first email and two follow-ups.
	for _, id := range []string{"d1", "d2", "d3"} {
		row(t, pool, ws, id, "d@quiet.example", "fc-quiet", "sent", 5*day)
	}
	row(t, pool, ws, "fourth", "d@quiet.example", "fc-quiet", "pending", 0)
	// The owner rejected an email to this address once.
	row(t, pool, ws, "sent-e", "e@quiet.example", "fc-quiet", "sent", 4*day)
	row(t, pool, ws, "rej-e", "e@quiet.example", "fc-quiet", "rejected", 0)
	row(t, pool, ws, "after-reject", "e@quiet.example", "fc-quiet", "pending", 0)
	// Not linked to a pipeline contact, so a reply could not be seen.
	row(t, pool, ws, "sent-f", "f@unknown.example", "", "sent", 3*day)
	row(t, pool, ws, "unlinked", "f@unknown.example", "", "pending", 0)

	var sentTo []string
	send := func(_ context.Context, it Item) error { sentTo = append(sentTo, it.ID); return nil }
	n, err := AutoSendFollowUps(ctx, pool, "founderos", 10, send)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(sentTo) != 1 || sentTo[0] != "due" {
		t.Fatalf("sent %d: %v, want only [due]", n, sentTo)
	}
	for _, id := range []string{"first", "replied", "soon", "fourth", "after-reject", "unlinked"} {
		if s := status(t, pool, id); s != "pending" {
			t.Errorf("%s: %s, want pending", id, s)
		}
	}
	if s := status(t, pool, "due"); s != "sent" {
		t.Fatalf("due: %s", s)
	}
}

func TestAutoSendStopsAtTheDailyCap(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	venture := catalogdb.Workspace(t, pool, "vantage")
	contact(t, pool, venture, "fc-quiet", "first_touch")
	for _, x := range []string{"a", "b", "c"} {
		row(t, pool, ws, "sent-"+x, x+"@quiet.example", "fc-quiet", "sent", 3*day)
		row(t, pool, ws, "due-"+x, x+"@quiet.example", "fc-quiet", "pending", 0)
	}
	ok := func(context.Context, Item) error { return nil }
	if n, err := AutoSendFollowUps(ctx, pool, "founderos", 2, ok); err != nil || n != 2 {
		t.Fatalf("first pass: %d, %v; want 2", n, err)
	}
	// The two sent today count against the cap on the next pass.
	if n, err := AutoSendFollowUps(ctx, pool, "founderos", 2, ok); err != nil || n != 0 {
		t.Fatalf("second pass: %d, %v; want 0", n, err)
	}
}
