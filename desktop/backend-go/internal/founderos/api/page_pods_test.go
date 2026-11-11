package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/connectors"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
	"github.com/rhl/businessos-backend/internal/middleware"
)

func podsRouter(t *testing.T, pool *pgxpool.Pool, user *middleware.BetterAuthUser) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			c.Set(middleware.UserContextKey, user)
		}
		c.Next()
	})
	Register(r.Group("/api"), &Deps{Pool: pool}, func(c *gin.Context) { c.Next() })
	return r
}

func podsDo(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestPodsAreVisibleOnlyToTheOwnerAndThatPodsMembers(t *testing.T) {
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "owner@tfg.example")
	pool := catalogdb.TestDB(t)
	owner := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-owner", Email: "owner@tfg.example"})
	stranger := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-x", Email: "x@else.example"})
	member := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-ilya", Email: "ilya@tfg.example"})

	for _, name := range []string{"Brightvale", "Kestrel"} {
		if code, body := podsDo(t, owner, http.MethodPost, "/api/founderos/pods", `{"clientName":"`+name+`","package":"core","reps":5}`); code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, body)
		}
	}
	if code, _ := podsDo(t, stranger, http.MethodPost, "/api/founderos/pods", `{"clientName":"Sneaky"}`); code != http.StatusForbidden {
		t.Fatalf("non-owner created a pod: %d", code)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO workspace_members (workspace_id, user_id, status, role)
		SELECT workspace_id, 'u-ilya', 'active', 'member' FROM founderos_client_pods WHERE slug = 'client-brightvale'`); err != nil {
		t.Fatal(err)
	}
	count := func(h http.Handler) int {
		code, body := podsDo(t, h, http.MethodGet, "/api/founderos/pods", "")
		if code != http.StatusOK {
			t.Fatalf("list: %d %v", code, body)
		}
		return len(body["pods"].([]any))
	}
	if n := count(owner); n != 2 {
		t.Fatalf("owner sees %d pods", n)
	}
	if n := count(member); n != 1 {
		t.Fatalf("member sees %d pods, want only Brightvale", n)
	}
	if n := count(stranger); n != 0 {
		t.Fatalf("stranger sees %d pods", n)
	}
	for _, h := range []http.Handler{stranger, member} {
		if code, _ := podsDo(t, h, http.MethodGet, "/api/founderos/pods/client-kestrel", ""); code != http.StatusNotFound {
			t.Fatalf("foreign pod readable: %d", code)
		}
		if code, _ := podsDo(t, h, http.MethodPost, "/api/founderos/pods/client-kestrel/status", `{"status":"dry_run"}`); code != http.StatusNotFound {
			t.Fatalf("foreign pod writable: %d", code)
		}
	}
	if code, body := podsDo(t, member, http.MethodGet, "/api/founderos/pods/client-brightvale", ""); code != http.StatusOK || body["pod"].(map[string]any)["clientName"] != "Brightvale" {
		t.Fatalf("member read own pod: %d %v", code, body)
	}
}

func TestPodAPIWalksAPodFromSetupToLive(t *testing.T) {
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "owner@tfg.example")
	pool := catalogdb.TestDB(t)
	h := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-owner", Email: "owner@tfg.example"})
	podsDo(t, h, http.MethodPost, "/api/founderos/pods", `{"clientName":"Oakfield"}`)
	base := "/api/founderos/pods/client-oakfield"
	steps := []struct{ method, path, body string }{
		{http.MethodPost, base + "/status", `{"status":"dry_run"}`},
		{http.MethodPut, base + "/sender", `{"name":"Jo at Oakfield","email":"jo@oakfield-mail.com"}`},
		{http.MethodPut, base + "/docs/icp", `{"body":"Warehouse operators, 50-500 staff"}`},
		{http.MethodPut, base + "/docs/voice", `{"body":"Plain, warm, no jargon"}`},
		{http.MethodPost, base + "/docs/icp/approve", `{"by":"client:jo (email, 8 Nov)"}`},
		{http.MethodPost, base + "/docs/voice/approve", `{"by":"client:jo (email, 8 Nov)"}`},
	}
	for _, s := range steps {
		if code, body := podsDo(t, h, s.method, s.path, s.body); code != http.StatusOK {
			t.Fatalf("%s %s: %d %v", s.method, s.path, code, body)
		}
	}
	if code, body := podsDo(t, h, http.MethodPut, base+"/settings", `{"approvalPolicy":"client_approves_each","firstEmailDailyCap":25,
		"followUpDailyCap":50,"timezone":"America/New_York","sendWindow":[9,17],"dryRunInbox":"ops@tfg.example"}`); code != http.StatusOK ||
		body["pod"].(map[string]any)["dryRunInbox"] != "ops@tfg.example" {
		t.Fatalf("settings: %d %v", code, body)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE founderos_client_pods SET sending_account_id = 'pod:client-oakfield' WHERE slug = 'client-oakfield';
		INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, status, sent_at)
		SELECT 'sim-1', workspace_id, 'paperclip', 'a@b.example', 's', 'b', 'simulated', now() FROM founderos_client_pods WHERE slug = 'client-oakfield'`); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"product", "agent", "portal", "handoff", "calendar", "baseline"} {
		if code, body := podsDo(t, h, http.MethodPost, base+"/readiness-waivers", `{"item":"`+item+`","reason":"not part of this test","waive":true}`); code != http.StatusOK {
			t.Fatalf("waive %s: %d %v", item, code, body)
		}
	}
	code, body := podsDo(t, h, http.MethodPost, base+"/status", `{"status":"live"}`)
	if code != http.StatusOK || body["pod"].(map[string]any)["status"] != "live" {
		t.Fatalf("go live: %d %v", code, body)
	}
	code, body = podsDo(t, h, http.MethodGet, base+"/activity", "")
	if code != http.StatusOK || len(body["activity"].([]any)) < 4 {
		t.Fatalf("activity: %d %v", code, body)
	}
	// A blocked go-live explains what is missing.
	podsDo(t, h, http.MethodPost, "/api/founderos/pods", `{"clientName":"Ridgeway"}`)
	podsDo(t, h, http.MethodPost, "/api/founderos/pods/client-ridgeway/status", `{"status":"dry_run"}`)
	code, body = podsDo(t, h, http.MethodPost, "/api/founderos/pods/client-ridgeway/status", `{"status":"live"}`)
	if code != http.StatusConflict || body["error"] == nil {
		t.Fatalf("unready go-live: %d %v", code, body)
	}
}

