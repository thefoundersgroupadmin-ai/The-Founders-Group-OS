package saleskit

import (
	"strings"
	"testing"
)

func TestCommandPlanRejectsInventedCapabilitiesAndKeepsAllRegisteredAgentsAvailable(t *testing.T) {
	agents := []string{"tfg-pipeline-sdr", "gmail-worker", "stack-monitor"}
	if err := (CommandPlan{Agents: agents, Skills: []string{"pipeline-review"}}).Validate(agents, []string{"CEO"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []CommandPlan{{Agents: []string{"imaginary"}}, {Skills: []string{"send-money"}}, {Tasks: []CommandTask{{Agent: "imaginary", Title: "Do work"}}}} {
		if p.Validate(agents, []string{"CEO"}) == nil {
			t.Fatal("invented capability accepted", p)
		}
	}
}

func TestAgentBriefContainsActualWorkflowMethodsAndReviewBoundaries(t *testing.T) {
	text, err := AgentBrief("TFG HQ", "Approved profile", "Qualify leads", []string{"lead-triage", "draft-outreach"}, []Source{{ID: "lead-1", Title: "Lead", Text: "Evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Approved profile", "lead-triage", "draft-outreach", "outbox", "prospects", "Evidence", "never send"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
