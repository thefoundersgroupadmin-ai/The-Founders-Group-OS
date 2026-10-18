package tfg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time   { return now.Add(-time.Duration(n) * 24 * time.Hour) }
func tp(t time.Time) *time.Time { return &t }
func sp(s string) *string       { return &s }
func fp(f float64) *float64     { return &f }

type fakeLeads struct {
	ls  []Lead
	err error
}

func (f fakeLeads) Leads(context.Context) ([]Lead, error) { return f.ls, f.err }

type fakeRuns struct {
	rs    []RunStat
	err   error
	since time.Time
}

func (f *fakeRuns) Since(_ context.Context, since time.Time) ([]RunStat, error) {
	f.since = since
	return f.rs, f.err
}

func clock() time.Time { return now }

func TestPipelineSDRQueuesQuietOpenLeadsHottestFirst(t *testing.T) {
	a := &PipelineSDR{Now: clock, Leads: fakeLeads{ls: []Lead{
		{ID: "a", Name: "Ana", Venture: "vantage", Status: "engaged", Likelihood: 40, CreatedAt: daysAgo(10), LastTouch: tp(daysAgo(5))},
		{ID: "b", Name: "Ben", Venture: "launchpad-cohort", Status: "opted_in", Likelihood: 80, CreatedAt: daysAgo(10), LastTouch: tp(daysAgo(4))},
		{ID: "c", Name: "Cy", Venture: "vantage", Status: "engaged", Likelihood: 90, CreatedAt: daysAgo(10), LastTouch: tp(daysAgo(1))}, // touched recently
		{ID: "d", Name: "Di", Venture: "vantage", Status: "converted", Likelihood: 100, CreatedAt: daysAgo(30)},                         // won
		{ID: "e", Name: "Ed", Venture: "vantage", Status: "first_touch", Likelihood: 20, CreatedAt: daysAgo(3)},                         // never touched, 3 days old
	}}}
	r, err := a.Run(context.Background())
	if err != nil || !r.OK {
		t.Fatalf("run = %+v, %v", r, err)
	}
	if r.Summary != "4 open leads · 3 need a follow-up today: Ben, Ana, Ed" {
		t.Fatalf("summary = %q", r.Summary)
	}
	q := r.Data.(map[string]any)["queue"].([]map[string]any)
	if len(q) != 3 || q[0]["id"] != "b" || q[0]["daysQuiet"] != 4 || q[0]["offer"] == "launchpad-cohort" {
		t.Fatalf("queue = %+v (offer should be the brand label, not the slug)", q)
	}
}

func TestPipelineSDRSaysWhenThePipelineIsEmptyAndReportsReadErrors(t *testing.T) {
	r, _ := (&PipelineSDR{Now: clock, Leads: fakeLeads{}}).Run(context.Background())
	if !r.OK || !strings.Contains(r.Summary, "pipeline is empty") {
		t.Fatalf("empty = %+v", r)
	}
	r, err := (&PipelineSDR{Now: clock, Leads: fakeLeads{err: errors.New("db down")}}).Run(context.Background())
	if err != nil || r.OK || !strings.Contains(r.Summary, "db down") {
		t.Fatalf("error run = %+v, %v", r, err)
	}
}

func TestCRMHygieneFlagsEachIssueOnce(t *testing.T) {
	a := &CRMHygiene{Now: clock, Leads: fakeLeads{ls: []Lead{
		{ID: "a", Name: "Ana", Status: "opted_in", CreatedAt: daysAgo(2)},                                                                     // no email, no company, no value
		{ID: "b", Name: "Ben", Status: "engaged", Email: sp("b@x.co"), Company: sp("X"), CreatedAt: daysAgo(30), StageSince: tp(daysAgo(20))}, // stuck
		{ID: "c", Name: "Cy", Status: "engaged", Email: sp("c@x.co"), Company: sp("Y"), CreatedAt: daysAgo(30), StageSince: tp(daysAgo(2))},   // clean
		{ID: "d", Name: "Di", Status: "converted", CreatedAt: daysAgo(90)},                                                                    // won: ignored
		{ID: "e", Name: "Ed", Status: "opted_in", Email: sp("e@x.co"), Company: sp("Z"), AmountUSD: fp(75000), CreatedAt: daysAgo(1)},         // clean
	}}}
	r, err := a.Run(context.Background())
	if err != nil || !r.OK {
		t.Fatalf("run = %+v, %v", r, err)
	}
	if r.Summary != "2 records to fix · no email 1 · no company 1 · no deal value 1 · stuck 1" {
		t.Fatalf("summary = %q", r.Summary)
	}
	clean, _ := (&CRMHygiene{Now: clock, Leads: fakeLeads{}}).Run(context.Background())
	if clean.Summary != "Pipeline records are clean" {
		t.Fatalf("clean = %q", clean.Summary)
	}
}

func TestImpactReporterShowsHoursFailuresAndItsFormula(t *testing.T) {
	runs := &fakeRuns{rs: []RunStat{
		{AgentID: "comms-digest", OK: 7, CostUSD: 0.42},
		{AgentID: "tfg-pipeline-sdr", OK: 13, Failed: 1},
	}}
	a := &ImpactReporter{Runs: runs, Now: clock, MinutesPerRun: 6}
	r, err := a.Run(context.Background())
	if err != nil || !r.OK {
		t.Fatalf("run = %+v, %v", r, err)
	}
	if !runs.since.Equal(daysAgo(ImpactWindowDays)) {
		t.Fatalf("window starts %v", runs.since)
	}
	want := "Last 7d: 21 runs (20 ok, 1 failed) · ≈2.0 h handed back · $0.42 model cost · failing: tfg-pipeline-sdr (1)"
	if r.Summary != want {
		t.Fatalf("summary = %q\nwant      %q", r.Summary, want)
	}
	d := r.Data.(map[string]any)
	if !strings.Contains(d["formula"].(string), "× 6 min") || d["hoursHanded"].(float64) != 2.0 {
		t.Fatalf("data = %+v", d)
	}
}

func TestTheGroupIsTheThreeTFGAgents(t *testing.T) {
	var got []string
	for _, a := range Agents(rosterDepsForTest()) {
		got = append(got, a.Meta().ID)
	}
	if strings.Join(got, ",") != strings.Join(IDs, ",") {
		t.Fatalf("agents = %v, want %v", got, IDs)
	}
}
