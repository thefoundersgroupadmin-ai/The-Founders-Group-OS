package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

type salesModel struct {
	calls  int
	prompt string
}

func (m *salesModel) Complete(_ context.Context, p string) (string, error) {
	m.calls++
	m.prompt = p
	return "Draft: Hello Jordan [lead-1]", nil
}

const salesPacket = `{"workspace":"client-example","profile":"We sell inventory planning.","request":"Draft a first response","sources":[{"id":"lead-1","title":"Demo request","text":"Jordan requested a demo."}]}`

func TestSalesKitCatalogExportAndAuth(t *testing.T) {
	r := router(t, &Deps{Board: connectors.NewRegistry()})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/founderos/pages/sales-kit", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, p := range []string{"/pages/sales-kit", "/pages/sales-kit/export", "/pages/sales-kit/skills/draft-outreach"} {
		w := catalogGet(t, r, "/api/founderos"+p)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body)
		}
	}
}

func TestSalesKitPrepareAndRunRespectUsageLatch(t *testing.T) {
	peopleSetup(t)
	old := codexWeekly
	defer func() { codexWeekly = old }()
	pct, known := 10.0, true
	codexWeekly = func(time.Time) (float64, bool) { return pct, known }
	model := &salesModel{}
	d := &Deps{Board: connectors.NewRegistry()}
	setSalesKitModel(d, model)
	r := asUser(t, d, ownerUser)
	w := do(t, r, "POST", "/api/founderos/pages/sales-kit/prepare/draft-outreach", salesPacket)
	if w.Code != 200 || model.calls != 0 {
		t.Fatalf("prepare %d %s", w.Code, w.Body)
	}
	w = do(t, r, "POST", "/api/founderos/pages/sales-kit/run/draft-outreach", salesPacket)
	if w.Code != 200 || model.calls != 1 {
		t.Fatalf("run %d %s", w.Code, w.Body)
	}
	var body struct {
		Markdown string
		Mode     string
	}
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Mode != "draft" || !strings.Contains(body.Markdown, "Jordan") {
		t.Fatal(w.Body)
	}
	pct = 50
	w = do(t, r, "POST", "/api/founderos/pages/sales-kit/run/draft-outreach", salesPacket)
	if w.Code != 423 || model.calls != 1 {
		t.Fatalf("cutoff %d %s", w.Code, w.Body)
	}
	pct = 0
	w = do(t, r, "POST", "/api/founderos/pages/sales-kit/run/draft-outreach", salesPacket)
	if w.Code != 423 || model.calls != 1 {
		t.Fatal("reset resumed AI")
	}
	// Release here simulates the owner's explicit go-ahead in this isolated fixture.
	if err := os.Remove(aiPausedPath()); err != nil {
		t.Fatal(err)
	}
	known = false
	w = do(t, r, "POST", "/api/founderos/pages/sales-kit/run/draft-outreach", salesPacket)
	if w.Code != 503 || model.calls != 1 {
		t.Fatal("unknown meter permitted a model call")
	}
	// Local prompt preparation remains available while AI is paused.
	if err := os.MkdirAll(filepath.Dir(aiPausedPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aiPausedPath(), []byte("paused"), 0600); err != nil {
		t.Fatal(err)
	}
	w = do(t, r, "POST", "/api/founderos/pages/sales-kit/prepare/draft-outreach", salesPacket)
	if w.Code != 200 || model.calls != 1 {
		t.Fatal("prepare unexpectedly used AI")
	}
}

func TestSalesKitRejectsInvalidPacketsBeforeModel(t *testing.T) {
	d := &Deps{Board: connectors.NewRegistry()}
	m := &salesModel{}
	setSalesKitModel(d, m)
	r := asUser(t, d, ownerUser)
	for _, packet := range []string{`{`, `{}`, strings.Replace(salesPacket, `"lead-1"`, `""`, 1)} {
		w := do(t, r, "POST", "/api/founderos/pages/sales-kit/run/draft-outreach", packet)
		if w.Code != 400 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if m.calls != 0 {
		t.Fatal("invalid input invoked model")
	}
}

func TestSalesAutomationWithoutDatabaseDoesNotPretendToBeActive(t *testing.T) {
	peopleSetup(t)
	r := asUser(t, &Deps{}, ownerUser)
	w := do(t, r, "GET", "/api/founderos/pages/sales-kit/automation", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = do(t, r, "PUT", "/api/founderos/pages/sales-kit/automation", `{}`)
	if w.Code != 503 {
		t.Fatalf("write without storage: %d %s", w.Code, w.Body)
	}
}
