package api

import (
	"context"
	"encoding/json"
	"github.com/rhl/businessos-backend/internal/founderos/connectors"
	"github.com/rhl/businessos-backend/internal/founderos/toolbridge"
	"io"
	"net/http"
	"strings"
	"testing"
)

type providerFixtureTransport func(*http.Request) (*http.Response, error)

func (f providerFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProviderRequestsUseFixedHostsAndExactReviewedPayload(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	t.Setenv("LUSHA_API_KEY", "fixture-lusha")
	t.Setenv("HIGGSFIELD_API_KEY", "fixture-generation")
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	cases := []struct{ name, input, host, path, header, key, body string }{
		{"lusha_enrich", `{"contacts":[{"linkedinUrl":"https://linkedin.com/in/example"}]}`, "api.lusha.com", "/v3/contacts/search-and-enrich", "api_key", "fixture-lusha", `{"contacts":[{"linkedinUrl":"https://linkedin.com/in/example"}]}`},
		{"higgsfield_generate", `{"model":"example/model","parameters":{"prompt":"reviewed"}}`, "api.higgsfield.ai", "/example/model", "Authorization", "Key fixture-generation", `{"prompt":"reviewed"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			http.DefaultTransport = providerFixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				if r.URL.Host != c.host || r.URL.Path != c.path || r.Method != "POST" || r.Header.Get(c.header) != c.key || string(raw) != c.body {
					t.Fatalf("incorrect provider request: %s %s body=%s", r.Method, r.URL, string(raw))
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"request_id":"job-1"}`)), Header: http.Header{}}, nil
			})
			tool, _ := toolbridge.Find(c.name)
			_, err := executeProviderTool(context.Background(), &Deps{Resolver: connectors.Resolver{}}, toolActor{}, tool, json.RawMessage(c.input))
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestProviderValidationDoesNotDispatchInvalidIdentifiers(t *testing.T) {
	tool, _ := toolbridge.Find("higgsfield_generate")
	_, err := executeProviderTool(context.Background(), &Deps{Resolver: connectors.Resolver{}}, toolActor{}, tool, json.RawMessage(`{"model":"../secret","parameters":{}}`))
	if err == nil {
		t.Fatal("accepted path traversal")
	}
	tool, _ = toolbridge.Find("lusha_enrich")
	_, err = executeProviderTool(context.Background(), &Deps{Resolver: connectors.Resolver{}}, toolActor{}, tool, json.RawMessage(`{"contacts":[]}`))
	if err == nil {
		t.Fatal("accepted unbounded/empty request")
	}
}
