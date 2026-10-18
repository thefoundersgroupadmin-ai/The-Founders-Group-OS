// Package ventures is FounderOS v1 lib/ventures.ts: the two offers, their
// focus lists (the operator's to edit), and which agents work each life area.
package ventures

import "github.com/rhl/businessos-backend/internal/founderos/brand"

type Venture struct {
	ID         string              `json:"id"`
	Label      string              `json:"label"`
	Kind       string              `json:"kind"`
	Color      string              `json:"color"`
	Detail     string              `json:"detail"`
	BrainTag   string              `json:"brainTag"` // the venture's Optimal Engine workspace slug
	Focus      []string            `json:"focus"`
	AreaAgents map[string][]string `json:"areaAgents"`
}

// All is the install's two offers, from the brand file (config/tfg/brand.yaml
// via scripts/tfg-brand.py). The agents per life area are the operator's to
// edit there.
var All = fromBrand()

func fromBrand() []Venture {
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

func Get(id string) *Venture {
	for i := range All {
		if All[i].ID == id {
			return &All[i]
		}
	}
	return nil
}

// AgentSet is every agent serving a venture, across its life areas.
func AgentSet(id string) map[string]bool {
	out := map[string]bool{}
	if v := Get(id); v != nil {
		for _, agents := range v.AreaAgents {
			for _, a := range agents {
				out[a] = true
			}
		}
	}
	return out
}

// ForAgent lists the ventures an agent works for.
func ForAgent(agentID string) []Venture {
	var out []Venture
	for _, v := range All {
		if AgentSet(v.ID)[agentID] {
			out = append(out, v)
		}
	}
	return out
}
