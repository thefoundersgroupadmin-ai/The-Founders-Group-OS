package lusha

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

func TestFindSendsTheLookupAndReadsTheReply(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	t.Setenv(EnvKey, "k-test")
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/contacts/search-and-enrich" || r.Header.Get("api_key") != "k-test" {
			http.Error(w, "bad", 400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"contacts":[{"clientReferenceId":"c1","data":{"emailAddresses":[{"email":"Sam@Acme.com","emailType":"work"}],
			"phoneNumbers":[{"internationalNumber":"+1 212 555 0000","phoneType":"landline"},{"internationalNumber":"+1 917 555 0001","phoneType":"mobile"}]}}]}`))
	}))
	defer srv.Close()
	old := Base
	Base = srv.URL
	defer func() { Base = old }()
	f, err := New(connectors.Resolver{}).Find(context.Background(), Person{Ref: "c1", Name: "Sam Lee", Domain: "acme.com"})
	if err != nil || f.Email != "sam@acme.com" || f.Phone != "+1 917 555 0001" {
		t.Fatalf("%+v %v", f, err)
	}
	c := got["contacts"].([]any)[0].(map[string]any)
	if c["firstName"] != "Sam" || c["lastName"] != "Lee" || c["companyDomain"] != "acme.com" {
		t.Fatalf("request %+v", got)
	}
}

func TestFindRefusesWithoutKeyOrIdentity(t *testing.T) {
	t.Setenv(EnvKey, "")
	if _, err := New(connectors.Resolver{}).Find(context.Background(), Person{Name: "A B", Domain: "x.com"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatal(err)
	}
	t.Setenv(EnvKey, "k")
	if _, err := New(connectors.Resolver{}).Find(context.Background(), Person{Name: "Cher"}); err == nil {
		t.Fatal("looked up with nothing to match on")
	}
}

func TestParseEmptyReply(t *testing.T) {
	if f := Parse([]byte(`{"contacts":[{"clientReferenceId":"c1","error":{"code":404}}]}`)); f != (Found{}) {
		t.Fatalf("%+v", f)
	}
	if f := Parse([]byte(`not json`)); f != (Found{}) {
		t.Fatal("garbage parsed")
	}
}
