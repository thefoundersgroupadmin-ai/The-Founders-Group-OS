package outbox

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestAutoSendToOwnerSendsOnlyMailAddressedToTheOwner(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	row(t, pool, ws, "self", "Nader@TheFoundersGroup.net", "", "pending", 0)
	row(t, pool, ws, "self-done", "nader@thefoundersgroup.net", "", "sent", day)
	row(t, pool, ws, "prospect", "owner@shop.example", "", "pending", 0)
	row(t, pool, ws, "colleague", "someone@thefoundersgroup.net", "", "pending", 0)

	var sentTo []string
	send := func(_ context.Context, it Item) error { sentTo = append(sentTo, it.ID); return nil }
	n, err := AutoSendToOwner(ctx, pool, "founderos", []string{" nader@thefoundersgroup.net ", ""}, 20, send)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(sentTo) != 1 || sentTo[0] != "self" {
		t.Fatalf("sent %d: %v, want only [self]", n, sentTo)
	}
	for _, id := range []string{"prospect", "colleague"} {
		if s := status(t, pool, id); s != "pending" {
			t.Errorf("%s: %s, want pending", id, s)
		}
	}
}

func TestAutoSendToOwnerDoesNothingWithoutOwnerAddressesAndRespectsCap(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	row(t, pool, ws, "s1", "me@own.example", "", "sent", 1)
	row(t, pool, ws, "p1", "me@own.example", "", "pending", 0)
	send := func(context.Context, Item) error { return nil }
	if n, err := AutoSendToOwner(ctx, pool, "founderos", nil, 20, send); err != nil || n != 0 {
		t.Fatalf("no owner addresses: sent %d err %v", n, err)
	}
	if n, err := AutoSendToOwner(ctx, pool, "founderos", []string{"me@own.example"}, 1, send); err != nil || n != 0 {
		t.Fatalf("cap reached: sent %d err %v", n, err)
	}
}
