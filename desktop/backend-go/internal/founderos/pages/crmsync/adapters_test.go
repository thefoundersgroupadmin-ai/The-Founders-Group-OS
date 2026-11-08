package crmsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recorded struct {
	method, path, auth, query string
	body                      map[string]any
}

func fakeAPI(t *testing.T, reply func(r recorded) any) (*httptest.Server, *[]recorded) {
	t.Helper()
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), query: r.URL.RawQuery}
		_ = json.NewDecoder(r.Body).Decode(&rec.body)
		got = append(got, rec)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reply(rec))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestHubSpotCreatesTheContactAndAssociatesEngagements(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	srv, got := fakeAPI(t, func(r recorded) any {
		if strings.HasSuffix(r.path, "/search") {
			return map[string]any{"results": []any{}}
		}
		return map[string]any{"id": "hs-1"}
	})
	h := HubSpot{Token: "pat", Base: srv.URL}
	ctx := context.Background()
	id, err := h.UpsertContact(ctx, Contact{Email: "priya@bv.example", FirstName: "Priya", LastName: "Shah", Company: "Brightvale"})
	if err != nil || id != "hs-1" {
		t.Fatalf("%q %v", id, err)
	}
	if err := h.LogEmail(ctx, "hs-1", Email{Subject: "Hi", Body: "Body", SentAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := h.LogMeeting(ctx, "hs-1", Meeting{Title: "Call", StartsAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	g := *got
	if len(g) != 4 || g[0].auth != "Bearer pat" || g[1].path != "/crm/v3/objects/contacts" || g[2].path != "/crm/v3/objects/emails" || g[3].path != "/crm/v3/objects/meetings" {
		t.Fatalf("calls %+v", g)
	}
	assoc := g[2].body["associations"].([]any)[0].(map[string]any)
	if assoc["to"].(map[string]any)["id"] != "hs-1" {
		t.Fatalf("email not associated: %v", assoc)
	}
	t.Setenv("FOUNDEROS_WRITES", "0")
	n := len(*got)
	if err := h.AddNote(ctx, "hs-1", "x"); err == nil || len(*got) != n {
		t.Fatal("wrote to HubSpot with writes off")
	}
}

func TestPipedriveFindsAnExistingPersonAndLogsActivities(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	srv, got := fakeAPI(t, func(r recorded) any {
		if r.path == "/persons/search" {
			return map[string]any{"data": map[string]any{"items": []any{map[string]any{"item": map[string]any{"id": 42}}}}}
		}
		return map[string]any{"data": map[string]any{"id": 7}}
	})
	p := Pipedrive{Token: "tok", Base: srv.URL}
	ctx := context.Background()
	id, err := p.UpsertContact(ctx, Contact{Email: "jo@oak.example"})
	if err != nil || id != "42" {
		t.Fatalf("%q %v", id, err)
	}
	if err := p.LogMeeting(ctx, id, Meeting{Title: "Demo", StartsAt: time.Date(2030, 1, 7, 10, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	g := *got
	if !strings.Contains(g[0].query, "api_token=tok") || g[1].path != "/activities" || g[1].body["person_id"] != float64(42) || g[1].body["due_time"] != "10:00" {
		t.Fatalf("calls %+v", g)
	}
}

func TestSalesforceLogsInWithTheRefreshTokenAndEscapesSOQL(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/services/oauth2/token":
			_ = r.ParseForm()
			if r.Form.Get("refresh_token") != "rt" {
				t.Error("refresh token not sent")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "instance_url": srvURL})
		case strings.HasSuffix(r.URL.Path, "/query"):
			if r.Header.Get("Authorization") != "Bearer at" || !strings.Contains(r.URL.Query().Get("q"), `o\'brien@x.example`) {
				t.Errorf("query %q auth %q", r.URL.Query().Get("q"), r.Header.Get("Authorization"))
			}
			json.NewEncoder(w).Encode(map[string]any{"records": []any{}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": "003X"})
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	s := &Salesforce{ClientID: "ci", ClientSecret: "cs", RefreshToken: "rt", LoginURL: srv.URL}
	id, err := s.UpsertContact(context.Background(), Contact{Email: "o'brien@x.example", LastName: "O'Brien"})
	if err != nil || id != "003X" {
		t.Fatalf("%q %v", id, err)
	}
}
