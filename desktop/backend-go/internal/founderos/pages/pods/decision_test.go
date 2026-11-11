package pods

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// A decision recorded before a crash, with the document left unapproved, is finished on retry;
// a document edited after the client looked is never approved by their decision.
func TestClientDocDecisionIsAtomicAndRepairable(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Atomico")
	if err := s.SetDoc(ctx, p.Slug, "icp", "Ops leaders at US warehouses"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Approvals(ctx, p.ID)
	if err != nil || len(rows) == 0 {
		t.Fatal(rows, err)
	}
	hash := rows[0].Hash
	// Simulate the crash: the request was marked approved but the document wasn't.
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_client_approvals SET status = 'approved', decided_at = now() WHERE pod_id = $1 AND kind = 'doc' AND ref = 'icp'`, p.ID); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ApplyClientDecision(ctx, p.ID, "doc", "icp", hash, "approved", "Priya", "")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt == nil {
		t.Fatal("retry did not finish the approval")
	}
	// New wording: the old decision must not approve it.
	if err := s.SetDoc(ctx, p.Slug, "icp", "Ops leaders at US and UK warehouses"); err != nil {
		t.Fatal(err)
	}
	ok, err = s.ApplyClientDecision(ctx, p.ID, "doc", "icp", hash, "approved", "Priya", "")
	if err != nil || ok {
		t.Fatal("stale decision applied", ok, err)
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt != nil {
		t.Fatal("new wording approved by an old decision")
	}
}
