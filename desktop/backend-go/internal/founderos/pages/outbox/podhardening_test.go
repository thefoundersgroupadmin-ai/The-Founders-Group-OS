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
	for _, bad := range []string{"Brightvale; we guarantee £100k", "Acme! Best price", "a\nb", "ACME — 3x pipeline guaranteed", "", "Visit https://x.example",
		"Acme. We triple your revenue"} {
		if validField(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	// A company must match the recipient's domain.
	if !companyMatchesDomain("Brightvale Logistics", "brightvale.example") || companyMatchesDomain("Acme", "brightvale.example") {
		t.Error("company/domain anchoring wrong")
	}
}

// Codex third review: an approval is bound to the sender too.
func TestChangingTheSenderNeedsTheEmailApprovedAgain(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'client_approves_each';
		INSERT INTO founderos_client_approvals (pod_id, kind, ref, title, payload, status)
		SELECT id, 'target_list', 'l', 'L', jsonb_build_object('domains', '["brightvale.example"]'::jsonb), 'approved' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	file("sx-1", "priya@brightvale.example")
	got, send := recorder()
	_, _ = Approve(ctx, pool, "client-bv", "sx-1", podReviewed, send)
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_approvals SET status = 'approved' WHERE kind = 'message';
		UPDATE founderos_client_pods SET sender_email = 'other@bv-mail.example'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "client-bv", "sx-1", podReviewed, send); !errors.Is(err, ErrAwaitingClient) || len(*got) != 0 {
		t.Fatalf("old approval used after the sender changed: %v", err)
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

// M3: a conversation is answered any time of day; with inbound auto-reply on,
// it does not wait for the client's approval (every other check still applies).
func TestConversationRepliesIgnoreHoursAndMaySkipClientApproval(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'client_approves_each', inbound_auto_reply = true,
		send_window_start = 9, send_window_end = 10, send_weekends = false;
		INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email)
		SELECT 'ct-q', workspace_id, 'Tom', 'engaged', 'client', now(), 'tom@harbour.example' FROM founderos_client_pods;
		INSERT INTO founderos_pod_conversations (issue_id, workspace_id, contact_id) SELECT 'iss-q', workspace_id, 'ct-q' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	prev := nowFunc
	t.Cleanup(func() { nowFunc = prev })
	nowFunc = func() time.Time { return time.Date(2026, 11, 8, 23, 30, 0, 0, time.UTC) } // Sunday night
	file("q-1", "tom@harbour.example")
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sequence_key = 'reply', contact_id = 'ct-q' WHERE id = 'q-1'`); err != nil {
		t.Fatal(err)
	}
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "q-1", Content{Subject: "Re: two warehouses", Body: "Yes, we do. Sam"}, send); err != nil || len(*got) != 1 {
		t.Fatalf("Sunday-night reply blocked: %v", err)
	}
	// Outreach still keeps to the window and the client's approval.
	file("q-2", "jo@oakfield.example")
	if _, err := Approve(ctx, pool, "client-bv", "q-2", podReviewed, send); err == nil || len(*got) != 1 {
		t.Fatalf("outreach sent out of hours: %v", err)
	}
}

// Codex M2-M4 review #2/#3: once a person takes over, the AI stops replying;
// a reply goes only to the person who wrote in; auto-reply without client
// approval applies only to verified (email) conversations, not form submissions.
func TestTakeoverStopsRepliesAndFormConversationsNeedApproval(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'client_approves_each', inbound_auto_reply = true;
		INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email)
		SELECT 'ct-f', workspace_id, 'Tom', 'engaged', 'client', now(), 'tom@harbour.example' FROM founderos_client_pods;
		INSERT INTO founderos_pod_conversations (issue_id, workspace_id, contact_id, source)
		SELECT 'iss-f', workspace_id, 'ct-f', 'form' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	reply := func(id, to string) {
		file(id, to)
		if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sequence_key = 'reply', contact_id = 'ct-f' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	got, send := recorder()
	reply("f-1", "tom@harbour.example")
	if _, err := Approve(ctx, pool, "client-bv", "f-1", Content{Subject: "Re", Body: "Hello Tom. Sam"}, send); !errors.Is(err, ErrAwaitingClient) || len(*got) != 0 {
		t.Fatalf("form conversation auto-replied without approval: %v", err)
	}
	reply("f-2", "victim@elsewhere.example")
	if _, err := Approve(ctx, pool, "client-bv", "f-2", Content{Subject: "Re", Body: "Hi. Sam"}, send); err == nil || len(*got) != 0 {
		t.Fatalf("a reply went to someone who never wrote in: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE founderos_pod_conversations SET source = 'email', taken_over_at = now()`); err != nil {
		t.Fatal(err)
	}
	reply("f-3", "tom@harbour.example")
	if _, err := Approve(ctx, pool, "client-bv", "f-3", Content{Subject: "Re", Body: "More. Sam"}, send); !errors.Is(err, ErrHeld) || len(*got) != 0 {
		t.Fatalf("AI replied after a person took over: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE founderos_pod_conversations SET taken_over_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "client-bv", "f-3", Content{Subject: "Re", Body: "More. Sam"}, send); err != nil || len(*got) != 1 {
		t.Fatalf("verified conversation auto-reply blocked: %v", err)
	}
}
