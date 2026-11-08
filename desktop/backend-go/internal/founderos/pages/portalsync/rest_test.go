package portalsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTTalksToPostgRESTWithTheServiceKeyAndWritesOnlyWhenAllowed(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") != "svc" || r.Header.Get("Authorization") != "Bearer svc" {
			t.Errorf("missing service key on %s", r.URL)
		}
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("Prefer"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "sdr_approvals") {
			json.NewEncoder(w).Encode([]map[string]any{{"id": "a1", "os_key": "doc:icp", "status": "approved", "decided_at": "2026-11-09T10:00:00Z", "pulled_at": nil}})
			return
		}
		if r.Method == http.MethodGet {
			w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	// httptest is http://; build directly rather than through NewREST's https check.
	r := &REST{URL: srv.URL, Key: "svc", HTTP: srv.Client()}
	ctx := context.Background()
	if err := r.UpsertPod(ctx, Pod{ClientID: "c1", PodSlug: "client-bv", Missing: []string{}}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Approvals(ctx, "c1")
	if err != nil || len(got) != 1 || got[0].DecidedAt == "" || got[0].Pulled {
		t.Fatalf("approvals %+v %v", got, err)
	}
	if err := r.InsertActivity(ctx, []Activity{{ClientID: "c1", OSID: 7, Kind: "email_sent", Summary: "x"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /rest/v1/sdr_pods?on_conflict=client_id resolution=merge-duplicates,return=minimal",
		"GET /rest/v1/sdr_approvals?select=", "POST /rest/v1/sdr_activity?on_conflict=client_id,os_id resolution=ignore-duplicates"}
	for i, w := range want {
		if i >= len(seen) || !strings.HasPrefix(seen[i], w) {
			t.Fatalf("call %d = %q, want prefix %q", i, seen, w)
		}
	}
	t.Setenv("FOUNDEROS_WRITES", "0")
	n := len(seen)
	if err := r.UpsertPod(ctx, Pod{ClientID: "c1"}); err == nil || len(seen) != n {
		t.Fatal("wrote to the portal with writes off")
	}
	if _, ok := NewREST(func(string) string { return "" }); ok {
		t.Fatal("configured without settings")
	}
	if _, ok := NewREST(envMap{"PORTAL_SUPABASE_URL": "http://x", "PORTAL_SUPABASE_SERVICE_KEY": "k"}.lookup); ok {
		t.Fatal("accepted a non-https portal")
	}
}

type envMap map[string]string

func (m envMap) lookup(k string) string { return m[k] }
