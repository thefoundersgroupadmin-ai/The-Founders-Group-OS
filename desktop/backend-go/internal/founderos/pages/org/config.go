package org

import "github.com/rhl/businessos-backend/internal/founderos/brand"

// LifeArea is the slice of lib/life-map.ts LIFE_AREAS the org board uses:
// the tint every crew wears and the legend.
type LifeArea struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Color         string   `json:"color"`
	Detail        string   `json:"detail"`
	Agents        []string `json:"agents"`
	DepartmentIDs []string `json:"departmentIds"`
}

// LifeAreas in FounderOS v1 order. dept-tech rolls up to knowledge first
// (LifeAreaForDepartment takes the first match).
var LifeAreas = []LifeArea{
	{"marketing", "Marketing", "#f59e0b", "Everything that earns attention.",
		[]string{"social-agent", "postly-publisher", "adsmith-creative", "reelkit-editor", "renderly-creative", "dmflow-mcp", "social-pulse"},
		[]string{"dept-marketing-growth"}},
	{"sales", "Sales", "#ef4444", "Deals, pipeline, and revenue relationships.",
		[]string{"tfg-pipeline-sdr", "tfg-crm-hygiene", "sales-agent", "crm-pulse", "launchpad-cohort-sales", "vantage-sales", "paykit-sales", "vantage-paykit", "stripe-sales", "processor-confirmation", "flexpay-financing", "sales-calls-data"},
		[]string{"dept-sales"}},
	{"finances", "Finances", "#22c55e", "Money in, money out, every processor.",
		[]string{"payments-pulse"}, []string{"dept-finance"}},
	{"communication", "Communication", "#3b82f6", "Every person, every channel, one priority ladder.",
		[]string{"comms-agent", "gmail-worker", "whatsapp-worker", "slack-worker"}, []string{"dept-comms"}},
	{"clients", "Clients", "#14b8a6", "Every client, onboarded and served.",
		[]string{"client-roster", "client-onboarding", "client-success"}, []string{"dept-clients"}},
	{"knowledge", "Knowledge", "#a855f7", "Optimal Engine: markdown, vectors, and recall.",
		[]string{"data-agent", "markdown-auditor", "vector-auditor", "notion-sync", "brain-librarian"}, []string{"dept-tech"}},
	{"operations", "Operations", "#fafafa", "The machine that runs the machine.",
		[]string{"conductor", "stack-monitor", "tfg-impact-reporter"}, []string{"dept-tech"}},
}

func LifeAreaForDepartment(departmentID string) *LifeArea {
	for i := range LifeAreas {
		for _, d := range LifeAreas[i].DepartmentIDs {
			if d == departmentID {
				a := LifeAreas[i]
				return &a
			}
		}
	}
	return nil
}

// Venture is a saved filter over the one shared roster (lib/ventures.ts).
type Venture struct {
	ID         string              `json:"id"`
	Label      string              `json:"label"`
	Kind       string              `json:"kind"`
	Color      string              `json:"color"`
	Detail     string              `json:"detail"`
	BrainTag   string              `json:"brainTag"`
	Focus      []string            `json:"focus"`
	AreaAgents map[string][]string `json:"areaAgents"`
	// AgentIDs is VentureAgentSet, sorted, so the page can dim without a
	// second copy of the lens.
	AgentIDs []string `json:"agentIds"`
}

// Ventures are the install's two offers, from the brand file
// (config/tfg/brand.yaml via scripts/tfg-brand.py).
var Ventures = venturesFromBrand()

func venturesFromBrand() []Venture {
	out := make([]Venture, 0, len(brand.Ventures))
	for _, b := range brand.Ventures {
		areas := make(map[string][]string, len(b.AreaAgents))
		for k, v := range b.AreaAgents {
			areas[k] = append([]string(nil), v...)
		}
		out = append(out, Venture{
			ID: b.ID, Label: b.Label, Kind: b.Kind, Color: b.Color, Detail: b.Detail,
			BrainTag: b.ID, Focus: append([]string(nil), b.Focus...), AreaAgents: areas,
		})
	}
	return out
}

// VentureAgentSet is every agent serving a venture, across its areas.
func VentureAgentSet(id string) map[string]bool {
	set := map[string]bool{}
	for _, v := range Ventures {
		if v.ID == id {
			for _, list := range v.AreaAgents {
				for _, a := range list {
					set[a] = true
				}
			}
		}
	}
	return set
}
