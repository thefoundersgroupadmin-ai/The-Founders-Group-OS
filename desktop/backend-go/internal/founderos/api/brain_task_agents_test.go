package api

import (
	"errors"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/pages/brain"
	"testing"
)

func TestBrainMergesStableTaskIdentityAndKeepsIndependentAgents(t *testing.T) {
	org := &brainOrg{Agents: []brain.Agent{{ID: "worker", Name: "Worker label", Tools: []string{"source"}, Status: "active"}}}
	live := []paperclip.Agent{{ID: "task", WorkerID: "worker", Name: "Renamed task agent", Status: "paused"}, {ID: "independent", Name: "Worker label", Status: "idle"}}
	access := brainToolAccess{Tools: map[string]paperclip.ToolAssignment{"task": {Tools: []string{"pipeline_read"}, State: "assigned"}, "independent": {Tools: []string{"knowledge_query"}, State: "assigned"}}}
	page := composeBrainPage(org, live, nil, access)
	g := page["graph"].(brain.KnowledgeGraph)
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		if ids[n.ID] {
			t.Fatal("duplicate node", n.ID)
		}
		ids[n.ID] = true
	}
	if ids["board:task"] || !ids["emp:worker"] || !ids["board:independent"] {
		t.Fatalf("incorrect identity merge: %v", ids)
	}
	a := page["agents"].([]brain.Agent)[0]
	if a.TaskAgent == nil || a.TaskAgent.ID != "task" || a.Status != "paused" || len(a.TaskAgent.Tools) != 1 {
		t.Fatalf("missing runtime assignment: %+v", a)
	}
	uses := map[string]bool{}
	for _, e := range g.Edges {
		if e.Kind == "uses" {
			uses[e.Source+"/"+brain.ToolSlugOf(e.Target)] = true
		}
	}
	if !uses["emp:worker/pipeline_read"] || !uses["board:independent/knowledge_query"] {
		t.Fatalf("missing task action edges: %v", uses)
	}
	if org.Agents[0].TaskAgent != nil || len(org.Agents[0].Tools) != 1 {
		t.Fatal("mutated shared org snapshot")
	}
	missing := composeBrainPage(org, live, nil, brainToolAccess{Err: errors.New("offline")})["agents"].([]brain.Agent)[0]
	if missing.TaskAgent.ToolState != "unavailable" || len(missing.TaskAgent.Tools) != 0 {
		t.Fatal("invented tool connectivity during outage")
	}
}
func TestBrainDoesNotHideConflictingTaskBindings(t *testing.T) {
	org := &brainOrg{Agents: []brain.Agent{{ID: "worker", Name: "Worker"}}}
	p := composeBrainPage(org, []paperclip.Agent{{ID: "a", WorkerID: "worker", Name: "A"}, {ID: "b", WorkerID: "worker", Name: "B"}}, nil)
	if len(p["boardAgents"].([]brainSeat)) != 2 || p["agents"].([]brain.Agent)[0].TaskAgent != nil {
		t.Fatal("hid a real conflicting binding")
	}
}
