package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Cloudflare replaces an origin 502 with its own HTML page, so the owner saw
// HTML instead of the reason (Approvals, 2 Nov 2026). Browser routes answer an
// upstream failure with 424 and keep their JSON body.
func TestBrowserRoutesNeverAnswer502(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(noBadGateway)
	r.GET("/x", func(c *gin.Context) { c.JSON(http.StatusBadGateway, gin.H{"detail": "board said no"}) })
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/gone", func(c *gin.Context) { c.Status(http.StatusBadGateway) })
	for path, want := range map[string]int{"/x": http.StatusFailedDependency, "/ok": http.StatusOK, "/gone": http.StatusFailedDependency} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
		if path == "/x" && w.Body.String() != `{"detail":"board said no"}` {
			t.Errorf("body lost: %s", w.Body)
		}
	}
}