func TestPodMailboxIsSavedAsKeysNeverEchoedAndBindsTheSender(t *testing.T) {
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "owner@tfg.example")
	pool := catalogdb.TestDB(t)
	env := t.TempDir() + "/env"
	if err := os.WriteFile(env, []byte("EXISTING=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mk := func(u *middleware.BetterAuthUser) http.Handler {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.UserContextKey, u); c.Next() })
		Register(r.Group("/api"), &Deps{Pool: pool, Resolver: connectors.Resolver{EnvLocal: env}}, func(c *gin.Context) { c.Next() })
		return r
	}
	owner := mk(&middleware.BetterAuthUser{ID: "u-owner", Email: "owner@tfg.example"})
	podsDo(t, owner, http.MethodPost, "/api/founderos/pods", `{"clientName":"Brightvale"}`)
	if _, err := pool.Exec(context.Background(), `INSERT INTO workspace_members (workspace_id, user_id, status, role)
		SELECT workspace_id, 'u-ilya', 'active', 'member' FROM founderos_client_pods WHERE slug = 'client-brightvale'`); err != nil {
		t.Fatal(err)
	}
	member := mk(&middleware.BetterAuthUser{ID: "u-ilya", Email: "ilya@tfg.example"})
	body := `{"host":"imap.brightvale-mail.example","user":"sam@brightvale-mail.example","pass":"s3cret-app-pass","fromName":"Sam | Brightvale"}`
	if code, _ := podsDo(t, member, http.MethodPut, "/api/founderos/pods/client-brightvale/mailbox", body); code != http.StatusForbidden {
		t.Fatalf("member set a mailbox password: %d", code)
	}
	code, out := podsDo(t, owner, http.MethodPut, "/api/founderos/pods/client-brightvale/mailbox", body)
	if code != http.StatusOK {
		t.Fatalf("set mailbox: %d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	if bytes.Contains(raw, []byte("s3cret-app-pass")) {
		t.Fatalf("password echoed: %s", raw)
	}
	saved, _ := os.ReadFile(env)
	for _, want := range []string{"EXISTING=1", "POD_CLIENT_BRIGHTVALE_PASS=s3cret-app-pass", "POD_CLIENT_BRIGHTVALE_USER=sam@brightvale-mail.example"} {
		if !bytes.Contains(saved, []byte(want)) {
			t.Fatalf("env lacks %q:\n%s", want, saved)
		}
	}
	p := out["pod"].(map[string]any)
	if p["senderEmail"] != "sam@brightvale-mail.example" || p["senderName"] != "Sam | Brightvale" || p["sendingDomain"] != "brightvale-mail.example" {
		t.Fatalf("sender not bound: %v", p)
	}
	var acct string
	_ = pool.QueryRow(context.Background(), `SELECT coalesce(sending_account_id,'') FROM founderos_client_pods WHERE slug='client-brightvale'`).Scan(&acct)
	if acct != "pod:client-brightvale" {
		t.Fatalf("account %q", acct)
	}
	code, out = podsDo(t, member, http.MethodGet, "/api/founderos/pods/client-brightvale/mailbox", "")
	mb, _ := out["mailbox"].(map[string]any)
	if code != http.StatusOK || mb["configured"] != true || mb["pass"] == "s3cret-app-pass" {
		t.Fatalf("mailbox status: %d %v", code, out)
	}
}

func TestAPodsOutboxIsReachableOnlyThroughThePodAndObeysTheGate(t *testing.T) {
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "owner@tfg.example")
	pool := catalogdb.TestDB(t)
	var sent []email.Reply
	prev := commsSourcesFor
	commsSourcesFor = func(*Deps) *commsSources {
		return &commsSources{SendEmail: func(_ context.Context, r email.Reply) error { sent = append(sent, r); return nil }}
	}
	t.Cleanup(func() { commsSourcesFor = prev })
	owner := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-owner", Email: "owner@tfg.example"})
	stranger := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-x", Email: "x@else.example"})
	podsDo(t, owner, http.MethodPost, "/api/founderos/pods", `{"clientName":"Brightvale"}`)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET status = 'live', approval_policy = 'client_approves_each',
		sending_account_id = 'pod:client-brightvale', sender_name = 'Sam', send_window_start = 0, send_window_end = 24, send_weekends = true;
		INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body)
		SELECT 'po-1', workspace_id, 'paperclip', 'priya@brightvale.example', 'Hello', 'Body' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	if code, _ := podsDo(t, stranger, http.MethodGet, "/api/founderos/pods/client-brightvale/outbox", ""); code != http.StatusNotFound {
		t.Fatalf("stranger read a client outbox: %d", code)
	}
	code, body := podsDo(t, owner, http.MethodGet, "/api/founderos/pods/client-brightvale/outbox", "")
	if code != http.StatusOK || len(body["pending"].([]any)) != 1 {
		t.Fatalf("pod outbox: %d %v", code, body)
	}
	// Not on an approved target list and not approved by the client: the operator's click waits too.
	code, body = podsDo(t, owner, http.MethodPost, "/api/founderos/pods/client-brightvale/outbox/po-1/approve", `{"subject":"Hello","body":"Body"}`)
	if code != http.StatusConflict || len(sent) != 0 {
		t.Fatalf("operator click bypassed the client: %d %v sent=%d", code, body, len(sent))
	}
	if code, _ := podsDo(t, owner, http.MethodPost, "/api/founderos/pods/client-brightvale/outbox/po-1/reject", ``); code != http.StatusOK {
		t.Fatalf("reject: %d", code)
	}
}

