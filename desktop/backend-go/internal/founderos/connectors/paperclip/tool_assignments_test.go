package paperclip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOSAssignmentsRequireProfileAndInstallation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/companies/c1/tools/connections":
			w.Write([]byte(`{"connections":[{"id":"gateway","name":"TFG OS task tools","status":"active","enabled":true}]}`))
		case "/api/tool-connections/gateway/catalog":
			w.Write([]byte(`{"catalog":[{"id":"read","toolName":"pipeline_read"},{"id":"write","toolName":"pipeline_update"}]}`))
		case "/api/tool-connections/gateway/installs":
			w.Write([]byte(`{"installs":[{"targetType":"agent","targetId":"ready"}]}`))
		case "/api/companies/c1/tools/profiles":
			w.Write([]byte(`{"profiles":[{"profileKey":"tfg-os-ready","status":"active","entries":[{"connectionId":"gateway","catalogEntryId":"read","effect":"include"},{"connectionId":"gateway","catalogEntryId":"write","effect":"exclude"}],"bindings":[{"targetType":"agent","targetId":"ready"},{"targetType":"agent","targetId":"uninstalled"}]}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(resolver(t, "PAPERCLIP_API_URL="+srv.URL, "PAPERCLIP_BOARD_KEY=board", "PAPERCLIP_COMPANY_ID=c1"))
	a, err := c.OSActionAssignments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a["ready"].State != "assigned" || len(a["ready"].Tools) != 1 || a["ready"].Tools[0] != "pipeline_read" || a["uninstalled"].State != "not_installed" {
		t.Fatalf("assignments: %+v", a)
	}
	mapped := MapAgents([]any{map[string]any{"id": "ready", "name": "Renamed", "metadata": map[string]any{"tfgWorkerId": "stable-worker"}}})
	if mapped[0].WorkerID != "stable-worker" {
		t.Fatal("lost stable worker link")
	}
}
