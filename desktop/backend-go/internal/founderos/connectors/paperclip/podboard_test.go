package paperclip

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPodBoardWritesProjectsRoutinesTriggersAndInstructions(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	var calls []string
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.Method + " " + r.URL.Path
		calls = append(calls, key)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies[key] = b
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects"):
			json.NewEncoder(w).Encode(map[string]any{"id": "proj-1"})
		case strings.HasSuffix(r.URL.Path, "/routines") && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]any{"id": "rt-1"})
		default:
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer srv.Close()
	b := PodBoard{C: New(resolver(t, "PAPERCLIP_API_URL="+srv.URL, "PAPERCLIP_BOARD_KEY=k", "PAPERCLIP_COMPANY_ID=c1"))}
	ctx := context.Background()
	if id, err := b.EnsureProject(ctx, "tfg-pod:client-bv", "Client: Brightvale", "d"); err != nil || id != "proj-1" {
		t.Fatalf("project %q %v", id, err)
	}
	if bodies["POST /api/companies/c1/projects"]["idempotencyKey"] != "tfg-pod:client-bv" || bodies["POST /api/companies/c1/projects"]["visibility"] != "private" {
		t.Fatalf("project body %v", bodies["POST /api/companies/c1/projects"])
	}
	id, err := b.CreateRoutineFields(ctx, "proj-1", "ag-1", "Brightvale: daily research", "brief", "paused")
	if err != nil || id != "rt-1" {
		t.Fatalf("routine %q %v", id, err)
	}
	rb := bodies["POST /api/companies/c1/routines"]
	if rb["projectId"] != "proj-1" || rb["assigneeAgentId"] != "ag-1" || rb["status"] != "paused" || rb["catchUpPolicy"] != "skip_missed" || rb["concurrencyPolicy"] != "coalesce_if_active" {
		t.Fatalf("routine body %v", rb)
	}
	if err := b.AddScheduleTrigger(ctx, "rt-1", "0 7 * * 1-5", "America/New_York"); err != nil {
		t.Fatal(err)
	}
	if tb := bodies["POST /api/routines/rt-1/triggers"]; tb["kind"] != "schedule" || tb["timezone"] != "America/New_York" {
		t.Fatalf("trigger %v", tb)
	}
	if err := b.UpdateRoutineFields(ctx, "rt-1", "proj-1", "ag-1", "t", "new brief", "active"); err != nil {
		t.Fatal(err)
	}
	if err := b.PutInstructions(ctx, "ag-1", "# Brightvale AI SDR"); err != nil {
		t.Fatal(err)
	}
	if ib := bodies["PUT /api/agents/ag-1/instructions-bundle/file"]; ib["path"] != "AGENTS.md" || ib["content"] != "# Brightvale AI SDR" {
		t.Fatalf("instructions %v", ib)
	}
	t.Setenv("FOUNDEROS_WRITES", "0")
	before := len(calls)
	if _, err := b.EnsureProject(ctx, "k", "n", "d"); err == nil || len(calls) != before {
		t.Fatal("wrote to the board with writes off")
	}
}
