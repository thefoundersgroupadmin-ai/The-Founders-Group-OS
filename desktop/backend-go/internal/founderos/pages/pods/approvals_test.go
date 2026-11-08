package pods

import (
	"context"
	"errors"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func approvalRow(t *testing.T, s *Store, kind, ref string) (status, hash string) {
	t.Helper()
	_ = s.Pool.QueryRow(context.Background(), `SELECT status, coalesce(payload->>'hash', '') FROM founderos_client_approvals
		WHERE kind = $1 AND ref = $2`, kind, ref).Scan(&status, &hash)
	return
}

func TestADocumentFilesAnApprovalForItsExactTextAndAnEditReopensIt(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators")
	st, h1 := approvalRow(t, s, "doc", "icp")
	if st != "pending" || h1 == "" {
		t.Fatalf("no approval filed: %q %q", st, h1)
	}
	applied, err := s.ApplyClientDecision(ctx, p.ID, "doc", "icp", h1, "approved", "Priya Shah", "")
	if err != nil || !applied {
		t.Fatalf("decision: %v %v", applied, err)
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt == nil {
		t.Fatal("client approval did not approve the document")
	}
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators, 50-500 staff")
	st, h2 := approvalRow(t, s, "doc", "icp")
	if st != "pending" || h2 == h1 {
		t.Fatalf("edit did not reopen: %q", st)
	}
	// A decision on the old wording is ignored.
	if applied, _ := s.ApplyClientDecision(ctx, p.ID, "doc", "icp", h1, "approved", "Priya Shah", ""); applied {
		t.Fatal("a stale approval was applied")
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt != nil {
		t.Fatal("stale approval approved the new text")
	}
}

func TestTargetListsAreProposedThenApprovedByTheClient(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	if err := s.ProposeTargetList(ctx, p.Slug, "Multi-site warehouses", []string{"Brightvale.example", " oakfield.example ", "bad domain"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad domain accepted: %v", err)
	}
	if err := s.ProposeTargetList(ctx, p.Slug, "Multi-site warehouses", []string{"Brightvale.example", "oakfield.example"}); err != nil {
		t.Fatal(err)
	}
	st, h := approvalRow(t, s, "target_list", "multi-site-warehouses")
	if st != "pending" {
		t.Fatalf("list not pending: %q", st)
	}
	var domains string
	_ = s.Pool.QueryRow(ctx, `SELECT payload->>'domains' FROM founderos_client_approvals WHERE kind = 'target_list'`).Scan(&domains)
	if domains != `["brightvale.example", "oakfield.example"]` {
		t.Fatalf("domains %s", domains)
	}
	if applied, err := s.ApplyClientDecision(ctx, p.ID, "target_list", "multi-site-warehouses", h, "changes_requested", "Priya", "drop Oakfield"); err != nil || !applied {
		t.Fatalf("changes requested: %v %v", applied, err)
	}
	var note, by string
	_ = s.Pool.QueryRow(ctx, `SELECT client_note, decided_by FROM founderos_client_approvals WHERE kind = 'target_list'`).Scan(&note, &by)
	if note != "drop Oakfield" || by != "client:Priya" {
		t.Fatalf("decision not recorded: %q %q", note, by)
	}
}

func TestAPodLinksToAPortalClient(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	if err := s.LinkPortal(ctx, p.Slug, "not-a-uuid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id accepted: %v", err)
	}
	if err := s.LinkPortal(ctx, p.Slug, "8f14e45f-ceea-467a-9a36-dedd4bea2543"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, p.Slug)
	if got.PortalClientID == nil || *got.PortalClientID != "8f14e45f-ceea-467a-9a36-dedd4bea2543" {
		t.Fatalf("portal link %v", got.PortalClientID)
	}
}

// Codex M2-M4 review #4: applying the same decision again is not "stale".
func TestApplyingTheSameDecisionTwiceIsSafe(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators")
	_, h := approvalRow(t, s, "doc", "icp")
	for i := 0; i < 2; i++ {
		applied, err := s.ApplyClientDecision(ctx, p.ID, "doc", "icp", h, "approved", "Priya", "")
		if err != nil || !applied {
			t.Fatalf("pass %d: %v %v", i, applied, err)
		}
	}
}
