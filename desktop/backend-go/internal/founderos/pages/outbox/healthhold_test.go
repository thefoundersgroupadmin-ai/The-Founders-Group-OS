package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// Even a manual approval can't send from a live pod whose inbox or portal wasn't read recently.
func TestStaleInboxOrPortalHoldsEvenAManualApproval(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	file := podFixture(t, pool, "client-bv", "live")
	file("h-1", "priya@brightvale.example")
	got, send := recorder()
	if _, err := pool.Exec(ctx, `UPDATE founderos_pod_health SET checked_at = now() - interval '1 hour' WHERE check_name = 'inbox'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "client-bv", "h-1", podReviewed, send); !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "inbox") || len(*got) != 0 {
		t.Fatalf("stale inbox sent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE founderos_pod_health SET checked_at = now() WHERE check_name = 'inbox';
		`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET portal_client_id = '8f14e45f-ceea-467a-9a36-dedd4bea2543'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "client-bv", "h-1", podReviewed, send); !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "portal") {
		t.Fatalf("unread portal sent: %v", err)
	}
}
