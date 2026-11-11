package pods

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// A live pod is held unless its inbox was read successfully in the last 20 minutes,
// so replies and opt-outs are never missed while emails keep going out.
func TestInboxHoldCoversUnreadAndFailedInboxes(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	fresh, stale, broken, never := readyPod(t, s, "Freshco"), readyPod(t, s, "Staleco"), readyPod(t, s, "Brokenco"), readyPod(t, s, "Neverco")
	for _, p := range []Pod{fresh, stale, broken, never} {
		if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET status = 'live' WHERE id = $1`, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.SetHealth(ctx, fresh.ID, "inbox", true, "read")
	_ = s.SetHealth(ctx, stale.ID, "inbox", true, "read")
	_, _ = s.Pool.Exec(ctx, `UPDATE founderos_pod_health SET checked_at = now() - interval '45 minutes' WHERE pod_id = $1`, stale.ID)
	_ = s.SetHealth(ctx, broken.ID, "inbox", false, "login refused")

	hold, err := s.InboxHold(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hold[fresh.Slug] || !hold[stale.Slug] || !hold[broken.Slug] || !hold[never.Slug] {
		t.Fatalf("hold = %v", hold)
	}
}
