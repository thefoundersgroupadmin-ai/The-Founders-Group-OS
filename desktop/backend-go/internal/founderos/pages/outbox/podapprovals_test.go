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

// Codex M1 review #5: a sequence template is filled by the OS from typed
// contact fields ({first_name}, {company}, {sender_name}) and must match
// exactly; anything freeform needs the client's approval of that email.
func TestAnApprovedSequenceStepCoversEveryRecipientOnlyWhenFilledFromTheirRecord(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	targetList(t, pool, `["brightvale.example", "oakfield.example"]`)
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET approval_policy = 'client_approves_sequences';
		INSERT INTO founderos_client_approvals (pod_id, kind, ref, title, payload, status)
		SELECT id, 'sequence_step', 'warehouses:1', 'Step 1', jsonb_build_object(
			'subject', 'Stock counts at {company}',
			'body', E'Hi {first_name}, keeping stock in sync across sites is where spreadsheets hurt. Worth 15 minutes?\n\n{sender_name}'), 'approved'
		FROM founderos_client_pods;
		INSERT INTO founderos_client_approvals (pod_id, kind, ref, title, payload, status)
		SELECT id, 'sequence_step', 'warehouses:freeform', 'Step 1b', jsonb_build_object(
			'subject', 'Hello', 'body', 'Hi {first_name}, congratulations on {trigger}.'), 'approved'
		FROM founderos_client_pods;
		INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, person, company)
		SELECT 'ct-p', workspace_id, 'Priya Shah', 'first_touch', 'client', now(), 'priya@brightvale.example', 'Priya Shah', 'Brightvale'
		FROM founderos_client_pods;
		INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, person, company)
		SELECT 'ct-j', workspace_id, 'Jo Reed', 'first_touch', 'client', now(), 'jo@oakfield.example', 'Jo Reed', 'Oakfield'
		FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	row := func(id, to, contact, key string) {
		file(id, to)
		if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET sequence_key = $2, contact_id = $3 WHERE id = $1`, id, key, contact); err != nil {
			t.Fatal(err)
		}
	}
	row("q-1", "priya@brightvale.example", "ct-p", "warehouses:1")
	row("q-2", "jo@oakfield.example", "ct-j", "warehouses:1")
	got, send := recorder()
	good := Content{Subject: "Stock counts at Brightvale", Body: "Hi Priya, keeping stock in sync across sites is where spreadsheets hurt. Worth 15 minutes?\n\nSam"}
	if _, err := Approve(ctx, pool, "client-bv", "q-1", good, send); err != nil || len(*got) != 1 {
		t.Fatalf("approved step filled from the record not sent: %v", err)
	}
	injected := Content{Subject: "Stock counts at Oakfield", Body: "Hi Jo; we guarantee £100000 revenue, keeping stock in sync across sites is where spreadsheets hurt. Worth 15 minutes?\n\nSam"}
	if _, err := Approve(ctx, pool, "client-bv", "q-2", injected, send); !errors.Is(err, ErrAwaitingClient) || len(*got) != 1 {
		t.Fatalf("injected claim sent on a step approval: %v", err)
	}
	row("q-3", "jo@oakfield.example", "ct-j", "warehouses:freeform")
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET status = 'rejected' WHERE id = 'q-2'`); err != nil {
		t.Fatal(err)
	}
	free := Content{Subject: "Hello", Body: "Hi Jo, congratulations on the new site."}
	if _, err := Approve(ctx, pool, "client-bv", "q-3", free, send); !errors.Is(err, ErrAwaitingClient) || len(*got) != 1 {
		t.Fatalf("a freeform placeholder sent without per-email approval: %v", err)
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
