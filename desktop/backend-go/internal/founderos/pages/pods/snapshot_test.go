package pods

import (
	"context"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// The pod's routines carry its own pipeline: research skips known companies, outreach sees stages.
// A daily refresh is due for running pods, but only a real change holds sending.
func TestRoutinesCarryAFreshPipelineSnapshot(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := readyPod(t, s, "Snapco")
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, company, person)
		VALUES ('sc1', $1, 'Dana', 'first_touch', 'client', now(), 'dana@north.example', 'Northwind Depot', 'Dana Reyes')`, p.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	b := newFakeBoard()
	bind, err := s.Provision(ctx, b, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	research, outreach := b.routines[bind.ResearchRoutineID].Description, b.routines[bind.OutreachRoutineID].Description
	if !strings.Contains(research, "Northwind Depot") || !strings.Contains(outreach, "Dana Reyes") || !strings.Contains(outreach, "stage researched") {
		t.Fatalf("snapshot missing:\n%s\n---\n%s", research, outreach)
	}
	for _, q := range []string{`UPDATE founderos_client_pods SET status = 'dry_run' WHERE id = $1`,
		`UPDATE founderos_pod_agents SET synced_at = now() - interval '25 hours' WHERE pod_id = $1`,
		// No change since the agent was last updated: only the daily refresh is due.
		`UPDATE founderos_client_pods SET config_changed_at = now() - interval '30 hours' WHERE id = $1`,
		`UPDATE founderos_client_pod_docs SET updated_at = now() - interval '30 hours', approved_at = CASE WHEN approved_at IS NULL THEN NULL ELSE now() - interval '30 hours' END WHERE pod_id = $1`} {
		if _, err := s.Pool.Exec(ctx, q, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	due, _ := s.AgentsDueRefresh(ctx)
	stale, _ := s.StaleAgents(ctx)
	if len(due) != 1 || len(stale) != 0 {
		t.Fatalf("due=%v stale=%v", due, stale)
	}
}
