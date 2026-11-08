package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// podFixture makes a client pod workspace with the given status and returns
// its workspace slug and a helper that files an outbox row there.
func podFixture(t *testing.T, pool *pgxpool.Pool, slug, status string) func(id, to string) {
	t.Helper()
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, slug)
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_client_pods (workspace_id, slug, client_name, status,
		sender_name, sender_email, sending_account_id, sending_domain, dry_run_inbox, timezone,
		send_window_start, send_window_end, send_weekends, first_email_daily_cap, follow_up_daily_cap, approval_policy)
		VALUES ($1, $2, 'Brightvale', $3, 'Sam', 'sam@bv-mail.example', 'pod:'||$2, 'bv-mail.example',
		'operator@tfg.example', 'UTC', 0, 24, true, 20, 40, 'auto')`, ws, slug, status); err != nil {
		t.Fatal(err)
	}
	return func(id, to string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body)
			VALUES ($1, $2, 'paperclip', $3, 'Three warehouses, one stock count', 'Hi Priya, body')`, id, ws, to); err != nil {
			t.Fatal(err)
		}
	}
}

func recorder() (*[]Item, SendFunc) {
	var got []Item
	return &got, func(_ context.Context, it Item) error { got = append(got, it); return nil }
}

var podReviewed = Content{Subject: "Three warehouses, one stock count", Body: "Hi Priya, body"}

func TestAPodSendsOnlyWhenLiveFromItsOwnAccount(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	for _, st := range []string{"setup", "paused", "handed_over"} {
		file := podFixture(t, pool, "client-"+strings.ReplaceAll(st, "_", "-"), st)
		file("r-"+st, "priya@brightvale.example")
		got, send := recorder()
		_, err := Approve(ctx, pool, "client-"+strings.ReplaceAll(st, "_", "-"), "r-"+st, podReviewed, send)
		if !errors.Is(err, ErrHeld) || len(*got) != 0 || status(t, pool, "r-"+st) != "pending" {
			t.Fatalf("%s pod: err=%v sent=%d status=%s", st, err, len(*got), status(t, pool, "r-"+st))
		}
	}
	file := podFixture(t, pool, "client-live", "live")
	file("l-1", "priya@brightvale.example")
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-live", "l-1", podReviewed, send); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].AccountID == nil || *(*got)[0].AccountID != "pod:client-live" || !(*got)[0].RequireAccount {
		t.Fatalf("live send: %+v", *got)
	}
	// A row that names another identity is refused, never re-routed.
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, account_id)
		SELECT 'l-2', workspace_id, 'paperclip', 'tom@harbourline.example', 's', 'b', 'inbox-1' FROM founderos_client_pods WHERE slug = 'client-live'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "client-live", "l-2", Content{Subject: "s", Body: "b"}, send); !errors.Is(err, ErrForeignAccount) || len(*got) != 1 || status(t, pool, "l-2") != "rejected" {
		t.Fatalf("foreign account: %v sent=%d status=%s", err, len(*got), status(t, pool, "l-2"))
	}
}

