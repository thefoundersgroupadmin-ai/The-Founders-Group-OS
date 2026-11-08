package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/connectors"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/middleware"
)

func podsRouter(t *testing.T, pool *pgxpool.Pool, user *middleware.BetterAuthUser) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.UserContextKey, user); c.Next() })
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
