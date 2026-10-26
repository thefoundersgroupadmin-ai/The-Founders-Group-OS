package paperclip

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureTaskRoleIsIdempotentAndDoesNotCopySecretsOrPrivileges(t *testing.T) {
	t.Setenv("FOUNDEROS_WRITES", "1")
	rows := []map[string]any{{"id": "template", "name": "Sales Operations", "status": "idle", "adapterType": "paperclip_runner", "adapterConfig": map[string]any{"provider": "codex", "model": "fixture-model", "env": map[string]any{"SECRET": "never-copy"}, "instructionsFilePath": "/private/template"}, "runtimeConfig": map[string]any{"aiConnection": map[string]any{"mode": "responsible_user", "method": "subscription", "provider": "openai"}}}}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(rows)
			return
		}
		posts++
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		ac := b["adapterConfig"].(map[string]any)
		if ac["env"] != nil || ac["instructionsFilePath"] != nil || ac["dangerouslyBypassApprovalsAndSandbox"] != nil {
			t.Error("copied privileged config")
		}
		if ac["model"] != "fixture-model" {
			t.Error("lost configured model")
		}
		rt := b["runtimeConfig"].(map[string]any)["heartbeat"].(map[string]any)
		if rt["enabled"] != false || rt["maxConcurrentRuns"] != float64(1) {
			t.Error("unbounded/background execution")
		}
		b["id"] = "role-id"
		b["status"] = "idle"
		rows = append(rows, b)
		json.NewEncoder(w).Encode(b)
	}))
	defer srv.Close()
	c := New(resolver(t, "PAPERCLIP_API_URL="+srv.URL, "PAPERCLIP_BOARD_KEY=board", "PAPERCLIP_COMPANY_ID=c1"))
	for i := 0; i < 2; i++ {
		a, created, err := c.EnsureTaskRole(context.Background(), "worker-1", "Pipeline SDR", "Role instructions", "boss")
		if err != nil || a.ID != "role-id" || created != (i == 0) {
			t.Fatalf("%+v %v %v", a, created, err)
		}
	}
	if posts != 1 {
		t.Fatalf("created %d times", posts)
	}
}

func TestEnsureTaskRoleFailsClosedWithoutSuitableRuntime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := New(resolver(t, "PAPERCLIP_API_URL="+srv.URL, "PAPERCLIP_BOARD_KEY=k", "PAPERCLIP_COMPANY_ID=c1"))
	if _, _, err := c.EnsureTaskRole(context.Background(), "worker", "Worker", "Instructions", ""); err == nil {
		t.Fatal("created an unconfigured agent")
	}
}

func TestTaskRunFailureFindsQuotaEvenWhenTaskRemainsInProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/i1":
			w.Write([]byte(`{"id":"i1","status":"in_progress","assigneeAgentId":"a1"}`))
		case "/api/companies/c1/heartbeat-runs":
			w.Write([]byte(`[{"id":"r1","status":"failed","contextSnapshot":{"issueId":"i1"}}]`))
		case "/api/heartbeat-runs/r1":
			w.Write([]byte(`{"error":"Native session failed","resultJson":{"nativeTurnFailure":{"codexErrorInfo":"usageLimitExceeded"}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := New(resolver(t, "PAPERCLIP_API_URL="+srv.URL, "PAPERCLIP_BOARD_KEY=k", "PAPERCLIP_COMPANY_ID=c1"))
	f, err := c.TaskRunFailure(context.Background(), "i1")
	if err != nil || f == nil || !f.ProviderLimit || f.RunID != "r1" {
		t.Fatalf("%+v %v", f, err)
	}
}
