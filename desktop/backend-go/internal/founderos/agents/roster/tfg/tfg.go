// Package tfg holds The Founders Group's own agents: the AI SDR layer TFG
// runs on its own pipeline and implants for clients.
//
// Transparency rules, same as the rest of the roster:
//   - All three are read-only. None sends, posts, books or edits anything, so
//     none needs FOUNDEROS_WRITES. Each run is stored in founderos_agent_runs
//     with its summary, so every output can be audited later.
//   - None calls an LLM: the numbers are plain SQL over the founderos_* tables,
//     and every report carries the rule it used (thresholds, the hours formula)
//     in its Data so a client can check it.
package tfg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rhl/businessos-backend/internal/founderos/agents"
	"github.com/rhl/businessos-backend/internal/founderos/agents/roster"
	"github.com/rhl/businessos-backend/internal/founderos/brand"
)

// Thresholds. Changing one is a code change with a test, on purpose: the
// numbers the team is judged on should not drift silently.
const (
	FollowUpAfterDays = 3  // an open lead untouched this long needs a follow-up
	StuckAfterDays    = 14 // an open lead in the same stage this long is stuck
	ImpactWindowDays  = 7  // the Impact Reporter's look-back
	maxListed         = 5  // names quoted in a summary line
)

// IDs are the three TFG agents, in registry order.
var IDs = []string{"tfg-pipeline-sdr", "tfg-crm-hygiene", "tfg-impact-reporter"}

// Agents builds the group over the founderos_* tables.
func Agents(d roster.Deps) []agents.Agent {
	leads := &PgLeads{Pool: d.Pool, Workspaces: d.Workspaces}
	return []agents.Agent{
		&PipelineSDR{Leads: leads, Now: time.Now},
		&CRMHygiene{Leads: leads, Now: time.Now},
		&ImpactReporter{Runs: &PgRuns{Pool: d.Pool}, Now: time.Now, MinutesPerRun: brand.MinutesPerSuccessfulRun},
	}
}

// ---- data -------------------------------------------------------------------

// Lead is one funnel contact with its most recent touch.
type Lead struct {
	ID         string
	Name       string
	Venture    string
	Status     string
	Likelihood int
	Email      *string
	Company    *string
	AmountUSD  *float64
	CreatedAt  time.Time
	LastTouch  *time.Time // nil: never touched since the contact was created
	StageSince *time.Time // first touch at the current stage, nil if unknown
}

// LeadStore reads every lead in both offers.
type LeadStore interface {
	Leads(ctx context.Context) ([]Lead, error)
}

// RunStat is one agent's runs in the window.
type RunStat struct {
	AgentID string
	OK      int
	Failed  int
	CostUSD float64
}

// RunStore reads agent-run totals since a moment.
type RunStore interface {
	Since(ctx context.Context, since time.Time) ([]RunStat, error)
}

// ventureSlugs are the two offers' workspaces.
var ventureSlugs = []string{"vantage", "launchpad-cohort"}

// PgLeads reads founderos_funnel_contacts + founderos_funnel_touches.
type PgLeads struct {
	Pool       *pgxpool.Pool
	Workspaces map[string]string
}

