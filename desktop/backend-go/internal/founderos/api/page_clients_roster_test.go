package api

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

// New AI SDR clients appear on /os/clients from their pods, once per client however many pods.
func TestClientRosterIncludesEveryPodClientOnce(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	st := &pods.Store{Pool: pool}
	for _, n := range []pods.NewPod{{ClientName: "Kestrel", OwnerID: "u1", Package: "core"}, {ClientName: "Kestrel", Label: "UK", OwnerID: "u1", Package: "core"}} {
		if _, err := st.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	roster := clientsRoster(ctx, &Deps{Pool: pool})
	n := 0
	for _, p := range roster {
		if p.Name == "Kestrel" {
			n++
			if len(p.Sources) != 2 || p.Retainer != "Core package" || p.Service != "AI SDR" {
				t.Fatalf("entry %+v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("Kestrel listed %d times", n)
	}
}
