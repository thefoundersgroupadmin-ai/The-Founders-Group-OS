package saleskit_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
)

func TestAutomationStoreScopesSourcesAndClaimsWorkOnce(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	a := catalogdb.Workspace(t, pool, "sales-a")
	b := catalogdb.Workspace(t, pool, "sales-b")
	for _, item := range []struct{ id, ws, name string }{{"lead-a", a, "Allowed buyer"}, {"lead-b", b, "Other client secret"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts(id,workspace_id,name,venture,status,created_at) VALUES($1,$2,'buyer','vantage','first_touch',now())`, item.id, item.ws); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_call_archive(source,external_id,workspace_id,slug,entry) VALUES('fathom','call-a',$1,'call', '{"summary":"our buyer call"}')`, a); err != nil {
		t.Fatal(err)
	}
	store := saleskit.AutomationStore{Pool: pool}
	snapshot, err := store.Snapshot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	if !json.Valid(raw) || !snapshot.HasLeads || !snapshot.HasCalls {
		t.Fatalf("snapshot: %s", raw)
	}
	for _, s := range snapshot.Sources {
		if s.ID == "os-pipeline" && strings.Contains(s.Text, "lead-b") {
			t.Fatal("other workspace leaked")
		}
	}
	config := saleskit.AutomationConfig{WorkspaceID: a, Workspace: "sales-a", UserID: "u1", UserEmail: "owner@example.com", Profile: "Approved sales profile", Enabled: true, Timezone: "Europe/London"}
	if err := store.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	plan := saleskit.Plan(time.Now(), saleskit.Checkpoint{}, snapshot)
	id, claimed, err := store.Claim(ctx, config, plan, snapshot)
	if err != nil || !claimed {
		t.Fatalf("claim %v %v", claimed, err)
	}
	if _, again, err := store.Claim(ctx, config, plan, snapshot); err != nil || again {
		t.Fatal("duplicate model work allowed")
	}
	if err := store.Finish(ctx, config, id, plan.Next, "Sales report", nil); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(ctx, a)
	if err != nil || len(runs) != 1 || runs[0].Markdown != "Sales report" {
		t.Fatalf("runs %+v %v", runs, err)
	}
	other, err := store.Runs(ctx, b)
	if err != nil || len(other) != 0 {
		t.Fatal("run history crossed workspaces")
	}
}
