package pods

import (
	"context"
	"errors"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func newPod(t *testing.T, s *Store, name string) Pod {
	t.Helper()
	p, err := s.Create(context.Background(), NewPod{ClientName: name, OwnerID: "u1", Package: "core", Reps: 6})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateMakesAWorkspaceAndAPodWithSafeDefaults(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Northwind Logistics")
	if p.Slug != "client-northwind-logistics" || p.Status != StatusSetup || p.Package != "core" || p.Reps != 6 {
		t.Fatalf("got %+v", p)
	}
	if p.ApprovalPolicy != "client_approves_each" || p.FirstEmailDailyCap != 20 || p.SendWindowStart != 9 || p.SendWindowEnd != 17 {
		t.Fatalf("defaults wrong: %+v", p)
	}
	var wsSlug string
	if err := s.Pool.QueryRow(ctx, `SELECT slug FROM workspaces WHERE id = $1`, p.WorkspaceID).Scan(&wsSlug); err != nil || wsSlug != p.Slug {
		t.Fatalf("workspace %q %v", wsSlug, err)
	}
	if _, err := s.Create(ctx, NewPod{ClientName: "Northwind  logistics", OwnerID: "u1"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate client: %v", err)
	}
	if _, err := s.Create(ctx, NewPod{ClientName: "  ", OwnerID: "u1"}); err == nil {
		t.Fatal("blank client name accepted")
	}
	got, err := s.Get(ctx, p.Slug)
	if err != nil || got.ID != p.ID {
		t.Fatalf("get: %+v %v", got, err)
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %d %v", len(all), err)
	}
}

func TestGoingLiveNeedsADryRunASenderAndApprovedTargetingAndVoice(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	if _, err := s.SetStatus(ctx, p.Slug, StatusLive, ""); !errors.Is(err, ErrTransition) {
		t.Fatalf("setup → live allowed: %v", err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusLive, ""); !errors.Is(err, ErrNotReady) {
		t.Fatalf("live without sender/docs: %v", err)
	}
	if err := s.SetSender(ctx, p.Slug, Sender{Name: "Sam at Brightvale", Email: "sam@brightvale-mail.com", Domain: "brightvale-mail.com"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"icp", "voice"} {
		if err := s.SetDoc(ctx, p.Slug, k, "body of "+k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusLive, ""); !errors.Is(err, ErrNotReady) {
		t.Fatalf("live with unapproved docs: %v", err)
	}
	for _, k := range []string{"icp", "voice"} {
		if err := s.ApproveDoc(ctx, p.Slug, k, "client:priya"); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.SetStatus(ctx, p.Slug, StatusLive, "")
	if err != nil || live.Status != StatusLive || live.WentLiveAt == nil {
		t.Fatalf("go live: %+v %v", live, err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusPaused, ""); err == nil {
		t.Fatal("pause without a reason accepted")
	}
	paused, err := s.SetStatus(ctx, p.Slug, StatusPaused, "client asked to pause")
	if err != nil || paused.PausedReason != "client asked to pause" {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusLive, ""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusHandedOver, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusLive, ""); !errors.Is(err, ErrTransition) {
		t.Fatalf("handed over pod reopened: %v", err)
	}
}

func TestEditingAnApprovedDocNeedsApprovalAgain(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Kestrel")
	if err := s.SetDoc(ctx, p.Slug, "nonsense", "x"); err == nil {
		t.Fatal("unknown doc key accepted")
	}
	_ = s.SetDoc(ctx, p.Slug, "voice", "v1")
	_ = s.ApproveDoc(ctx, p.Slug, "voice", "client:a")
	if err := s.SetDoc(ctx, p.Slug, "voice", "v2"); err != nil {
		t.Fatal(err)
	}
	d, err := s.Doc(ctx, p.Slug, "voice")
	if err != nil || d.Body != "v2" || d.Version != 2 || d.ApprovedAt != nil {
		t.Fatalf("doc after edit: %+v %v", d, err)
	}
	// Saving identical text is not a new version and keeps the approval.
	_ = s.ApproveDoc(ctx, p.Slug, "voice", "client:a")
	_ = s.SetDoc(ctx, p.Slug, "voice", "v2")
	if d, _ := s.Doc(ctx, p.Slug, "voice"); d.Version != 2 || d.ApprovedAt == nil {
		t.Fatalf("no-op save reset approval: %+v", d)
	}
}

func TestActivityIsRecordedPerPodNewestFirst(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	a, b := newPod(t, s, "Alpha"), newPod(t, s, "Beta")
	_ = s.Log(ctx, a.ID, "research", "Researched Brightvale Logistics", "")
	_ = s.Log(ctx, a.ID, "email_sent", "Emailed Priya Shah", "out-1")
	_ = s.Log(ctx, b.ID, "research", "Researched Oakfield", "")
	got, err := s.Recent(ctx, a.ID, 10)
	if err != nil || len(got) != 3 || got[0].Kind != "email_sent" || got[2].Kind != "pod_created" {
		t.Fatalf("recent: %+v %v", got, err)
	}
}

// Codex M1 review #2: a pod can be bound only to its own mailbox.
func TestAPodCanOnlyBeBoundToItsOwnMailbox(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	for _, foreign := range []string{"pod:client-kestrel", "inbox-1", "pod:"} {
		if err := s.SetSender(ctx, p.Slug, Sender{Name: "Sam", Email: "sam@bv.example", AccountID: foreign}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bound to %q: %v", foreign, err)
		}
	}
	if err := s.SetSender(ctx, p.Slug, Sender{Name: "Sam", Email: "sam@bv.example", AccountID: "pod:" + p.Slug}); err != nil {
		t.Fatal(err)
	}
}
