package api

import (
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/brand"
	"github.com/rhl/businessos-backend/internal/founderos/pages/integrations"
)

// A connector the brand file hides (hide_connectors) is not on the board, so
// it is never counted, and its catalog tile is gone too.
func TestHiddenConnectorsLeaveTheBoardAndTheCatalog(t *testing.T) {
	was := brand.HiddenConnectors
	brand.HiddenConnectors = []string{"robinhood"}
	t.Cleanup(func() { brand.HiddenConnectors = was })

	for _, e := range visibleBoard() {
		if e.meta.ID == "robinhood" {
			t.Fatal("robinhood is still on the board")
		}
	}
	if len(visibleBoard()) != len(board())-1 {
		t.Fatalf("hid %d connectors, want 1", len(board())-len(visibleBoard()))
	}
	for _, e := range visibleCatalog([]integrations.Entry{{Integration: integrations.Integration{Slug: "robinhood", ConnectorID: "robinhood"}},
		{Integration: integrations.Integration{Slug: "slack", ConnectorID: "slack"}}}) {
		if e.Slug == "robinhood" {
			t.Fatal("robinhood tile is still in the catalog")
		}
	}
}