type recBoard struct {
	routines map[string]pods.BoardRoutine
	n        int
}

func (b *recBoard) EnsureProject(context.Context, string, string, string) (string, error) {
	return "proj-1", nil
}
func (b *recBoard) EnsureAgent(context.Context, string, string, string) (string, error) {
	return "agent-1", nil
}
func (b *recBoard) PutInstructions(context.Context, string, string) error { return nil }
func (b *recBoard) CreateRoutine(_ context.Context, r pods.BoardRoutine) (string, error) {
	b.n++
	id := fmt.Sprintf("rt-%d", b.n)
	b.routines[id] = r
	return id, nil
}
func (b *recBoard) UpdateRoutine(_ context.Context, id string, r pods.BoardRoutine) error {
	b.routines[id] = r
	return nil
}
func (b *recBoard) AddScheduleTrigger(context.Context, string, string, string) (string, error) {
	return "tr", nil
}
func (b *recBoard) UpdateScheduleTrigger(context.Context, string, string, string) error { return nil }

func TestOwnerProvisionsAPodsAISDRAndPausingReachesTheBoard(t *testing.T) {
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "owner@tfg.example")
	pool := catalogdb.TestDB(t)
	board := &recBoard{routines: map[string]pods.BoardRoutine{}}
	prev := podBoardFor
	podBoardFor = func(*Deps) pods.Board { return board }
	t.Cleanup(func() { podBoardFor = prev })
	owner := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-owner", Email: "owner@tfg.example"})
	base := "/api/founderos/pods/client-brightvale"
	podsDo(t, owner, http.MethodPost, "/api/founderos/pods", `{"clientName":"Brightvale"}`)
	if code, _ := podsDo(t, owner, http.MethodPost, base+"/agent", ""); code != http.StatusBadRequest {
		t.Fatalf("provisioned without sender/icp: %d", code)
	}
	podsDo(t, owner, http.MethodPut, base+"/sender", `{"name":"Sam","email":"sam@bv-mail.example"}`)
	podsDo(t, owner, http.MethodPut, base+"/docs/icp", `{"body":"Warehouse operators"}`)
	if code, body := podsDo(t, owner, http.MethodPost, base+"/agent", ""); code != http.StatusOK || body["agent"].(map[string]any)["agentId"] != "agent-1" {
		t.Fatalf("provision: %d %v", code, body)
	}
	podsDo(t, owner, http.MethodPost, base+"/status", `{"status":"dry_run"}`)
	for id, r := range board.routines {
		if r.Status != "active" {
			t.Fatalf("%s not active in dry run", id)
		}
	}
	podsDo(t, owner, http.MethodPost, base+"/status", `{"status":"paused","reason":"client holiday"}`)
	for id, r := range board.routines {
		if r.Status != "paused" {
			t.Fatalf("%s still active after pause", id)
		}
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO workspace_members (workspace_id, user_id, status, role)
		SELECT workspace_id, 'u-ilya', 'active', 'member' FROM founderos_client_pods`); err != nil {
		t.Fatal(err)
	}
	member := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-ilya", Email: "ilya@tfg.example"})
	if code, _ := podsDo(t, member, http.MethodPost, base+"/agent", ""); code != http.StatusForbidden {
		t.Fatalf("member provisioned an agent: %d", code)
	}
}

func TestTheCommsReplyBoxCannotSendAsAClientPod(t *testing.T) {
	pool := catalogdb.TestDB(t)
	var sent []email.Reply
	h := commsRouter(t, pool, &commsSources{SendEmail: func(_ context.Context, r email.Reply) error { sent = append(sent, r); return nil }})
	code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/comms/reply",
		`{"source":"email","account":"pod:client-brightvale","to":"priya@brightvale.example","subject":"Hi","text":"Hello"}`)
	if code != http.StatusForbidden || len(sent) != 0 {
		t.Fatalf("comms reply sent as a pod: %d, %d sends", code, len(sent))
	}
}

func TestAWebsiteFormPostsEnquiriesThroughThePodsPrivateLink(t *testing.T) {
	t.Setenv("FOUNDEROS_OWNER_EMAIL", "owner@tfg.example")
	pool := catalogdb.TestDB(t)
	owner := podsRouter(t, pool, &middleware.BetterAuthUser{ID: "u-owner", Email: "owner@tfg.example"})
	podsDo(t, owner, http.MethodPost, "/api/founderos/pods", `{"clientName":"Brightvale"}`)
	if _, err := pool.Exec(context.Background(), `UPDATE founderos_client_pods SET status = 'live'`); err != nil {
		t.Fatal(err)
	}
	code, body := podsDo(t, owner, http.MethodPost, "/api/founderos/pods/client-brightvale/inbound-link", "")
	endpoint, _ := body["endpoint"].(string)
	if code != http.StatusOK || !strings.Contains(endpoint, "/api/founderos/inbound/") || !strings.Contains(body["snippet"].(string), `name="website"`) {
		t.Fatalf("link: %d %v", code, body)
	}
	path := endpoint[strings.Index(endpoint, "/api/founderos/inbound/"):]
	// No session at all: the public route stands on the token.
	anon := podsRouter(t, pool, nil)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"Tom","email":"tom@harbour.example","message":"Two warehouses?"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	anon.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("json enquiry: %d %s", w.Code, w.Body)
	}
	form := url.Values{"email": {"jo@oakfield.example"}, "message": {"Pricing?"}, "_redirect": {"https://brightvale.example/thanks"}}
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	anon.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://brightvale.example/thanks" {
		t.Fatalf("form enquiry: %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	anon.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/founderos/inbound/nope", strings.NewReader(`{}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("bad link: %d", w.Code)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM founderos_pod_inbound`).Scan(&n)
	if n != 2 || !CSRFExempt(path) {
		t.Fatalf("enquiries %d, csrf-exempt %v", n, CSRFExempt(path))
	}
}
