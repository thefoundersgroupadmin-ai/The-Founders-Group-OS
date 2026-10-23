package people

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush/collect"
)

type usageTransport func(*http.Request) (*http.Response, error)

func (f usageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPersonalClaudeGaugeUsesOwnTokenCachesAndClearsOnReconnect(t *testing.T) {
	t.Setenv("FOUNDEROS_PEOPLE_DIR", filepath.Join(t.TempDir(), "people"))
	p := Person{ID: "ilja", Email: "ilja@example.com", Name: "Ilja"}
	ctx := With(context.Background(), p)
	const token = "sk-ant-oat01-test-personal-usage-token-123456789"
	if err := SaveClaudeToken(p.ID, token); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if s := ClaudeUsage(ctx, now); s == nil || s.Official != nil {
		t.Fatal("connected account without a gauge must stay honest")
	}
	calls := 0
	gauge := collect.NewClaudeGauge(func(context.Context) string { return ClaudeToken(p.ID) })
	gauge.URL = "https://usage.invalid"
	gauge.Client = &http.Client{Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("account gauge used a different person's token")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":12},"seven_day":{"utilization":34}}`)), Header: http.Header{}}, nil
	})}
	personalClaudeReaders.Lock()
	personalClaudeReaders.byDir[ClaudeConfigDir(p.ID)].gauge = gauge
	personalClaudeReaders.Unlock()
	for i := 0; i < 2; i++ {
		s := ClaudeUsage(ctx, now)
		if s.Official == nil || s.Official.Weekly.UsedPercent != 34 {
			t.Fatalf("missing account gauge: %+v", s)
		}
		raw, _ := json.Marshal(s)
		if strings.Contains(string(raw), token) {
			t.Fatal("saved credential leaked into the usage payload")
		}
	}
	if calls != 1 {
		t.Fatalf("gauge fetched %d times; want a single cached read", calls)
	}
	if s := ClaudeUsage(With(context.Background(), Person{ID: "other"}), now); s != nil {
		t.Fatal("a disconnected teammate borrowed another account")
	}
	if s := ClaudeUsage(With(context.Background(), Person{ID: p.ID, Owner: true}), now); s != nil {
		t.Fatal("owner must remain on device-pushed usage")
	}
	if err := SaveClaudeToken(p.ID, token+"-rotated"); err != nil {
		t.Fatal(err)
	}
	if s := ClaudeUsage(ctx, now); s == nil || s.Official != nil {
		t.Fatal("rotating credentials kept the previous account's cached gauge")
	}
	if err := Forget(p.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	if s := ClaudeUsage(ctx, now); s != nil {
		t.Fatal("disconnected account still published usage")
	}
}
