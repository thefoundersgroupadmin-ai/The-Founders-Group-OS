package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// Codex M1 review #3: one first email and at most two follow-ups per person,
// at least two days apart, whatever drafts exist.
func TestAPersonGetsOneFirstEmailAndAtMostTwoSpacedFollowUps(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	got, send := recorder()
	age := func(days int) {
		if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sent_at = now() - make_interval(days => $1) WHERE status = 'sent'`, days); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"s-0", "s-1", "s-2", "s-3"} {
		file(id, "priya@brightvale.example")
		if i > 0 && i < 3 {
			if _, err := Approve(ctx, pool, "client-bv", id, podReviewed, send); !errors.Is(err, ErrHeld) {
				t.Fatalf("%s: sent too soon after the last email: %v", id, err)
			}
			age(3)
		}
		_, err := Approve(ctx, pool, "client-bv", id, podReviewed, send)
		if i < 3 && err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if i == 3 && (!errors.Is(err, ErrSequenceDone) || status(t, pool, id) != "rejected") {
			t.Fatalf("a third follow-up: %v %s", err, status(t, pool, id))
		}
	}
	if len(*got) != 3 {
		t.Fatalf("sent %d, want first + 2 follow-ups", len(*got))
	}
}

// Codex M1 review #9: an email whose delivery is uncertain may have arrived,
// so it counts against the cap.
func TestUncertainDeliveriesCountAgainstTheCap(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET first_email_daily_cap = 1`); err != nil {
		t.Fatal(err)
	}
	file("u-1", "a@one.example")
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET status = 'uncertain', decided_at = now() WHERE id = 'u-1'`); err != nil {
		t.Fatal(err)
	}
	file("u-2", "b@two.example")
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "u-2", podReviewed, send); !errors.Is(err, ErrHeld) || len(*got) != 0 {
		t.Fatalf("cap ignored an uncertain delivery: %v", err)
	}
}

// Codex M1 review #8: a reply in a conversation the prospect started is not a
// follow-up; once approved it goes out.
func TestAnApprovedConversationReplyGoesOutAfterTheProspectWroteBack(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email)
		SELECT 'ct-r', workspace_id, 'Priya', 'engaged', 'client', now(), 'priya@brightvale.example' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	file("r-0", "priya@brightvale.example")
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET status = 'sent', sent_at = now() - interval '1 hour', contact_id = 'ct-r'`); err != nil {
		t.Fatal(err)
	}
	file("r-1", "priya@brightvale.example")
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sequence_key = 'reply', contact_id = 'ct-r' WHERE id = 'r-1'`); err != nil {
		t.Fatal(err)
	}
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "r-1", podReviewed, send); err != nil || len(*got) != 1 {
		t.Fatalf("approved reply blocked: %v", err)
	}
}

// Codex M1 review #4: a pause that lands after the checks but before the
// mail server is reached stops the send.
func TestAPauseBeforeDispatchStopsTheSend(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	file("p-1", "priya@brightvale.example")
	prev := beforeDispatch
	t.Cleanup(func() { beforeDispatch = prev })
	beforeDispatch = func() {
		if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET status = 'paused', paused_reason = 'client asked'`); err != nil {
			t.Error(err)
		}
	}
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "p-1", podReviewed, send); !errors.Is(err, ErrHeld) || len(*got) != 0 {
		t.Fatalf("sent after a pause: %v %d", err, len(*got))
	}
	if status(t, pool, "p-1") != "pending" {
		t.Fatalf("row left %s", status(t, pool, "p-1"))
	}
	var reserved int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM founderos_send_steps WHERE outbox_id = 'p-1'`).Scan(&reserved)
	if reserved != 0 {
		t.Fatal("a stopped send kept its step reservation")
	}
	_ = time.Second
}
