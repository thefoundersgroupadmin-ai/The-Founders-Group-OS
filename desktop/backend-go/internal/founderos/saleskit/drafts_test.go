package saleskit_test

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
)

func TestDraftHandoffResolvesOnlyEligibleAuthorisedContactsAndNeverSends(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	catalogdb.Workspace(t, pool, "founderos")
	a := catalogdb.Workspace(t, pool, "draft-a")
	b := catalogdb.Workspace(t, pool, "draft-b")
	for _, item := range []struct{ id, ws, status, email string }{{"allowed", a, "first_touch", "buyer@allowed.test"}, {"replied", a, "engaged", "buyer@replied.test"}, {"foreign", b, "first_touch", "buyer@foreign.test"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts(id,workspace_id,name,venture,status,email,created_at) VALUES($1,$2,'Buyer','vantage',$3,$4,now())`, item.id, item.ws, item.status, item.email); err != nil {
			t.Fatal(err)
		}
	}
	drafts := []saleskit.Draft{{ContactID: "allowed", Subject: "Relevant draft", Body: "Hello buyer"}, {ContactID: "foreign", Subject: "Must not appear", Body: "Other client"}, {ContactID: "replied", Subject: "Must not appear", Body: "Already replied"}, {ContactID: "invented", Subject: "Must not appear", Body: "Unknown contact"}}
	cfg := saleskit.AutomationConfig{WorkspaceID: a}
	if n, err := saleskit.QueueDrafts(ctx, pool, cfg, "run-a", drafts); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := saleskit.QueueDrafts(ctx, pool, cfg, "another-run", drafts); err != nil || n != 0 {
		t.Fatal("duplicate outreach", n, err)
	}
	var count int
	var status, address string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM founderos_outbox`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,to_address FROM founderos_outbox`).Scan(&status, &address); err != nil || status != "pending" || address != "buyer@allowed.test" {
		t.Fatal(status, address, err)
	}
}

func TestAnalysisParserKeepsMarkdownAndRejectsMalformedDraftEnvelopes(t *testing.T) {
	for _, raw := range []string{"# A report", `{"drafts":[{"contactId":"invented"}]}`, "```json\n{broken}\n```"} {
		a := saleskit.ParseAnalysis(raw)
		if a.Report != raw || len(a.Drafts) != 0 {
			t.Fatal(a)
		}
	}
	a := saleskit.ParseAnalysis("```json\n{\"report\":\"A report\",\"drafts\":[{\"contactId\":\"lead-a\",\"subject\":\"Hello\",\"body\":\"Draft\"}]}\n```")
	if a.Report != "A report" || len(a.Drafts) != 1 {
		t.Fatal(a)
	}
}
