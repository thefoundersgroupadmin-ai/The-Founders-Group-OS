package caldotcom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

func resolver(t *testing.T, body string) connectors.Resolver {
	t.Helper()
	t.Setenv(keyName, "")
	p := filepath.Join(t.TempDir(), "env.local")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return connectors.Resolver{EnvLocal: p}
}

const samplePage = `{"status":"success","data":[
 {"uid":"b1","title":"Discovery call","status":"accepted","start":"2026-10-27T15:00:00Z","end":"2026-10-27T15:30:00Z",
  "eventType":{"id":5,"slug":"discovery"},"attendees":[{"name":"Ben Ortiz","email":" Ben@Basalt.Health "},{"name":"No mail","email":""}]},
 {"uid":"b2","title":"Cancelled","status":"cancelled","start":"2026-10-28T15:00:00Z","attendees":[{"email":"x@y.io"}]},
 {"title":"no uid","start":"2026-10-28T15:00:00Z"}
],"pagination":{"nextCursor":null,"hasMore":false}}`

func TestParseBookingsKeepsWhoBookedAndWhen(t *testing.T) {
	got := ParseBookings([]byte(samplePage))
	if len(got) != 2 {
		t.Fatalf("got %d bookings, want 2 (the one without a uid is skipped): %+v", len(got), got)
	}
	b := got[0]
	if b.UID != "b1" || b.Title != "Discovery call" || b.Status != "accepted" || b.Start != "2026-10-27T15:00:00Z" || b.EventSlug != "discovery" {
		t.Fatalf("booking = %+v", b)
	}
	if len(b.Attendees) != 1 || b.Attendees[0].Email != "ben@basalt.health" || b.Attendees[0].Name != "Ben Ortiz" {
		t.Fatalf("attendees = %+v", b.Attendees)
	}
	if got[1].Status != "cancelled" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestStatusWithoutAKeyIsNotConfigured(t *testing.T) {
	c := New(resolver(t, ""))
	if s := c.Status(context.Background()); s.State != connectors.StateNotConfigured || !strings.Contains(s.Detail, keyName) {
		t.Fatalf("status = %+v", s)
	}
}

func TestStatusAndBookingsCallTheAPIWithTheKeyAndVersion(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("Authorization") != "Bearer cal_live_test" || r.Header.Get("cal-api-version") != apiVersion {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(samplePage))
	}))
	defer srv.Close()
	c := New(resolver(t, keyName+"=cal_live_test\n"))
	c.baseURL = srv.URL

	s := c.Status(context.Background())
	if s.State != connectors.StateConnected || !strings.Contains(s.Detail, "Cal.com reachable") {
		t.Fatalf("status = %+v", s)
	}
	got, err := c.Bookings(context.Background(), "past")
	if err != nil || len(got) != 2 {
		t.Fatalf("bookings: %v, %d", err, len(got))
	}
	if !strings.HasPrefix(seen[len(seen)-1], "/bookings?") || !strings.Contains(seen[len(seen)-1], "status=past") {
		t.Fatalf("paths = %v", seen)
	}
}

func TestABadKeyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(resolver(t, keyName+"=cal_live_wrong\n"))
	c.baseURL = srv.URL
	if s := c.Status(context.Background()); s.State != connectors.StateError {
		t.Fatalf("status = %+v", s)
	}
}
