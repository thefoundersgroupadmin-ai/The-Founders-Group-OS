package saleskit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAutomationSelectsRelevantWorkAndDoesNotRepeatUnchangedRecords(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	snap := Snapshot{Sources: []Source{{ID: "lead-1", Text: "A new lead"}}, LeadRevision: "lead-v1", CallRevision: "call-v1", HasLeads: true, HasCalls: true}
	plan := Plan(now, Checkpoint{}, snap)
	if len(plan.Routines) != 4 {
		t.Fatalf("initial routines: %+v", plan.Routines)
	}
	for _, id := range []string{"lead-triage", "draft-outreach", "call-follow-up", "pipeline-review", "weekly-wrap"} {
		if !strings.Contains(strings.Join(plan.Skills, ","), id) {
			t.Fatalf("missing %s", id)
		}
	}
	if p := Plan(now.Add(time.Minute), plan.Next, snap); len(p.Skills) != 0 {
		t.Fatalf("repeated work: %+v", p)
	}
	snap.LeadRevision = "lead-v2"
	p := Plan(now.Add(2*time.Minute), plan.Next, snap)
	if len(p.Routines) != 1 || p.Routines[0] != "lead changes" {
		t.Fatalf("new lead should not repeat call/weekly work: %+v", p)
	}
	nextDay := Plan(now.Add(24*time.Hour), plan.Next, Snapshot{Sources: snap.Sources, HasLeads: true, HasCalls: true, LeadRevision: "lead-v1", CallRevision: "call-v1"})
	if len(nextDay.Routines) != 1 || nextDay.Routines[0] != "daily pipeline review" {
		t.Fatalf("daily: %+v", nextDay)
	}
}
func TestAutomationNeedsRecordsAndGroundsOneCombinedPrompt(t *testing.T) {
	now := time.Now()
	if p := Plan(now, Checkpoint{}, Snapshot{}); len(p.Skills) != 0 {
		t.Fatal("empty data scheduled AI")
	}
	snap := Snapshot{Sources: []Source{{ID: "lead-123", Text: "Enquiry for planning"}}, HasLeads: true, LeadRevision: "r1"}
	p := Plan(now, Checkpoint{}, snap)
	prompt, err := AutomationPrompt("client-a", "Approved offer: inventory planning", snap, p, now)
	if err != nil || !strings.Contains(prompt, "lead-123") || !strings.Contains(prompt, "draft-outreach") {
		t.Fatalf("%v %s", err, prompt)
	}
	if _, err := AutomationPrompt("client-a", "", snap, p, now); err == nil {
		t.Fatal("empty profile accepted")
	}
	if BatchKey(p, snap, "profile-a") == BatchKey(p, snap, "profile-b") {
		t.Fatal("profile change did not invalidate work")
	}
}

func TestAutomationOnlyDraftsForChangedRecords(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	initial := Snapshot{HasLeads: true, Sources: []Source{{ID: "os-pipeline", Text: "leads"}}, LeadRevision: "r1", LeadRecords: map[string]string{"a": "one", "b": "two"}}
	p := Plan(now, Checkpoint{}, initial)
	initial.LeadRevision = "r2"
	initial.LeadRecords = map[string]string{"a": "one", "b": "changed", "c": "new"}
	changed := Plan(now.Add(time.Minute), p.Next, initial)
	if strings.Join(changed.ChangedLeads, ",") != "b,c" {
		t.Fatalf("changed records: %+v", changed.ChangedLeads)
	}
}
func TestRefreshingImportTimesDoesNotGenerateOutreach(t *testing.T) {
	first := []json.RawMessage{json.RawMessage(`{"id":"a","name":"Buyer","imported_at":"old","touches":[{"at":"2026-10-25","imported_at":"old"}]}`)}
	second := []json.RawMessage{json.RawMessage(`{"id":"a","name":"Buyer","imported_at":"new","touches":[{"at":"2026-10-25","imported_at":"new"}]}`)}
	if recordRevisions(first, false)["a"] != recordRevisions(second, false)["a"] {
		t.Fatal("refresh timestamps changed semantic revision")
	}
}
