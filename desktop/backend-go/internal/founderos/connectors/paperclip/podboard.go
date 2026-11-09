package paperclip

// The task-board side of client pods (pods.Board): a pod's project, agent,
// instructions and routines. Every write goes through send, so it obeys the
// writes guard and the board's own idempotency where it has one.

import (
	"context"
	"errors"
	"net/http"
)

// PodBoard adapts the connector to pods.Board.
type PodBoard struct{ C *Connector }

func idOf(v any) string {
	if m, ok := v.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			return id
		}
		for _, k := range []string{"project", "routine", "agent"} {
			if inner, ok := m[k].(map[string]any); ok {
				if id, ok := inner["id"].(string); ok {
					return id
				}
			}
		}
	}
	return ""
}

func (b PodBoard) company() (string, error) {
	cr, ok := b.C.creds()
	if !ok {
		return "", errNoCreds
	}
	return cr.company, nil
}

func (b PodBoard) EnsureProject(ctx context.Context, key, name, description string) (string, error) {
	co, err := b.company()
	if err != nil {
		return "", err
	}
	out, err := b.C.send(ctx, "paperclip.pod.project", http.MethodPost, "/api/companies/"+co+"/projects", map[string]any{
		"idempotencyKey": key, "name": name, "description": description, "visibility": "private", "status": "in_progress"})
	if err != nil {
		return "", err
	}
	if id := idOf(out); id != "" {
		return id, nil
	}
	return "", errors.New("board did not return a project id")
}

func (b PodBoard) EnsureAgent(ctx context.Context, workerID, name, instructions string) (string, error) {
	a, _, err := b.C.EnsureTaskRole(ctx, workerID, name, instructions, "")
	return a.ID, err
}

func (b PodBoard) PutInstructions(ctx context.Context, agentID, content string) error {
	_, err := b.C.send(ctx, "paperclip.pod.instructions", http.MethodPut, "/api/agents/"+agentID+"/instructions-bundle/file",
		map[string]any{"path": "AGENTS.md", "content": content})
	return err
}

func routineBody(r podRoutine) map[string]any {
	return map[string]any{"projectId": r.ProjectID, "assigneeAgentId": r.AssigneeAgentID, "title": r.Title,
		"description": r.Description, "status": r.Status, "priority": "high"}
}

// podRoutine mirrors pods.BoardRoutine without importing pods.
type podRoutine struct{ ProjectID, AssigneeAgentID, Title, Description, Status string }

func (b PodBoard) CreateRoutineFields(ctx context.Context, projectID, agentID, title, description, status string) (string, error) {
	co, err := b.company()
	if err != nil {
		return "", err
	}
	body := routineBody(podRoutine{projectID, agentID, title, description, status})
	body["concurrencyPolicy"], body["catchUpPolicy"] = "coalesce_if_active", "skip_missed"
	out, err := b.C.send(ctx, "paperclip.pod.routine", http.MethodPost, "/api/companies/"+co+"/routines", body)
	if err != nil {
		return "", err
	}
	if id := idOf(out); id != "" {
		return id, nil
	}
	return "", errors.New("board did not return a routine id")
}

func (b PodBoard) UpdateRoutineFields(ctx context.Context, id, projectID, agentID, title, description, status string) error {
	_, err := b.C.send(ctx, "paperclip.pod.routine", http.MethodPatch, "/api/routines/"+id,
		routineBody(podRoutine{projectID, agentID, title, description, status}))
	return err
}

func (b PodBoard) AddScheduleTrigger(ctx context.Context, routineID, cron, timezone string) (string, error) {
	out, err := b.C.send(ctx, "paperclip.pod.trigger", http.MethodPost, "/api/routines/"+routineID+"/triggers",
		map[string]any{"kind": "schedule", "cronExpression": cron, "timezone": timezone})
	if err != nil {
		return "", err
	}
	if id := idOf(out); id != "" {
		return id, nil
	}
	if m, ok := out.(map[string]any); ok {
		if tr, ok := m["trigger"].(map[string]any); ok {
			if id, ok := tr["id"].(string); ok {
				return id, nil
			}
		}
	}
	return "", nil
}

func (b PodBoard) UpdateScheduleTrigger(ctx context.Context, triggerID, cron, timezone string) error {
	_, err := b.C.send(ctx, "paperclip.pod.trigger", http.MethodPatch, "/api/routine-triggers/"+triggerID,
		map[string]any{"cronExpression": cron, "timezone": timezone})
	return err
}

// CreateTask opens a task for an agent in a project (a pod's enquiry, brief,
// coaching or review). The key makes it safe to retry: the board replays the
// task first created with that key instead of opening a second one, so a
// timeout or a crash after creation never duplicates work.
func (b PodBoard) CreateTask(ctx context.Context, key, projectID, agentID, title, description string) (string, error) {
	co, err := b.company()
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"title": title, "description": description, "status": "todo", "priority": "high", "workMode": "standard",
		"projectId": projectID, "assigneeAgentId": agentID}
	if key != "" {
		body["idempotencyKey"] = key
	}
	out, err := b.C.send(ctx, "paperclip.pod.task", http.MethodPost, "/api/companies/"+co+"/issues", body)
	if err != nil {
		return "", err
	}
	if id := idOf(out); id != "" {
		return id, nil
	}
	return "", errors.New("board did not return a task id")
}

// TaskStatus reads a task's status on the board (todo, in_progress, blocked,
// done, cancelled, ...).
func (b PodBoard) TaskStatus(ctx context.Context, issueID string) (string, error) {
	out, err := b.C.get(ctx, "/api/issues/"+issueID, issueTimeout)
	if err != nil {
		return "", err
	}
	if m, ok := out.(map[string]any); ok {
		if st, ok := m["status"].(string); ok {
			return st, nil
		}
	}
	return "", errors.New("board did not return the task's status")
}

// Nudge comments on a task, which wakes its agent.
func (b PodBoard) Nudge(ctx context.Context, issueID, message string) error {
	_, err := b.C.send(ctx, "paperclip.pod.nudge", http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"body": message})
	return err
}
