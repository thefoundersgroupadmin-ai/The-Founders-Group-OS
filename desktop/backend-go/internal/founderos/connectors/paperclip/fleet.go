package paperclip

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// EnsureTaskRole creates an on-demand managed role using an existing working
// subscription binding. It never copies credentials, host commands or privilege
// flags. Existing roles (including paused roles) are left untouched.
func (c *Connector) EnsureTaskRole(ctx context.Context, workerID, name, instructions, reportsTo string) (Agent, bool, error) {
	body, err := c.companyGet(ctx, "/agents")
	if err != nil {
		return Agent{}, false, err
	}
	rows := listFrom(body, "agents")
	var template map[string]any
	for _, v := range rows {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		meta, _ := m["metadata"].(map[string]any)
		if meta["tfgWorkerId"] == workerID {
			mapped := MapAgents([]any{m})
			if len(mapped) != 1 {
				return Agent{}, false, errors.New("invalid existing task role")
			}
			return mapped[0], false, nil
		}
		if m["name"] == name {
			return Agent{}, false, fmt.Errorf("agent name %q already exists without this worker binding", name)
		}
		cfg, _ := m["adapterConfig"].(map[string]any)
		rt, _ := m["runtimeConfig"].(map[string]any)
		binding, _ := rt["aiConnection"].(map[string]any)
		model, _ := cfg["model"].(string)
		if m["adapterType"] == "paperclip_runner" && m["status"] != "paused" && cfg["provider"] == "codex" && model != "" && binding["mode"] == "responsible_user" && binding["provider"] == "openai" {
			template = m
		}
	}
	if template == nil {
		return Agent{}, false, errors.New("no configured OpenAI task runtime; connect an AI subscription in OS first")
	}
	cfg := template["adapterConfig"].(map[string]any)
	payload := map[string]any{
		"name": name, "role": "general", "title": name, "capabilities": instructions,
		"adapterType": "paperclip_runner", "runner": "paperclip",
		"adapterConfig":      map[string]any{"provider": "codex", "model": cfg["model"], "lifecycleMode": "per_turn", "codexPermissionMode": "never"},
		"runtimeConfig":      map[string]any{"aiConnection": map[string]any{"mode": "responsible_user", "method": "subscription", "provider": "openai"}, "heartbeat": map[string]any{"enabled": false, "wakeOnDemand": true, "maxConcurrentRuns": 1}},
		"instructionsBundle": map[string]any{"entryFile": "AGENTS.md", "files": map[string]string{"AGENTS.md": instructions}},
		"permissions":        map[string]bool{"canCreateAgents": false, "canCreateSkills": false},
		"metadata":           map[string]any{"tfgWorkerId": workerID, "managedBy": "tfg-os", "taskRoleVersion": 1},
	}
	if reportsTo != "" {
		payload["reportsTo"] = reportsTo
	}
	cr, ok := c.creds()
	if !ok {
		return Agent{}, false, errors.New("task board not configured")
	}
	created, err := c.send(ctx, "paperclip.task-role.create", http.MethodPost, "/api/companies/"+cr.company+"/agents", payload)
	if err != nil {
		return Agent{}, false, err
	}
	mapped := MapAgents([]any{created})
	if len(mapped) != 1 {
		return Agent{}, false, errors.New("board did not return a task agent")
	}
	return mapped[0], true, nil
}

// TaskRoleBindings reads durable worker identity tags instead of matching display
// names, so renames cannot silently create duplicates or select another agent.
func (c *Connector) TaskRoleBindings(ctx context.Context) (map[string]Agent, error) {
	body, err := c.companyGet(ctx, "/agents")
	if err != nil {
		return nil, err
	}
	out := map[string]Agent{}
	for _, v := range listFrom(body, "agents") {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		meta, _ := m["metadata"].(map[string]any)
		id, _ := meta["tfgWorkerId"].(string)
		if id == "" {
			continue
		}
		a := MapAgents([]any{m})
		if len(a) == 1 {
			if _, dup := out[id]; dup {
				return nil, fmt.Errorf("duplicate task binding for %s", id)
			}
			out[id] = a[0]
		}
	}
	return out, nil
}

