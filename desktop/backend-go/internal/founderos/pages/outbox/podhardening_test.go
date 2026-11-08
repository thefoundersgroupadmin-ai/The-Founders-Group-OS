package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// Codex fix review #3: the exact same email never goes to a person twice.
func TestTheSameEmailNeverGoesToAPersonTwice(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	file("d-0", "priya@brightvale.example")
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "d-0", podReviewed, send); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sent_at = now() - interval '3 days' WHERE id = 'd-0'`); err != nil {
		t.Fatal(err)
	}
	file("d-1", "priya@brightvale.example")
	if _, err := Approve(ctx, pool, "client-bv", "d-1", podReviewed, send); !errors.Is(err, ErrDuplicate) || len(*got) != 1 {
		t.Fatalf("identical email sent again: %v %d", err, len(*got))
	}
}

// Codex fix review #5: typed fields must look like a name or a company, so a
// claim stored in the contact record cannot ride in through {company}.
func TestTemplateFieldsMustLookLikeNames(t *testing.T) {
	for _, ok := range []string{"Brightvale", "Brightvale Logistics Ltd.", "O'Neill & Sons (UK)", "Zoë"} {
		if !validField(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"Brightvale; we guarantee £100k", "Acme! Best price", "a\nb", "ACME — 3x pipeline guaranteed", "", "Visit https://x.example"} {
		if validField(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Codex fix review #9: dismissing an uncertain email keeps it on the cap and
// keeps its step, because it may have arrived.
func TestDismissingAnUncertainEmailKeepsItsCapAndStep(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET first_email_daily_cap = 1`); err != nil {
		t.Fatal(err)
	}
	file("m-1", "a@one.example")
	sendErr := func(context.Context, Item) error { return ErrUncertain }
	if _, err := Approve(ctx, pool, "client-bv", "m-1", podReviewed, sendErr); !errors.Is(err, ErrUncertain) {
		t.Fatalf("want uncertain: %v", err)
	}
	if _, err := Reject(ctx, pool, "client-bv", "m-1"); err != nil {
		t.Fatal(err)
	}
	var reserved int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM founderos_send_steps WHERE outbox_id = 'm-1'`).Scan(&reserved)
	if reserved != 1 {
		t.Fatal("a maybe-delivered email lost its step reservation")
	}
	file("m-2", "b@two.example")
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "m-2", podReviewed, send); !errors.Is(err, ErrHeld) || len(*got) != 0 {
		t.Fatalf("cap forgot a maybe-delivered email: %v", err)
	}
}

// Codex fix review #8: a conversation reply needs the prospect's recorded
// reply, and may answer an inbound message with no earlier outbound email.
func TestAConversationReplyNeedsARecordedConversation(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email)
		SELECT 'ct-i', workspace_id, 'Tom', 'engaged', 'client', now(), 'tom@harbour.example' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	file("c-1", "tom@harbour.example")
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sequence_key = 'reply', contact_id = 'ct-i' WHERE id = 'c-1'`); err != nil {
		t.Fatal(err)
	}
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "c-1", podReviewed, send); !errors.Is(err, ErrHeld) || len(*got) != 0 {
		t.Fatalf("a 'reply' with no recorded conversation was sent: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_pod_conversations (issue_id, workspace_id, contact_id)
		SELECT 'iss-1', workspace_id, 'ct-i' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "client-bv", "c-1", podReviewed, send); err != nil || len(*got) != 1 {
		t.Fatalf("an inbound conversation's reply was blocked: %v", err)
	}
}

// Codex fix review #4: the sending window is checked again at dispatch.
func TestTheWindowIsCheckedAgainAtDispatch(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	file("w-1", "priya@brightvale.example")
	prev := beforeDispatch
	t.Cleanup(func() { beforeDispatch = prev })
	beforeDispatch = func() {
		if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET send_window_start = 0, send_window_end = 1, send_weekends = false`); err != nil {
			t.Error(err)
		}
	}
	prevNow := nowFunc
	t.Cleanup(func() { nowFunc = prevNow })
	nowFunc = func() time.Time { return time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC) }
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "w-1", podReviewed, send); !errors.Is(err, ErrHeld) || len(*got) != 0 {
		t.Fatalf("sent outside the window set before dispatch: %v", err)
	}
}
