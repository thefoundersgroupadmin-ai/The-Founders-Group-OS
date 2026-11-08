package portalsync

import (
	"context"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

type fakePortal struct {
	meetings  map[int64]Meeting
	pods      map[string]Pod
	approvals map[string]*Approval // id → row
	activity  []Activity
	requests  []*Request
	n         int
}

func newFake() *fakePortal {
	return &fakePortal{pods: map[string]Pod{}, approvals: map[string]*Approval{}}
}

func (f *fakePortal) UpsertPod(_ context.Context, p Pod) error { f.pods[p.ClientID] = p; return nil }
func (f *fakePortal) Approvals(_ context.Context, client string) ([]Approval, error) {
	var out []Approval
	for _, a := range f.approvals {
		if a.ClientID == client {
			out = append(out, *a)
		}
	}
	return out, nil
}
func (f *fakePortal) InsertApproval(_ context.Context, a Approval) error {
	f.n++
	a.ID = "pa-" + string(rune('a'+f.n))
	f.approvals[a.ID] = &a
	return nil
}
func (f *fakePortal) UpdateApproval(_ context.Context, id string, fields map[string]any) error {
	a := f.approvals[id]
	for k, v := range fields {
		switch k {
		case "status":
			a.Status = v.(string)
		case "content_hash":
			a.ContentHash = v.(string)
		case "pulled_at":
			a.Pulled = v != nil
		case "decided_at":
			if v == nil {
				a.DecidedAt = ""
			}
		}
	}
	return nil
}
func (f *fakePortal) InsertActivity(_ context.Context, rows []Activity) error {
	f.activity = append(f.activity, rows...)
	return nil
}
func (f *fakePortal) OpenRequests(_ context.Context, client string) ([]Request, error) {
	var out []Request
	for _, r := range f.requests {
		if r.ClientID == client && r.Outcome == "" {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakePortal) CloseRequest(_ context.Context, id, outcome string) error {
	for _, r := range f.requests {
		if r.ID == id {
			r.Outcome = outcome
		}
	}
	return nil
}

const portalClient = "8f14e45f-ceea-467a-9a36-dedd4bea2543"

func setup(t *testing.T) (*pods.Store, pods.Pod) {
	t.Helper()
	s := &pods.Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p, err := s.Create(ctx, pods.NewPod{ClientName: "Brightvale", OwnerID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetSender(ctx, p.Slug, pods.Sender{Name: "Sam", Email: "sam@bv.example", AccountID: "pod:" + p.Slug})
	_ = s.LinkPortal(ctx, p.Slug, portalClient)
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators")
	return s, p
}

func TestSyncPushesStateApprovalsAndActivityAndAppliesCurrentDecisions(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	f := newFake()
	if err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if got := f.pods[portalClient]; got.PodSlug != p.Slug || got.Status != "setup" || !strings.Contains(got.Sender, "sam@bv.example") {
		t.Fatalf("pod row %+v", got)
	}
	if len(f.approvals) != 1 || len(f.activity) == 0 {
		t.Fatalf("approvals %d activity %d", len(f.approvals), len(f.activity))
	}
	var a *Approval
	for _, v := range f.approvals {
		a = v
	}
	if a.OSKey != "doc:icp" || a.Status != "pending" || a.ContentHash == "" {
		t.Fatalf("approval %+v", a)
	}
	// Running again pushes nothing twice.
	before := len(f.activity)
	_ = Sync(ctx, s, f)
	if len(f.approvals) != 1 || len(f.activity) != before {
		t.Fatalf("duplicates: %d approvals, %d→%d activity", len(f.approvals), before, len(f.activity))
	}
	// The client approves in the portal; the OS applies it once.
	a.Status, a.DecidedAt, a.DecidedByName = "approved", "2026-11-09T10:00:00Z", "Priya Shah"
	if err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt == nil {
		t.Fatal("portal approval not applied")
	}
	if !a.Pulled {
		t.Fatal("decision not marked pulled")
	}
}

func TestADecisionOnOldWordingIsSentBackToPending(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	f := newFake()
	_ = Sync(ctx, s, f)
	var a *Approval
	for _, v := range f.approvals {
		a = v
	}
	_ = s.SetDoc(ctx, p.Slug, "icp", "Warehouse operators, 50-500 staff") // edited after the client looked
	a.Status, a.DecidedAt, a.DecidedByName = "approved", "2026-11-09T10:00:00Z", "Priya Shah"
	if err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Doc(ctx, p.Slug, "icp"); d.ApprovedAt != nil {
		t.Fatal("approval of old wording applied to new wording")
	}
	if a.Status != "pending" {
		t.Fatalf("stale decision not reset: %s", a.Status)
	}
}

func TestClientPauseRequestsPauseThePod(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	if _, err := s.SetStatus(ctx, p.Slug, pods.StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	f.requests = append(f.requests, &Request{ID: "rq-1", ClientID: portalClient, Kind: "pause", Reason: "holiday", RequestedByName: "Priya"})
	if err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, p.Slug)
	if got.Status != pods.StatusPaused || !strings.Contains(got.PausedReason, "holiday") || f.requests[0].Outcome == "" {
		t.Fatalf("pause not applied: %s %q %q", got.Status, got.PausedReason, f.requests[0].Outcome)
	}
}

func TestMeetingsAndBriefsReachThePortal(t *testing.T) {
	s, p := setup(t)
	ctx := context.Background()
	if _, err := s.RecordMeetings(ctx, p.Slug, []pods.Meeting{{UID: "b-1", Title: "Discovery call", Start: "2030-01-07T10:00:00Z",
		AttendeeEmail: "priya@bv.example", AttendeeName: "Priya Shah"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET brief = 'Priya runs ops.'`); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	if err := Sync(ctx, s, f); err != nil {
		t.Fatal(err)
	}
	if len(f.meetings) != 1 {
		t.Fatalf("meetings %d", len(f.meetings))
	}
	for _, m := range f.meetings {
		if m.Attendee != "Priya Shah" || m.Brief != "Priya runs ops." || m.StartsAt == nil || *m.StartsAt != "2030-01-07T10:00:00Z" {
			t.Fatalf("meeting %+v", m)
		}
	}
}

func (f *fakePortal) UpsertMeetings(_ context.Context, rows []Meeting) error {
	if f.meetings == nil {
		f.meetings = map[int64]Meeting{}
	}
	for _, m := range rows {
		f.meetings[m.OSID] = m
	}
	return nil
}
