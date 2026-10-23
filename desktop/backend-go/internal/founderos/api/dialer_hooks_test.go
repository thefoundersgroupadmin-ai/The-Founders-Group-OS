package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/apollo"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/twilio"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/dialer"
)

func twilioPost(t *testing.T, r *gin.Engine, path, query string, form url.Values, sign bool) *httptest.ResponseRecorder {
	t.Helper()
	full := "https://os.example" + path
	if query != "" {
		full += "?" + query
	}
	target := path
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if sign {
		req.Header.Set("X-Twilio-Signature", twilio.Sign("authtok", full, form))
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestTwilioOnlyDialsTheCallTheOSJustStarted(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	venture := catalogdb.Workspace(t, pool, "vantage")
	t.Setenv("FOUNDEROS_PUBLIC_URL", "https://os.example")
	t.Setenv(twilio.EnvAccount, "AC1")
	t.Setenv(twilio.EnvAuth, "authtok")
	t.Setenv(twilio.EnvCallerID, "+15005550006")
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, phone, created_at)
		VALUES ('p1', $1, 'Pat', 'vantage', 'first_touch', '+12127365000', now())`, venture); err != nil {
		t.Fatal(err)
	}
	// 15:00 UTC on a Thursday is inside New York's hours.
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	call, err := dialer.Start(ctx, pool, ws, "p1", "nader@x.com", at)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `UPDATE founderos_calls SET started_at = now() WHERE id = $1`, call.ID)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDialerHooks(r.Group("/api/founderos"), &Deps{Pool: pool})
	voice := url.Values{"CallId": {call.ID}, "From": {"client:nader_at_x_com"}, "To": {""}}

	if w := twilioPost(t, r, hookVoicePath, "", voice, false); w.Code != http.StatusForbidden {
		t.Fatalf("unsigned request answered: %d", w.Code)
	}
	other := url.Values{"CallId": {call.ID}, "From": {"client:karim_at_x_com"}}
	if w := twilioPost(t, r, hookVoicePath, "", other, true); !strings.Contains(w.Body.String(), "belongs to someone else") {
		t.Fatalf("someone else's call: %s", w.Body)
	}
	w := twilioPost(t, r, hookVoicePath, "", voice, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `callerId="+15005550006"`) || !strings.Contains(w.Body.String(), ">+12127365000</Number>") ||
		!strings.Contains(w.Body.String(), "https://os.example/api/founderos/hooks/twilio/status?call="+call.ID) {
		t.Fatalf("dial: %d %s", w.Code, w.Body)
	}
	// The same call can't be dialled twice.
	if w := twilioPost(t, r, hookVoicePath, "", voice, true); !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("redial: %s", w.Body)
	}

	st := url.Values{"CallSid": {"CA9"}, "CallStatus": {"completed"}, "CallDuration": {"42"}}
	if w := twilioPost(t, r, hookStatusPath, "call="+call.ID, st, true); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	got, _ := dialer.Get(ctx, pool, call.ID)
	if got.Status != "completed" || got.DurationS == nil || *got.DurationS != 42 {
		t.Fatalf("tracked %+v", got)
	}
}

func TestApolloNumbersLandOnTheRightContact(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	venture := catalogdb.Workspace(t, pool, "vantage")
	t.Setenv("FOUNDEROS_HOOK_SECRET", "s3cret")
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, created_at)
		VALUES ('p1', $1, 'Pat', 'vantage', 'first_touch', now())`, venture); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `INSERT INTO founderos_dial_state (contact_id, apollo_id) VALUES ('p1', 'ap-77')`)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDialerHooks(r.Group("/api/founderos"), &Deps{Pool: pool})
	body := `{"people":[{"id":"ap-77","phone_numbers":[{"sanitized_number":"+12125550000","type_cd":"work_hq"},{"sanitized_number":"+12127365000","type_cd":"mobile"}]}]}`
	req := httptest.NewRequest(http.MethodPost, hookApolloPath+"?k=wrong", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong key accepted: %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, hookApolloPath+"?k=s3cret", strings.NewReader(body))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var phone string
	_ = pool.QueryRow(ctx, `SELECT COALESCE(phone, '') FROM founderos_funnel_contacts WHERE id = 'p1'`).Scan(&phone)
	if w.Code != 200 || phone != "+12127365000" {
		t.Fatalf("%d phone %q (the mobile, not the switchboard)", w.Code, phone)
	}
}

func TestBestSkipsSwitchboardsAndInvalidNumbers(t *testing.T) {
	if got := apollo.Best([]apollo.Phone{{Sanitized: "+1", Type: "work_hq"}, {Sanitized: "+2", Type: "mobile", Status: "invalid_number"}, {Sanitized: "+3", Type: "work_direct"}}); got != "+3" {
		t.Fatal(got)
	}
	if apollo.Best([]apollo.Phone{{Sanitized: "+1", Type: "work_hq"}}) != "" {
		t.Fatal("a switchboard is not a dial")
	}
}
