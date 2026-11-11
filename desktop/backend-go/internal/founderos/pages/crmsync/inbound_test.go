package crmsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestDomainOf(t *testing.T) {
	for in, want := range map[string]string{"https://www.Acme.com/about": "acme.com", "ops@north.io": "north.io", "acme": "", "": ""} {
		if got := domainOf(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// HubSpot's opt-outs and customer companies become the pod's do-not-contact list; free-mail
// domains never block everyone; waiting emails to them are withdrawn.
func TestReconcileAddsHubSpotOptOutsAndCustomers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.Contains(r.URL.Path, "/contacts/search"):
			w.Write([]byte(`{"results":[{"properties":{"email":"Gone@Acme.example"}}]}`))
		case strings.Contains(r.URL.Path, "/companies/search"):
			w.Write([]byte(`{"results":[{"properties":{"domain":"customer.example"}},{"properties":{"domain":"gmail.com"}}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	var podID, ws string
	if err := pool.QueryRow(ctx, `WITH w AS (INSERT INTO workspaces (name, slug, owner_id) VALUES ('Inb', 'client-inb', 'u1') RETURNING id)
		INSERT INTO founderos_client_pods (workspace_id, slug, client_name) SELECT id, 'client-inb', 'Inb' FROM w RETURNING id::text, workspace_id::text`).Scan(&podID, &ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body) VALUES
		('o1', $1, 'paperclip', 'gone@acme.example', 's', 'b'), ('o2', $1, 'paperclip', 'ceo@customer.example', 's', 'b'), ('o3', $1, 'paperclip', 'x@gmail.com', 's', 'b')`, ws); err != nil {
		t.Fatal(err)
	}
	n, err := Reconcile(ctx, pool, podID, HubSpot{Token: "t", Base: srv.URL})
	if err != nil || n != 2 {
		t.Fatalf("added %d, %v", n, err)
	}
	if n, _ := Reconcile(ctx, pool, podID, HubSpot{Token: "t", Base: srv.URL}); n != 0 {
		t.Fatalf("second pass added %d", n)
	}
	for id, want := range map[string]string{"o1": "rejected", "o2": "rejected", "o3": "pending"} {
		var st string
		_ = pool.QueryRow(ctx, `SELECT status FROM founderos_outbox WHERE id = $1`, id).Scan(&st)
		if st != want {
			t.Errorf("%s: %s, want %s", id, st, want)
		}
	}
}

func TestPipedriveBlockedReadsUnsubscribedPeopleAndWonDeals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/persons":
			w.Write([]byte(`{"data":[{"email":[{"value":"stop@a.example"}],"marketing_status":"unsubscribed"},{"email":[{"value":"ok@a.example"}],"marketing_status":"subscribed"}],"additional_data":{"pagination":{"more_items_in_collection":false}}}`))
		case "/deals":
			if r.URL.Query().Get("status") != "won" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"data":[{"person_id":{"email":[{"value":"buyer@won.example"},{"value":"me@gmail.com"}]}}],"additional_data":{"pagination":{"more_items_in_collection":false}}}`))
		}
	}))
	defer srv.Close()
	got, err := Pipedrive{Token: "t", Base: srv.URL}.Blocked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Address != "stop@a.example" || got[1].Domain != "won.example" {
		t.Fatalf("got %+v", got)
	}
}