func (s *PgLeads) Leads(ctx context.Context) ([]Lead, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("leads: founderos Postgres is not wired")
	}
	ids := make([]string, 0, len(ventureSlugs))
	for _, slug := range ventureSlugs {
		id := s.Workspaces[slug]
		if id == "" {
			return nil, fmt.Errorf("leads: workspace %q is not resolved (run make tfg)", slug)
		}
		ids = append(ids, id)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.name, c.venture, c.status, c.likelihood, c.email, c.company, c.amount_usd, c.created_at,
		       MAX(t.at)::timestamptz,
		       MIN(t.at) FILTER (WHERE t.stage = c.status)::timestamptz
		FROM founderos_funnel_contacts c
		LEFT JOIN founderos_funnel_touches t ON t.contact_id = c.id
		WHERE c.workspace_id = ANY($1::uuid[])
		GROUP BY c.id
		ORDER BY c.likelihood DESC, c.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("leads: %w", err)
	}
	defer rows.Close()
	out := []Lead{}
	for rows.Next() {
		var l Lead
		if err := rows.Scan(&l.ID, &l.Name, &l.Venture, &l.Status, &l.Likelihood, &l.Email, &l.Company, &l.AmountUSD, &l.CreatedAt, &l.LastTouch, &l.StageSince); err != nil {
			return nil, fmt.Errorf("leads: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("leads: %w", err)
	}
	return out, nil
}

// PgRuns reads founderos_agent_runs across every workspace.
type PgRuns struct{ Pool *pgxpool.Pool }

func (s *PgRuns) Since(ctx context.Context, since time.Time) ([]RunStat, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("runs: founderos Postgres is not wired")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT agent_id,
		       COUNT(*) FILTER (WHERE ok),
		       COUNT(*) FILTER (WHERE NOT ok),
		       COALESCE(SUM(cost_usd), 0)
		FROM founderos_agent_runs
		WHERE started_at >= $1
		GROUP BY agent_id
		ORDER BY agent_id`, since)
	if err != nil {
		return nil, fmt.Errorf("runs: %w", err)
	}
	defer rows.Close()
	out := []RunStat{}
	for rows.Next() {
		var r RunStat
		if err := rows.Scan(&r.AgentID, &r.OK, &r.Failed, &r.CostUSD); err != nil {
			return nil, fmt.Errorf("runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runs: %w", err)
	}
	return out, nil
}

// ---- helpers ------------------------------------------------------------------

func open(l Lead) bool { return l.Status != "converted" }

// lastActivity is the latest touch, else the day the lead was created.
func lastActivity(l Lead) time.Time {
	if l.LastTouch != nil && l.LastTouch.After(l.CreatedAt) {
		return *l.LastTouch
	}
	return l.CreatedAt
}

func daysBetween(from, to time.Time) int {
	if to.Before(from) {
		return 0
	}
	return int(to.Sub(from).Hours() / 24)
}

func names(ls []Lead) string {
	n := make([]string, 0, maxListed)
	for i, l := range ls {
		if i == maxListed {
			n = append(n, fmt.Sprintf("+%d more", len(ls)-maxListed))
			break
		}
		n = append(n, l.Name)
	}
	return strings.Join(n, ", ")
}

func leadRow(l Lead, now time.Time) map[string]any {
	return map[string]any{
		"id": l.ID, "name": l.Name, "offer": brand.Label(l.Venture), "stage": l.Status,
		"likelihood": l.Likelihood, "daysQuiet": daysBetween(lastActivity(l), now),
	}
}

// ---- tfg-pipeline-sdr -----------------------------------------------------------

// PipelineSDR builds today's follow-up queue: open leads nobody has touched
// for FollowUpAfterDays, hottest first. It queues; a human sends.
type PipelineSDR struct {
	Leads LeadStore
	Now   func() time.Time
}

func (*PipelineSDR) Meta() agents.Meta {
	return agents.Meta{
		ID: "tfg-pipeline-sdr", Name: "Pipeline SDR", DepartmentID: "dept-sales",
		Description: fmt.Sprintf("AI SDR: builds today's follow-up queue from both offers' pipelines (open leads untouched for %d+ days, hottest first). Read-only: it queues, a person sends.", FollowUpAfterDays),
	}
}

func (a *PipelineSDR) Run(ctx context.Context) (agents.Result, error) {
	ls, err := a.Leads.Leads(ctx)
	if err != nil {
		return agents.Result{OK: false, Summary: "Pipeline SDR could not read the pipeline: " + err.Error()}, nil
	}
	now := a.Now()
	var openLeads, due []Lead
	for _, l := range ls {
		if !open(l) {
			continue
		}
		openLeads = append(openLeads, l)
		if daysBetween(lastActivity(l), now) >= FollowUpAfterDays {
			due = append(due, l)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		if due[i].Likelihood != due[j].Likelihood {
			return due[i].Likelihood > due[j].Likelihood
		}
		return lastActivity(due[i]).Before(lastActivity(due[j]))
	})
	queue := make([]map[string]any, 0, len(due))
	for _, l := range due {
		queue = append(queue, leadRow(l, now))
	}
	summary := fmt.Sprintf("%d open leads · %d need a follow-up today", len(openLeads), len(due))
	if len(due) > 0 {
		summary += ": " + names(due)
	} else if len(openLeads) == 0 {
		summary += " (pipeline is empty: add leads or connect Typeform/Calendar)"
	}
	return agents.Result{
		OK:      true,
		Summary: summary,
		Data: map[string]any{
			"rule":  fmt.Sprintf("open (not converted) and no touch for %d+ days; sorted by likelihood, then longest quiet", FollowUpAfterDays),
			"open":  len(openLeads),
			"queue": queue,
		},
	}, nil
}

// ---- tfg-crm-hygiene ------------------------------------------------------------

// CRMHygiene lists the pipeline records a person needs to fix: no email, no
// company, an opted-in lead with no deal value, or a lead stuck in one stage.
type CRMHygiene struct {
	Leads LeadStore
	Now   func() time.Time
}

func (*CRMHygiene) Meta() agents.Meta {
	return agents.Meta{
		ID: "tfg-crm-hygiene", Name: "CRM Hygiene", DepartmentID: "dept-sales",
		Description: fmt.Sprintf("AI SDR: flags pipeline records to fix (no email, no company, opted-in with no deal value, stuck %d+ days in one stage). Read-only: it lists, a person fixes.", StuckAfterDays),
	}
}

func (a *CRMHygiene) Run(ctx context.Context) (agents.Result, error) {
	ls, err := a.Leads.Leads(ctx)
	if err != nil {
		return agents.Result{OK: false, Summary: "CRM Hygiene could not read the pipeline: " + err.Error()}, nil
	}
	now := a.Now()
	issues := map[string][]Lead{"no_email": {}, "no_company": {}, "no_deal_value": {}, "stuck": {}}
	flagged := map[string]bool{}
	add := func(kind string, l Lead) {
		issues[kind] = append(issues[kind], l)
		flagged[l.ID] = true
	}
	for _, l := range ls {
		if !open(l) {
			continue
		}
		if l.Email == nil || strings.TrimSpace(*l.Email) == "" {
			add("no_email", l)
		}
		if l.Company == nil || strings.TrimSpace(*l.Company) == "" {
			add("no_company", l)
		}
		if l.Status == "opted_in" && l.AmountUSD == nil {
			add("no_deal_value", l)
		}
		since := l.CreatedAt
		if l.StageSince != nil {
			since = *l.StageSince
		}
		if daysBetween(since, now) >= StuckAfterDays {
			add("stuck", l)
		}
	}
	data := map[string]any{
		"rule": fmt.Sprintf("open leads only; stuck = same stage %d+ days", StuckAfterDays),
	}
	for kind, list := range issues {
		rows := make([]map[string]any, 0, len(list))
		for _, l := range list {
			rows = append(rows, leadRow(l, now))
		}
		data[kind] = rows
	}
	summary := fmt.Sprintf("%d records to fix · no email %d · no company %d · no deal value %d · stuck %d",
		len(flagged), len(issues["no_email"]), len(issues["no_company"]), len(issues["no_deal_value"]), len(issues["stuck"]))
	if len(flagged) == 0 {
		summary = "Pipeline records are clean"
	}
	return agents.Result{OK: true, Summary: summary, Data: data}, nil
}

// ---- tfg-impact-reporter --------------------------------------------------------

// ImpactReporter turns the last week of agent runs into the number TFG sells:
// hours of non-selling work handed to agents. The formula is in every report.
type ImpactReporter struct {
	Runs          RunStore
	Now           func() time.Time
	MinutesPerRun int
}

func (*ImpactReporter) Meta() agents.Meta {
	return agents.Meta{
		ID: "tfg-impact-reporter", Name: "Impact Reporter", DepartmentID: "dept-tech",
		Description: fmt.Sprintf("Weekly proof: the last %d days of agent runs, failures, model cost, and the hours handed back (successful runs × the brand file's minutes per run). Shows its formula.", ImpactWindowDays),
	}
}

func (a *ImpactReporter) Run(ctx context.Context) (agents.Result, error) {
	since := a.Now().Add(-ImpactWindowDays * 24 * time.Hour)
	stats, err := a.Runs.Since(ctx, since)
	if err != nil {
		return agents.Result{OK: false, Summary: "Impact Reporter could not read the run log: " + err.Error()}, nil
	}
	var ok, failed int
	var cost float64
	var failing []string
	per := make([]map[string]any, 0, len(stats))
	for _, s := range stats {
		ok += s.OK
		failed += s.Failed
		cost += s.CostUSD
		if s.Failed > 0 {
			failing = append(failing, fmt.Sprintf("%s (%d)", s.AgentID, s.Failed))
		}
		per = append(per, map[string]any{"agent": s.AgentID, "ok": s.OK, "failed": s.Failed, "costUsd": s.CostUSD})
	}
	hours := float64(ok*a.MinutesPerRun) / 60
	summary := fmt.Sprintf("Last %dd: %d runs (%d ok, %d failed) · ≈%.1f h handed back · $%.2f model cost",
		ImpactWindowDays, ok+failed, ok, failed, hours, cost)
	if len(failing) > 0 {
		summary += " · failing: " + strings.Join(failing, ", ")
	}
	return agents.Result{
		OK:      true,
		Summary: summary,
		Data: map[string]any{
			"formula":       fmt.Sprintf("hours = successful runs × %d min ÷ 60 (impact.minutes_per_successful_run in the brand file)", a.MinutesPerRun),
			"windowDays":    ImpactWindowDays,
			"ok":            ok,
			"failed":        failed,
			"hoursHanded":   hours,
			"modelCostUsd":  cost,
			"perAgent":      per,
			"minutesPerRun": a.MinutesPerRun,
		},
	}, nil
}
