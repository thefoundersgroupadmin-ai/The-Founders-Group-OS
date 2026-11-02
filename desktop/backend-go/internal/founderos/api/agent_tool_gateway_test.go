package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/people"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
)

func TestAgentToolRequestsRequireReviewAndStableIntent(t *testing.T) {
	peopleSetup(t)
	pool := catalogdb.TestDB(t)
	ws := catalogdb.Workspace(t, pool, "tool-client")
	ctx := context.Background()
	c := saleskit.Command{ID: uuid.NewString(), WorkspaceID: ws, UserID: "owner", UserEmail: people.OwnerEmail(), Plan: saleskit.CommandPlan{}, Steps: []saleskit.CommandStep{}}
	if _, err := (saleskit.CommandStore{Pool: pool}).Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	d := &Deps{Pool: pool}
	a := toolActor{Command: c, Config: saleskit.AutomationConfig{WorkspaceID: ws, Workspace: "tool-client"}, TaskID: uuid.NewString(), AgentID: uuid.NewString(), RunID: uuid.NewString(), Role: "Pipeline SDR", Department: "dept-sales"}
	id := uuid.NewString()
	input := json.RawMessage(`{"contactId":"not-executed","nextAction":"Review evidence"}`)
	result, err := invokeAgentTool(ctx, d, a, "pipeline_update", id, input)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "needs_approval") {
		t.Fatal(string(raw))
	}
	if _, err := invokeAgentTool(ctx, d, a, "pipeline_update", id, input); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeAgentTool(ctx, d, a, "pipeline_update", id, json.RawMessage(`{"contactId":"different","nextAction":"changed"}`)); err == nil {
		t.Fatal("changed intent reused a request identity")
	}
	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM founderos_tool_calls`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate action", count)
	}
	a.Role = "Data Agent"
	a.Department = "dept-tech"
	if _, err := invokeAgentTool(ctx, d, a, "social_publish", uuid.NewString(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("role escalation")
	}
	if _, err := invokeAgentTool(ctx, d, a, "keys_export", uuid.NewString(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("invented tool")
	}
}
func TestMCPToolsNeverExposeSecretsOrRunWithoutAuthentication(t *testing.T) {
	old := agentToolKey
	agentToolKey = func() (string, error) { return "fixture-bridge-secret", nil }
	defer func() { agentToolKey = old }()
	r := gin.New()
	r.POST("/mcp", agentToolMCP(&Deps{}))
	for _, auth := range []string{"", "Bearer wrong", "Bearer fixture-bridge-secret"} {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if auth != "Bearer fixture-bridge-secret" {
			if w.Code != http.StatusUnauthorized {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 200 || strings.Contains(w.Body.String(), "fixture-bridge-secret") || !strings.Contains(w.Body.String(), "lusha_enrich") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestOwnerApprovalExecutesOnceAndRejectNeverExecutes(t *testing.T) {
	peopleSetup(t)
	t.Setenv("PAPERCLIP_API_URL", "")
	t.Setenv("PAPERCLIP_BOARD_KEY", "")
	pool := catalogdb.TestDB(t)
	ws := catalogdb.Workspace(t, pool, "review-fixture")
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts(id,workspace_id,name,venture,status,created_at) VALUES('review-contact',$1,'Fixture','vantage','first_touch',now())`, ws)
	if err != nil {
		t.Fatal(err)
	}
	taskID := uuid.NewString()
	agentID := uuid.NewString()
	runID := uuid.NewString()
	c := saleskit.Command{ID: uuid.NewString(), WorkspaceID: ws, UserID: ownerUser.ID, UserEmail: ownerUser.Email, Plan: saleskit.CommandPlan{Tasks: []saleskit.CommandTask{{Agent: "CEO", Title: "Fixture review"}}}, Steps: []saleskit.CommandStep{{Kind: "task", ID: "0", TaskID: taskID, Status: "waiting"}}}
	if _, err = (saleskit.CommandStore{Pool: pool}).Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	d := &Deps{Pool: pool}
	r := asUser(t, d, ownerUser)
	for _, decision := range []string{"approve", "reject"} {
		id := uuid.NewString()
		input := `{"contactId":"review-contact","nextAction":"` + decision + `"}`
		_, err = pool.Exec(ctx, `INSERT INTO founderos_tool_calls(id,command_id,task_id,agent_id,run_id,request_id,tool,input,status) VALUES($1,$2,$3,$4,$5,$6,'pipeline_update',$7,'needs_approval')`, id, c.ID, taskID, agentID, runID, uuid.NewString(), input)
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/founderos/pages/agent-tools/" + id + "/" + decision
		w := do(t, r, "POST", path, `{}`)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", decision, w.Code, w.Body)
		}
		var next, state string
		pool.QueryRow(ctx, `SELECT next_action FROM founderos_funnel_contacts WHERE id='review-contact'`).Scan(&next)
		pool.QueryRow(ctx, `SELECT status FROM founderos_tool_calls WHERE id=$1`, id).Scan(&state)
		if next != "approve" {
			t.Fatalf("decision %s changed contact to %q", decision, next)
		}
		if decision == "approve" && state != "succeeded" || decision == "reject" && state != "rejected" {
			t.Fatal(state)
		}
		if w = do(t, r, "POST", path, `{}`); w.Code != 409 {
			t.Fatalf("replayed decision: %d", w.Code)
		}
	}
}
