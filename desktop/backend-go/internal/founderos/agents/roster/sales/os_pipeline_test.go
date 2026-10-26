package sales

import (
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

func TestSalesAgentUsesSyncedOSPipelineWhenExternalIntakeIsNotConfigured(t *testing.T) {
	a := &SalesAgent{Pipeline: st(connectors.StateConnected, "38 synced leads"), Leads: st(connectors.StateNotConfigured, ""), Calls: st(connectors.StateNotConfigured, ""), Processors: procs()}
	result := run(t, a)
	if !result.OK || !strings.Contains(result.Summary, "38 synced leads") {
		t.Fatalf("%+v", result)
	}
}
