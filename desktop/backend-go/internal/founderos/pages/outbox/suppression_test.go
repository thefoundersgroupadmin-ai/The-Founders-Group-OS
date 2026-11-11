package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// TFG's own emails honour the do-not-contact list: an opted-out address (or its whole domain)
// is never sent to, and the waiting email is withdrawn with the reason.
func TestApproveNeverSendsToASuppressedAddress(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_email_suppression (workspace_id, address, reason) VALUES (NULL, 'eren@hitratech.example', 'opted out by reply')`); err != nil {
		t.Fatal(err)
	}
	sent := false
	_, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(context.Context, Item) error { sent = true; return nil })
	if !errors.Is(err, ErrSuppressed) || sent || status(t, pool, "ob-1") != "rejected" {
		t.Fatalf("err=%v sent=%v status=%s", err, sent, status(t, pool, "ob-1"))
	}

	seed(t, pool, ws, "ob-2", nil)
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET to_address = 'someone@blocked.example' WHERE id = 'ob-2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_email_suppression (workspace_id, domain, reason) VALUES ($1, 'blocked.example', 'company asked')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "founderos", "ob-2", reviewed, func(context.Context, Item) error { sent = true; return nil }); !errors.Is(err, ErrSuppressed) || sent {
		t.Fatalf("domain suppression ignored: %v sent=%v", err, sent)
	}
}
