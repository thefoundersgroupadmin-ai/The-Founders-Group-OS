package api

import (
	"context"
	"log/slog"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

// podBoard adapts the task board connector to pods.Board.
type podBoard struct{ b paperclip.PodBoard }

func (p podBoard) EnsureProject(ctx context.Context, key, name, desc string) (string, error) {
	return p.b.EnsureProject(ctx, key, name, desc)
}
func (p podBoard) EnsureAgent(ctx context.Context, workerID, name, instructions string) (string, error) {
	return p.b.EnsureAgent(ctx, workerID, name, instructions)
}
func (p podBoard) PutInstructions(ctx context.Context, agentID, content string) error {
	return p.b.PutInstructions(ctx, agentID, content)
}
func (p podBoard) CreateRoutine(ctx context.Context, r pods.BoardRoutine) (string, error) {
	return p.b.CreateRoutineFields(ctx, r.ProjectID, r.AssigneeAgentID, r.Title, r.Description, r.Status)
}
func (p podBoard) UpdateRoutine(ctx context.Context, id string, r pods.BoardRoutine) error {
	return p.b.UpdateRoutineFields(ctx, id, r.ProjectID, r.AssigneeAgentID, r.Title, r.Description, r.Status)
}
func (p podBoard) AddScheduleTrigger(ctx context.Context, routineID, cron, tz string) (string, error) {
	return p.b.AddScheduleTrigger(ctx, routineID, cron, tz)
}
func (p podBoard) UpdateScheduleTrigger(ctx context.Context, triggerID, cron, tz string) error {
	return p.b.UpdateScheduleTrigger(ctx, triggerID, cron, tz)
}

func (p podBoard) CreateTask(ctx context.Context, projectID, agentID, title, description string) (string, error) {
	return p.b.CreateTask(ctx, projectID, agentID, title, description)
}

// podBoardFor is swapped in tests.
var podBoardFor = func(d *Deps) pods.Board { return podBoard{paperclip.PodBoard{C: paperclip.New(d.Resolver)}} }

// podTasksFor is swapped in tests.
var podTasksFor = func(d *Deps) pods.TaskBoard { return podBoard{paperclip.PodBoard{C: paperclip.New(d.Resolver)}} }

// resyncPodAgent refreshes a provisioned pod's agent and routines after a
// status or document change; a pod without an agent yet is left alone.
func resyncPodAgent(ctx context.Context, d *Deps, st *pods.Store, slug string) string {
	if provisioned, err := st.HasAgent(ctx, slug); err != nil || !provisioned {
		return ""
	}
	if _, err := st.Provision(ctx, podBoardFor(d), slug); err != nil {
		slog.Warn("client pods: agent resync failed", "pod", slug, "error", err)
		return "the AI SDR could not be updated on the task board: " + err.Error()
	}
	return ""
}