func TestAPodKeepsToItsWindowAndCaps(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET send_window_start = 9, send_window_end = 17,
		send_weekends = false, timezone = 'America/New_York', first_email_daily_cap = 1`); err != nil {
		t.Fatal(err)
	}
	prev := nowFunc
	t.Cleanup(func() { nowFunc = prev })
	ny, _ := time.LoadLocation("America/New_York")
	got, send := recorder()

	file("w-1", "a@one.example")
	nowFunc = func() time.Time { return time.Date(2026, 11, 9, 22, 0, 0, 0, ny) } // Monday 10pm
	if _, err := Approve(ctx, pool, "client-bv", "w-1", podReviewed, send); !errors.Is(err, ErrHeld) {
		t.Fatalf("outside hours: %v", err)
	}
	nowFunc = func() time.Time { return time.Date(2026, 11, 8, 11, 0, 0, 0, ny) } // Sunday
	if _, err := Approve(ctx, pool, "client-bv", "w-1", podReviewed, send); !errors.Is(err, ErrHeld) {
		t.Fatalf("weekend: %v", err)
	}
	nowFunc = func() time.Time { return time.Date(2026, 11, 9, 11, 0, 0, 0, ny) } // Monday 11am
	if _, err := Approve(ctx, pool, "client-bv", "w-1", podReviewed, send); err != nil {
		t.Fatal(err)
	}
	file("w-2", "b@two.example")
	if _, err := Approve(ctx, pool, "client-bv", "w-2", podReviewed, send); !errors.Is(err, ErrHeld) || status(t, pool, "w-2") != "pending" {
		t.Fatalf("first-email cap: %v %s", err, status(t, pool, "w-2"))
	}
	if len(*got) != 1 {
		t.Fatalf("sent %d", len(*got))
	}
}

func TestSuppressedAddressesAndDomainsAreNeverSent(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_email_suppression (workspace_id, address, reason)
		SELECT workspace_id, 'optout@one.example', 'replied no thanks' FROM founderos_client_pods;
		INSERT INTO founderos_email_suppression (domain, reason) VALUES ('competitor.example', 'global: competitor')`); err != nil {
		t.Fatal(err)
	}
	file("s-1", "OptOut@One.example")
	file("s-2", "ceo@competitor.example")
	got, send := recorder()
	for _, id := range []string{"s-1", "s-2"} {
		if _, err := Approve(ctx, pool, "client-bv", id, podReviewed, send); !errors.Is(err, ErrSuppressed) || status(t, pool, id) != "rejected" {
			t.Fatalf("%s: %v %s", id, err, status(t, pool, id))
		}
	}
	if len(*got) != 0 {
		t.Fatalf("a suppressed address was sent: %+v", *got)
	}
}

func TestADryRunGoesToTheSinkAndNeverCountsAsADelivery(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "dry_run")
	file("d-1", "priya@brightvale.example")
	got, send := recorder()
	it, err := Approve(ctx, pool, "client-bv", "d-1", podReviewed, send)
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].To != "operator@tfg.example" || !strings.Contains((*got)[0].Subject, "DRY RUN") ||
		!strings.Contains((*got)[0].Body, "priya@brightvale.example") {
		t.Fatalf("dry run delivery: %+v", *got)
	}
	if it.Status != "simulated" || status(t, pool, "d-1") != "simulated" {
		t.Fatalf("dry run recorded as %s / %s", it.Status, status(t, pool, "d-1"))
	}
	var sent int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM founderos_outbox WHERE status = 'sent'`).Scan(&sent)
	if sent != 0 {
		t.Fatalf("dry run counted as a delivery")
	}
	// No sink configured: held, never sent to the prospect.
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET dry_run_inbox = ''`); err != nil {
		t.Fatal(err)
	}
	file("d-2", "tom@harbourline.example")
	if _, err := Approve(ctx, pool, "client-bv", "d-2", podReviewed, send); !errors.Is(err, ErrHeld) || len(*got) != 1 {
		t.Fatalf("dry run without sink: %v sent=%d", err, len(*got))
	}
}

func TestNoSecondEmailWhileAnotherToTheSamePersonIsInFlightOrUncertain(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	file("c-1", "priya@brightvale.example")
	file("c-2", "Priya@Brightvale.example")
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET status = 'uncertain' WHERE id = 'c-1'`); err != nil {
		t.Fatal(err)
	}
	got, send := recorder()
	if _, err := Approve(ctx, pool, "client-bv", "c-2", podReviewed, send); !errors.Is(err, ErrHeld) || len(*got) != 0 {
		t.Fatalf("sent beside an uncertain email: %v %d", err, len(*got))
	}
}

func TestConcurrentSendsCannotBreakAPodCap(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET first_email_daily_cap = 1`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		file(fmt.Sprintf("k-%d", i), fmt.Sprintf("p%d@co%d.example", i, i))
	}
	var mu sync.Mutex
	sent := 0
	send := func(context.Context, Item) error {
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		sent++
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = Approve(ctx, pool, "client-bv", fmt.Sprintf("k-%d", i), podReviewed, send)
		}(i)
	}
	wg.Wait()
	if sent != 1 {
		t.Fatalf("cap 1, sent %d", sent)
	}
}
