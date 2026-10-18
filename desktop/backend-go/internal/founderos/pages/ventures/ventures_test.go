package ventures

import (
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/brand"
)

func TestVenturesMatchFounderosOS(t *testing.T) {
	if len(All) != 2 || All[0].ID != "vantage" || All[1].ID != "launchpad-cohort" {
		t.Fatalf("ventures = %+v", All)
	}
	for i, b := range brand.Ventures {
		if All[i].Label != b.Label || All[i].Color != b.Color {
			t.Fatalf("venture %d = %+v, want the brand file's %+v", i, All[i], b)
		}
	}
	set := AgentSet("launchpad-cohort")
	for _, a := range []string{"tfg-pipeline-sdr", "tfg-impact-reporter", "conductor", "data-agent"} {
		if !set[a] {
			t.Errorf("launchpad-cohort should include %s", a)
		}
	}
	if got := ForAgent("conductor"); len(got) != 2 {
		t.Errorf("shared ops serve both ventures, got %d", len(got))
	}
	if got := ForAgent("tfg-pipeline-sdr"); len(got) != 2 {
		t.Errorf("the pipeline SDR works both offers, got %+v", got)
	}
}
