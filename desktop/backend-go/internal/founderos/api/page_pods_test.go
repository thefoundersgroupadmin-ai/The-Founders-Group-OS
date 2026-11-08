package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
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
