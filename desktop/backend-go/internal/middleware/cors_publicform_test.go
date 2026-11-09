package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/config"
)

// Client websites post leads to /api/founderos/inbound/<token> from their own
// domain; every other route keeps the origin allow-list.
func TestClientWebsiteFormsPassCORSOnlyOnTheirOwnPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORSMiddleware(&config.Config{AllowedOrigins: []string{"https://os.example"}}))
	r.POST("/api/founderos/inbound/:token", func(c *gin.Context) { c.String(404, "unknown form") })
	r.POST("/api/founderos/pages/pods", func(c *gin.Context) { c.String(200, "ok") })

	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader("email=a@b.example"))
		req.Header.Set("Origin", "https://client-site.example")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ServeHTTP(w, req)
		return w
	}
	if w := do("POST", "/api/founderos/inbound/tok"); w.Code != 404 || w.Body.String() != "unknown form" {
		t.Fatalf("form post blocked: %d %q", w.Code, w.Body.String())
	} else if w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("form path must not allow credentials")
	}
	if w := do("OPTIONS", "/api/founderos/inbound/tok"); w.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d", w.Code)
	}
	if w := do("POST", "/api/founderos/pages/pods"); w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin reached a normal route: %d", w.Code)
	}
	// Only the exact token route and POST/OPTIONS are public (Codex M6 review).
	r.POST("/api/founderos/inbound/:token/admin", func(c *gin.Context) { c.String(200, "nested") })
	r.GET("/api/founderos/inbound/:token", func(c *gin.Context) { c.String(200, "get") })
	if w := do("POST", "/api/founderos/inbound/tok/admin"); w.Code != http.StatusForbidden {
		t.Fatalf("nested path was public: %d", w.Code)
	}
	if w := do("GET", "/api/founderos/inbound/tok"); w.Code != http.StatusForbidden {
		t.Fatalf("GET was public: %d", w.Code)
	}
}
