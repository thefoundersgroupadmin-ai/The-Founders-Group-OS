package seed

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rhl/businessos-backend/internal/founderos/brainimport"
	"github.com/rhl/businessos-backend/internal/founderos/topology"
)

// brain/ is the demo's knowledge: the markdown v1 generates from its seed
// (demo-v1 scripts/generate-brain-docs.ts → lib/brain-docs.ts: one page per
// agent, SOP, tool, human and pillar, wikilinked), captured on DumpedOn. The
// only edit is the rename v2 applies everywhere: G-Brain is the Optimal
// Engine ("the Brain"), so tools/gbrain.md ships as tools/optimal-engine.md
// (with the engine's own one-line description).
//
//go:embed brain tfgbrain
var brainFS embed.FS

// tfgDropped are demo pages about the demo operator's people; the tfg
// profile replaces them with tfgbrain/.
func tfgDropped(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "people/") {
		return true
	}
	for _, p := range []string{"sops/sop-dana.md", "sops/sop-marco.md", "sops/sop-mia.md", "sops/sop-nadia.md", "sops/sop-sasha.md"} {
		if rel == p {
			return true
		}
	}
	return false
}

// BrainDocs is how many knowledge pages the demo ships.
const BrainDocs = 114

// MaterializeBrain writes the knowledge pages into dir as a brain-store
// (agents/, sops/, tools/, people/, org/) and returns how many it wrote.
func MaterializeBrain(dir string) (int, error) { return MaterializeBrainProfile(dir, "demo") }

// MaterializeBrainProfile is MaterializeBrain for a seed profile: "tfg" drops
// the demo people pages and adds The Founders Group's pages (tfgbrain/).
func MaterializeBrainProfile(dir, profile string) (int, error) {
	n := 0
	copyTree := func(root string, skip func(string) bool) error {
		return fs.WalkDir(brainFS, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.FromSlash(p))
			if skip != nil && skip(rel) {
				return nil
			}
			raw, err := brainFS.ReadFile(p)
			if err != nil {
				return err
			}
			out := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			n++
			return os.WriteFile(out, raw, 0o644)
		})
	}
	switch profile {
	case "demo":
		return n, copyTree("brain", nil)
	case "tfg":
		if err := copyTree("brain", tfgDropped); err != nil {
			return n, err
		}
		return n, copyTree("tfgbrain", nil)
	case "client":
		if err := copyTree("brain", tfgDropped); err != nil {
			return n, err
		}
		// TFG's rules and agents, not TFG's people or sales offers.
		return n, copyTree("tfgbrain", func(rel string) bool {
			rel = filepath.ToSlash(rel)
			return strings.HasPrefix(rel, "people/") || rel == "org/tfg-offers.md" ||
				rel == "sops/sop-nader.md" || rel == "sops/sop-ilya.md" || rel == "sops/sop-karim.md"
		})
	}
	return 0, fmt.Errorf("seed: unknown profile %q", profile)
}

// BrainReport is what one SeedBrain run did.
type BrainReport struct {
	Planned  int                   `json:"planned"`  // pages routed to an engine
	Skipped  int                   `json:"skipped"`  // too short to be worth a page (brainimport.MinChars)
	Landed   int                   `json:"landed"`   // already in the engine: not sent again
	Ingested int                   `json:"ingested"` // sent this run
	Errors   []brainimport.Failure `json:"errors,omitempty"`
}

// SeedBrain imports the demo's knowledge into the Optimal Engine, routed by
// the engine topology (every page lands in HQ, the founderos workspace). It
// creates the topology's workspaces, registers each page's slug as an entity
// so the wikilinks become graph edges, and sends only the pages the engine
// does not already hold: a re-run ingests nothing.
func SeedBrain(topo *topology.Topology, engines map[string]brainimport.Endpoint) (*BrainReport, error) {
	return SeedBrainProfile(topo, engines, "demo")
}

// SeedBrainProfile is SeedBrain for a seed profile (see MaterializeBrainProfile).
func SeedBrainProfile(topo *topology.Topology, engines map[string]brainimport.Endpoint, profile string) (*BrainReport, error) {
	dir, err := os.MkdirTemp("", "founderos-brain-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	store := filepath.Join(dir, "store")
	if _, err := MaterializeBrainProfile(store, profile); err != nil {
		return nil, err
	}
	items, skipped, err := brainimport.Plan(store, topo)
	if err != nil {
		return nil, err
	}
	rep := &BrainReport{Planned: len(items), Skipped: skipped}
	if err := brainimport.EnsureWorkspaces(topo, engines); err != nil {
		return rep, err
	}
	if err := brainimport.EnsureEntities(items, engines); err != nil {
		return rep, err
	}
	rest, landed, err := brainimport.WithoutLanded(items, engines)
	if err != nil {
		return rep, err
	}
	rep.Landed = landed
	// The engine is the record (WithoutLanded); the ledger is per run only.
	run, err := brainimport.Run(rest, engines, filepath.Join(dir, "ledger.json"), false)
	if run != nil {
		rep.Ingested, rep.Errors = run.Ingested, run.Errors
	}
	return rep, err
}
