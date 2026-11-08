package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func approval(t *testing.T, pool *pgxpool.Pool, ref string) (status string) {
	t.Helper()
	_ = pool.QueryRow(context.Background(), `SELECT status FROM founderos_client_approvals WHERE ref = $1`, ref).Scan(&status)
	return status
}

func approveAsClient(t *testing.T, pool *pgxpool.Pool, ref string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE founderos_client_approvals SET status = 'approved',
		decided_at = now(), decided_by = 'client:priya' WHERE ref = $1`, ref); err != nil {
		t.Fatal(err)
	}
}

func targetList(t *testing.T, pool *pgxpool.Pool, domains string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO founderos_client_approvals (pod_id, kind, ref, title, payload, status)
		SELECT id, 'target_list', 'list-1', 'Multi-site warehouses', jsonb_build_object('domains', $1::jsonb), 'approved'
		FROM founderos_client_pods`, domains); err != nil {
		t.Fatal(err)
	}
}

func TestEachMessageWaitsForTheClientAndAnEditNeedsApprovalAgain(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'client_approves_each'`); err != nil {
		t.Fatal(err)
	}
	targetList(t, pool, `["brightvale.example"]`)
	file("m-1", "priya@brightvale.example")
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "m-1", podReviewed, send); !errors.Is(err, ErrAwaitingClient) || len(*got) != 0 {
		t.Fatalf("sent before client approval: %v", err)
	}
	if approval(t, pool, "m-1") != "pending" {
		t.Fatalf("no approval request filed: %q", approval(t, pool, "m-1"))
	}
	approveAsClient(t, pool, "m-1")
	edited := Content{Subject: podReviewed.Subject, Body: podReviewed.Body + " (edited)"}
	if _, err := Approve(ctx, pool, "client-bv", "m-1", edited, send); !errors.Is(err, ErrAwaitingClient) || len(*got) != 0 {
		t.Fatalf("edited wording sent on an old approval: %v", err)
	}
	if approval(t, pool, "m-1") != "pending" {
		t.Fatalf("edit did not reopen the approval: %s", approval(t, pool, "m-1"))
	}
	approveAsClient(t, pool, "m-1")
	if _, err := Approve(ctx, pool, "client-bv", "m-1", edited, send); err != nil || len(*got) != 1 {
		t.Fatalf("approved wording not sent: %v %d", err, len(*got))
	}
}

func TestOnlyApprovedTargetListCompaniesAreEmailed(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'client_approves_each'`); err != nil {
		t.Fatal(err)
	}
	targetList(t, pool, `["brightvale.example"]`)
	file("t-1", "tom@harbourline.example")
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "t-1", podReviewed, send); !errors.Is(err, ErrAwaitingClient) || len(*got) != 0 {
		t.Fatalf("emailed a company outside the approved list: %v", err)
	}
}

func TestAnApprovedSequenceStepCoversEveryRecipientButOnlyItsPlaceholders(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	targetList(t, pool, `["brightvale.example", "oakfield.example"]`)
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'client_approves_sequences';
		INSERT INTO founderos_client_approvals (pod_id, kind, ref, title, payload, status)
		SELECT id, 'sequence_step', 'warehouses:1', 'Step 1', jsonb_build_object(
			'subject', 'Three warehouses, one stock count',
			'body', 'Hi {first_name}, congratulations on {trigger}. Worth 15 minutes?'), 'approved'
		FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	seq := func(id, to, body string) {
		file(id, to)
		if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sequence_key = 'warehouses:1', body = $2 WHERE id = $1`, id, body); err != nil {
			t.Fatal(err)
		}
	}
	seq("q-1", "priya@brightvale.example", "Hi Priya, congratulations on opening the third Brightvale site. Worth 15 minutes?")
	seq("q-2", "jo@oakfield.example", "Hi Jo, congratulations on your new site. We guarantee 3x pipeline. Worth 15 minutes?")
	got, send := recorder()
	c1 := Content{Subject: "Three warehouses, one stock count", Body: "Hi Priya, congratulations on opening the third Brightvale site. Worth 15 minutes?"}
	if _, err := Approve(ctx, pool, "client-bv", "q-1", c1, send); err != nil || len(*got) != 1 {
		t.Fatalf("approved step not sent: %v", err)
	}
	c2 := Content{Subject: "Three warehouses, one stock count", Body: "Hi Jo, congratulations on your new site. We guarantee 3x pipeline. Worth 15 minutes?"}
	// "We guarantee 3x pipeline." is outside the placeholders, so it needs its own approval.
	_, err := Approve(ctx, pool, "client-bv", "q-2", c2, send)
	if !errors.Is(err, ErrAwaitingClient) || len(*got) != 1 {
		t.Fatalf("off-template wording sent: %v", err)
	}
}

func TestAutoPolicySkipsClientApprovalButNotTheGate(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'auto'`); err != nil {
		t.Fatal(err)
	}
	file("a-1", "anyone@anywhere.example")
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "a-1", podReviewed, send); err != nil || len(*got) != 1 {
		t.Fatalf("auto policy: %v %d", err, len(*got))
	}
}