// TaskRunFailure checks the actual native terminal state. A task can remain
// in_progress after its runtime has failed; that is not a running task.
type TaskFailure struct {
	RunID         string `json:"runId"`
	Code          string `json:"code"`
	Detail        string `json:"detail"`
	ProviderLimit bool   `json:"providerLimit"`
}

func (c *Connector) TaskRunFailure(ctx context.Context, issueID string) (*TaskFailure, error) {
	body, err := c.get(ctx, "/api/issues/"+issueID, issueTimeout)
	if err != nil {
		return nil, err
	}
	issue := obj(body)
	if inner := obj(issue["issue"]); inner != nil {
		issue = inner
	}
	agentID, _ := issue["assigneeAgentId"].(string)
	if agentID == "" {
		return nil, nil
	}
	body, err = c.companyGet(ctx, "/heartbeat-runs?agentId="+agentID+"&limit=20")
	if err != nil {
		return nil, err
	}
	for _, value := range listFrom(body, "runs") {
		run := obj(value)
		snapshot := obj(run["contextSnapshot"])
		if snapshot["issueId"] != issueID && snapshot["taskId"] != issueID {
			continue
		}
		status, _ := run["status"].(string)
		if status != "failed" && status != "timed_out" && status != "cancelled" {
			return nil, nil
		}
		id, _ := run["id"].(string)
		if id == "" {
			return nil, errors.New("native task run has no identity")
		}
		detail, err := c.get(ctx, "/api/heartbeat-runs/"+id, issueTimeout)
		if err != nil {
			return nil, err
		}
		full := obj(detail)
		result := obj(full["resultJson"])
		failure := obj(result["nativeTurnFailure"])
		code, _ := failure["codexErrorInfo"].(string)
		message, _ := full["error"].(string)
		limit := code == "usageLimitExceeded"
		if limit {
			message = "Codex short-term usage limit reached. Downstream task launches stopped; review capacity before an explicit new command."
		}
		if message == "" {
			message = "Native task runtime ended with status " + status
		}
		return &TaskFailure{RunID: id, Code: code, Detail: message, ProviderLimit: limit}, nil
	}
	return nil, nil
}

// A durable command step supplies a stable key across uncertain HTTP outcomes.
func (c *Connector) CreateCommandIssue(ctx context.Context, title, description, key string) (*Issue, error) {
	cr, ok := c.creds()
	if !ok {
		return nil, errNoCreds
	}
	body, err := c.send(ctx, "paperclip.issue.create", http.MethodPost, "/api/companies/"+cr.company+"/issues", map[string]any{"title": title, "description": description, "idempotencyKey": key})
	if err != nil {
		return nil, err
	}
	rec := obj(body)
	if inner, ok := rec["issue"]; ok {
		return first(MapIssues([]any{inner})), nil
	}
	return first(MapIssues([]any{body})), nil
}

func (c *Connector) VerifyToolRun(ctx context.Context, runID, agentID, taskID string) error {
	body, err := c.get(ctx, "/api/heartbeat-runs/"+runID, issueTimeout)
	if err != nil {
		return err
	}
	r := obj(body)
	snap := obj(r["contextSnapshot"])
	cr, ok := c.creds()
	if !ok || r["companyId"] != cr.company || r["agentId"] != agentID || r["status"] != "running" || (snap["issueId"] != taskID && snap["taskId"] != taskID) {
		return errors.New("native run is not the active assignee for this work order")
	}
	return nil
}
func (c *Connector) ReturnToolReceipt(ctx context.Context, taskID, agentID, id, body string) error {
	_, err := c.send(ctx, "paperclip.document.write", http.MethodPut, "/api/issues/"+taskID+"/documents/tool-"+id, map[string]any{"title": "OS tool receipt " + id, "body": body, "format": "markdown"})
	if err != nil {
		return err
	}
	_, err = c.CommentOnIssue(ctx, taskID, "The owner decided OS tool action "+id+". Its exact result is saved in document tool-"+id+". Read it; do not repeat the external action. Continue the original task using the receipt.")
	if err != nil {
		return err
	}
	return c.AssignIssue(ctx, taskID, agentID)
}
