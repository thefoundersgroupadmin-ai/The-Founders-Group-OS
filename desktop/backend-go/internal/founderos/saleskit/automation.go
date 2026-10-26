package saleskit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Snapshot contains only the selected workspace's synced OS records, never
// credentials or records fetched from another installation.
type Snapshot struct {
	LeadRecords     map[string]string `json:"-"`
	CallRecords     map[string]string `json:"-"`
	Sources         []Source          `json:"sources"`
	LeadRevision    string            `json:"leadRevision"`
	CallRevision    string            `json:"callRevision"`
	HasLeads        bool              `json:"hasLeads"`
	HasCalls        bool              `json:"hasCalls"`
	ContextRevision string            `json:"contextRevision"`
	Gaps            []string          `json:"gaps"`
}
type Checkpoint struct {
	LeadRecords map[string]string `json:"leadRecords,omitempty"`
	CallRecords map[string]string `json:"callRecords,omitempty"`
	Day         string            `json:"day"`
	Week        string            `json:"week"`
	Leads       string            `json:"leads"`
	Calls       string            `json:"calls"`
	Context     string            `json:"context"`
}
type AutomationPlan struct {
	ChangedLeads []string   `json:"changedLeads"`
	ChangedCalls []string   `json:"changedCalls"`
	Routines     []string   `json:"routines"`
	Skills       []string   `json:"skills"`
	Next         Checkpoint `json:"next"`
}

// Plan routes source changes and calendar windows into relevant workflows.
// It deliberately does not run every skill on every tick.
func Plan(now time.Time, previous Checkpoint, s Snapshot) AutomationPlan {
	p := AutomationPlan{Routines: []string{}, Skills: []string{}, Next: previous}
	if len(s.Sources) == 0 {
		return p
	}
	day := now.Format("2006-01-02")
	y, w := now.ISOWeek()
	week := fmt.Sprintf("%d-W%02d", y, w)
	add := func(name string, ids ...string) {
		p.Routines = append(p.Routines, name)
		p.Skills = append(p.Skills, ids...)
	}
	if s.HasLeads && previous.Leads != s.LeadRevision {
		p.ChangedLeads = changedRecords(previous.LeadRecords, s.LeadRecords)
		if len(s.LeadRecords) == 0 || len(p.ChangedLeads) > 0 {
			add("lead changes", "lead-triage", "research-prospect", "draft-outreach", "assign-target-to-sdr")
		}
		p.Next.LeadRecords = s.LeadRecords
		p.Next.Leads = s.LeadRevision
	}
	if s.HasCalls && previous.Calls != s.CallRevision {
		p.ChangedCalls = changedRecords(previous.CallRecords, s.CallRecords)
		if len(s.CallRecords) == 0 || len(p.ChangedCalls) > 0 {
			add("new call records", "summarize-conversation-transcript", "call-follow-up", "log-activity")
		}
		p.Next.CallRecords = s.CallRecords
		p.Next.Calls = s.CallRevision
	}
	if s.ContextRevision != "" && previous.Context != s.ContextRevision {
		add("connected email and calendar changes", "inbox-sweep", "calendar-events", "schedule-meeting")
		p.Next.Context = s.ContextRevision
	}
	if s.HasLeads && previous.Day != day {
		add("daily pipeline review", "daily-briefing", "pipeline-review", "crm-hygiene-check")
		p.Next.Day = day
	}
	if s.HasLeads && previous.Week != week {
		add("weekly sales review", "weekly-wrap", "win-loss-review")
		p.Next.Week = week
	}
	return p
}
func changedRecords(previous, current map[string]string) []string {
	out := []string{}
	for id, revision := range current {
		if previous[id] != revision {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func BatchKey(p AutomationPlan, s Snapshot, profile string) string {
	return Digest(struct {
		Plan                           AutomationPlan
		Leads, Calls, Profile, Context string
	}{p, s.LeadRevision, s.CallRevision, profile, s.ContextRevision})
}

func AutomationPrompt(workspace, profile string, s Snapshot, p AutomationPlan, now time.Time) (string, error) {
	if len(p.Skills) == 0 {
		return "", fmt.Errorf("no sales work is due")
	}
	in := Input{Workspace: workspace, Profile: profile, Request: "Run the relevant automated sales routines for " + now.Format(time.RFC3339), Sources: s.Sources}
	base, err := Prepare(p.Skills[0], in)
	if err != nil {
		return "", err
	}
	var methods []string
	for _, id := range p.Skills {
		skill, ok := Find(id)
		if !ok {
			return "", fmt.Errorf("unknown automated workflow: %s", id)
		}
		methods = append(methods, fmt.Sprintf("### %s (%s)\n%s\nDeliver: %s", skill.Name, id, skill.Method, skill.Output))
	}
	return base + "\n\n# Automatic sales work\n" +
		"Produce one consolidated report for these routines. Give each workflow a concise section. Do not ask the operator to execute each workflow. State when data cannot support a result. Only analyse records in the packet; do not imply these are live connector reads. Never send or apply a draft. Lead assignments and CRM log entries are proposals for review. Summaries must not label every lead as newly created: this trigger means the synced lead snapshot changed.\n" +
		strings.Join(methods, "\n\n") + fmt.Sprintf("\n\nChanged lead IDs: %v; changed call IDs: %v. For source-change routines, produce qualification, outreach and follow-up drafts only for these changed records when IDs are provided. Other records are context for daily or weekly analysis. Avoid proposing follow-ups on old calls without current evidence that a follow-up is still needed.", p.ChangedLeads, p.ChangedCalls) + "\n\nCoverage gaps: " + strings.Join(s.Gaps, "; "), nil
}
