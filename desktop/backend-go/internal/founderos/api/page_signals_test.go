package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/lusha"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/signals"
)

func TestSignalsBoardChecksRanksEnrichesAndRecordsTouches(t *testing.T) {
	peopleSetup(t)
	t.Setenv("FOUNDEROS_WRITES", "1")
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a href="https://jobs.lever.co/acme">jobs</a>`))
	}))
	defer site.Close()
	ats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"text":"Founding Account Executive","hostedUrl":"https://jobs.lever.co/acme/1"}]`))
	}))
	defer ats.Close()
	old := signalsChecker
	signalsChecker = func() *signals.Checker {
		c := signals.NewChecker()
		c.HTTP = site.Client()
		c.Lever = ats.URL + "/"
		return c
	}
	defer func() { signalsChecker = old }()

	for _, row := range [][]string{{"a1", "Sam Lee", "Acme", site.URL}, {"b1", "Ana Ruiz", "Beta", ""}} {
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, person, company, website, created_at)
			VALUES ($1, $2::uuid, $3, 'vantage', 'first_touch', $3, $4, NULLIF($5, ''), now())`, row[0], ws, row[1], row[2], row[3]); err != nil {
			t.Fatal(err)
		}
	}
	r := asUser(t, &Deps{Pool: pool}, ownerUser)

	if w := do(t, r, http.MethodPost, "/api/founderos/pages/signals/check", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"checked":2`) {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	w := do(t, r, http.MethodGet, "/api/founderos/pages/signals", "")
	var got struct {
		Rows  []signals.Row `json:"rows"`
		Lusha struct {
			Configured bool `json:"configured"`
		} `json:"lusha"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("board: %d %s", w.Code, w.Body)
	}
	if len(got.Rows) != 2 || got.Rows[0].ContactID != "a1" || got.Rows[0].Score < 30 || got.Rows[0].Plays.Next != "find_contact" || got.Lusha.Configured {
		t.Fatalf("board rows %+v", got)
	}
	if !strings.Contains(got.Rows[0].Plays.LinkedIn, "Acme is hiring a Founding Account Executive") {
		t.Fatalf("note %q", got.Rows[0].Plays.LinkedIn)
	}
	if got.Rows[1].Problem != "no website on file" {
		t.Fatalf("second %+v", got.Rows[1])
	}

	// Lusha not connected: an honest state, not an error.
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/signals/enrich", `{"contactId":"a1"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "not_configured") {
		t.Fatalf("enrich unconfigured: %d %s", w.Code, w.Body)
	}
	lu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contacts":[{"data":{"emailAddresses":[{"email":"sam@acme.example"}]}}]}`))
	}))
	defer lu.Close()
	oldBase := lusha.Base
	lusha.Base = lu.URL
	defer func() { lusha.Base = oldBase }()
	t.Setenv("LUSHA_API_KEY", "k")
	t.Setenv("FOUNDEROS_LUSHA_DAILY_CAP", "1")
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/signals/enrich", `{"contactId":"a1"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"email":"sam@acme.example"`) {
		t.Fatalf("enrich: %d %s", w.Code, w.Body)
	}
	var email string
	_ = pool.QueryRow(ctx, `SELECT email FROM founderos_funnel_contacts WHERE id = 'a1'`).Scan(&email)
	if email != "sam@acme.example" {
		t.Fatalf("email not saved: %q", email)
	}
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/signals/enrich", `{"contactId":"b1"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("cap not held: %d %s", w.Code, w.Body)
	}

	// A first email from the signals, filed for approval (never sent by itself).
	oldW := signalsWriter
	var prompt string
	signalsWriter = func(_ context.Context, _, p, _ string, _ time.Duration) (string, error) {
		prompt = p
		return "Subject: your founding AE\n\nHi Sam, saw the Founding Account Executive role.\n\nOwner", nil
	}
	defer func() { signalsWriter = oldW }()
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/signals/draft", `{"contactId":"b1"}`); w.Code != http.StatusConflict {
		t.Fatalf("draft without email: %d %s", w.Code, w.Body)
	}
	if w := do(t, r, http.MethodPost, "/api/founderos/pages/signals/draft", `{"contactId":"a1"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"signals-a1"`) {
		t.Fatalf("draft: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(prompt, "Hiring: Founding Account Executive") {
		t.Fatalf("prompt lacks the signal: %s", prompt)
	}
	var src, status, subject, to string
	_ = pool.QueryRow(ctx, `SELECT source, status, subject, to_address FROM founderos_outbox WHERE id = 'signals-a1'`).Scan(&src, &status, &subject, &to)
	if src != "signals" || status != "pending" || subject != "your founding AE" || to != "sam@acme.example" {
		t.Fatalf("outbox row %s %s %q %s", src, status, subject, to)
	}

	if w := do(t, r, http.MethodPost, "/api/founderos/pages/signals/touched", `{"contactId":"a1","channel":"linkedin"}`); w.Code != 200 {
		t.Fatalf("touched: %d %s", w.Code, w.Body)
	}
	var next string
	_ = pool.QueryRow(ctx, `SELECT next_action FROM founderos_funnel_contacts WHERE id = 'a1'`).Scan(&next)
	if !strings.HasPrefix(next, "LinkedIn connection sent") {
		t.Fatalf("next action %q", next)
	}
}
